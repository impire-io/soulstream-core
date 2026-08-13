package topic

import (
	"context"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/record"
)

func signedRecord(t *testing.T, key *identity.SigningKey, author, realmName, binding string) record.Record {
	t.Helper()
	rec := record.Record{
		ID:        record.NewID(),
		Author:    author,
		Type:      TypeTurnPost,
		Timestamp: time.Now().UTC(),
		Payload:   []byte(`{"body":"hi"}`),
	}
	canonical, err := rec.Canonical(realmName, binding)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	rec.Signature, err = key.Sign(canonical)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return rec
}

// TestVerifyRecordStatuses pins the four-status contract (FR-009/FR-011).
func TestVerifyRecordStatuses(t *testing.T) {
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	kr := &identity.Keyring{
		Keys:       map[string][]string{"signer": {key.PublicKey()}, "impostor": {other.PublicKey()}},
		Distrusted: map[string]bool{"mallory": true},
	}

	unsigned := record.Record{ID: record.NewID(), Author: "signer", Type: TypeTurnPost,
		Timestamp: time.Now().UTC(), Payload: []byte(`{}`)}

	cases := []struct {
		name string
		rec  record.Record
		kr   *identity.Keyring
		want SigStatus
	}{
		{"unsigned", unsigned, kr, SigUnsigned},
		{"verified", signedRecord(t, key, "signer", "acme", "a-topic"), kr, SigVerified},
		{"author's key does not match (FR-011)", signedRecord(t, key, "impostor", "acme", "a-topic"), kr, SigFailed},
		{"unknown author", signedRecord(t, key, "mystery", "acme", "a-topic"), kr, SigUnknownKey},
		{"distrusted author", signedRecord(t, key, "mallory", "acme", "a-topic"), kr, SigFailed},
		{"nil keyring degrades", signedRecord(t, key, "signer", "acme", "a-topic"), nil, SigUnknownKey},
		{"malformed signature", func() record.Record {
			r := unsigned
			r.Signature = "%%not-base64%%"
			return r
		}(), kr, SigFailed},
	}
	for _, c := range cases {
		if got := VerifyRecord(c.rec, "acme", "a-topic", c.kr); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}

	// Superseded-era ops verify against any chain key.
	rotated := &identity.Keyring{Keys: map[string][]string{"signer": {key.PublicKey(), other.PublicKey()}}}
	oldEra := signedRecord(t, key, "signer", "acme", "a-topic")
	newEra := signedRecord(t, other, "signer", "acme", "a-topic")
	if got := VerifyRecord(oldEra, "acme", "a-topic", rotated); got != SigVerified {
		t.Errorf("old-era op after rotation: got %s, want verified", got)
	}
	if got := VerifyRecord(newEra, "acme", "a-topic", rotated); got != SigVerified {
		t.Errorf("new-era op: got %s, want verified", got)
	}
}

