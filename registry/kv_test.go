package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
)

// clientOn connects a persona-bound client to an embedded server.
func clientOn(t *testing.T, url, persona string) *realm.Client {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	c, err := realm.NewClient(context.Background(), nc, realm.Config{Realm: "acme", Persona: persona})
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// provisioned starts a server, provisions the realm, and returns a client plus URL.
func provisioned(t *testing.T, persona string) (*realm.Client, string) {
	t.Helper()
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	c := clientOn(t, url, persona)
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, url
}

func profileFor(persona string, key *identity.SigningKey) Profile {
	p := Profile{
		Name:      persona,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	if key != nil {
		p.SigningKey = &SigningKeyInfo{Ed25519: key.PublicKey(), Since: p.CreatedAt}
	}
	return p
}

func TestPublishLookupRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "architect")
	key := testKey(t)

	if err := Publish(ctx, c, profileFor("architect", key)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	got, ok, err := Lookup(ctx, c, "architect")
	if err != nil || !ok {
		t.Fatalf("Lookup: ok=%v err=%v", ok, err)
	}
	if got.SigningKey == nil || got.SigningKey.Ed25519 != key.PublicKey() {
		t.Errorf("looked-up key = %+v", got.SigningKey)
	}

	_, ok, err = Lookup(ctx, c, "stranger")
	if err != nil || ok {
		t.Errorf("absent persona: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestPublishEnforcesOwnPersona(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "architect")

	if err := Publish(ctx, c, profileFor("mallory", testKey(t))); err == nil {
		t.Error("published a profile for another persona")
	}
}

func TestPublishMetadataUpdatePreservesKey(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "architect")
	key := testKey(t)

	if err := Publish(ctx, c, profileFor("architect", key)); err != nil {
		t.Fatalf("first Publish: %v", err)
	}

	// Metadata-only update: no key in the incoming profile.
	update := profileFor("architect", nil)
	update.DisplayName = "The Architect"
	update.OperatedBy = "daan"
	if err := Publish(ctx, c, update); err != nil {
		t.Fatalf("metadata update: %v", err)
	}

	got, ok, err := Lookup(ctx, c, "architect")
	if err != nil || !ok {
		t.Fatalf("Lookup: ok=%v err=%v", ok, err)
	}
	if got.DisplayName != "The Architect" || got.OperatedBy != "daan" {
		t.Errorf("metadata not updated: %+v", got)
	}
	if got.SigningKey == nil || got.SigningKey.Ed25519 != key.PublicKey() {
		t.Errorf("stored key not preserved: %+v", got.SigningKey)
	}
}

// The two-clients-different-key race (spec edge case, FR-004): the second client's
// publish is refused with ErrKeyConflict and the stored key is untouched.
func TestPublishDifferentKeyRefused(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "architect")
	firstKey := testKey(t)

	if err := Publish(ctx, c, profileFor("architect", firstKey)); err != nil {
		t.Fatalf("first Publish: %v", err)
	}

	second := clientOn(t, url, "architect")
	err := Publish(ctx, second, profileFor("architect", testKey(t)))
	if !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("second publish with a different key: err=%v, want ErrKeyConflict", err)
	}

	got, _, err := Lookup(ctx, c, "architect")
	if err != nil {
		t.Fatal(err)
	}
	if got.SigningKey.Ed25519 != firstKey.PublicKey() {
		t.Error("stored key was overwritten by the refused publish")
	}
}

func TestLookupAndAllTolerateMissingDirectory(t *testing.T) {
	// A realm that was never provisioned with the directory (pre-006 realm).
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	c := clientOn(t, url, "reader")
	ctx := context.Background()

	if _, ok, err := Lookup(ctx, c, "anyone"); err != nil || ok {
		t.Errorf("Lookup without directory: ok=%v err=%v, want false/nil", ok, err)
	}
	profiles, warnings, err := All(ctx, c)
	if err != nil || profiles != nil || warnings != nil {
		t.Errorf("All without directory: %v, %v, %v — want nil, nil, nil", profiles, warnings, err)
	}
}

