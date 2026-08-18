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

// rawMemoryResponder subscribes raw on the memory subject (its own connection,
// flushed before returning, so it is live) and calls respond for each parseable
// request that carries a reply inbox. It returns a stop func.
func rawMemoryResponder(t *testing.T, url string, respond func(req record.Record, reply string, nc *nats.Conn)) func() {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect responder: %v", err)
	}
	sub, err := nc.Subscribe(SvcMemorySubject, func(msg *nats.Msg) {
		rec, perr := record.Parse(msg.Header, msg.Data)
		if perr != nil || msg.Reply == "" {
			return
		}
		respond(rec, msg.Reply, nc)
	})
	if err != nil {
		t.Fatalf("subscribe responder: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush responder: %v", err)
	}
	return func() { _ = sub.Unsubscribe(); nc.Close() }
}

// publishAnswer builds a memory.answer record as persona (signed over the service
// binding when key != nil) and publishes it to reply.
func publishAnswer(t *testing.T, nc *nats.Conn, reply, realmName, persona string, key *identity.SigningKey, ap MemoryAnswerPayload) {
	t.Helper()
	payload, _ := json.Marshal(ap)
	rec := record.Record{
		ID: record.NewID(), Author: persona, Acting: persona, Type: TypeMemoryAnswer,
		Timestamp: time.Now().UTC(), Payload: payload,
	}
	if key != nil {
		canonical, err := rec.Canonical(realmName, ServiceMemory)
		if err != nil {
			t.Fatalf("canonical: %v", err)
		}
		sig, err := key.Sign(canonical)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		rec.Signature = sig
	}
	h, b, err := rec.Build()
	if err != nil {
		t.Fatalf("build answer: %v", err)
	}
	if err := nc.PublishMsg(&nats.Msg{Subject: reply, Header: nats.Header(h), Data: b}); err != nil {
		t.Fatalf("publish answer: %v", err)
	}
}

