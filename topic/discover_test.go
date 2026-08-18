package topic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

func boardEntry(path, name, subject string, tags []string, lc Lifecycle) BoardEntry {
	return BoardEntry{
		Path:         path,
		Announcement: Announcement{TopicID: path, Name: name, SubjectMatter: subject, Tags: tags},
		Lifecycle:    lc,
	}
}

func TestMatchEntries(t *testing.T) {
	entries := []BoardEntry{
		boardEntry("vat-q2-x7m2", "Q2 VAT filing", "the filing", []string{"finance"}, Active),
		boardEntry("onboard-a1b2", "Onboarding", "new joiners", []string{"people"}, Closed),
		boardEntry("vat-q1-b4k9", "Q1 VAT filing", "", []string{"Finance", "archive"}, Archived),
	}

	cases := []struct {
		name  string
		query string
		limit int
		want  []string
	}{
		{"by name", "vat", 10, []string{"vat-q2-x7m2", "vat-q1-b4k9"}},
		{"case-insensitive", "VAT", 10, []string{"vat-q2-x7m2", "vat-q1-b4k9"}},
		{"by subject matter", "joiners", 10, []string{"onboard-a1b2"}},
		{"by tag (case-insensitive)", "finance", 10, []string{"vat-q2-x7m2", "vat-q1-b4k9"}},
		{"empty query matches all", "", 10, []string{"vat-q2-x7m2", "onboard-a1b2", "vat-q1-b4k9"}},
		{"limit caps", "", 2, []string{"vat-q2-x7m2", "onboard-a1b2"}},
		{"no match", "quantum", 10, nil},
	}
	for _, c := range cases {
		got := matchEntries(entries, c.query, c.limit)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d matches, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i, e := range got {
			if e.Path != c.want[i] {
				t.Errorf("%s[%d] = %s, want %s", c.name, i, e.Path, c.want[i])
			}
		}
	}

	// Matched entries carry the board's identity fields.
	m := matchEntries(entries, "onboarding", 10)
	if len(m) != 1 || m[0].Name != "Onboarding" || m[0].Lifecycle != Closed || m[0].SubjectMatter != "new joiners" {
		t.Errorf("entry fields lost: %+v", m)
	}
}

func TestMergeReply(t *testing.T) {
	results := map[string]*DiscoverResult{}
	var order []string
	entry := DiscoverEntry{Path: "vat-q2", Name: "Q2 VAT"}

	mergeReply(results, &order, "architect", SigVerified, []DiscoverEntry{entry})
	mergeReply(results, &order, "historian", SigUnsigned, []DiscoverEntry{{Path: "vat-q2", Name: "Q2 VAT (as I know it)"}})
	mergeReply(results, &order, "architect", SigVerified, []DiscoverEntry{entry}) // duplicate reply

	if len(order) != 1 {
		t.Fatalf("order = %v, want one path", order)
	}
	r := results["vat-q2"]
	if len(r.Answers) != 2 {
		t.Fatalf("answers = %+v, want architect + historian once each", r.Answers)
	}
	if r.Name != "Q2 VAT" {
		t.Errorf("first-seen fields must win, got %q", r.Name)
	}
	if r.Answers[0].Persona != "architect" || r.Answers[0].Sig != SigVerified {
		t.Errorf("answer[0] = %+v", r.Answers[0])
	}
	if r.Answers[1].Persona != "historian" || r.Answers[1].Sig != SigUnsigned {
		t.Errorf("answer[1] = %+v", r.Answers[1])
	}
}

// scriptedAnswerer subscribes raw on the discovery subject and replies with the
// given entries as persona (signed when key != nil). It returns a stop func.
func scriptedAnswerer(t *testing.T, url, persona string, key *identity.SigningKey, entries []DiscoverEntry, delay time.Duration) func() {
	t.Helper()
	responderRealmKey := testRealmKey(t, url)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := nc.Subscribe(SvcDiscoverSubject, func(msg *nats.Msg) {
		if msg.Reply == "" {
			return
		}
		time.Sleep(delay)
		payload, _ := json.Marshal(DiscoverReplyPayload{Matches: entries})
		rec := record.Record{
			ID: record.NewID(), Author: persona, Acting: persona, Type: TypeDiscoverReply,
			Timestamp: time.Now().UTC(), Payload: payload,
		}
		if key != nil {
			canonical, cerr := rec.Canonical(responderRealmKey, ServiceDiscover)
			if cerr != nil {
				return
			}
			sig, serr := key.Sign(canonical)
			if serr != nil {
				return
			}
			rec.Signature = sig
		}
		headers, body, berr := rec.Build()
		if berr != nil {
			return
		}
		_ = nc.PublishMsg(&nats.Msg{Subject: msg.Reply, Header: nats.Header(headers), Data: body})
	})
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	// Guarantee the server has registered the interest before the test publishes.
	if err := nc.Flush(); err != nil {
		nc.Close()
		t.Fatal(err)
	}
	return func() { _ = sub.Unsubscribe(); nc.Close() }
}

