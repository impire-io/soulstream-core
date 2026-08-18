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

// The memory convention: the substrate forgets by design (rollup compacts op
// tails), so remembering is something personas do for each other. A realm's
// memory is the union of what its personas bothered to keep — queried socially
// (scatter/gather, like discovery), answered as testimony with citations, and
// verified by the asker actually checking, never by trusting the witness. There
// is no index, no ranking, no archive role, and no truth in the substrate: the
// realm commits only to the subject, the vocabulary, and the grading rules.

// ServiceMemory is the memory service's name — the canonical binding signed over
// by requests and replies alike (replies travel on ephemeral inboxes, which would
// be meaningless to sign over; the discovery rule).
const ServiceMemory = "MEMORY"

// Memory defaults and caps.
const (
	DefaultMemoryTimeout = 3 * time.Second
	MinMemoryTimeout     = 100 * time.Millisecond
	MaxMemoryTimeout     = 30 * time.Second
	// MaxMemoryAnswers is the per-query safety cap: the deadline is the primary
	// bound, this one just refuses floods. Arrivals beyond it are discarded.
	MaxMemoryAnswers = 100
)

// MemoryGrade is a claim's verifiability standing, computed by the asker.
type MemoryGrade string

const (
	// GradeFact means the citation resolves in its topic's current state — checked.
	GradeFact MemoryGrade = "fact"
	// GradeProvenance means an exhibit obtained by an explicit fetch verifies.
	GradeProvenance MemoryGrade = "fact-with-provenance"
	// GradeTestimony means readable but unverifiable content — an unsigned
	// exhibit, or the witness's word. As trustworthy as its keeper.
	GradeTestimony MemoryGrade = "testimony"
	// GradeGossip means an uncited answer. Useful for leads, never decisions.
	GradeGossip MemoryGrade = "gossip"
	// GradeUnverifiable means cited, but the citation resolves nowhere live.
	// Compacted history and fabrication are indistinguishable here — presented
	// with caution, never as fact; an explicit fetch may upgrade it.
	GradeUnverifiable MemoryGrade = "unverifiable"
)

// GradeForVerdict maps a fetched exhibit's verdict to the citation grade it
// supports, so clients never reimplement the mapping.
func GradeForVerdict(s SigStatus) MemoryGrade {
	switch s {
	case SigVerified:
		return GradeProvenance
	case SigUnsigned:
		return GradeTestimony
	default:
		return GradeUnverifiable
	}
}

// MemoryQueryInput is an ask: the question, an optional relevance hint, and how
// long the asker listens.
type MemoryQueryInput struct {
	Query   string
	Topics  []string      // optional scope hint (name patterns, witness-interpreted)
	After   time.Time     // optional scope hint (interest horizon)
	Timeout time.Duration // 0 means DefaultMemoryTimeout; clamped to [MinMemoryTimeout, MaxMemoryTimeout]
}

// GradedCitation is a citation plus the asker-computed grade it earned.
type GradedCitation struct {
	MemoryCitation
	Grade MemoryGrade `json:"grade"`
}

// MemoryAnswer is one witness's testimony as the asker received and graded it.
type MemoryAnswer struct {
	Witness      string           `json:"witness"`
	Sig          SigStatus        `json:"sig,omitempty"`
	Answer       string           `json:"answer"`
	CoverageFrom time.Time        `json:"coverage_from,omitzero"`
	Citations    []GradedCitation `json:"citations,omitempty"`
}

// MemoryResult is the merged, attributed outcome of one query. Conflicting
// answers all appear — the convention grades, it never arbitrates.
type MemoryResult struct {
	Answers []MemoryAnswer `json:"answers"`
}

