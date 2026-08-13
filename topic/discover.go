package topic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// Discovery is the realm's second, live layer of finding topics (the first is the
// durable board): scatter/gather over plain request-reply. A persona shouts a query
// at SOULSTREAM.SVC.DISCOVER with a reply inbox and a deadline; any persona that
// maintains a projection may answer from it; non-answers are silent; the asker
// merges whatever arrived in time. There is no registry to keep consistent and no
// component whose absence breaks discovery — with zero responders, asks resolve
// empty and the board still works.

// ServiceDiscover is the discovery service's name — the canonical binding signed
// over by requests and replies alike (a reply travels on an ephemeral inbox, which
// would be a meaningless thing to sign over).
const ServiceDiscover = "DISCOVER"

// Discovery defaults and caps.
const (
	DefaultDiscoverTimeout = 2 * time.Second
	DefaultDiscoverLimit   = 10
	MaxDiscoverLimit       = 50
)

// DiscoverAnswer credits one answerer for one discovered topic, with the
// verification status of its reply.
type DiscoverAnswer struct {
	Persona string    `json:"persona"`
	Sig     SigStatus `json:"sig,omitempty"`
}

// DiscoverResult is the asker's merged view of one discovered topic: the entry as
// first reported, plus every persona that reported it.
type DiscoverResult struct {
	DiscoverEntry
	Answers []DiscoverAnswer `json:"answers"`
}

// DiscoverInput is an ask: the query, how many matches each answerer should cap at,
// and how long the asker listens.
type DiscoverInput struct {
	Query   string
	Limit   int           // per-answerer cap; 0 means DefaultDiscoverLimit
	Timeout time.Duration // the ask's deadline; 0 means DefaultDiscoverTimeout
}

// Discover publishes a topic.discover request and gathers replies until the
// deadline, merging them into one result per topic path with every answerer
// credited and each answer verified against kr (nil kr: signed answers report
// unknown-key). Zero replies resolve to (nil, nil) — silence is a defined answer,
// and the durable board remains the fallback that always works.
func Discover(ctx context.Context, c *realm.Client, in DiscoverInput, kr *identity.Keyring) ([]DiscoverResult, error) {
	timeout := in.Timeout
	if timeout <= 0 {
		timeout = DefaultDiscoverTimeout
	}
	limit := clampDiscoverLimit(in.Limit)

	nc := c.Conn()
	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("topic: subscribe reply inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	deadline := time.Now().Add(timeout)
	reqMsg, _, err := buildOpMsg(c, SvcDiscoverSubject, ServiceDiscover, TypeDiscover,
		DiscoverPayload{Query: in.Query, Limit: limit, Deadline: deadline.UTC()}, nil, "")
	if err != nil {
		return nil, err
	}
	reqMsg.Reply = inbox
	if err := nc.PublishMsg(reqMsg); err != nil {
		return nil, fmt.Errorf("topic: publish discover request: %w", err)
	}

	// Gather until the deadline. Every reply is one answerer's testimony; malformed
	// ones are skipped, late ones never arrive (we stop listening).
	var (
		order   []string
		results = map[string]*DiscoverResult{}
	)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			// Nobody at all is listening on the service subject (since 014 no
			// stream captures it either, so the server can say so outright).
			// Silence is an answer, not an error.
			if errors.Is(err, nats.ErrNoResponders) {
				break
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("topic: gather replies: %w", err)
		}
		rec, perr := record.Parse(msg.Header, msg.Data)
		if perr != nil || rec.Type != TypeDiscoverReply {
			continue
		}
		var reply DiscoverReplyPayload
		if json.Unmarshal(rec.Payload, &reply) != nil || len(reply.Matches) == 0 {
			continue
		}
		sig := VerifyRecord(rec, c.Realm(), ServiceDiscover, kr)
		mergeReply(results, &order, rec.Author, sig, reply.Matches)
	}

	if len(order) == 0 {
		return nil, nil
	}
	out := make([]DiscoverResult, 0, len(order))
	for _, path := range order {
		out = append(out, *results[path])
	}
	return out, nil
}

// mergeReply folds one answerer's matches into the merged results: one entry per
// topic path (first-seen fields win — answers are testimony, not reconciled facts),
// one credit per (path, persona) however often it repeats itself.
func mergeReply(results map[string]*DiscoverResult, order *[]string, persona string, sig SigStatus, matches []DiscoverEntry) {
	for _, m := range matches {
		r, ok := results[m.Path]
		if !ok {
			r = &DiscoverResult{DiscoverEntry: m}
			results[m.Path] = r
			*order = append(*order, m.Path)
		}
		credited := false
		for _, a := range r.Answers {
			if a.Persona == persona {
				credited = true
				break
			}
		}
		if !credited {
			r.Answers = append(r.Answers, DiscoverAnswer{Persona: persona, Sig: sig})
		}
	}
}