func TestClampMemoryTimeout(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                     DefaultMemoryTimeout,
		-time.Second:          DefaultMemoryTimeout,
		50 * time.Millisecond: MinMemoryTimeout,
		40 * time.Second:      MaxMemoryTimeout,
		5 * time.Second:       5 * time.Second,
	}
	for in, want := range cases {
		if got := clampMemoryTimeout(in); got != want {
			t.Errorf("clampMemoryTimeout(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestMemoryQueryNeedsAQuestion(t *testing.T) {
	c := provisionedClient(t, "asker")
	if _, err := MemoryQuery(context.Background(), c, MemoryQueryInput{Query: "   "}, nil); err == nil {
		t.Error("empty query must fail loudly before anything is published")
	}
}

// TestMemoryQuerySilence: a realm with no witnesses resolves to a clean empty
// result at the deadline — silence is an honest answer (US1 scenario 2).
func TestMemoryQuerySilence(t *testing.T) {
	c := provisionedClient(t, "asker")
	start := time.Now()
	res, err := MemoryQuery(context.Background(), c, MemoryQueryInput{Query: "anyone?", Timeout: MinMemoryTimeout}, nil)
	if err != nil {
		t.Fatalf("silence must not be an error: %v", err)
	}
	if res == nil || len(res.Answers) != 0 {
		t.Errorf("result = %+v, want empty", res)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("silent query took %v — must complete near its deadline", elapsed)
	}
}

// TestMemoryQueryGathersAndGrades is the US1 round-trip: answers arrive merged and
// attributed, and every citation is graded by actually resolving it, never by
// trusting the witness (scenarios 1, 3, 4; SC-001, SC-002).
func TestMemoryQueryGathersAndGrades(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClient(t, url, "asker")
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	rk := testRealmKey(t, url)
	_ = rk
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "decisions", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "weekly cadence it is")
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	coverage := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	stop := rawMemoryResponder(t, url, func(req record.Record, reply string, nc *nats.Conn) {
		if req.Type != TypeMemoryQuery {
			return
		}
		publishAnswer(t, nc, reply, rk, "historian", nil, MemoryAnswerPayload{
			Answer: "weekly, decided in the topic",
			Citations: []MemoryCitation{
				{Topic: h.Path(), OpID: turnID},
				{Topic: h.Path(), OpID: record.NewID()}, // fabricated or compacted — must not grade fact
				{Topic: "no-such-topic", OpID: record.NewID()},
			},
			CoverageFrom: coverage,
		})
		publishAnswer(t, nc, reply, rk, "scribbler", nil, MemoryAnswerPayload{
			Answer: "I think it was monthly", // conflicting and uncited: gossip, still shown
		})
	})
	defer stop()

	res, err := MemoryQuery(ctx, c, MemoryQueryInput{Query: "cadence?", Timeout: 500 * time.Millisecond}, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Answers) != 2 {
		t.Fatalf("answers = %+v, want 2", res.Answers)
	}
	first := res.Answers[0]
	if first.Witness != "historian" || first.Sig != SigUnsigned || !first.CoverageFrom.Equal(coverage) {
		t.Errorf("attribution lost: %+v", first)
	}
	wantGrades := []MemoryGrade{GradeFact, GradeUnverifiable, GradeUnverifiable}
	if len(first.Citations) != len(wantGrades) {
		t.Fatalf("citations = %+v", first.Citations)
	}
	for i, want := range wantGrades {
		if first.Citations[i].Grade != want {
			t.Errorf("citation[%d] grade = %s, want %s", i, first.Citations[i].Grade, want)
		}
	}
	second := res.Answers[1]
	if second.Witness != "scribbler" || len(second.Citations) != 0 {
		t.Errorf("conflicting uncited answer must appear, uncited: %+v", second)
	}
}

// TestMemoryQuerySignatureRules: a failed witness signature is discarded as
// tampering; unsigned and unknown-key answers are kept with their status visible.
func TestMemoryQuerySignatureRules(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClient(t, url, "asker")
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	rk := testRealmKey(t, url)
	_ = rk

	goodKey, _ := identity.GenerateSigningKey()
	strangerKey, _ := identity.GenerateSigningKey()
	impostorKey, _ := identity.GenerateSigningKey()
	pinnedKey, _ := identity.GenerateSigningKey() // what the asker pinned for "impostor"

	stop := rawMemoryResponder(t, url, func(req record.Record, reply string, nc *nats.Conn) {
		if req.Type != TypeMemoryQuery {
			return
		}
		publishAnswer(t, nc, reply, rk, "goodwit", goodKey, MemoryAnswerPayload{Answer: "verified testimony"})
		publishAnswer(t, nc, reply, rk, "stranger", strangerKey, MemoryAnswerPayload{Answer: "signed by an unknown key"})
		publishAnswer(t, nc, reply, rk, "impostor", impostorKey, MemoryAnswerPayload{Answer: "wearing a stolen name"})
	})
	defer stop()

	kr := &identity.Keyring{Keys: map[string][]string{
		"goodwit":  {goodKey.PublicKey()},
		"impostor": {pinnedKey.PublicKey()},
	}}
	res, err := MemoryQuery(ctx, c, MemoryQueryInput{Query: "who speaks?", Timeout: 500 * time.Millisecond}, kr)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := map[string]SigStatus{}
	for _, a := range res.Answers {
		got[a.Witness] = a.Sig
	}
	if got["goodwit"] != SigVerified {
		t.Errorf("goodwit = %s, want verified", got["goodwit"])
	}
	if got["stranger"] != SigUnknownKey {
		t.Errorf("stranger = %s, want unknown-key (kept, status visible)", got["stranger"])
	}
	if _, present := got["impostor"]; present {
		t.Error("an answer whose signature fails against the pinned chain must be discarded")
	}
}

// TestMemoryQueryCapAndLateness: the 100-answer safety cap holds, and a witness
// replying after the deadline is simply absent (US1 scenario 6).
func TestMemoryQueryCapAndLateness(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClient(t, url, "asker")
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	rk := testRealmKey(t, url)
	_ = rk

	stop := rawMemoryResponder(t, url, func(req record.Record, reply string, nc *nats.Conn) {
		if req.Type != TypeMemoryQuery {
			return
		}
		for range MaxMemoryAnswers + 20 {
			publishAnswer(t, nc, reply, rk, "flooder", nil, MemoryAnswerPayload{Answer: "again"})
		}
	})
	res, err := MemoryQuery(ctx, c, MemoryQueryInput{Query: "flood", Timeout: time.Second}, nil)
	stop()
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Answers) != MaxMemoryAnswers {
		t.Errorf("answers = %d, want the cap %d", len(res.Answers), MaxMemoryAnswers)
	}

	// Pre-build the late answer so the delayed goroutine never touches t; wait for
	// it before the test returns.
	latePayload, _ := json.Marshal(MemoryAnswerPayload{Answer: "wait for me"})
	lateRec := record.Record{ID: record.NewID(), Author: "latecomer", Acting: "latecomer", Type: TypeMemoryAnswer, Timestamp: time.Now().UTC(), Payload: latePayload}
	lateHd, lateBody, err := lateRec.Build()
	if err != nil {
		t.Fatalf("build late answer: %v", err)
	}
	done := make(chan struct{})
	stopLate := rawMemoryResponder(t, url, func(req record.Record, reply string, nc *nats.Conn) {
		if req.Type != TypeMemoryQuery {
			return
		}
		go func() {
			defer close(done)
			time.Sleep(400 * time.Millisecond)
			_ = nc.PublishMsg(&nats.Msg{Subject: reply, Header: nats.Header(lateHd), Data: lateBody})
		}()
	})
	res, err = MemoryQuery(ctx, c, MemoryQueryInput{Query: "late", Timeout: MinMemoryTimeout}, nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Answers) != 0 {
		t.Errorf("late answer must be absent, got %+v", res.Answers)
	}
	<-done
	stopLate()
}

