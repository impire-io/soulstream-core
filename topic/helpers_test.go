package topic

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
)

// testServer starts an in-process JetStream server and returns its client URL.
func testServer(t *testing.T) string {
	t.Helper()
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	return url
}

// connectClient connects a realm client (as persona) to url.
func connectClient(t *testing.T, url, persona string) *realm.Client {
	t.Helper()
	return connectClientSigned(t, url, persona, nil)
}

// connectClientSigned connects a realm client (as persona) that signs with key —
// any Signer: a local key or a delegate standing in for a remote custodian.
func connectClientSigned(t *testing.T, url, persona string, key identity.Signer) *realm.Client {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	rcfg := realm.Config{Realm: "test-realm", Persona: persona}
	if key != nil { // never put a typed-nil key into the interface field
		rcfg.Signer = key
	}
	c, err := realm.NewClient(context.Background(), nc, rcfg)
	if err != nil {
		nc.Close()
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// provisionedClient starts a server, connects as persona, and provisions the realm.
func provisionedClient(t *testing.T, persona string) *realm.Client {
	t.Helper()
	c := connectClient(t, testServer(t), persona)
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return c
}

// testRealmKey resolves the provisioned realm's identity for the manual
// signing and offline verification tests — since v2 the KEY, never the
// name, binds canonicals (A10, episode 0112).
func testRealmKey(t *testing.T, url string) string {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	id, err := realm.LoadIdentity(context.Background(), js)
	if err != nil {
		t.Fatal(err)
	}
	return id.RealmKey
}