// TestDiscoverMergesAndAttributes (SC-001/SC-002/SC-004): overlapping answers merge
// to one entry credited to both, with per-answer verification status.
func TestDiscoverMergesAndAttributes(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}

	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	shared := DiscoverEntry{Path: "vat-q2-x7m2", Name: "Q2 VAT filing", Lifecycle: Active}
	only := DiscoverEntry{Path: "vat-q1-b4k9", Name: "Q1 VAT filing", Lifecycle: Archived}

	stop1 := scriptedAnswerer(t, url, "architect", key, []DiscoverEntry{shared, only}, 0)
	defer stop1()
	stop2 := scriptedAnswerer(t, url, "historian", nil, []DiscoverEntry{shared}, 0)
	defer stop2()

	kr := &identity.Keyring{Keys: map[string][]string{"architect": {key.PublicKey()}}}
	results, err := Discover(ctx, asker, DiscoverInput{Query: "vat", Timeout: 500 * time.Millisecond}, kr)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2 (merged)", len(results))
	}

	byPath := map[string]DiscoverResult{}
	for _, r := range results {
		byPath[r.Path] = r
	}
	sharedRes := byPath["vat-q2-x7m2"]
	if len(sharedRes.Answers) != 2 {
		t.Fatalf("shared topic answers = %+v, want both answerers", sharedRes.Answers)
	}
	sigs := map[string]SigStatus{}
	for _, a := range sharedRes.Answers {
		sigs[a.Persona] = a.Sig
	}
	if sigs["architect"] != SigVerified {
		t.Errorf("architect's answer sig = %s, want verified", sigs["architect"])
	}
	if sigs["historian"] != SigUnsigned {
		t.Errorf("historian's answer sig = %s, want unsigned", sigs["historian"])
	}
	if len(byPath["vat-q1-b4k9"].Answers) != 1 {
		t.Errorf("solo topic answers = %+v", byPath["vat-q1-b4k9"].Answers)
	}
}

// TestDiscoverSilenceIsAnAnswer (SC-003): zero responders resolve to empty at the
// deadline plus a small constant, never an error.
func TestDiscoverSilenceIsAnAnswer(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	results, err := Discover(ctx, asker, DiscoverInput{Query: "anything", Timeout: 300 * time.Millisecond}, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("silent ask errored: %v", err)
	}
	if results != nil {
		t.Errorf("silent ask results = %v, want nil", results)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("silent ask took %v, want deadline + small constant", elapsed)
	}
}