// TestRespondMemoryCapabilities: the two capabilities are independently optional —
// a fetch-only keeper never answers queries (silence, not an error), stale
// requests are skipped with the reason in OnServed's error, and a witness needs at least one
// capability (US3 scenarios 1, 2).
func TestRespondMemoryCapabilities(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClientSignedProvisioned(t, url, "owner")
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "kept", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "keep this")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	kept, err := CaptureExhibit(ctx, c, h.Path(), turnID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	if err := RespondMemory(ctx, c, MemoryWitness{}); err == nil {
		t.Error("a witness with no capability must be refused")
	}

	served := make(chan servedEvent, 16)
	wc := connectClient(t, url, "keeper")
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = RespondMemory(wctx, wc, MemoryWitness{
			Fetch: func(topicPath, opID string) (record.Exhibit, bool) {
				if topicPath == h.Path() && opID == turnID {
					return kept, true
				}
				return record.Exhibit{}, false
			},
			OnServed: func(kind string, n int, err error) {
				if kind == "fetch" {
					served <- servedEvent{n, err}
				}
			},
		})
	}()

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	askFetch := func(topicPath, opID string, deadline time.Time) *nats.Msg {
		t.Helper()
		inbox := nc.NewRespInbox()
		sub, err := nc.SubscribeSync(inbox)
		if err != nil {
			t.Fatalf("inbox: %v", err)
		}
		defer func() { _ = sub.Unsubscribe() }()
		payload, _ := json.Marshal(MemoryFetchPayload{Topic: topicPath, OpID: opID, Deadline: deadline})
		rec := record.Record{ID: record.NewID(), Author: "prober", Acting: "prober", Type: TypeMemoryFetch, Timestamp: time.Now().UTC(), Payload: payload}
		hd, b, _ := rec.Build()
		msg := &nats.Msg{Subject: SvcMemorySubject, Header: nats.Header(hd), Data: b, Reply: inbox}
		if err := nc.PublishMsg(msg); err != nil {
			t.Fatalf("publish: %v", err)
		}
		reply, err := sub.NextMsg(300 * time.Millisecond)
		if err != nil {
			return nil
		}
		return reply
	}

	// The subscription needs a moment to go live; retry until the fetch serves.
	var reply *nats.Msg
	for range 20 {
		if reply = askFetch(h.Path(), turnID, time.Now().Add(time.Second)); reply != nil {
			break
		}
	}
	if reply == nil {
		t.Fatal("fetch capability never served")
	}
	rec, err := record.Parse(reply.Header, reply.Data)
	if err != nil || rec.Type != TypeMemoryExhibit {
		t.Fatalf("reply = %+v (err %v), want a memory.exhibit", rec, err)
	}
	<-served // the successful fetch

	// Unknown op: silence with OnServed(0, nil) — a no-match, not a failure.
	if got := askFetch(h.Path(), record.NewID(), time.Now().Add(time.Second)); got != nil {
		t.Error("a keeper with nothing must stay silent")
	}
	if ev := <-served; ev.sent != 0 || ev.err != nil {
		t.Errorf("OnServed = %+v, want 0 sent, no error for a silent no-match", ev)
	}

	// Stale deadline: skipped, with the reason in the error.
	if got := askFetch(h.Path(), turnID, time.Now().Add(-time.Second)); got != nil {
		t.Error("a stale fetch must be skipped")
	}
	if ev := <-served; ev.sent != 0 || ev.err == nil || !strings.Contains(ev.err.Error(), "stale") {
		t.Errorf("OnServed = %+v, want 0 sent with a stale-request error", ev)
	}

	// Queries: not this witness's capability — pure silence, no OnServed event.
	inbox := nc.NewRespInbox()
	qsub, _ := nc.SubscribeSync(inbox)
	defer func() { _ = qsub.Unsubscribe() }()
	payload, _ := json.Marshal(MemoryQueryPayload{Query: "anything?", Deadline: time.Now().Add(time.Second)})
	qrec := record.Record{ID: record.NewID(), Author: "prober", Acting: "prober", Type: TypeMemoryQuery, Timestamp: time.Now().UTC(), Payload: payload}
	hd, b, _ := qrec.Build()
	if err := nc.PublishMsg(&nats.Msg{Subject: SvcMemorySubject, Header: nats.Header(hd), Data: b, Reply: inbox}); err != nil {
		t.Fatalf("publish query: %v", err)
	}
	if _, err := qsub.NextMsg(250 * time.Millisecond); err == nil {
		t.Error("fetch-only witness must not answer queries")
	}
	select {
	case ev := <-served:
		t.Errorf("unexpected OnServed(%+v) for an unserved capability", ev)
	default:
	}
}

