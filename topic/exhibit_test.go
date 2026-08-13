package topic

import (
	"context"
	"errors"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/record"
)

// keyringFor builds a single-persona keyring for tests.
func keyringFor(persona string, key *identity.SigningKey) *identity.Keyring {
	return &identity.Keyring{Keys: map[string][]string{persona: {key.PublicKey()}}}
}

// TestCaptureExhibitDelegatedSignerVerifies (US1/T010, SC-001): an op signed
// through the Signer seam captures into an exhibit whose offline verdict
// (GradeForVerdict over VerifyExhibit — the offline-verify machinery) is
// exactly what a locally signed op yields. Evidence does not care who held
// the pen.
func TestCaptureExhibitDelegatedSignerVerifies(t *testing.T) {
	ctx := context.Background()
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	url := testServer(t)
	c := connectClientSigned(t, url, "envoy", &delegateSigner{key: key})
	if _, err := c.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "proxy-evidence", SubjectMatter: "delegated exhibits"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "the delegated decision")
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	ex, err := CaptureExhibit(ctx, c, h.Path(), turnID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	verdict, err := VerifyExhibit(ex, keyringFor("envoy", key))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict != SigVerified {
		t.Errorf("verdict = %s, want verified", verdict)
	}
	if grade := GradeForVerdict(verdict); grade != GradeProvenance {
		t.Errorf("offline grade = %s, want fact-with-provenance", grade)
	}
}

func TestCaptureExhibitLiveOpVerifies(t *testing.T) {
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
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "evidence", SubjectMatter: "exhibits"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "the decision")
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	ex, err := CaptureExhibit(ctx, c, h.Path(), turnID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if ex.Version != record.ExhibitVersion || ex.Realm != "test-realm" || ex.Binding != h.Path() {
		t.Errorf("exhibit envelope wrong: %+v", ex)
	}
	if ex.Subject != OpsSubject(h.Path()) {
		t.Errorf("subject = %s", ex.Subject)
	}

	kr := keyringFor("signer", key)
	verdict, err := VerifyExhibit(ex, kr)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict != SigVerified {
		t.Errorf("verdict = %s, want verified", verdict)
	}

	// The document survives serialisation and still verifies — this is the whole
	// point: evidence with no realm in sight.
	doc, err := ex.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := record.ParseExhibit(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, _ := VerifyExhibit(back, kr); v != SigVerified {
		t.Errorf("verdict after round-trip = %s, want verified", v)
	}
}

func TestCaptureExhibitAnnounceOp(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "starter")
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "info side", SubjectMatter: "announce"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if mt.Announcement == nil || mt.Announcement.OpID == "" {
		t.Fatalf("announcement op-id not exposed: %+v", mt.Announcement)
	}

	ex, err := CaptureExhibit(ctx, c, h.Path(), mt.Announcement.OpID)
	if err != nil {
		t.Fatalf("capture announce: %v", err)
	}
	if ex.Subject != InfoSubject(h.Path()) || ex.Binding != h.Path() {
		t.Errorf("announce exhibit envelope wrong: subject=%s binding=%s", ex.Subject, ex.Binding)
	}
	rec, err := ex.Record()
	if err != nil || rec.Type != TypeAnnounce {
		t.Errorf("announce exhibit record: %+v err=%v", rec, err)
	}
	// Unsigned realm: the exhibit honestly reports unsigned, content readable.
	if v, _ := VerifyExhibit(ex, nil); v != SigUnsigned {
		t.Errorf("verdict = %v, want unsigned", v)
	}
}

func TestCaptureExhibitNotFound(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "seeker")
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "haystack", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := CaptureExhibit(ctx, c, h.Path(), record.NewID()); !errors.Is(err, ErrOpNotLive) {
		t.Errorf("unknown op: err = %v, want ErrOpNotLive", err)
	}
	if _, err := CaptureExhibit(ctx, c, "no-such-topic", record.NewID()); !errors.Is(err, ErrOpNotLive) {
		t.Errorf("unknown topic: err = %v, want ErrOpNotLive", err)
	}
}

// TestExhibitTamperMatrix is SC-003: altering anything about a signed exhibit —
// payload, attribution, binding, realm, or the signature itself — flips the
// verdict to failed. Export never launders; distrust wins over a valid signature.
func TestExhibitTamperMatrix(t *testing.T) {
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
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "tamperproof", SubjectMatter: "s"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "the truth")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	ex, err := CaptureExhibit(ctx, c, h.Path(), turnID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	kr := keyringFor("signer", key)

	tamper := []struct {
		name string
		mut  func(e *record.Exhibit)
	}{
		{"payload", func(e *record.Exhibit) { e.Payload = []byte(`{"body":"a lie"}`) }},
		{"binding", func(e *record.Exhibit) { e.Binding = "another-topic" }},
		{"realm", func(e *record.Exhibit) { e.Realm = "another-realm" }},
		{"signature", func(e *record.Exhibit) {
			e.Headers[record.HeaderSig] = []string{"AAAA" + e.Headers[record.HeaderSig][0][4:]}
		}},
		{"timestamp", func(e *record.Exhibit) {
			e.Headers[record.HeaderTs] = []string{"2020-01-01T00:00:00Z"}
		}},
	}
	for _, tc := range tamper {
		mutated := ex
		mutated.Headers = map[string][]string{}
		for k, v := range ex.Headers {
			mutated.Headers[k] = append([]string(nil), v...)
		}
		mutated.Payload = append([]byte(nil), ex.Payload...)
		tc.mut(&mutated)
		if v, err := VerifyExhibit(mutated, kr); err != nil || v != SigFailed {
			t.Errorf("tampered %s: verdict = %v (err %v), want failed", tc.name, v, err)
		}
	}

	// Attribution tamper: reassigning the op to a persona whose key differs fails;
	// to a persona with no known key it is honestly unknown-key, never verified.
	reattributed := ex
	reattributed.Headers = map[string][]string{}
	for k, v := range ex.Headers {
		reattributed.Headers[k] = append([]string(nil), v...)
	}
	reattributed.Headers[record.HeaderAuthor] = []string{"impostor"}
	if v, _ := VerifyExhibit(reattributed, kr); v != SigUnknownKey {
		t.Errorf("reattributed to unknown persona: verdict = %v, want unknown-key", v)
	}
	otherKey, _ := identity.GenerateSigningKey()
	kr2 := keyringFor("impostor", otherKey)
	if v, _ := VerifyExhibit(reattributed, kr2); v != SigFailed {
		t.Errorf("reattributed to known persona: verdict = %v, want failed", v)
	}

	// Distrusted author: even a valid signature reports failed (the standing rule).
	distrust := &identity.Keyring{
		Keys:       map[string][]string{"signer": {key.PublicKey()}},
		Distrusted: map[string]bool{"signer": true},
	}
	if v, _ := VerifyExhibit(ex, distrust); v != SigFailed {
		t.Errorf("distrusted author: verdict = %v, want failed", v)
	}

	// No keyring at all: signed evidence degrades to unknown-key, never verified.
	if v, _ := VerifyExhibit(ex, nil); v != SigUnknownKey {
		t.Errorf("nil keyring: verdict = %v, want unknown-key", v)
	}

	// An exhibit whose record does not parse is a malformed document — an error,
	// not a verdict.
	broken := ex
	broken.Headers = map[string][]string{record.HeaderMsgID: {"not-a-uuid"}}
	if _, err := VerifyExhibit(broken, kr); err == nil {
		t.Error("unreadable record must be an error, not a verdict")
	}
}