// TestDiscoverLateAndMalformedRepliesIgnored: replies past the deadline never land;
// malformed replies are skipped.
func TestDiscoverLateAndMalformedRepliesIgnored(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}

	// One answerer replies after the asker's deadline.
	stopLate := scriptedAnswerer(t, url, "slowpoke", nil,
		[]DiscoverEntry{{Path: "late-topic", Name: "Too Late"}}, 600*time.Millisecond)
	defer stopLate()

	// One "answerer" replies with garbage bytes immediately.
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	garbageSub, err := nc.Subscribe(SvcDiscoverSubject, func(msg *nats.Msg) {
		if msg.Reply != "" {
			_ = nc.Publish(msg.Reply, []byte("not a record at all"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = garbageSub.Unsubscribe() }()
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	results, err := Discover(ctx, asker, DiscoverInput{Query: "late", Timeout: 300 * time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if results != nil {
		t.Errorf("late/malformed replies leaked into results: %v", results)
	}
}

// servedEvent is one onServed notification: what was sent, and why nothing was
// when the request could not be served.
type servedEvent struct {
	sent int
	err  error
}

// startResponder runs RespondDiscovery for persona and returns a stop func plus a
// channel of served notifications.
func startResponder(t *testing.T, url, persona string) (func(), <-chan servedEvent) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := connectClient(t, url, persona)
	servedCh := make(chan servedEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = RespondDiscovery(ctx, c, func(_ string, sent int, err error) { servedCh <- servedEvent{sent, err} })
	}()
	// Give the subscription a moment to establish.
	time.Sleep(50 * time.Millisecond)
	return func() { cancel(); <-done }, servedCh
}

// TestRespondDiscoveryRoundTrip (US2 scenarios 1 & 3): real responders answer from
// their own board projections; several answer independently and the merge credits
// each.
func TestRespondDiscoveryRoundTrip(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	starter := connectClient(t, url, "starter")
	if _, err := starter.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTopic(ctx, starter, StartTopicInput{Name: "Q2 VAT filing", SubjectMatter: "filing", Tags: []string{"finance"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTopic(ctx, starter, StartTopicInput{Name: "Onboarding"}); err != nil {
		t.Fatal(err)
	}

	stop1, _ := startResponder(t, url, "architect")
	defer stop1()
	stop2, _ := startResponder(t, url, "historian")
	defer stop2()

	results, err := Discover(ctx, starter, DiscoverInput{Query: "vat", Timeout: 700 * time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want the one VAT topic", results)
	}
	r := results[0]
	if r.Name != "Q2 VAT filing" || r.Lifecycle != Proposed {
		t.Errorf("entry = %+v", r.DiscoverEntry)
	}
	if len(r.Answers) != 2 {
		t.Errorf("answers = %+v, want both responders independently", r.Answers)
	}

	// Scenario 4: stop both — asks degrade to empty, the board still works.
	stop1()
	stop2()
	silent, err := Discover(ctx, starter, DiscoverInput{Query: "vat", Timeout: 300 * time.Millisecond}, nil)
	if err != nil || silent != nil {
		t.Errorf("after stopping responders: results=%v err=%v", silent, err)
	}
	entries, err := Board(ctx, starter)
	if err != nil || len(entries) != 2 {
		t.Errorf("board after responders stopped: %d entries, %v", len(entries), err)
	}
}

// TestRespondDiscoverySilentOnNoMatch (SC-005): a no-match request produces zero
// replies on the wire, and the responder keeps serving afterwards.
func TestRespondDiscoverySilentOnNoMatch(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTopic(ctx, asker, StartTopicInput{Name: "Only Topic"}); err != nil {
		t.Fatal(err)
	}

	stop, served := startResponder(t, url, "architect")
	defer stop()

	// Raw wire assertion: subscribe our own inbox, send a no-match request, and
	// verify nothing arrives.
	nc := asker.Conn()
	inbox := nc.NewRespInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	reqMsg, _, err := buildOpMsg(asker, SvcDiscoverSubject, ServiceDiscover, TypeDiscover,
		DiscoverPayload{Query: "no such thing"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	reqMsg.Reply = inbox
	if err := nc.PublishMsg(reqMsg); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-served:
		if ev.sent != 0 || ev.err != nil {
			t.Errorf("served = %+v, want 0 sent, no error (silent no-match)", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("responder never saw the request")
	}
	// The inbox may receive the stream's pub-ack for the captured request — that is
	// not an answer. Fail only if an actual discover.reply record arrives.
	for {
		msg, err := sub.NextMsg(200 * time.Millisecond)
		if err != nil {
			break
		}
		if rec, perr := record.Parse(msg.Header, msg.Data); perr == nil && rec.Type == TypeDiscoverReply {
			t.Errorf("no-match request produced a wire reply: %s", msg.Data)
		}
	}

	// Malformed request: raw garbage; the responder skips and keeps serving.
	if err := nc.PublishRequest(SvcDiscoverSubject, inbox, []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-served:
		if ev.sent != 0 || ev.err == nil {
			t.Errorf("malformed request served = %+v, want 0 sent with an error (skipped)", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("responder died on the malformed request")
	}

	// Still serving: a matching ask round-trips.
	results, err := Discover(ctx, asker, DiscoverInput{Query: "only", Timeout: 700 * time.Millisecond}, nil)
	if err != nil || len(results) != 1 {
		t.Errorf("responder stopped serving after malformed input: %v, %v", results, err)
	}
}

// TestRespondDiscoveryWithCustomAnswerer: a caller-supplied answerer's entries reach
// the asker through the unchanged mechanism.
func TestRespondDiscoveryWithCustomAnswerer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}

	answerer := connectClient(t, url, "oracle")
	go func() {
		_ = RespondDiscoveryWith(ctx, answerer, func(query string, limit int) []DiscoverEntry {
			if query != "anything" || limit <= 0 {
				return nil
			}
			return []DiscoverEntry{{Path: "custom-topic", Name: "From A Custom Projection"}}
		}, nil)
	}()
	time.Sleep(50 * time.Millisecond)

	results, err := Discover(ctx, asker, DiscoverInput{Query: "anything", Timeout: 500 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "custom-topic" || results[0].Answers[0].Persona != "oracle" {
		t.Errorf("custom answerer results = %+v", results)
	}
}

// TestRespondDiscoveryFailingSignerStaysSilent (US2/FR-012, SC-005): a responder
// whose signer cannot sign answers nothing — the asker sees the protocol's
// ordinary silence — while the host observes the failure as an error naming the
// custodian's cause. No unsigned reply ever.
func TestRespondDiscoveryFailingSignerStaysSilent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}

	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	responder := connectClientSigned(t, url, "oracle", &delegateSigner{key: key, err: errors.New("vault unreachable")})
	served := make(chan servedEvent, 16)
	go func() {
		_ = RespondDiscoveryWith(ctx, responder, func(_ string, _ int) []DiscoverEntry {
			return []DiscoverEntry{{Path: "would-match", Name: "It Would Match"}}
		}, func(_ string, sent int, err error) { served <- servedEvent{sent, err} })
	}()

	// Retry until the responder demonstrably processed a request; every round
	// must come back silent to the asker.
	heard := false
	for range 20 {
		results, derr := Discover(ctx, asker, DiscoverInput{Query: "anything", Timeout: 300 * time.Millisecond}, nil)
		if derr != nil {
			t.Fatalf("discover: %v", derr)
		}
		if len(results) != 0 {
			t.Fatalf("got %d replies from a responder that cannot sign", len(results))
		}
		select {
		case ev := <-served:
			if ev.sent != 0 {
				t.Fatalf("responder sent %d replies with a failing signer", ev.sent)
			}
			if ev.err == nil || !strings.Contains(ev.err.Error(), "vault unreachable") {
				t.Fatalf("served err = %v, want the custodian's cause", ev.err)
			}
			heard = true
		default:
		}
		if heard {
			return
		}
	}
	t.Fatal("responder never processed a request")
}

// TestDiscoverWrongKeyAnswerIsFailed (SC-004): a reply signed with a key that is not
// the answerer's pinned key is labelled failed — and still delivered.
func TestDiscoverWrongKeyAnswerIsFailed(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	asker := connectClient(t, url, "asker")
	if _, err := asker.Provision(ctx); err != nil {
		t.Fatal(err)
	}

	realKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	stop := scriptedAnswerer(t, url, "architect", wrongKey,
		[]DiscoverEntry{{Path: "vat-q2", Name: "Q2 VAT"}}, 0)
	defer stop()

	kr := &identity.Keyring{Keys: map[string][]string{"architect": {realKey.PublicKey()}}}
	results, err := Discover(ctx, asker, DiscoverInput{Query: "vat", Timeout: 400 * time.Millisecond}, kr)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || len(results[0].Answers) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Answers[0].Sig != SigFailed {
		t.Errorf("wrong-key answer sig = %s, want failed (delivered, labelled)", results[0].Answers[0].Sig)
	}
}

// 014/US3: a discovery round-trip leaves zero retained messages in either stream —
// requests and replies are transient by construction now that no stream captures
// the service subjects.
func TestDiscoveryLeavesNoResidue(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	starter := connectClient(t, url, "starter")
	if _, err := starter.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTopic(ctx, starter, StartTopicInput{Name: "Q2 VAT filing"}); err != nil {
		t.Fatal(err)
	}
	stop, _ := startResponder(t, url, "architect")
	defer stop()

	countAll := func() uint64 {
		t.Helper()
		var total uint64
		for _, name := range []string{realm.StreamName, realm.NotifyStreamName} {
			s, err := starter.JetStream().Stream(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			info, err := s.Info(ctx)
			if err != nil {
				t.Fatal(err)
			}
			total += info.State.Msgs
		}
		return total
	}

	before := countAll()
	for i := 0; i < 3; i++ {
		results, err := Discover(ctx, starter, DiscoverInput{Query: "vat", Timeout: 500 * time.Millisecond}, nil)
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("results = %+v, want the VAT topic", results)
		}
	}
	if after := countAll(); after != before {
		t.Errorf("stored messages grew %d → %d across discovery round-trips; must be zero growth", before, after)
	}
}