// connectClientSignedProvisioned is a signed client on a provisioned realm.
func connectClientSignedProvisioned(t *testing.T, url, persona string) *realm.Client {
	t.Helper()
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	c := connectClientSigned(t, url, persona, key)
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return c
}

func TestFetchExhibitLiveFirst(t *testing.T) {
	ctx := context.Background()
	key, _ := identity.GenerateSigningKey()
	url := testServer(t)
	c := connectClientSigned(t, url, "owner", key)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "live", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "still here")
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	// No witness anywhere: the live stream answers directly.
	res, err := FetchExhibit(ctx, c, h.Path(), turnID, MinMemoryTimeout, keyringFor("owner", key))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res == nil || res.Source != "live" || res.Verdict != SigVerified {
		t.Fatalf("result = %+v, want live + verified", res)
	}

	// A compacted-or-nonexistent op with no witnesses: silence, not an error.
	res, err = FetchExhibit(ctx, c, h.Path(), record.NewID(), MinMemoryTimeout, nil)
	if err != nil {
		t.Fatalf("fetch silence: %v", err)
	}
	if res != nil {
		t.Errorf("result = %+v, want nil (silence is an answer)", res)
	}
}

// TestCompactionRecallEndToEnd is SC-004: an op physically removed by rollup —
// here a life.transition, whose id compaction consumes without baking — comes back
// as verifying evidence from any keeper that kept it.
func TestCompactionRecallEndToEnd(t *testing.T) {
	ctx := context.Background()
	key, _ := identity.GenerateSigningKey()
	url := testServer(t)
	c := connectClientSigned(t, url, "owner", key)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "forensics", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := h.PostTurn(ctx, "some content"); err != nil {
		t.Fatalf("post: %v", err)
	}
	transitionID, err := h.Transition(ctx, Closed)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}

	// The historian keeps the op while it is live (retention is not retrofittable).
	kept, err := CaptureExhibit(ctx, c, h.Path(), transitionID)
	if err != nil {
		t.Fatalf("capture while live: %v", err)
	}

	// Rollup physically removes the tail; the transition op is gone from the stream
	// (its lifecycle effect is baked, its id is not — op-level forensics need a keeper).
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if _, err := CaptureExhibit(ctx, c, h.Path(), transitionID); !errors.Is(err, ErrOpNotLive) {
		t.Fatalf("after rollup: err = %v, want ErrOpNotLive", err)
	}

	// The keeper serves it; the asker gets it back verifying.
	wc := connectClient(t, url, "historian")
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = RespondMemory(wctx, wc, MemoryWitness{
			CoverageFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Fetch: func(topicPath, opID string) (record.Exhibit, bool) {
				if topicPath == h.Path() && opID == transitionID {
					return kept, true
				}
				return record.Exhibit{}, false
			},
		})
	}()

	kr := keyringFor("owner", key)
	var res *ExhibitResult
	for range 20 { // witness liveness: retry until the subscription serves
		var ferr error
		res, ferr = FetchExhibit(ctx, c, h.Path(), transitionID, 300*time.Millisecond, kr)
		if ferr != nil {
			t.Fatalf("fetch: %v", ferr)
		}
		if res != nil {
			break
		}
	}
	if res == nil {
		t.Fatal("witness never served the exhibit")
	}
	if res.Verdict != SigVerified || res.Source != "historian" {
		t.Errorf("result = verdict %s from %s, want verified from historian", res.Verdict, res.Source)
	}
	rec, err := res.Exhibit.Record()
	if err != nil || rec.ID != transitionID || rec.Type != TypeLifeTransition {
		t.Errorf("recovered record = %+v (err %v)", rec, err)
	}
}

