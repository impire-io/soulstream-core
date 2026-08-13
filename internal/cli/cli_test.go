package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/internal/version"
	"github.com/impire-io/soulstream-core/realm"
)

// testConnector starts an in-process server and returns a Connector that binds a fresh
// realm client (per invocation) to it — mirroring how each CLI process connects anew.
func testConnector(t *testing.T) Connector {
	t.Helper()
	connect, _ := testConnectorWithURL(t)
	return connect
}

// testConnectorWithURL also exposes the server URL, for tests that inspect the raw
// stream. Like the production connector, it loads the persona's signer from cfg so
// signing behaves identically under test.
func testConnectorWithURL(t *testing.T) (Connector, string) {
	t.Helper()
	// Isolate from any real key on the developer's machine: default resolution must
	// land in an (empty) temp location, not the user config dir. Tests that sign
	// pass --key-file explicitly, which wins over the environment.
	t.Setenv(keystore.EnvKeyFile, filepath.Join(t.TempDir(), "absent.ed25519"))
	isolateIdentity(t)
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	return func(ctx context.Context, cfg Config) (*realm.Client, error) {
		signer, err := loadSigner(cfg)
		if err != nil {
			return nil, err
		}
		nc, err := nats.Connect(url)
		if err != nil {
			return nil, err
		}
		rcfg := realm.Config{Realm: cfg.Realm, Persona: cfg.Persona}
		if signer != nil { // never put a typed-nil key into the interface field
			rcfg.Signer = signer
		}
		c, err := realm.NewClient(ctx, nc, rcfg)
		if err != nil {
			nc.Close()
			return nil, err
		}
		return c, nil
	}, url
}

func run(connect Connector, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Run(context.Background(), args, &out, &errb, connect)
	return code, out.String(), errb.String()
}

func TestProvisionAndBoard(t *testing.T) {
	connect := testConnector(t)

	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision")
	if code != 0 {
		t.Fatalf("provision exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "stream") {
		t.Errorf("provision output missing stream: %q", out)
	}

	code, out, _ = run(connect, "--realm", "acme", "board")
	if code != 0 {
		t.Fatalf("board exit %d", code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("empty realm board should be empty, got %q", out)
	}
}

func TestStartConverseShow(t *testing.T) {
	connect := testConnector(t)
	run(connect, "--realm", "acme", "--persona", "daan", "provision")

	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "start", "Q2 VAT", "--subject", "filing", "--tag", "finance")
	if code != 0 {
		t.Fatalf("start exit %d: %s", code, errs)
	}
	path := strings.TrimSpace(out)
	if path == "" {
		t.Fatal("start printed no path")
	}

	if code, _, errs = run(connect, "--realm", "acme", "--persona", "daan", "post", path, "hi @bookkeeper-agent"); code != 0 {
		t.Fatalf("post exit %d: %s", code, errs)
	}

	_, out, _ = run(connect, "--realm", "acme", "board")
	if !strings.Contains(out, path) {
		t.Errorf("board missing the topic: %q", out)
	}

	_, out, _ = run(connect, "--realm", "acme", "show", path)
	if !strings.Contains(out, "hi @bookkeeper-agent") || !strings.Contains(out, "active") {
		t.Errorf("show output: %q", out)
	}
	if !strings.Contains(out, "bookkeeper-agent") {
		t.Errorf("show missing mention: %q", out)
	}
	if !strings.Contains(out, "filing") { // the --subject flag (after the positional) was applied
		t.Errorf("show missing subject matter: %q", out)
	}

	code, out, _ = run(connect, "--realm", "acme", "show", path, "--json")
	if code != 0 || !strings.Contains(out, `"lifecycle"`) {
		t.Errorf("show --json output: %q", out)
	}
}

func TestWriteRequiresPersona(t *testing.T) {
	connect := testConnector(t)
	code, _, errs := run(connect, "--realm", "acme", "start", "x")
	if code == 0 {
		t.Error("a write command without a persona should fail")
	}
	if !strings.Contains(errs, "persona") {
		t.Errorf("stderr should mention persona: %q", errs)
	}
}

func TestUnknownCommandAndNoArgs(t *testing.T) {
	connect := testConnector(t)
	code, _, errs := run(connect, "bogus")
	if code == 0 || !strings.Contains(errs, "unknown command") {
		t.Errorf("unknown command: exit %d, stderr %q", code, errs)
	}
	code, _, errs = run(connect)
	if code == 0 || !strings.Contains(errs, "Usage") {
		t.Errorf("no args: exit %d, stderr %q", code, errs)
	}
}

func TestVersionCommand(t *testing.T) {
	// version must answer without connecting: a nil connector proves it.
	code, out, _ := run(nil, "version")
	if code != 0 {
		t.Errorf("version: exit %d", code)
	}
	if strings.TrimSpace(out) != version.Version {
		t.Errorf("version output %q, want %q", out, version.Version)
	}
}

func TestConnectorFailureExitsNonZero(t *testing.T) {
	failing := func(_ context.Context, _ Config) (*realm.Client, error) {
		return nil, fmt.Errorf("boom")
	}
	code, _, errs := run(failing, "--realm", "acme", "board")
	if code == 0 || !strings.Contains(errs, "boom") {
		t.Errorf("connector failure: exit %d, stderr %q", code, errs)
	}
}

func TestAttachGetClose(t *testing.T) {
	connect := testConnector(t)
	run(connect, "--realm", "acme", "--persona", "daan", "provision")
	_, out, _ := run(connect, "--realm", "acme", "--persona", "daan", "start", "files")
	path := strings.TrimSpace(out)

	dir := t.TempDir()
	src := filepath.Join(dir, "data.csv")
	if err := os.WriteFile(src, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "attach", path, src, "--type", "text/csv")
	if code != 0 {
		t.Fatalf("attach exit %d: %s", code, errs)
	}
	object := strings.TrimSpace(out)
	if !strings.HasPrefix(object, "attachments/") {
		t.Errorf("attach printed %q, want an object key", object)
	}

	_, out, _ = run(connect, "--realm", "acme", "show", path)
	if !strings.Contains(out, "data.csv") {
		t.Errorf("show missing attachment: %q", out)
	}

	dst := filepath.Join(dir, "out.csv")
	if code, _, errs = run(connect, "--realm", "acme", "get", object, dst); code != 0 {
		t.Fatalf("get exit %d: %s", code, errs)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "a,b\n1,2\n" {
		t.Errorf("get reproduced %q", got)
	}

	// No clobber without --force.
	if code, _, _ = run(connect, "--realm", "acme", "get", object, dst); code == 0 {
		t.Error("get onto an existing file without --force should fail")
	}

	// Close.
	if code, _, errs = run(connect, "--realm", "acme", "--persona", "daan", "close", path); code != 0 {
		t.Fatalf("close exit %d: %s", code, errs)
	}
	_, out, _ = run(connect, "--realm", "acme", "show", path)
	if !strings.Contains(out, "closed") {
		t.Errorf("show after close: %q", out)
	}
}
