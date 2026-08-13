// Package keystore is the one place both clients (CLI and MCP adapter) keep a
// persona's client-side signing state: the secret Ed25519 seed and the per-realm
// pin file. It owns only files and paths — no NATS, no crypto policy.
package keystore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/impire-io/soulstream-core/identity"
)

// EnvKeyFile and EnvPinsFile override the default file locations.
const (
	EnvKeyFile  = "SOULSTREAM_KEY_FILE"
	EnvPinsFile = "SOULSTREAM_PINS_FILE"
)

// DefaultKeyFile returns the default seed-file path for a persona in a realm:
// <user-config-dir>/soulstream/keys/<realm>-<persona>.ed25519.
func DefaultKeyFile(realm, persona string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("keystore: resolve config dir: %w", err)
	}
	return filepath.Join(dir, "soulstream", "keys", realm+"-"+persona+".ed25519"), nil
}

// DefaultPinsFile returns the default pins-file path for a realm:
// <user-config-dir>/soulstream/pins/<realm>.json.
func DefaultPinsFile(realm string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("keystore: resolve config dir: %w", err)
	}
	return filepath.Join(dir, "soulstream", "pins", realm+".json"), nil
}

// SaveKey writes a signing key's seed to path (base64, single line, mode 0600),
// creating parent directories. It refuses to overwrite an existing file: replacing
// a key is rotation, never a silent overwrite.
func SaveKey(path string, key *identity.SigningKey) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("keystore: key file %s already exists (rotate instead of overwriting)", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("keystore: probe key file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("keystore: create key dir: %w", err)
	}
	data := base64.StdEncoding.EncodeToString(key.Seed()) + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		return fmt.Errorf("keystore: write key file: %w", err)
	}
	return nil
}

// LoadKey reads a seed file written by SaveKey and reconstructs the signing key.
// A missing file returns (nil, nil): no key just means publishing unsigned.
func LoadKey(path string) (*identity.SigningKey, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("keystore: read key file: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("keystore: key file %s is not base64: %w", path, err)
	}
	key, err := identity.SigningKeyFromSeed(seed)
	if err != nil {
		return nil, fmt.Errorf("keystore: key file %s: %w", path, err)
	}
	return key, nil
}

// ReplaceKey swaps the seed file for a rotated key, keeping the previous seed at
// path+".prev" so a failed rotation is recoverable by hand.
func ReplaceKey(path string, newKey *identity.SigningKey) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("keystore: no key file to replace at %s: %w", path, err)
	}
	if err := os.Rename(path, path+".prev"); err != nil {
		return fmt.Errorf("keystore: keep previous key: %w", err)
	}
	if err := SaveKey(path, newKey); err != nil {
		// Best effort to restore the old seed so the persona is not left key-less.
		_ = os.Rename(path+".prev", path)
		return err
	}
	return nil
}

// Pins is a client's durable trust state for one realm: for each persona, the
// validated key chain as first seen (oldest first). Pins are only ever extended by
// valid rotations — a diverging directory is a substitution, not an update.
type Pins struct {
	Realm    string              `json:"realm"`
	Personas map[string][]string `json:"personas"`
}

// LoadPins reads the pins file for realm. A missing file returns empty pins — the
// first-contact state of TOFU. A pins file recorded for a different realm is an
// error: trust must never leak across realms.
func LoadPins(path, realm string) (Pins, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Pins{Realm: realm, Personas: map[string][]string{}}, nil
	}
	if err != nil {
		return Pins{}, fmt.Errorf("keystore: read pins file: %w", err)
	}
	var p Pins
	if err := json.Unmarshal(data, &p); err != nil {
		return Pins{}, fmt.Errorf("keystore: pins file %s is not valid JSON: %w", path, err)
	}
	if p.Realm != realm {
		return Pins{}, fmt.Errorf("keystore: pins file %s belongs to realm %q, not %q", path, p.Realm, realm)
	}
	if p.Personas == nil {
		p.Personas = map[string][]string{}
	}
	return p, nil
}

// SavePins writes the pins file atomically (temp file + rename), creating parent
// directories.
func SavePins(path string, p Pins) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("keystore: create pins dir: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("keystore: encode pins: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("keystore: write pins: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("keystore: replace pins: %w", err)
	}
	return nil
}

// ResolveKeyFile picks the seed-file path: explicit flag value, then the
// environment, then the per-realm default.
func ResolveKeyFile(flagValue, realm, persona string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv(EnvKeyFile); env != "" {
		return env, nil
	}
	return DefaultKeyFile(realm, persona)
}

// ResolvePinsFile picks the pins-file path: explicit flag value, then the
// environment, then the per-realm default.
func ResolvePinsFile(flagValue, realm string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv(EnvPinsFile); env != "" {
		return env, nil
	}
	return DefaultPinsFile(realm)
}
