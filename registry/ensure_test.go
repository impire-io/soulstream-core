package registry

import (
	"context"
	"errors"
	"testing"
)

// TestEnsureSigningKeyCreatesAndIdempotent: no directory entry → a
// minimal profile carrying the key appears; the second call changes
// nothing (F1: signer construction is the call site, so it repeats).
func TestEnsureSigningKeyCreatesAndIdempotent(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "runner")
	key := testKey(t)

	if err := EnsureSigningKey(ctx, c, key); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	p, found, err := Lookup(ctx, c, "runner")
	if err != nil || !found {
		t.Fatalf("lookup after ensure: %v found=%v", err, found)
	}
	if p.SigningKey == nil || p.SigningKey.Ed25519 != key.PublicKey() {
		t.Fatalf("profile carries %+v, want the signer's key", p.SigningKey)
	}

	if err := EnsureSigningKey(ctx, c, key); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	again, _, err := Lookup(ctx, c, "runner")
	if err != nil {
		t.Fatal(err)
	}
	if !again.SigningKey.Since.Equal(p.SigningKey.Since) {
		t.Fatal("idempotent ensure rewrote the key's Since")
	}
}

// TestEnsureSigningKeyAddsKeyPreservesMetadata: an existing keyless
// profile gains the key; display metadata survives.
func TestEnsureSigningKeyAddsKeyPreservesMetadata(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "archivist")
	if err := Publish(ctx, c, Profile{Name: "archivist", DisplayName: "The Keeper"}); err != nil {
		t.Fatal(err)
	}
	key := testKey(t)
	if err := EnsureSigningKey(ctx, c, key); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	p, _, err := Lookup(ctx, c, "archivist")
	if err != nil {
		t.Fatal(err)
	}
	if p.SigningKey == nil || p.SigningKey.Ed25519 != key.PublicKey() {
		t.Fatalf("key not added: %+v", p.SigningKey)
	}
	if p.DisplayName != "The Keeper" {
		t.Fatalf("display metadata lost: %q", p.DisplayName)
	}
}

// TestEnsureSigningKeyDifferentKeyRefused: stored key material is
// authoritative — a different signer refuses with ErrKeyConflict, the
// rotation door, never an overwrite.
func TestEnsureSigningKeyDifferentKeyRefused(t *testing.T) {
	ctx := context.Background()
	c, _ := provisioned(t, "daan")
	if err := EnsureSigningKey(ctx, c, testKey(t)); err != nil {
		t.Fatal(err)
	}
	if err := EnsureSigningKey(ctx, c, testKey(t)); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("want ErrKeyConflict, got %v", err)
	}
}