// MemoryQuery publishes a memory.query and gathers answers until the (clamped)
// deadline or the safety cap, verifying each answer op against kr (failed
// signatures are discarded as tampering; unsigned answers are kept with their
// status visible) and grading every citation by actually resolving it in the
// cited topic's current state — one materialisation per distinct cited topic,
// memoised. Zero witnesses and zero answers both resolve to an empty result at
// the deadline: silence is an honest answer.
func MemoryQuery(ctx context.Context, c *realm.Client, in MemoryQueryInput, kr *identity.Keyring) (*MemoryResult, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, fmt.Errorf("topic: a memory query needs a question")
	}
	timeout := clampMemoryTimeout(in.Timeout)

	nc := c.Conn()
	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("topic: subscribe reply inbox: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	deadline := time.Now().Add(timeout)
	var scope *MemoryScope
	if len(in.Topics) > 0 || !in.After.IsZero() {
		scope = &MemoryScope{Topics: in.Topics, After: in.After.UTC()}
	}
	reqMsg, _, err := buildOpMsg(c, SvcMemorySubject, ServiceMemory, TypeMemoryQuery,
		MemoryQueryPayload{Query: query, Scope: scope, Deadline: deadline.UTC()}, nil, "")
	if err != nil {
		return nil, err
	}
	reqMsg.Reply = inbox
	if err := nc.PublishMsg(reqMsg); err != nil {
		return nil, fmt.Errorf("topic: publish memory query: %w", err)
	}

	res := &MemoryResult{}
	grader := newCitationGrader(ctx, c, kr)
	for len(res.Answers) < MaxMemoryAnswers {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		msg, err := sub.NextMsg(remaining)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, nats.ErrNoResponders) {
				break // silence is an answer, not an error
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("topic: gather answers: %w", err)
		}
		rec, perr := record.Parse(msg.Header, msg.Data)
		if perr != nil || rec.Type != TypeMemoryAnswer {
			continue
		}
		var ap MemoryAnswerPayload
		if json.Unmarshal(rec.Payload, &ap) != nil || strings.TrimSpace(ap.Answer) == "" {
			continue
		}
		sig := VerifyRecord(rec, c.RealmKey(), ServiceMemory, kr)
		if sig == SigFailed {
			continue // evidence of tampering is not testimony
		}
		ans := MemoryAnswer{Witness: rec.Author, Sig: sig, Answer: ap.Answer, CoverageFrom: ap.CoverageFrom}
		for _, cit := range ap.Citations {
			ans.Citations = append(ans.Citations, GradedCitation{MemoryCitation: cit, Grade: grader.grade(cit)})
		}
		res.Answers = append(res.Answers, ans)
	}
	return res, nil
}

// citationGrader grades citations by checking, never by trusting: one
// materialisation per distinct cited topic, memoised for the query's lifetime.
type citationGrader struct {
	ctx   context.Context
	c     *realm.Client
	kr    *identity.Keyring
	views map[string]*MaterializedTopic // nil entry: topic unreadable
}

func newCitationGrader(ctx context.Context, c *realm.Client, kr *identity.Keyring) *citationGrader {
	return &citationGrader{ctx: ctx, c: c, kr: kr, views: map[string]*MaterializedTopic{}}
}

func (g *citationGrader) grade(cit MemoryCitation) MemoryGrade {
	if cit.Topic == "" || cit.OpID == "" {
		return GradeUnverifiable
	}
	mt, ok := g.views[cit.Topic]
	if !ok {
		h := Open(g.c, cit.Topic)
		h.UseKeyring(g.kr)
		v, err := h.Materialise(g.ctx)
		if err != nil {
			v = nil // unreadable topic: nothing resolves there
		}
		g.views[cit.Topic] = v
		mt = v
	}
	if mt.ContainsOp(cit.OpID) {
		return GradeFact
	}
	return GradeUnverifiable
}

// ExhibitResult is a fetched exhibit plus its own embedded-signature verdict and
// where it came from ("live" for the stream, else the serving witness).
type ExhibitResult struct {
	Exhibit record.Exhibit `json:"exhibit"`
	Verdict SigStatus      `json:"verdict"`
	Source  string         `json:"source"`
}

