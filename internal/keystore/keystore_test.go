package keystore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
)

func TestKeySaveLoadRoundTrip(t *testing.T) {
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "keys", "acme-daan.ed25519")

	if err := SaveKey(path, key); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	loaded, err := LoadKey(path)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if loaded.PublicKey() != key.PublicKey() {
		t.Error("loaded key differs from saved key")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("key file mode = %v, want 0600", info.Mode().Perm())
		}
	}
}

func TestSaveKeyRefusesOverwrite(t *testing.T) {
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "k.ed25519")
	if err := SaveKey(path, key); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	if err := SaveKey(path, key); err == nil {
		t.Error("SaveKey overwrote an existing key file")
	}
}

func TestLoadKeyMissingMeansUnsigned(t *testing.T) {
	key, err := LoadKey(filepath.Join(t.TempDir(), "nope.ed25519"))
	if err != nil {
		t.Fatalf("LoadKey on missing file: %v", err)
	}
	if key != nil {
		t.Error("missing key file must load as nil (publish unsigned)")
	}
}

func TestLoadKeyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.ed25519")
	if err := os.WriteFile(path, []byte("not base64 at all %%\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadKey(path); err == nil {
		t.Error("LoadKey accepted a garbage file")
	}
}

func TestReplaceKeyKeepsPrev(t *testing.T) {
	oldKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	newKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "k.ed25519")
	if err := SaveKey(path, oldKey); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}

	if err := ReplaceKey(path, newKey); err != nil {
		t.Fatalf("ReplaceKey: %v", err)
	}
	cur, err := LoadKey(path)
	if err != nil {
		t.Fatalf("LoadKey: %v", err)
	}
	if cur.PublicKey() != newKey.PublicKey() {
		t.Error("current key is not the new key")
	}
	prev, err := LoadKey(path + ".prev")
	if err != nil {
		t.Fatalf("LoadKey prev: %v", err)
	}
	if prev.PublicKey() != oldKey.PublicKey() {
		t.Error(".prev is not the old key")
	}
}

func TestReplaceKeyRequiresExisting(t *testing.T) {
	newKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := ReplaceKey(filepath.Join(t.TempDir(), "absent.ed25519"), newKey); err == nil {
		t.Error("ReplaceKey without an existing key must fail")
	}
}

func TestPinsRoundTripAndRealmGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins", "acme.json")

	p, err := LoadPins(path, "acme")
	if err != nil {
		t.Fatalf("LoadPins on missing file: %v", err)
	}
	if len(p.Personas) != 0 || p.Realm != "acme" {
		t.Fatalf("fresh pins = %+v", p)
	}

	p.Personas["architect"] = []string{"rootB64", "currentB64"}
	if err := SavePins(path, p); err != nil {
		t.Fatalf("SavePins: %v", err)
	}

	loaded, err := LoadPins(path, "acme")
	if err != nil {
		t.Fatalf("LoadPins: %v", err)
	}
	chain := loaded.Personas["architect"]
	if len(chain) != 2 || chain[0] != "rootB64" || chain[1] != "currentB64" {
		t.Errorf("loaded chain = %v", chain)
	}

	if _, err := LoadPins(path, "other-realm"); err == nil {
		t.Error("pins from another realm loaded without error — trust leaked across realms")
	}
	if leftovers, _ := filepath.Glob(path + ".tmp"); len(leftovers) != 0 {
		t.Errorf("temp file left behind: %v", leftovers)
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Setenv(EnvKeyFile, "/env/key")
	t.Setenv(EnvPinsFile, "/env/pins")

	if got, err := ResolveKeyFile("/flag/key", "acme", "daan"); err != nil || got != "/flag/key" {
		t.Errorf("flag should win: got %q, %v", got, err)
	}
	if got, err := ResolveKeyFile("", "acme", "daan"); err != nil || got != "/env/key" {
		t.Errorf("env should win over default: got %q, %v", got, err)
	}
	if got, err := ResolvePinsFile("", "acme"); err != nil || got != "/env/pins" {
		t.Errorf("env should win over default: got %q, %v", got, err)
	}

	t.Setenv(EnvKeyFile, "")
	got, err := ResolveKeyFile("", "acme", "daan")
	if err != nil {
		t.Fatalf("default resolution: %v", err)
	}
	if !strings.Contains(got, filepath.Join("soulstream", "keys", "acme-daan.ed25519")) {
		t.Errorf("default key path = %q", got)
	}
}
