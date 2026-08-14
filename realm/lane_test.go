package realm

import (
	"context"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/internal/natstest"
)

// A caller handed an address dials it, with no context anywhere in the picture.
// This is the whole of what an agent configured by its environment can rely on:
// nothing was saved on the machine it runs on.
func TestConnectDialsAnAddressWithNoContext(t *testing.T) {
	url, shutdown := natstest.StartJetStream(t)
	defer shutdown()

	c, err := Connect(context.Background(), Config{URL: url, Realm: "acme"})
	if err != nil {
		t.Fatalf("Connect to %s: %v", url, err)
	}
	defer func() { _ = c.Close() }()

	if got := c.Realm(); got != "acme" {
		t.Errorf("Realm() = %q, want %q", got, "acme")
	}
}

// The token reaches the wire. A server that admits nobody without one refuses
// the empty-handed connection and admits the one that carries it — so the field
// is a credential presented, not a value parked in a struct.
func TestTheTokenIsPresentedOnConnect(t *testing.T) {
	const token = "sit_0123456789abcdef"
	url, shutdown := natstest.StartJetStreamToken(t, token)
	defer shutdown()

	ctx := context.Background()
	if _, err := Connect(ctx, Config{URL: url, Realm: "acme"}); err == nil {
		t.Fatal("Connect with no token to a server that requires one: got nil, want a refusal")
	}

	c, err := Connect(ctx, Config{URL: url, Realm: "acme", Token: token})
	if err != nil {
		t.Fatalf("Connect with the token: %v", err)
	}
	defer func() { _ = c.Close() }()
}

// A credentials file is opened, not ignored: an address that cannot be read
// fails the connection by name rather than admitting the caller anonymously.
func TestTheCredentialsFileIsRead(t *testing.T) {
	url, shutdown := natstest.StartJetStream(t)
	defer shutdown()

	_, err := Connect(context.Background(), Config{
		URL: url, Realm: "acme", CredsFile: t.TempDir() + "/sentinel.creds",
	})
	if err == nil {
		t.Fatal("Connect with an unreadable creds file: got nil, want an error")
	}
	if !strings.Contains(err.Error(), "sentinel.creds") {
		t.Errorf("error does not name the file it could not read: %v", err)
	}
}

// Two answers to the same question is a configuration mistake, refused by name
// before any server contact rather than resolved by a rule nobody remembers.
func TestAnAddressAndAContextTogetherAreRefused(t *testing.T) {
	_, err := Connect(context.Background(), Config{
		URL: "nats://127.0.0.1:4222", ContextName: "home", Realm: "acme",
	})
	if err == nil {
		t.Fatal("Connect with both URL and ContextName: got nil, want an error")
	}
	for _, want := range []string{"URL", "ContextName", "set one"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}
