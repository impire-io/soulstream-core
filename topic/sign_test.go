package topic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// rawMessages reads every message currently on subject (headers + payload + subject),
// bypassing all topic-layer machinery, so signature checks see exactly the wire form.
func rawMessages(t *testing.T, c *realm.Client, subject string) []struct {
	Subject string
	Rec     record.Record
} {
	t.Helper()
	ctx := context.Background()

	// Notify subjects live in the inbox stream since 014; everything else in the op-log.
	streamName := realm.StreamName
	if strings.HasPrefix(subject, NotifySubjectPrefix) {
		streamName = realm.NotifyStreamName
	}
	stream, err := c.JetStream().Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("look up stream: %v", err)
	}
	if _, err := stream.GetLastMsgForSubject(ctx, subject); err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return nil
		}
		t.Fatalf("probe %s: %v", subject, err)
	}
	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subject},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	it, err := cons.Messages()
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	defer it.Stop()

	var out []struct {
		Subject string
		Rec     record.Record
	}
	for {
		msg, err := it.Next()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		rec, perr := record.Parse(msg.Headers(), msg.Data())
		if perr != nil {
			t.Fatalf("parse %s: %v", msg.Subject(), perr)
		}
		out = append(out, struct {
			Subject string
			Rec     record.Record
		}{msg.Subject(), rec})
		md, err := msg.Metadata()
		if err != nil {
			t.Fatalf("metadata: %v", err)
		}
		if md.NumPending == 0 {
			return out
		}
	}
}

// verifyWire checks a wire record's signature offline: only the record, the subject it
// arrived on, and the public key — no server, no directory (SC-001, FR-014).
func verifyWire(rec record.Record, subject, pubKey, realmKey string) bool {
	unsigned := rec
	unsigned.Signature = ""
	canonical, err := unsigned.Canonical(realmKey, canonicalBinding(subject))
	if err != nil {
		return false
	}
	return identity.VerifySignature(pubKey, canonical, rec.Signature)
}

// TestSignedPersonaSignsEveryOpFamily is US1's independent test: a key-configured
// persona publishes every op family and each wire record carries a verifying
// signature (SC-002), checked out-of-band with no registry anywhere.
func TestSignedPersonaSignsEveryOpFamily(t *testing.T) {
	ctx := context.Background()
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	url := testServer(t)
	c := connectClientSigned(t, url, "signer", key)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "signed", SubjectMatter: "signing e2e"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "hello @reader")
	if err != nil {
		t.Fatalf("post turn: %v", err)
	}
	if _, err := h.AddComment(ctx, "a comment", turnID); err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if _, err := h.Attach(ctx, "note.txt", "text/plain", []byte("attachment body"), turnID); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := h.Transition(ctx, Active); err != nil {
		t.Fatalf("transition: %v", err)
	}

	// Every op on every subject family must verify: ops, info (announce), notify
	// (the @reader mention fired one).
	families := []string{OpsSubject(h.Path()), InfoSubject(h.Path()), NotifySubject("reader")}
	total := 0
	for _, subject := range families {
		msgs := rawMessages(t, c, subject)
		if len(msgs) == 0 {
			t.Fatalf("no messages on %s", subject)
		}
		for _, m := range msgs {
			total++
			if m.Rec.Signature == "" {
				t.Errorf("%s %s: unsigned op from a key-configured persona", m.Subject, m.Rec.Type)
				continue
			}
			if !verifyWire(m.Rec, m.Subject, key.PublicKey(), testRealmKey(t, url)) {
				t.Errorf("%s %s: signature does not verify", m.Subject, m.Rec.Type)
			}
		}
	}
	// announce + baseline + turn + comment + attachment + transition + notify = 7
	if total != 7 {
		t.Errorf("op count = %d, want 7 (announce, baseline, turn, comment, attachment, transition, notify)", total)
	}
}

