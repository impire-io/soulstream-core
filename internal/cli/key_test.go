package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
	"github.com/impire-io/soulstream-core/topic"
)

func TestKeyLifecycle(t *testing.T) {
	connect, url := testConnectorWithURL(t)
	keyFile := filepath.Join(t.TempDir(), "acme-daan.ed25519")

	// show before init: helpful error.
	code, _, errs := run(connect, "--realm", "acme", "--persona", "daan", "--key-file", keyFile, "key", "show")
	if code == 0 {
		t.Fatal("key show without a key should fail")
	}
	if !strings.Contains(errs, "key init") {
		t.Errorf("key show error should point at key init: %q", errs)
	}

	code, initOut, errs := run(connect, "--realm", "acme", "--persona", "daan", "--key-file", keyFile, "key", "init")
	if code != 0 {
		t.Fatalf("key init exit %d: %s", code, errs)
	}

	// The seed must never appear in output; the public key must.
	key, err := keystore.LoadKey(keyFile)
	if err != nil || key == nil {
		t.Fatalf("load key: %v", err)
	}
	if !strings.Contains(initOut, key.PublicKey()) {
		t.Errorf("key init did not print the public key")
	}

	// init twice: refuses.
	if code, _, _ := run(connect, "--realm", "acme", "--persona", "daan", "--key-file", keyFile, "key", "init"); code == 0 {
		t.Error("second key init should refuse to overwrite")
	}

	// show: prints the same public key.
	code, showOut, errs := run(connect, "--realm", "acme", "--persona", "daan", "--key-file", keyFile, "key", "show")
	if code != 0 {
		t.Fatalf("key show exit %d: %s", code, errs)
	}
	if !strings.Contains(showOut, key.PublicKey()) {
		t.Errorf("key show output %q missing public key", showOut)
	}

	// A post made with the key configured is signed on the wire.
	run(connect, "--realm", "acme", "--persona", "daan", "provision")
	code, startOut, errs := run(connect, "--realm", "acme", "--persona", "daan", "--key-file", keyFile, "start", "Signed Topic")
	if code != 0 {
		t.Fatalf("start exit %d: %s", code, errs)
	}
	path := strings.TrimSpace(startOut)
	if code, _, errs := run(connect, "--realm", "acme", "--persona", "daan", "--key-file", keyFile, "post", path, "signed hello"); code != 0 {
		t.Fatalf("post exit %d: %s", code, errs)
	}

	rec := lastOpsRecord(t, url, path)
	if rec.Signature == "" {
		t.Fatal("posted op carries no Soulstream-Sig")
	}
	unsigned := rec
	unsigned.Signature = ""
	canonical, err := unsigned.Canonical(realmKeyOf(t, url), path)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if !identity.VerifySignature(key.PublicKey(), canonical, rec.Signature) {
		t.Error("posted op's signature does not verify against the persona's key")
	}

	// A persona without a key still posts — unsigned (missing key file is not an error).
	if code, _, errs := run(connect, "--realm", "acme", "--persona", "ghost", "post", path, "unsigned hello"); code != 0 {
		t.Fatalf("unsigned post exit %d: %s", code, errs)
	}
	if rec := lastOpsRecord(t, url, path); rec.Signature != "" {
		t.Error("key-less persona published a signed op")
	}
}

func TestKeyRequiresPersonaAndRealm(t *testing.T) {
	connect := testConnector(t)
	if code, _, _ := run(connect, "--realm", "acme", "key", "init"); code == 0 {
		t.Error("key init without persona should fail")
	}
	if code, _, _ := run(connect, "--persona", "daan", "key", "init"); code == 0 {
		t.Error("key init without realm should fail")
	}
}

// lastOpsRecord reads the newest record on the topic's ops subject, raw.
func lastOpsRecord(t *testing.T, url, path string) record.Record {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	stream, err := js.Stream(context.Background(), realm.StreamName)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	msg, err := stream.GetLastMsgForSubject(context.Background(), topic.OpsSubject(path))
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			t.Fatal("no ops on subject")
		}
		t.Fatalf("get last: %v", err)
	}
	rec, err := record.Parse(msg.Header, msg.Data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return rec
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