// FetchExhibit obtains one operation as an exhibit, live-first: when the op is
// still in the stream it is captured directly (no scatter/gather at all);
// otherwise the realm's witnesses are asked via memory.fetch. The first exhibit
// that VERIFIES wins immediately — a verifying exhibit is self-authenticating, so
// waiting for more adds nothing. An unsigned exhibit is held as testimony-grade
// fallback and returned only when nothing verifying arrived by the deadline; a
// failed one is discarded as tampering. Nothing at the deadline resolves to
// (nil, nil): silence is an answer.
func FetchExhibit(ctx context.Context, c *realm.Client, path, opID string, timeout time.Duration, kr *identity.Keyring) (*ExhibitResult, error) {
	if path == "" || opID == "" {
		return nil, fmt.Errorf("topic: fetching an exhibit needs a topic and an op-id")
	}
	ex, err := CaptureExhibit(ctx, c, path, opID)
	if err == nil {
		verdict, verr := VerifyExhibit(ex, kr)
		if verr != nil {
			return nil, verr
		}
		return &ExhibitResult{Exhibit: ex, Verdict: verdict, Source: "live"}, nil
	}
	if !errors.Is(err, ErrOpNotLive) {
		return nil, err
	}

	timeout = clampMemoryTimeout(timeout)
	nc := c.Conn()
	inbox := nc.NewRespInbox()
	sub, serr := nc.SubscribeSync(inbox)
	if serr != nil {
		return nil, fmt.Errorf("topic: subscribe reply inbox: %w", serr)
	}
	defer func() { _ = sub.Unsubscribe() }()

	deadline := time.Now().Add(timeout)
	reqMsg, _, berr := buildOpMsg(c, SvcMemorySubject, ServiceMemory, TypeMemoryFetch,
		MemoryFetchPayload{Topic: path, OpID: opID, Deadline: deadline.UTC()}, nil, "")
	if berr != nil {
		return nil, berr
	}
	reqMsg.Reply = inbox
	if perr := nc.PublishMsg(reqMsg); perr != nil {
		return nil, fmt.Errorf("topic: publish memory fetch: %w", perr)
	}

	var fallback *ExhibitResult
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		msg, merr := sub.NextMsg(remaining)
		if merr != nil {
			if errors.Is(merr, nats.ErrTimeout) || errors.Is(merr, nats.ErrNoResponders) {
				break
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("topic: gather exhibits: %w", merr)
		}
		rec, perr := record.Parse(msg.Header, msg.Data)
		if perr != nil || rec.Type != TypeMemoryExhibit {
			continue
		}
		if VerifyRecord(rec, c.RealmKey(), ServiceMemory, kr) == SigFailed {
			continue // the reply op itself is tampered
		}
		var ep MemoryExhibitPayload
		if json.Unmarshal(rec.Payload, &ep) != nil {
			continue
		}
		// The exhibit must actually be the requested op in this realm — a witness
		// answering with a different (even validly signed) document is malformed.
		exRec, rerr := ep.Exhibit.Record()
		if rerr != nil || exRec.ID != opID || ep.Exhibit.Realm != c.RealmKey() || ep.Exhibit.Binding != path {
			continue
		}
		verdict := VerifyRecord(exRec, ep.Exhibit.Realm, ep.Exhibit.Binding, kr)
		switch verdict {
		case SigVerified:
			return &ExhibitResult{Exhibit: ep.Exhibit, Verdict: verdict, Source: rec.Author}, nil
		case SigFailed:
			continue // tampering is not testimony
		default: // unsigned, unknown-key: keeper-trust only — hold as fallback
			if fallback == nil {
				fallback = &ExhibitResult{Exhibit: ep.Exhibit, Verdict: verdict, Source: rec.Author}
			}
		}
	}
	return fallback, nil
}

// MemoryQueryRequest is what a witness's Answer capability sees of a query.
type MemoryQueryRequest struct {
	Query  string
	Topics []string
	After  time.Time
}

// MemoryAnswerDraft is one answer a witness wants to send: prose plus citations.
// The library owns signing, payload assembly, and reply publishing.
type MemoryAnswerDraft struct {
	Answer    string
	Citations []MemoryCitation
}

// MemoryWitness is a persona's memory service: what it answers, what it can
// produce as exhibits, and where its memory starts. The two capabilities are
// independently optional — a fetch-only exhibit keeper is a legitimate witness —
// and a nil capability simply stays silent for its kind of request.
//
// OnServed, if non-nil, is called after each handled request with the kind
// ("query" or "fetch"; "" when the request was unreadable), the number of
// replies sent (0 = silence), and — when the request was heard but could not be
// (fully) served — an error saying why: unreadable or reply-less, malformed,
// stale, or replies that could not be built (a failing signer) or published.
// To the asker every unserved request is ordinary silence; the error exists so
// the hosting process can tell a broken custodian from ambient noise.
// Observability, nothing more.
type MemoryWitness struct {
	CoverageFrom time.Time
	Answer       func(q MemoryQueryRequest) []MemoryAnswerDraft
	Fetch        func(topic, opID string) (record.Exhibit, bool)
	OnServed     func(kind string, n int, err error)
}