// TestTamperingBreaksTheSignature is the SC-003 matrix: altering any canonical field
// of a signed op makes verification fail, including cross-topic and cross-realm
// splicing (US1 scenarios 4–5).
func TestTamperingBreaksTheSignature(t *testing.T) {
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	const rk = "AREALMKEYSTANDIN" // canonical-binding math needs no server
	rec := record.Record{
		ID:     record.NewID(),
		Author: "signer", Acting: "signer",
		Parents:   []string{record.NewID()},
		Type:      TypeTurnPost,
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Payload:   []byte(`{"body":"the truth"}`),
	}
	canonical, err := rec.Canonical(rk, "a-topic")
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	rec.Signature, err = key.Sign(canonical)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	verify := func(r record.Record, realmName, binding string) bool {
		unsigned := r
		unsigned.Signature = ""
		c, err := unsigned.Canonical(realmName, binding)
		if err != nil {
			return false
		}
		return identity.VerifySignature(key.PublicKey(), c, r.Signature)
	}

	if !verify(rec, rk, "a-topic") {
		t.Fatal("untampered record must verify")
	}

	tampers := map[string]func(r *record.Record) (realmName, binding string){
		"author": func(r *record.Record) (string, string) { r.Author = "mallory"; return rk, "a-topic" },
		"payload": func(r *record.Record) (string, string) {
			r.Payload = []byte(`{"body":"a lie"}`)
			return rk, "a-topic"
		},
		"parents": func(r *record.Record) (string, string) { r.Parents = nil; return rk, "a-topic" },
		"ts": func(r *record.Record) (string, string) {
			r.Timestamp = r.Timestamp.Add(time.Hour)
			return rk, "a-topic"
		},
		"type":        func(r *record.Record) (string, string) { r.Type = TypeCommentAdd; return rk, "a-topic" },
		"id":          func(r *record.Record) (string, string) { r.ID = record.NewID(); return rk, "a-topic" },
		"cross-topic": func(_ *record.Record) (string, string) { return rk, "other-topic" },
		"cross-realm": func(_ *record.Record) (string, string) { return "other-realm", "a-topic" },
	}
	for name, tamper := range tampers {
		tampered := rec
		realmName, binding := tamper(&tampered)
		if verify(tampered, realmName, binding) {
			t.Errorf("tampering %s: still verifies", name)
		}
	}
}

// TestNoSignerPublishesUnsigned is US1 scenario 3: a persona with no key publishes
// exactly as before — no Soulstream-Sig header, no error.
func TestNoSignerPublishesUnsigned(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "plain")

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "unsigned", SubjectMatter: "no key"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	if _, err := h.PostTurn(ctx, "no signature here"); err != nil {
		t.Fatalf("post turn: %v", err)
	}

	for _, subject := range []string{OpsSubject(h.Path()), InfoSubject(h.Path())} {
		for _, m := range rawMessages(t, c, subject) {
			if m.Rec.Signature != "" {
				t.Errorf("%s %s: signed op from a key-less persona", m.Subject, m.Rec.Type)
			}
		}
	}
}

// --- 017: the Signer seam ---

// delegateSigner stands in for a remote custodian behind the identity.Signer
// seam: capability in, signature or error out. The wrapped key plays the vaulted
// seed; the injectable failure modes play the custodian being unreachable (err)
// or broken (empty). Call counting is mutex-guarded because clients sign from
// many goroutines (FR-011).
type delegateSigner struct {
	key *identity.SigningKey

	mu    sync.Mutex
	calls int

	err   error // when set, every Sign fails: the custodian is unreachable
	empty bool  // when set, Sign returns ("", nil): a broken custodian
}

func (d *delegateSigner) PublicKey() string { return d.key.PublicKey() }

func (d *delegateSigner) Sign(canonical []byte) (string, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.err != nil {
		return "", d.err
	}
	if d.empty {
		return "", nil
	}
	return d.key.Sign(canonical)
}

func (d *delegateSigner) signCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

var _ identity.Signer = (*delegateSigner)(nil)

