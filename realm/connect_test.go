package realm

import (
	"context"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
)

// Connect must reject malformed names before it makes any server contact.
func TestConnectRejectsInvalidNamesBeforeContact(t *testing.T) {
	ctx := context.Background()

	if _, err := Connect(ctx, Config{ContextName: "irrelevant", Realm: "Bad Realm"}); err == nil {
		t.Error("Connect with invalid realm name: got nil, want error")
	}
	if _, err := Connect(ctx, Config{ContextName: "irrelevant", Realm: "acme", Persona: "Bad.Persona"}); err == nil {
		t.Error("Connect with invalid persona name: got nil, want error")
	}
}

// A typed-nil signer — a nil *identity.SigningKey assigned into the interface
// field — is refused at construction with an error naming the fix, before any
// server contact. Without this check it would pass every `!= nil` guard and
// SIGSEGV at the first publish (it did, in six of our own test helpers).
func TestConnectRejectsTypedNilSignerBeforeContact(t *testing.T) {
	ctx := context.Background()

	var nilKey *identity.SigningKey
	_, err := Connect(ctx, Config{ContextName: "irrelevant", Realm: "acme", Persona: "daan", Signer: nilKey})
	if err == nil || !strings.Contains(err.Error(), "typed-nil") {
		t.Errorf("Connect with typed-nil signer: err = %v, want a typed-nil explanation", err)
	}
	_, err = NewClient(ctx, nil, Config{Realm: "acme", Persona: "daan", Signer: nilKey})
	if err == nil || !strings.Contains(err.Error(), "typed-nil") {
		t.Errorf("NewClient with typed-nil signer: err = %v, want a typed-nil explanation", err)
	}

	// A non-pointer implementation must not trip the check (IsNil would panic
	// on non-nillable kinds if the guard were naive).
	if err := validateConfig(Config{Realm: "acme", Signer: valueSigner{}}); err != nil {
		t.Errorf("value-type signer refused: %v", err)
	}
	// And a genuinely absent signer stays legal.
	if err := validateConfig(Config{Realm: "acme"}); err != nil {
		t.Errorf("nil signer refused: %v", err)
	}
}

// valueSigner is a non-pointer Signer implementation (what a consumer's
// custodian adapter may well be).
type valueSigner struct{}

func (valueSigner) PublicKey() string           { return "" }
func (valueSigner) Sign([]byte) (string, error) { return "", nil }

// The client carries its optional signer verbatim: set → returned, unset → nil
// (nil is the publishes-unsigned mode every pre-signing caller relies on).
func TestClientCarriesSigner(t *testing.T) {
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	signed := &Client{cfg: Config{Realm: "acme", Persona: "daan", Signer: key}}
	if signed.Signer() != key {
		t.Error("Signer() did not return the configured key")
	}
	unsigned := &Client{cfg: Config{Realm: "acme", Persona: "daan"}}
	if unsigned.Signer() != nil {
		t.Error("Signer() on a key-less client: want nil")
	}
}

// Conn exposes the raw connection for request-reply surfaces, same as JetStream()
// exposes the stream-side handle.
func TestClientExposesConn(t *testing.T) {
	c := &Client{cfg: Config{Realm: "acme"}}
	if c.Conn() != c.nc {
		t.Error("Conn() did not return the underlying connection")
	}
}

// Connect must error (never panic, never partially mutate) when the named context
// does not exist / the server cannot be reached.
func TestConnectMissingContextErrors(t *testing.T) {
	ctx := context.Background()

	_, err := Connect(ctx, Config{
		ContextName: "soulstream-nonexistent-context-zzq",
		Realm:       "acme",
	})
	if err == nil {
		t.Fatal("Connect with nonexistent context: got nil, want error")
	}
}
