package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
)

// testConnectorLimited is testConnector against a server whose account requires
// MaxBytes on every stream (the NGS R1 shape) — the account this feature exists for.
func testConnectorLimited(t *testing.T) Connector {
	t.Helper()
	t.Setenv(keystore.EnvKeyFile, filepath.Join(t.TempDir(), "absent.ed25519"))
	isolateIdentity(t)
	url, shutdown := natstest.StartJetStreamMaxBytesRequired(t)
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
	}
}

// US1 (016): on a limit-enforced account, --budgets provisions out of the box and
// the plain command keeps failing with the artefact-naming error.
func TestProvisionBudgetsSwitchOnLimitedAccount(t *testing.T) {
	connect := testConnectorLimited(t)

	code, _, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision")
	if code == 0 {
		t.Fatal("plain provision on a MaxBytesRequired account succeeded, want failure")
	}
	if !strings.Contains(errs, "SOULSTREAM") {
		t.Errorf("error output %q does not name the refused artefact", errs)
	}

	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision", "--budgets")
	if code != 0 {
		t.Fatalf("provision --budgets exit %d: %s", code, errs)
	}
	for _, want := range []string{"created", "1.0 GiB", "64.0 MiB", "512.0 MiB"} {
		if !strings.Contains(out, want) {
			t.Errorf("provision --budgets output missing %q:\n%s", want, out)
		}
	}
}

// US2 (016): explicit flags overwrite the switch's defaults; flags alone leave the
// unnamed artefacts unlimited (clarification 2026-07-27).
func TestProvisionBudgetFlagComposition(t *testing.T) {
	connect := testConnector(t)
	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision", "--budgets", "--budget-objects", "2GiB")
	if code != 0 {
		t.Fatalf("provision exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "2.0 GiB") {
		t.Errorf("explicit object budget not applied:\n%s", out)
	}
	if !strings.Contains(out, "1.0 GiB") {
		t.Errorf("switch default for the op-log missing:\n%s", out)
	}

	// Flags alone: only the named artefact gets a roof; the op-log stays unlimited.
	connect = testConnector(t)
	code, out, errs = run(connect, "--realm", "acme", "--persona", "daan", "provision", "--budget-personas", "32MiB")
	if code != 0 {
		t.Fatalf("provision exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "32.0 MiB") {
		t.Errorf("explicit personas budget not applied:\n%s", out)
	}
	if !strings.Contains(out, "unlimited") {
		t.Errorf("unnamed artefacts should read unlimited:\n%s", out)
	}
}

// FR-005 (016): an explicit zero or negative budget is a flag error naming the
// artefact, before any connection.
func TestProvisionBudgetFlagRejectsZero(t *testing.T) {
	connect := testConnector(t)
	code, _, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision", "--budget-oplog", "0")
	if code != 2 {
		t.Fatalf("provision --budget-oplog 0 exit %d, want 2", code)
	}
	if !strings.Contains(errs, "budget-oplog") {
		t.Errorf("error output %q does not name the flag/artefact", errs)
	}
}

// US3 (016): re-provisioning reports roofs as found and never resizes.
func TestProvisionReportsRoofsOnRerun(t *testing.T) {
	connect := testConnector(t)
	if code, _, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision", "--budgets"); code != 0 {
		t.Fatalf("first provision exit %d: %s", code, errs)
	}
	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision", "--budget-oplog", "9GiB")
	if code != 0 {
		t.Fatalf("second provision exit %d: %s", code, errs)
	}
	if strings.Contains(out, "9.0 GiB") {
		t.Errorf("re-provision applied a budget to an existing artefact:\n%s", out)
	}
	if !strings.Contains(out, "1.0 GiB") {
		t.Errorf("as-found roof missing from report:\n%s", out)
	}
}
