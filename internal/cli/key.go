package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/registry"
)

// keyFilePath resolves the seed-file location for cfg: --key-file flag, then
// SOULSTREAM_KEY_FILE, then the per-realm default under the user config dir.
func keyFilePath(cfg Config) (string, error) {
	return keystore.ResolveKeyFile(cfg.KeyFile, cfg.Realm, cfg.Persona)
}

// loadSigner loads the persona's signing key for cfg, or nil when no key file
// exists (publish unsigned). Both the production connector and tests use it, so
// signing behaves identically everywhere.
func loadSigner(cfg Config) (*identity.SigningKey, error) {
	path, err := keyFilePath(cfg)
	if err != nil {
		return nil, err
	}
	return keystore.LoadKey(path)
}

// cmdKey manages the persona's signing identity. init and show never connect (keys
// are client-side state); rotate publishes the hand-over to the directory first,
// then swaps the local seed.
func cmdKey(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "soulstream: usage: key init|show|rotate")
		return 2
	}
	if cfg.Persona == "" {
		fmt.Fprintln(stderr, "soulstream: key commands require a persona (--persona or SOULSTREAM_PERSONA)")
		return 2
	}
	if cfg.Realm == "" {
		fmt.Fprintln(stderr, "soulstream: key commands require a realm (--realm or SOULSTREAM_REALM)")
		return 2
	}

	switch args[0] {
	case "init":
		return keyInit(cfg, stdout, stderr)
	case "show":
		return keyShow(cfg, stdout, stderr)
	case "rotate":
		return keyRotate(ctx, connect, cfg, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "soulstream: unknown key subcommand %q (want init|show|rotate)\n", args[0])
		return 2
	}
}

func keyInit(cfg Config, stdout, stderr io.Writer) int {
	path, err := keyFilePath(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	key, err := identity.GenerateSigningKey()
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	if err := keystore.SaveKey(path, key); err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "generated signing key for persona %q\n", cfg.Persona)
	fmt.Fprintf(stdout, "public key: %s (ed25519)\n", key.PublicKey())
	fmt.Fprintf(stdout, "seed file:  %s\n", path)
	return 0
}

// keyRotate publishes the rotation (new key endorsed by the old) and only then
// replaces the local seed file, keeping the old seed at <file>.prev. Order matters:
// if the directory write fails, the local key is untouched and nothing changed.
func keyRotate(ctx context.Context, connect Connector, cfg Config, stdout, stderr io.Writer) int {
	path, err := keyFilePath(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	oldKey, err := keystore.LoadKey(path)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	if oldKey == nil {
		fmt.Fprintf(stderr, "soulstream: no signing key for %q (looked at %s); run: key init\n", cfg.Persona, path)
		return 2
	}
	newKey, err := identity.GenerateSigningKey()
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}

	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		if _, err := registry.Rotate(ctx, c, oldKey, newKey); err != nil {
			return err
		}
		if err := keystore.ReplaceKey(path, newKey); err != nil {
			return fmt.Errorf("rotation published but the local seed swap failed — recover the new seed manually: %w", err)
		}
		fmt.Fprintf(stdout, "rotated signing key for %q: %s → %s\n", cfg.Persona, oldKey.PublicKey(), newKey.PublicKey())
		fmt.Fprintf(stdout, "previous seed kept at %s.prev\n", path)
		return nil
	})
}

func keyShow(cfg Config, stdout, stderr io.Writer) int {
	path, err := keyFilePath(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	key, err := keystore.LoadKey(path)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	if key == nil {
		fmt.Fprintf(stderr, "soulstream: no signing key for %q (looked at %s); run: key init\n", cfg.Persona, path)
		return 2
	}
	fmt.Fprintf(stdout, "public key: %s (ed25519)\n", key.PublicKey())
	return 0
}
