package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/registry"
)

func TestPublishProfileWithSessionKey(t *testing.T) {
	url, key := signedSetupURL(t)
	c := signedClientOn(t, url, "bookkeeper-agent", key)
	h := newHandlers(c)
	ctx := context.Background()

	res, _, err := h.publishProfile(ctx, nil, publishProfileInput{
		DisplayName: "The Bookkeeper",
		OperatedBy:  "daan",
	})
	if err != nil {
		t.Fatalf("publishProfile: %v", err)
	}
	out := resultText(t, res)
	for _, want := range []string{`"name": "bookkeeper-agent"`, key.PublicKey(), `"operated_by": "daan"`} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %s:\n%s", want, out)
		}
	}

	// Metadata update keeps the stored key.
	if _, _, err := h.publishProfile(ctx, nil, publishProfileInput{DisplayName: "Bookkeeper v2"}); err != nil {
		t.Fatalf("metadata update: %v", err)
	}
	p, ok, err := registry.Lookup(ctx, c, "bookkeeper-agent")
	if err != nil || !ok {
		t.Fatalf("Lookup: ok=%v err=%v", ok, err)
	}
	if p.DisplayName != "Bookkeeper v2" || p.SigningKey == nil || p.SigningKey.Ed25519 != key.PublicKey() {
		t.Errorf("update lost data: %+v", p)
	}
}

// The operated persona includes its operator's attestation token at publish; a token
// for the wrong persona is refused.
func TestPublishProfileWithAttestation(t *testing.T) {
	url, key := signedSetupURL(t)
	c := signedClientOn(t, url, "bookkeeper-agent", key)
	h := newHandlers(c)
	ctx := context.Background()

	operatorKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	token, err := registry.NewAttestationToken(operatorKey, "daan", "bookkeeper-agent", key.PublicKey())
	if err != nil {
		t.Fatal(err)
	}

	res, _, err := h.publishProfile(ctx, nil, publishProfileInput{OperatedBy: "daan", Attestation: token})
	if err != nil {
		t.Fatalf("publishProfile with attestation: %v", err)
	}
	out := resultText(t, res)
	for _, want := range []string{`"operated_by": "daan"`, `"operator_attestation"`, `"sig"`} {
		if !strings.Contains(out, want) {
			t.Errorf("result missing %s:\n%s", want, out)
		}
	}

	// The stored claim verifies against the operator's key.
	p, ok, err := registry.Lookup(ctx, c, "bookkeeper-agent")
	if err != nil || !ok {
		t.Fatalf("Lookup: ok=%v err=%v", ok, err)
	}
	status := registry.AttestationStatus(p, []string{operatorKey.PublicKey()}, false, []string{key.PublicKey()})
	if status != registry.ClaimAttested {
		t.Errorf("stored claim status = %q, want attested", status)
	}

	// Refusals: wrong operated persona, mismatched operated_by, missing operated_by.
	wrong, err := registry.NewAttestationToken(operatorKey, "daan", "someone-else", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.publishProfile(ctx, nil, publishProfileInput{OperatedBy: "daan", Attestation: wrong}); err == nil {
		t.Error("token for another persona accepted")
	}
	if _, _, err := h.publishProfile(ctx, nil, publishProfileInput{OperatedBy: "mallory", Attestation: token}); err == nil {
		t.Error("token with mismatched operated_by accepted")
	}
	if _, _, err := h.publishProfile(ctx, nil, publishProfileInput{Attestation: token}); err == nil {
		t.Error("token without operated_by accepted")
	}
}

// signedSetupURL starts a provisioned realm and returns its URL plus a fresh key.
func signedSetupURL(t *testing.T) (string, *identity.SigningKey) {
	t.Helper()
	h, url := setup(t, "provisioner")
	_ = h
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	return url, key
}

// signedClientOn mirrors clientOn with a signer attached.
func signedClientOn(t *testing.T, url, persona string, key *identity.SigningKey) *realm.Client {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	rcfg := realm.Config{Realm: "acme", Persona: persona}
	if key != nil { // never put a typed-nil key into the interface field
		rcfg.Signer = key
	}
	c, err := realm.NewClient(context.Background(), nc, rcfg)
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