// TestRotateEndToEnd (US4/SC-006): ops signed before and after a rotation both
// verify; a proof-less key change is distrusted; rotating without a profile or from
// the wrong key fails.
func TestRotateEndToEnd(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "architect")
	oldKey, newKey := testKey(t), testKey(t)

	// No profile yet: refuse.
	if _, err := Rotate(ctx, c, oldKey, newKey); err == nil {
		t.Fatal("rotated with no published profile")
	}

	if err := Publish(ctx, c, profileFor("architect", oldKey)); err != nil {
		t.Fatal(err)
	}

	// Wrong current key: refuse.
	if _, err := Rotate(ctx, c, newKey, testKey(t)); err == nil {
		t.Fatal("rotated from a key the directory does not hold")
	}

	updated, err := Rotate(ctx, c, oldKey, newKey)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if updated.SigningKey.Ed25519 != newKey.PublicKey() || len(updated.Rotations) != 1 {
		t.Fatalf("rotated profile = %+v", updated)
	}

	// A reader who pinned the old chain accepts the rotation and keeps both eras.
	pins := map[string][]string{"architect": {oldKey.PublicKey()}}
	stored, ok, err := Lookup(ctx, c, "architect")
	if err != nil || !ok {
		t.Fatal(err)
	}
	kr, newPins := BuildKeyring([]Profile{stored}, pins)
	if kr.Distrusted["architect"] {
		t.Fatal("valid rotation distrusted")
	}
	chain, _ := kr.ChainFor("architect")
	if len(chain) != 2 || chain[0] != oldKey.PublicKey() || chain[1] != newKey.PublicKey() {
		t.Fatalf("chain after rotation = %v", chain)
	}
	if len(newPins["architect"]) != 2 {
		t.Fatalf("pin not extended: %v", newPins["architect"])
	}

	// Both key eras verify (any-chain-key rule); a third key does not.
	canonical := []byte("era test bytes")
	if !identity.VerifySignature(chain[0], canonical, mustSign(t, oldKey, canonical)) ||
		!identity.VerifySignature(chain[1], canonical, mustSign(t, newKey, canonical)) {
		t.Error("chain keys do not verify their own eras")
	}

	// Second rotation chains further.
	thirdKey := testKey(t)
	if _, err := Rotate(ctx, c, newKey, thirdKey); err != nil {
		t.Fatalf("second Rotate: %v", err)
	}
	stored, _, err = Lookup(ctx, c, "architect")
	if err != nil {
		t.Fatal(err)
	}
	gotChain, err := Chain(stored)
	if err != nil {
		t.Fatalf("chain after two rotations: %v", err)
	}
	if len(gotChain) != 3 {
		t.Fatalf("chain length = %d, want 3", len(gotChain))
	}
}

// TestRotateDelegatedSigner (US3/T025, SC-003): a rotation whose proof was
// signed through the Signer seam validates through the existing chain rules,
// and a failing delegate aborts the rotation before any directory write.
func TestRotateDelegatedSigner(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "architect")
	oldKey, newKey := testKey(t), testKey(t)

	if err := Publish(ctx, c, profileFor("architect", oldKey)); err != nil {
		t.Fatal(err)
	}

	// Old key behind a failing custodian: no rotation, no write.
	_, err := Rotate(ctx, c, delegated{key: oldKey, fail: errors.New("vault unreachable")}, delegated{key: newKey})
	if err == nil || !strings.Contains(err.Error(), "vault unreachable") {
		t.Fatalf("failing delegate: err = %v, want the custodian's cause", err)
	}
	stored, _, err := Lookup(ctx, c, "architect")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Rotations) != 0 || stored.SigningKey.Ed25519 != oldKey.PublicKey() {
		t.Fatalf("aborted rotation left a directory write: %+v", stored)
	}

	// Both keys behind (working) delegates: the chain validates as with local keys.
	updated, err := Rotate(ctx, c, delegated{key: oldKey}, delegated{key: newKey})
	if err != nil {
		t.Fatalf("Rotate via delegates: %v", err)
	}
	if updated.SigningKey.Ed25519 != newKey.PublicKey() || len(updated.Rotations) != 1 {
		t.Fatalf("rotated profile = %+v", updated)
	}
	chain, err := Chain(updated)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 2 || chain[0] != oldKey.PublicKey() || chain[1] != newKey.PublicKey() {
		t.Fatalf("chain = %v", chain)
	}
}