// TestDelegatedSigningIsTransparent (US1, SC-001): a record signed through the
// seam is byte-identical to local-key signing over the same canonical bytes, and
// the read surfaces (wire verify, materialise, follow, inbox) report it verified
// — readers cannot tell, and never need to know, where the signature was made.
func TestDelegatedSigningIsTransparent(t *testing.T) {
	ctx := context.Background()
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	del := &delegateSigner{key: key}

	url := testServer(t)
	c := connectClientSigned(t, url, "envoy", del)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "delegated", SubjectMatter: "signed by proxy"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	if _, err := h.PostTurn(ctx, "hello @reader"); err != nil {
		t.Fatalf("post turn: %v", err)
	}
	if del.signCount() == 0 {
		t.Fatal("the delegate was never asked to sign")
	}

	// Wire form: every op verifies against the key, and signing the same
	// canonical bytes locally yields the very same signature (Ed25519 is
	// deterministic) — delegation added nothing and lost nothing.
	for _, subject := range []string{OpsSubject(h.Path()), InfoSubject(h.Path()), NotifySubject("reader")} {
		msgs := rawMessages(t, c, subject)
		if len(msgs) == 0 {
			t.Fatalf("no messages on %s", subject)
		}
		for _, m := range msgs {
			if !verifyWire(m.Rec, m.Subject, key.PublicKey(), testRealmKey(t, url)) {
				t.Errorf("%s %s: delegated signature does not verify", m.Subject, m.Rec.Type)
			}
			unsigned := m.Rec
			unsigned.Signature = ""
			canonical, cerr := unsigned.Canonical(testRealmKey(t, url), canonicalBinding(m.Subject))
			if cerr != nil {
				t.Fatalf("recompute canonical: %v", cerr)
			}
			local, serr := key.Sign(canonical)
			if serr != nil {
				t.Fatalf("local sign: %v", serr)
			}
			if local != m.Rec.Signature {
				t.Errorf("%s %s: delegated signature differs from local signing over the same bytes", m.Subject, m.Rec.Type)
			}
		}
	}

	// Read surfaces. Materialise:
	kr := keyringFor("envoy", key)
	h.UseKeyring(kr)
	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if mt.Announcement.Sig != SigVerified {
		t.Errorf("announcement sig = %s, want verified", mt.Announcement.Sig)
	}
	if len(mt.Contributions) != 1 || mt.Contributions[0].Sig != SigVerified {
		t.Errorf("contribution sig not verified: %+v", mt.Contributions)
	}

	// Follow (a second, keyring-equipped follower replaying history):
	follower := Open(c, h.Path())
	follower.UseKeyring(kr)
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	views := make(chan *MaterializedTopic, 32)
	go func() { _ = follower.Follow(fctx, func(v *MaterializedTopic) { views <- v }) }()
	fv := waitForView(t, views, func(v *MaterializedTopic) bool { return len(v.Contributions) == 1 })
	if fv.Contributions[0].Sig != SigVerified {
		t.Errorf("followed contribution sig = %s, want verified", fv.Contributions[0].Sig)
	}
	cancel()

	// Inbox (the @reader mention fired a notify op, signed through the delegate):
	notes, err := FetchInbox(ctx, c, "reader", 10, kr)
	if err != nil {
		t.Fatalf("fetch inbox: %v", err)
	}
	if len(notes) != 1 || notes[0].Sig != SigVerified {
		t.Errorf("inbox notification not verified: %+v", notes)
	}
}

// TestDelegatedSignerConcurrentPublish exercises the FR-011 contract: one client,
// one delegate, many goroutines publishing at once. Meaningful under -race (the
// full gate runs it there once per feature); the guarded call counter is the
// double honouring the same contract it tests.
func TestDelegatedSignerConcurrentPublish(t *testing.T) {
	ctx := context.Background()
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	del := &delegateSigner{key: key}

	url := testServer(t)
	c := connectClientSigned(t, url, "envoy", del)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "crowded", SubjectMatter: "concurrent signing"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	before := del.signCount()

	const writers = 8
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Each goroutine holds its own Handle; the shared surfaces are the
			// client and the signer — exactly what FR-011 is about.
			if _, err := Open(c, h.Path()).PostTurn(ctx, fmt.Sprintf("turn %d", n)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent post: %v", err)
	}
	if got := del.signCount() - before; got != writers {
		t.Errorf("delegate signed %d ops, want %d", got, writers)
	}
}

