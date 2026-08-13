package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/internal/keystore"
)

// TestKeyRotateFlow (US4): rotate publishes the hand-over, swaps the seed keeping
// .prev, and ops from both key eras render ✓ afterwards.
func TestKeyRotateFlow(t *testing.T) {
	connect := testConnector(t)
	tmp := t.TempDir()
	keyFile := filepath.Join(tmp, "acme-daan.ed25519")
	pinsFile := filepath.Join(tmp, "acme.json")
	base := []string{"--realm", "acme", "--persona", "daan", "--key-file", keyFile, "--pins-file", pinsFile}

	// Rotation without a key refuses.
	if code, _, _ := run(connect, append(base, "key", "rotate")...); code == 0 {
		t.Fatal("key rotate without a key should fail")
	}

	run(connect, append(base, "provision")...)
	run(connect, append(base, "key", "init")...)
	oldKey, err := keystore.LoadKey(keyFile)
	if err != nil || oldKey == nil {
		t.Fatal(err)
	}

	// Rotation without a published profile refuses (and leaves the seed alone).
	if code, _, _ := run(connect, append(base, "key", "rotate")...); code == 0 {
		t.Fatal("key rotate without a published profile should fail")
	}
	if cur, _ := keystore.LoadKey(keyFile); cur.PublicKey() != oldKey.PublicKey() {
		t.Fatal("failed rotation touched the seed file")
	}

	run(connect, append(base, "profile", "publish")...)

	// An op signed in the old era.
	_, out, _ := run(connect, append(base, "start", "Two Eras")...)
	path := strings.TrimSpace(out)
	if code, _, errs := run(connect, append(base, "post", path, "old era words")...); code != 0 {
		t.Fatalf("old-era post: %s", errs)
	}

	code, out, errs := run(connect, append(base, "key", "rotate")...)
	if code != 0 {
		t.Fatalf("key rotate exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "rotated signing key") {
		t.Errorf("rotate output: %q", out)
	}
	newKey, err := keystore.LoadKey(keyFile)
	if err != nil || newKey == nil {
		t.Fatal(err)
	}
	if newKey.PublicKey() == oldKey.PublicKey() {
		t.Fatal("seed file still holds the old key")
	}
	prev, err := keystore.LoadKey(keyFile + ".prev")
	if err != nil || prev == nil || prev.PublicKey() != oldKey.PublicKey() {
		t.Fatal(".prev does not hold the old key")
	}

	// An op signed in the new era.
	if code, _, errs := run(connect, append(base, "post", path, "new era words")...); code != 0 {
		t.Fatalf("new-era post: %s", errs)
	}

	// Both eras verify for a reader (SC-006) — and no substitution banner.
	code, out, errs = run(connect, append(base, "show", path)...)
	if code != 0 {
		t.Fatalf("show: %s", errs)
	}
	if strings.Contains(out, "!!") {
		t.Errorf("valid rotation raised a substitution banner:\n%s", out)
	}
	for _, body := range []string{"old era words", "new era words"} {
		line := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, body) {
				line = l
			}
		}
		if !strings.Contains(line, "✓") {
			t.Errorf("%q not verified after rotation: %q", body, line)
		}
	}
}
