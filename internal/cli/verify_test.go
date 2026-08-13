package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/internal/keystore"
)

// TestShowRendersVerificationGlyphs: a signed persona's ops show ✓ once its profile
// is in the directory; unsigned ops show no glyph; a substituted pin shows the loud
// banner and ✗.
func TestShowRendersVerificationGlyphs(t *testing.T) {
	connect := testConnector(t)
	tmp := t.TempDir()
	keyFile := filepath.Join(tmp, "acme-daan.ed25519")
	pinsFile := filepath.Join(tmp, "acme.json")
	base := []string{"--realm", "acme", "--persona", "daan", "--key-file", keyFile, "--pins-file", pinsFile}

	run(connect, append(base, "provision")...)
	run(connect, append(base, "key", "init")...)
	run(connect, append(base, "profile", "publish")...)

	code, out, errs := run(connect, append(base, "start", "Signed")...)
	if code != 0 {
		t.Fatalf("start: %s", errs)
	}
	path := strings.TrimSpace(out)
	if code, _, errs := run(connect, append(base, "post", path, "sealed statement")...); code != 0 {
		t.Fatalf("post: %s", errs)
	}
	// An unsigned persona posts too.
	if code, _, errs := run(connect, "--realm", "acme", "--persona", "ghost", "--pins-file", pinsFile, "post", path, "plain words"); code != 0 {
		t.Fatalf("unsigned post: %s", errs)
	}

	code, out, errs = run(connect, append(base, "show", path)...)
	if code != 0 {
		t.Fatalf("show: %s", errs)
	}
	var signedLine, unsignedLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "sealed statement") {
			signedLine = line
		}
		if strings.Contains(line, "plain words") {
			unsignedLine = line
		}
	}
	if !strings.Contains(signedLine, "✓") {
		t.Errorf("signed op line missing ✓: %q", signedLine)
	}
	if strings.ContainsAny(unsignedLine, "✓✗?") {
		t.Errorf("unsigned op line has a glyph: %q", unsignedLine)
	}

	// Substitution: replace daan's pin with a different key → banner + failed.
	pins, err := keystore.LoadPins(pinsFile, "acme")
	if err != nil {
		t.Fatal(err)
	}
	pins.Personas["daan"] = []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	if err := keystore.SavePins(pinsFile, pins); err != nil {
		t.Fatal(err)
	}

	code, out, errs = run(connect, append(base, "show", path)...)
	if code != 0 {
		t.Fatalf("show after substitution: %s", errs)
	}
	if !strings.Contains(out, "!! possible key substitution for daan") {
		t.Errorf("stdout missing the substitution banner:\n%s", out)
	}
	if !strings.Contains(errs, "!! possible key substitution for daan") {
		t.Errorf("stderr missing the substitution banner:\n%s", errs)
	}
	if !strings.Contains(out, "✗") {
		t.Errorf("distrusted persona's op not marked ✗:\n%s", out)
	}
	if !strings.Contains(out, "sealed statement") {
		t.Error("distrusted op was hidden — flags must never drop content")
	}
}