func TestAllListsEveryProfile(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "architect")

	if err := Publish(ctx, c, profileFor("architect", testKey(t))); err != nil {
		t.Fatal(err)
	}
	second := clientOn(t, url, "historian")
	if err := Publish(ctx, second, profileFor("historian", nil)); err != nil {
		t.Fatal(err)
	}

	profiles, warnings, err := All(ctx, c)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
	}
	if !names["architect"] || !names["historian"] {
		t.Errorf("All = %v, want architect + historian", names)
	}
}

// Republish is the documented recovery from a legacy document: publishing over a
// stored profile that fails strict decode succeeds, while the stored key material
// and creation time stay authoritative — and the rewritten document is clean.
func TestPublishRecoversLegacyProfile(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "architect")
	key := testKey(t)

	kv, err := c.JetStream().KeyValue(ctx, BucketName)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"name":"architect","kind":"human","display_name":"Old Card",` +
		`"created_at":"2026-07-01T00:00:00Z",` +
		`"signing_key":{"ed25519":"` + key.PublicKey() + `","since":"2026-07-01T00:00:00Z"}}`)
	if _, err := kv.Create(ctx, "architect", legacy); err != nil {
		t.Fatal(err)
	}

	// A different incoming key is still refused during recovery — the guard holds.
	wrong := profileFor("architect", testKey(t))
	if err := Publish(ctx, c, wrong); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("recovery publish with a different key: err=%v, want ErrKeyConflict", err)
	}

	// Republish with matching (or no) key succeeds and rewrites a clean document.
	fresh := Profile{Name: "architect", DisplayName: "New Card", CreatedAt: time.Now().UTC()}
	if err := Publish(ctx, c, fresh); err != nil {
		t.Fatalf("recovery publish: %v", err)
	}
	got, ok, err := Lookup(ctx, c, "architect")
	if err != nil || !ok {
		t.Fatalf("Lookup after recovery: ok=%v err=%v", ok, err)
	}
	if got.DisplayName != "New Card" {
		t.Errorf("display name = %q, want New Card", got.DisplayName)
	}
	if got.SigningKey == nil || got.SigningKey.Ed25519 != key.PublicKey() {
		t.Errorf("stored key not preserved through recovery: %+v", got.SigningKey)
	}
	if got.CreatedAt.Format("2006-01-02") != "2026-07-01" {
		t.Errorf("created_at not preserved: %v", got.CreatedAt)
	}
}

// A legacy document (retired "kind" field) is scoped to its target: direct Lookup
// fails loudly naming the persona; All skips it with a warning and never fails.
func TestInvalidStoredProfileIsScoped(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "architect")

	if err := Publish(ctx, c, profileFor("architect", nil)); err != nil {
		t.Fatal(err)
	}

	// Plant a legacy document directly in the bucket, as a pre-014 client would have.
	kv, err := c.JetStream().KeyValue(ctx, BucketName)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"name":"old-timer","kind":"human","created_at":"2026-07-01T00:00:00Z"}`)
	if _, err := kv.Create(ctx, "old-timer", legacy); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Lookup(ctx, c, "old-timer"); err == nil {
		t.Fatal("Lookup accepted a legacy document")
	} else if !strings.Contains(err.Error(), "old-timer") || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("Lookup error must name persona and field: %v", err)
	}

	reader := clientOn(t, url, "reader")
	profiles, warnings, err := All(ctx, reader)
	if err != nil {
		t.Fatalf("All must not fail on one bad entry: %v", err)
	}
	if len(profiles) != 1 || profiles[0].Name != "architect" {
		t.Fatalf("All = %+v, want just architect", profiles)
	}
	if len(warnings) != 1 || warnings[0].Persona != "old-timer" {
		t.Fatalf("warnings = %+v, want one for old-timer", warnings)
	}
}
