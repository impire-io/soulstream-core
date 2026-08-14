package natstest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// StartJetStream starts an in-process NATS server with JetStream enabled, backed by
// a per-test temporary store directory, and returns its client URL together with a
// cleanup function. The store directory is removed automatically when the test ends.
//
// Typical use:
//
//	url, cleanup := natstest.StartJetStream(t)
//	defer cleanup()
//	nc, _ := nats.Connect(url)
func StartJetStream(t *testing.T) (url string, cleanup func()) {
	t.Helper()

	opts := &server.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		Host:      "127.0.0.1",
		Port:      -1, // pick a random free port
	}
	return start(t, opts)
}

// StartJetStreamToken starts an in-process JetStream server that admits nobody
// without the given token, so a test can prove a caller actually presented one
// rather than merely holding it in a struct field.
//
// It is the plainest server that refuses an empty-handed connection. A real
// deployment's token lane is a sentinel plus an auth callout that exchanges the
// pair for a scoped identity, which lives in the identity plane and is proven
// where that plane runs; what belongs here is that the connect option reaches
// the wire.
func StartJetStreamToken(t *testing.T, token string) (url string, cleanup func()) {
	t.Helper()

	opts := &server.Options{
		JetStream:     true,
		StoreDir:      t.TempDir(),
		Host:          "127.0.0.1",
		Port:          -1,
		Authorization: token,
	}
	return start(t, opts)
}

// StartJetStreamMaxBytesRequired starts an in-process server whose account refuses
// to create any stream without an explicit MaxBytes — the server switch behind NGS
// R1's "Stream Requires Max Bytes Set: true" — so tests can exercise a
// limit-enforced account with no external dependency. Unauthenticated connections
// land in the limited account.
func StartJetStreamMaxBytesRequired(t *testing.T) (url string, cleanup func()) {
	t.Helper()

	dir := t.TempDir()
	conf := filepath.Join(dir, "server.conf")
	config := fmt.Sprintf(`
listen: 127.0.0.1:-1
jetstream { store_dir: %q }
no_auth_user: limited
accounts {
	LIMITED {
		jetstream { max_bytes_required: true }
		users [ { user: limited, password: limited } ]
	}
}
`, filepath.Join(dir, "store"))
	if err := os.WriteFile(conf, []byte(config), 0o600); err != nil {
		t.Fatalf("natstest: write server config: %v", err)
	}

	opts, err := server.ProcessConfigFile(conf)
	if err != nil {
		t.Fatalf("natstest: parse server config: %v", err)
	}
	return start(t, opts)
}

// start runs a server from the given options with test-appropriate logging and
// signal handling, waiting until it accepts connections.
func start(t *testing.T, opts *server.Options) (url string, cleanup func()) {
	t.Helper()

	opts.NoLog = true
	opts.NoSigs = true // do not install OS signal handlers in tests

	ns, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("natstest: new server: %v", err)
	}

	go ns.Start()

	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		t.Fatal("natstest: server not ready for connections")
	}

	return ns.ClientURL(), ns.Shutdown
}