// TestFailingSignerFailsThePublish (US2, SC-002): a configured signer that
// cannot sign fails the operation loudly — the error names the cause, nothing
// lands on the log, and there is no unsigned fallback. The empty-signature
// custodian (T018/FR-005) fails identically, because an empty pressing would
// silently travel as "unsigned".
func TestFailingSignerFailsThePublish(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)

	// A working persona sets the stage: a real topic with real history.
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	good := connectClientSigned(t, url, "envoy", key)
	if _, err := good.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, good, StartTopicInput{Name: "fragile", SubjectMatter: "custodian outages"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "anchor")
	if err != nil {
		t.Fatalf("post turn: %v", err)
	}

	opLogMsgs := func() uint64 {
		t.Helper()
		stream, serr := good.JetStream().Stream(ctx, realm.StreamName)
		if serr != nil {
			t.Fatalf("stream: %v", serr)
		}
		info, ierr := stream.Info(ctx)
		if ierr != nil {
			t.Fatalf("stream info: %v", ierr)
		}
		return info.State.Msgs
	}

	vaultDown := errors.New("vault unreachable")
	cases := []struct {
		name   string
		del    *delegateSigner
		wantIn string
	}{
		{"signer error", &delegateSigner{key: key, err: vaultDown}, "vault unreachable"},
		{"empty signature", &delegateSigner{key: key, empty: true}, "empty signature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := connectClientSigned(t, url, "envoy", tc.del)
			before := opLogMsgs()
			bh := Open(broken, h.Path())

			if _, err := bh.PostTurn(ctx, "will not land"); err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("PostTurn error = %v, want mention of %q", err, tc.wantIn)
			}
			if _, err := bh.AddComment(ctx, "nor this", turnID); err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("AddComment error = %v, want mention of %q", err, tc.wantIn)
			}
			if _, err := StartTopic(ctx, broken, StartTopicInput{Name: "never-born", SubjectMatter: "x"}); err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("StartTopic error = %v, want mention of %q", err, tc.wantIn)
			}
			// The injected cause survives the wrapping chain (callers can errors.Is).
			if tc.del.err != nil {
				if _, err := bh.PostTurn(ctx, "check the chain"); !errors.Is(err, vaultDown) {
					t.Errorf("cause not in error chain: %v", err)
				}
			}

			if got := opLogMsgs(); got != before {
				t.Errorf("op log grew from %d to %d records under a failing signer", before, got)
			}
		})
	}
}

// TestCanonicalBinding pins the binding rule readers rely on to recompute signing
// input from the subject alone.
func TestCanonicalBinding(t *testing.T) {
	cases := []struct{ subject, want string }{
		{"SOULSTREAM.TOPICS.OPS.vat-q2-x7m2", "vat-q2-x7m2"},
		{"SOULSTREAM.TOPICS.OPS.parent-a1b2.child-c3d4", "parent-a1b2.child-c3d4"},
		{"SOULSTREAM.TOPICS.INFO.vat-q2-x7m2", "vat-q2-x7m2"},
		{"SOULSTREAM.PERSONA.NOTIFY.architect", "architect"},
		{"SOULSTREAM.SVC.DISCOVER", "DISCOVER"},
		{"SOMETHING.ELSE", ""},
	}
	for _, c := range cases {
		if got := canonicalBinding(c.subject); got != c.want {
			t.Errorf("canonicalBinding(%q) = %q, want %q", c.subject, got, c.want)
		}
	}
}

// TestSignedTurnVerifiesAgainstPublicKey is US1 scenario 1 in its smallest form,
// and a marshalling sanity check that the payload round-trips into canonical form.
func TestSignedTurnVerifiesAgainstPublicKey(t *testing.T) {
	ctx := context.Background()
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	url := testServer(t)
	c := connectClientSigned(t, url, "signer", key)
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "one turn", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	if _, err := h.PostTurn(ctx, "signed statement"); err != nil {
		t.Fatalf("post turn: %v", err)
	}

	for _, m := range rawMessages(t, c, OpsSubject(h.Path())) {
		if m.Rec.Type != TypeTurnPost {
			continue
		}
		var tp TurnPayload
		if err := json.Unmarshal(m.Rec.Payload, &tp); err != nil {
			t.Fatalf("unmarshal turn payload: %v", err)
		}
		if tp.Body != "signed statement" {
			t.Errorf("body = %q", tp.Body)
		}
		if !verifyWire(m.Rec, m.Subject, key.PublicKey(), testRealmKey(t, url)) {
			t.Error("turn signature does not verify against the persona's public key")
		}
		return
	}
	t.Fatal("no turn.post found")
}