// RespondMemory serves memory as c's persona until ctx is cancelled. Serving is a
// habit, not a role: any number of witnesses may run, none coordinates with
// another, several may disagree (which is honest), and stopping one changes
// nothing for the rest. Replies are signed when the client is keyed, over the
// service binding like every service reply.
func RespondMemory(ctx context.Context, c *realm.Client, w MemoryWitness) error {
	if c.Persona() == "" {
		return fmt.Errorf("topic: serving memory requires a persona")
	}
	if w.Answer == nil && w.Fetch == nil {
		return fmt.Errorf("topic: a memory witness needs at least one capability (Answer or Fetch)")
	}
	nc := c.Conn()

	served := func(kind string, n int, err error) {
		if w.OnServed != nil {
			w.OnServed(kind, n, err)
		}
	}

	// Plain subscribe — deliberately NO queue group: every witness must hear every
	// request; the asker's merge is the only aggregation point.
	sub, err := nc.Subscribe(SvcMemorySubject, func(msg *nats.Msg) {
		rec, perr := record.Parse(msg.Header, msg.Data)
		if perr != nil || msg.Reply == "" {
			served("", 0, fmt.Errorf("topic: unreadable or reply-less memory request"))
			return
		}
		switch rec.Type {
		case TypeMemoryQuery:
			if w.Answer == nil {
				return // not this witness's capability — silence, not a skip
			}
			var qp MemoryQueryPayload
			if json.Unmarshal(rec.Payload, &qp) != nil || strings.TrimSpace(qp.Query) == "" {
				served("query", 0, fmt.Errorf("topic: malformed memory query"))
				return
			}
			if !qp.Deadline.IsZero() && time.Now().After(qp.Deadline) {
				// Stale: the answer would be ignored anyway.
				served("query", 0, fmt.Errorf("topic: stale memory query (asker's deadline passed)"))
				return
			}
			req := MemoryQueryRequest{Query: qp.Query}
			if qp.Scope != nil {
				req.Topics = qp.Scope.Topics
				req.After = qp.Scope.After
			}
			sent := 0
			var replyErr error
			for _, d := range w.Answer(req) {
				if strings.TrimSpace(d.Answer) == "" {
					continue
				}
				reply, _, berr := buildOpMsg(c, msg.Reply, ServiceMemory, TypeMemoryAnswer,
					MemoryAnswerPayload{Answer: d.Answer, Citations: d.Citations, CoverageFrom: w.CoverageFrom}, nil, "")
				if berr != nil {
					if replyErr == nil {
						replyErr = fmt.Errorf("topic: memory answer not sent: %w", berr)
					}
					continue
				}
				if perr := nc.PublishMsg(reply); perr != nil {
					if replyErr == nil {
						replyErr = fmt.Errorf("topic: publish memory answer: %w", perr)
					}
					continue
				}
				sent++
			}
			// A witness that HAD answers but could not send them all must be
			// distinguishable from one with nothing to say: the asker sees
			// silence either way, the host must not (FR-012).
			served("query", sent, replyErr)
		case TypeMemoryFetch:
			if w.Fetch == nil {
				return
			}
			var fp MemoryFetchPayload
			if json.Unmarshal(rec.Payload, &fp) != nil || fp.Topic == "" || fp.OpID == "" {
				served("fetch", 0, fmt.Errorf("topic: malformed memory fetch"))
				return
			}
			if !fp.Deadline.IsZero() && time.Now().After(fp.Deadline) {
				served("fetch", 0, fmt.Errorf("topic: stale memory fetch (asker's deadline passed)"))
				return
			}
			ex, ok := w.Fetch(fp.Topic, fp.OpID)
			if !ok {
				served("fetch", 0, nil) // silence is cheaper than noise
				return
			}
			reply, _, berr := buildOpMsg(c, msg.Reply, ServiceMemory, TypeMemoryExhibit,
				MemoryExhibitPayload{Exhibit: ex}, nil, "")
			if berr != nil {
				served("fetch", 0, fmt.Errorf("topic: memory exhibit not sent: %w", berr))
				return
			}
			if perr := nc.PublishMsg(reply); perr != nil {
				served("fetch", 0, fmt.Errorf("topic: publish memory exhibit: %w", perr))
				return
			}
			served("fetch", 1, nil)
		}
	})
	if err != nil {
		return fmt.Errorf("topic: subscribe memory: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Flush so the server has registered the interest: once this returns, the
	// witness truly hears every subsequent request.
	if err := nc.Flush(); err != nil {
		return fmt.Errorf("topic: establish memory subscription: %w", err)
	}

	<-ctx.Done()
	return nil
}

func clampMemoryTimeout(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultMemoryTimeout
	case d < MinMemoryTimeout:
		return MinMemoryTimeout
	case d > MaxMemoryTimeout:
		return MaxMemoryTimeout
	default:
		return d
	}
}
