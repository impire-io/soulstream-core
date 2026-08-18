package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
	"github.com/impire-io/soulstream-core/topic"
)

// TestToolPublishesSignedOps: a session whose persona has a signing key signs every
// tool-published op; the signature verifies offline against the persona's public key.
func TestToolPublishesSignedOps(t *testing.T) {
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)

	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	c, err := realm.NewClient(context.Background(), nc, realm.Config{Realm: "acme", Persona: "bookkeeper-agent", Signer: key})
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := newHandlers(c)
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Signed"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	if _, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "signed by an agent"}); err != nil {
		t.Fatal(err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := js.Stream(ctx, realm.StreamName)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, topic.OpsSubject(path))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Parse(msg.Header, msg.Data)
	if err != nil {
		t.Fatal(err)
	}

	if rec.Signature == "" {
		t.Fatal("tool-published op carries no Soulstream-Sig")
	}
	unsigned := rec
	unsigned.Signature = ""
	canonical, err := unsigned.Canonical(realmKeyOf(t, url), path)
	if err != nil {
		t.Fatal(err)
	}
	if !identity.VerifySignature(key.PublicKey(), canonical, rec.Signature) {
		t.Error("tool-published op's signature does not verify")
	}
}

// realmKeyOf resolves the provisioned realm identity — since v2 the
// key, never the name, binds canonicals (A10).
func realmKeyOf(t *testing.T, url string) string {
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