// RespondDiscovery serves discovery as c's persona until ctx is cancelled: for each
// request it rebuilds the board projection, matches, and replies only when there is
// something to say (signed when the client is keyed). Answering is a habit, not a
// role — any number of responders may run, none coordinates with another, and
// stopping one changes nothing for the rest.
//
// onServed, if non-nil, is called after each request with the query, the number of
// matches sent (0 = silence), and — when the request was heard but could not be
// served — an error saying why: unreadable or reply-less request (query ""), a
// stale deadline, or a reply that could not be built or signed. To the asker every
// unserved request is ordinary silence; the error exists so the hosting process
// can tell a broken custodian from ambient noise. Observability, nothing more.
func RespondDiscovery(ctx context.Context, c *realm.Client, onServed func(query string, sent int, err error)) error {
	return RespondDiscoveryWith(ctx, c, func(query string, limit int) []DiscoverEntry {
		entries, err := Board(ctx, c)
		if err != nil {
			return nil
		}
		return matchEntries(entries, query, limit)
	}, onServed)
}

// RespondDiscoveryWith is RespondDiscovery with a caller-supplied answerer: answer
// receives each request's query and limit and returns the entries to send — nil or
// empty means silence. This is how a persona with a better projection (a curator)
// plugs into the same mechanism: same request, same reply shape, same merge.
func RespondDiscoveryWith(ctx context.Context, c *realm.Client, answer func(query string, limit int) []DiscoverEntry, onServed func(query string, sent int, err error)) error {
	if c.Persona() == "" {
		return fmt.Errorf("topic: responding to discovery requires a persona")
	}
	nc := c.Conn()

	served := func(query string, sent int, err error) {
		if onServed != nil {
			onServed(query, sent, err)
		}
	}

	// Plain subscribe — deliberately NO queue group: every responder must hear
	// every request; the asker's merge is the only aggregation point.
	sub, err := nc.Subscribe(SvcDiscoverSubject, func(msg *nats.Msg) {
		rec, perr := record.Parse(msg.Header, msg.Data)
		switch {
		case perr != nil:
			served("", 0, fmt.Errorf("topic: unreadable discovery request: %w", perr))
			return
		case rec.Type != TypeDiscover:
			served("", 0, fmt.Errorf("topic: unexpected op type %q on the discovery subject", rec.Type))
			return
		case msg.Reply == "":
			served("", 0, fmt.Errorf("topic: discovery request carries no reply inbox"))
			return
		}
		var req DiscoverPayload
		if uerr := json.Unmarshal(rec.Payload, &req); uerr != nil {
			served("", 0, fmt.Errorf("topic: malformed discovery payload: %w", uerr))
			return
		}
		if !req.Deadline.IsZero() && time.Now().After(req.Deadline) {
			// Stale: the reply would be ignored anyway.
			served(req.Query, 0, fmt.Errorf("topic: stale discovery request (asker's deadline passed)"))
			return
		}

		matches := answer(req.Query, req.Limit)
		if len(matches) == 0 {
			served(req.Query, 0, nil) // silence is cheaper than noise
			return
		}

		reply, _, berr := buildOpMsg(c, msg.Reply, ServiceDiscover, TypeDiscoverReply,
			DiscoverReplyPayload{Matches: matches}, nil, "")
		if berr != nil {
			served(req.Query, 0, fmt.Errorf("topic: discovery reply not sent: %w", berr))
			return
		}
		if perr := nc.PublishMsg(reply); perr != nil {
			served(req.Query, 0, fmt.Errorf("topic: publish discovery reply: %w", perr))
			return
		}
		served(req.Query, len(matches), nil)
	})
	if err != nil {
		return fmt.Errorf("topic: subscribe discovery: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Flush so the server has registered the interest: once this returns, the
	// responder truly hears every subsequent request.
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("topic: establish discovery subscription: %w", err)
	}

	<-ctx.Done()
	return nil
}

// matchEntries is the answerer's deterministic matcher: case-insensitive substring
// of the query against each topic's name, subject matter, and tags. An empty query
// matches everything. Results keep board order, capped at limit.
func matchEntries(entries []BoardEntry, query string, limit int) []DiscoverEntry {
	limit = clampDiscoverLimit(limit)
	q := strings.ToLower(strings.TrimSpace(query))

	var out []DiscoverEntry
	for _, e := range entries {
		if q != "" && !entryMatches(e, q) {
			continue
		}
		out = append(out, DiscoverEntry{
			Path:          e.Path,
			Name:          e.Announcement.Name,
			SubjectMatter: e.Announcement.SubjectMatter,
			Tags:          e.Announcement.Tags,
			Lifecycle:     e.Lifecycle,
		})
		if len(out) == limit {
			break
		}
	}
	return out
}

func entryMatches(e BoardEntry, q string) bool {
	if strings.Contains(strings.ToLower(e.Announcement.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Announcement.SubjectMatter), q) {
		return true
	}
	for _, tag := range e.Announcement.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

func clampDiscoverLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultDiscoverLimit
	case limit > MaxDiscoverLimit:
		return MaxDiscoverLimit
	default:
		return limit
	}
}