// TestFetchExhibitPreference: the first VERIFYING exhibit wins over an earlier
// unsigned one; an unsigned exhibit is returned only as fallback; an exhibit of a
// different op — however validly signed — is rejected (US3 scenario 4).
func TestFetchExhibitPreference(t *testing.T) {
	ctx := context.Background()
	key, _ := identity.GenerateSigningKey()
	url := testServer(t)
	c := connectClientSigned(t, url, "owner", key)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "preference", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := h.PostTurn(ctx, "content"); err != nil {
		t.Fatalf("post: %v", err)
	}
	targetID, err := h.Transition(ctx, Closed)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	signed, err := CaptureExhibit(ctx, c, h.Path(), targetID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	unsigned := signed
	unsigned.Headers = map[string][]string{}
	for k, v := range signed.Headers {
		if k == record.HeaderSig {
			continue
		}
		unsigned.Headers[k] = append([]string(nil), v...)
	}
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	kr := keyringFor("owner", key)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// sloppy answers instantly with the unsigned copy; careful takes its time but
	// brings the signed one.
	sloppy := connectClient(t, url, "sloppy")
	go func() {
		_ = RespondMemory(wctx, sloppy, MemoryWitness{
			Fetch: func(string, string) (record.Exhibit, bool) { return unsigned, true },
		})
	}()
	careful := connectClient(t, url, "careful")
	go func() {
		_ = RespondMemory(wctx, careful, MemoryWitness{
			Fetch: func(string, string) (record.Exhibit, bool) {
				time.Sleep(150 * time.Millisecond)
				return signed, true
			},
		})
	}()

	var res *ExhibitResult
	for range 20 {
		var ferr error
		res, ferr = FetchExhibit(ctx, c, h.Path(), targetID, 600*time.Millisecond, kr)
		if ferr != nil {
			t.Fatalf("fetch: %v", ferr)
		}
		if res != nil && res.Verdict == SigVerified {
			break
		}
	}
	if res == nil || res.Verdict != SigVerified || res.Source != "careful" {
		t.Fatalf("result = %+v, want the verifying exhibit from careful", res)
	}

	// Only the unsigned keeper left: the fallback is returned, honestly unsigned.
	cancel()
	wctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	sloppy2 := connectClient(t, url, "sloppy-two")
	go func() {
		_ = RespondMemory(wctx2, sloppy2, MemoryWitness{
			Fetch: func(string, string) (record.Exhibit, bool) { return unsigned, true },
		})
	}()
	for range 20 {
		var ferr error
		res, ferr = FetchExhibit(ctx, c, h.Path(), targetID, 300*time.Millisecond, kr)
		if ferr != nil {
			t.Fatalf("fetch: %v", ferr)
		}
		if res != nil {
			break
		}
	}
	if res == nil || res.Verdict != SigUnsigned || res.Source != "sloppy-two" {
		t.Fatalf("result = %+v, want the unsigned fallback", res)
	}

	// A witness serving a different op's (valid!) exhibit for this fetch is
	// malformed — rejected, silence results.
	cancel2()
	otherID, err := StartTopic(ctx, c, StartTopicInput{Name: "other", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start other: %v", err)
	}
	otherTurn, err := otherID.PostTurn(ctx, "unrelated")
	if err != nil {
		t.Fatalf("post other: %v", err)
	}
	wrong, err := CaptureExhibit(ctx, c, otherID.Path(), otherTurn)
	if err != nil {
		t.Fatalf("capture other: %v", err)
	}
	wctx3, cancel3 := context.WithCancel(ctx)
	defer cancel3()
	trickster := connectClient(t, url, "trickster")
	go func() {
		_ = RespondMemory(wctx3, trickster, MemoryWitness{
			Fetch: func(string, string) (record.Exhibit, bool) { return wrong, true },
		})
	}()
	time.Sleep(100 * time.Millisecond) // let the trickster go live — silence is the pass condition
	res, err = FetchExhibit(ctx, c, h.Path(), targetID, 300*time.Millisecond, kr)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res != nil {
		t.Errorf("a mis-matched exhibit must be rejected, got %+v", res)
	}
}

// TestMemoryTrafficLeavesNoResidue is SC-006: any amount of memory traffic adds
// zero retained messages to the realm's permanent stores.
func TestMemoryTrafficLeavesNoResidue(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClientSignedProvisioned(t, url, "asker")
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "residue", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "content")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	kept, err := CaptureExhibit(ctx, c, h.Path(), turnID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	wc := connectClient(t, url, "witness")
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = RespondMemory(wctx, wc, MemoryWitness{
			Answer: func(MemoryQueryRequest) []MemoryAnswerDraft {
				return []MemoryAnswerDraft{{Answer: "kept", Citations: []MemoryCitation{{Topic: h.Path(), OpID: turnID}}}}
			},
			Fetch: func(string, string) (record.Exhibit, bool) { return kept, true },
		})
	}()

	count := func() uint64 {
		var total uint64
		for _, name := range []string{realm.StreamName, realm.NotifyStreamName} {
			s, err := c.JetStream().Stream(ctx, name)
			if err != nil {
				t.Fatalf("stream %s: %v", name, err)
			}
			info, err := s.Info(ctx)
			if err != nil {
				t.Fatalf("info %s: %v", name, err)
			}
			total += info.State.Msgs
		}
		return total
	}

	before := count()
	for range 3 {
		if _, err := MemoryQuery(ctx, c, MemoryQueryInput{Query: "residue?", Timeout: 200 * time.Millisecond}, nil); err != nil {
			t.Fatalf("query: %v", err)
		}
	}
	for range 2 {
		if _, err := FetchExhibit(ctx, c, h.Path(), record.NewID(), MinMemoryTimeout, nil); err != nil {
			t.Fatalf("fetch: %v", err)
		}
	}
	if after := count(); after != before {
		t.Errorf("permanent stores grew from %d to %d messages — memory traffic must be transient", before, after)
	}
}

// TestRespondMemoryFailingSignerStaysSilent (US2/FR-012, SC-005): a witness whose
// signer cannot sign serves nothing — no answer, no exhibit, never an unsigned
// reply — while the host observes each failure as an error naming the cause.
func TestRespondMemoryFailingSignerStaysSilent(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	owner := connectClientSignedProvisioned(t, url, "owner")
	h, err := StartTopic(ctx, owner, StartTopicInput{Name: "kept-quiet", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "keep this")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	kept, err := CaptureExhibit(ctx, owner, h.Path(), turnID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	wc := connectClientSigned(t, url, "keeper", &delegateSigner{key: key, err: errors.New("vault unreachable")})
	served := make(chan servedEvent, 32)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = RespondMemory(wctx, wc, MemoryWitness{
			Answer: func(_ MemoryQueryRequest) []MemoryAnswerDraft {
				return []MemoryAnswerDraft{{Answer: "it would answer"}}
			},
			Fetch: func(_, _ string) (record.Exhibit, bool) {
				return kept, true
			},
			OnServed: func(_ string, n int, err error) { served <- servedEvent{n, err} },
		})
	}()

	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	ask := func(opType string, payload any) *nats.Msg {
		t.Helper()
		inbox := nc.NewRespInbox()
		sub, serr := nc.SubscribeSync(inbox)
		if serr != nil {
			t.Fatalf("inbox: %v", serr)
		}
		defer func() { _ = sub.Unsubscribe() }()
		raw, _ := json.Marshal(payload)
		rec := record.Record{ID: record.NewID(), Author: "prober", Acting: "prober", Type: opType, Timestamp: time.Now().UTC(), Payload: raw}
		hd, b, _ := rec.Build()
		if perr := nc.PublishMsg(&nats.Msg{Subject: SvcMemorySubject, Header: nats.Header(hd), Data: b, Reply: inbox}); perr != nil {
			t.Fatalf("publish: %v", perr)
		}
		reply, rerr := sub.NextMsg(300 * time.Millisecond)
		if rerr != nil {
			return nil
		}
		return reply
	}

	deadline := time.Now().Add(2 * time.Second)
	for kind, payload := range map[string]any{
		"query": MemoryQueryPayload{Query: "anything", Deadline: deadline},
		"fetch": MemoryFetchPayload{Topic: h.Path(), OpID: turnID, Deadline: deadline},
	} {
		opType := TypeMemoryQuery
		if kind == "fetch" {
			opType = TypeMemoryFetch
		}
		heard := false
		for range 20 {
			if reply := ask(opType, payload); reply != nil {
				t.Fatalf("%s: got a reply from a witness that cannot sign", kind)
			}
			select {
			case ev := <-served:
				if ev.sent != 0 {
					t.Fatalf("%s: witness sent %d replies with a failing signer", kind, ev.sent)
				}
				if ev.err == nil || !strings.Contains(ev.err.Error(), "vault unreachable") {
					t.Fatalf("%s: served err = %v, want the custodian's cause", kind, ev.err)
				}
				heard = true
			default:
			}
			if heard {
				break
			}
		}
		if !heard {
			t.Fatalf("%s: witness never processed a request", kind)
		}
	}
}