// TestMaterialiseFourStatuses is US3's independent test: one topic holding all four
// statuses reads back with every op visible and correctly labelled — and the statuses
// change nothing else about the view (FR-010).
func TestMaterialiseFourStatuses(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)

	signerKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	mysteryKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	pinnedForWrong, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}

	plain := connectClient(t, url, "plain")
	if _, err := plain.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := StartTopic(ctx, plain, StartTopicInput{Name: "mixed", SubjectMatter: "statuses"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "unsigned turn"); err != nil {
		t.Fatal(err)
	}

	post := func(persona string, key *identity.SigningKey, body string) {
		t.Helper()
		c := connectClientSigned(t, url, persona, key)
		ph := Open(c, h.Path())
		if _, err := ph.Materialise(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := ph.PostTurn(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	post("signer", signerKey, "verified turn")
	post("wrongkey", wrongKey, "failed turn")   // reader pins a different key for wrongkey
	post("mystery", mysteryKey, "unknown turn") // reader knows nothing about mystery

	kr := &identity.Keyring{Keys: map[string][]string{
		"signer":   {signerKey.PublicKey()},
		"wrongkey": {pinnedForWrong.PublicKey()},
	}}
	rh := Open(connectClient(t, url, "reader"), h.Path())
	rh.UseKeyring(kr)
	v, err := rh.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(v.Contributions) != 4 {
		t.Fatalf("contributions = %d, want 4 — no status may drop an op (FR-010)", len(v.Contributions))
	}
	want := map[string]SigStatus{
		"plain":    SigUnsigned,
		"signer":   SigVerified,
		"wrongkey": SigFailed,
		"mystery":  SigUnknownKey,
	}
	for _, c := range v.Contributions {
		if c.Sig != want[c.Author] {
			t.Errorf("%s: sig = %s, want %s", c.Author, c.Sig, want[c.Author])
		}
	}

	// Same topic without a keyring (a realm with no directory, SC-007): everything
	// still reads; signed ops degrade to unknown-key.
	bare := Open(connectClient(t, url, "reader2"), h.Path())
	vBare, err := bare.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vBare.Contributions) != 4 {
		t.Fatalf("keyring-less read dropped ops: %d", len(vBare.Contributions))
	}
	for _, c := range vBare.Contributions {
		wantBare := SigUnknownKey
		if c.Author == "plain" {
			wantBare = SigUnsigned
		}
		if c.Sig != wantBare {
			t.Errorf("keyring-less %s: sig = %s, want %s", c.Author, c.Sig, wantBare)
		}
	}

	// The two views agree on everything except statuses (annotation only).
	if v.Lifecycle != vBare.Lifecycle || len(v.Frontier) != len(vBare.Frontier) {
		t.Error("verification changed the projection itself")
	}
}

// TestFollowAnnotatesLiveOps: statuses arrive on live-followed ops too.
func TestFollowAnnotatesLiveOps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url := testServer(t)

	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := connectClientSigned(t, url, "signer", key)
	if _, err := signer.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := StartTopic(ctx, signer, StartTopicInput{Name: "live", SubjectMatter: "s"})
	if err != nil {
		t.Fatal(err)
	}

	kr := &identity.Keyring{Keys: map[string][]string{"signer": {key.PublicKey()}}}
	rh := Open(connectClient(t, url, "reader"), h.Path())
	rh.UseKeyring(kr)

	views := make(chan *MaterializedTopic, 16)
	go func() { _ = rh.Follow(ctx, func(v *MaterializedTopic) { views <- v }) }()

	if _, err := h.PostTurn(ctx, "signed live turn"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case v := <-views:
			if len(v.Contributions) == 1 {
				if v.Contributions[0].Sig != SigVerified {
					t.Errorf("live op sig = %s, want verified", v.Contributions[0].Sig)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the live op")
		}
	}
}

// TestInboxAnnotatesNotifications: FetchInbox statuses (the notify subject binding).
func TestInboxAnnotatesNotifications(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)

	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := connectClientSigned(t, url, "signer", key)
	if _, err := signer.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := StartTopic(ctx, signer, StartTopicInput{Name: "pings", SubjectMatter: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "hey @reader have a look"); err != nil {
		t.Fatal(err)
	}

	reader := connectClient(t, url, "reader")
	kr := &identity.Keyring{Keys: map[string][]string{"signer": {key.PublicKey()}}}
	notes, err := FetchInbox(ctx, reader, "reader", 10, kr)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notes))
	}
	if notes[0].Sig != SigVerified {
		t.Errorf("notification sig = %s, want verified", notes[0].Sig)
	}

	// Without a keyring: unknown-key, still delivered.
	notes, err = FetchInbox(ctx, reader, "reader", 10, nil)
	if err != nil || len(notes) != 1 {
		t.Fatalf("keyring-less inbox: %v, %d", err, len(notes))
	}
	if notes[0].Sig != SigUnknownKey {
		t.Errorf("keyring-less notification sig = %s, want unknown-key", notes[0].Sig)
	}
}
