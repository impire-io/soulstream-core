package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/registry"
	"github.com/impire-io/soulstream-core/topic"
)

// Connector builds a realm client from config. It is injectable so tests can supply a
// client bound to an in-process server instead of a named NATS context.
type Connector func(ctx context.Context, cfg Config) (*realm.Client, error)

// realmConnect is the production connector: it dials the named NATS context, and
// signs published ops when the persona's key file exists.
func realmConnect(ctx context.Context, cfg Config) (*realm.Client, error) {
	signer, err := loadSigner(cfg)
	if err != nil {
		return nil, err
	}
	rcfg := realm.Config{
		ContextName: cfg.Context,
		Realm:       cfg.Realm,
		Persona:     cfg.Persona,
	}
	// Assign only a real key: a typed-nil *SigningKey inside the interface
	// would read as "configured to sign" and panic at first use.
	if signer != nil {
		rcfg.Signer = signer
	}
	return realm.Connect(ctx, rcfg)
}

// withClient connects, enforces a persona for write commands, runs fn, and maps the
// outcome to an exit code: 0 on success, 2 on error (with a message on stderr).
func withClient(ctx context.Context, connect Connector, cfg Config, requirePersona bool, stderr io.Writer, fn func(*realm.Client) error) int {
	if requirePersona && cfg.Persona == "" {
		fmt.Fprintln(stderr, "soulstream: this command requires a persona (--persona or SOULSTREAM_PERSONA)")
		return 2
	}
	c, err := connect(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	defer func() { _ = c.Close() }()

	if err := fn(c); err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	return 0
}

// openAndMaterialise opens a handle and materialises it so posts parent onto the current
// tip and closed-topic warnings fire.
func openAndMaterialise(ctx context.Context, c *realm.Client, path string) (*topic.Handle, error) {
	h := topic.Open(c, path)
	if _, err := h.Materialise(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// realmKeyring builds the reader's keyring: pins + directory → keyring, persisting
// extended pins. A realm without a directory and without pins yields nil (signed ops
// degrade to unknown-key). Errors also degrade to nil — verification state must never
// break reading. Invalid directory entries are skipped with a loud warning on stderr;
// their personas simply stay unknown to the keyring.
func realmKeyring(ctx context.Context, c *realm.Client, cfg Config, stderr io.Writer) *identity.Keyring {
	pinsPath, err := keystore.ResolvePinsFile(cfg.PinsFile, cfg.Realm)
	if err != nil {
		return nil
	}
	pins, err := keystore.LoadPins(pinsPath, cfg.Realm)
	if err != nil {
		return nil
	}
	profiles, warnings, err := registry.All(ctx, c)
	if err != nil {
		return nil
	}
	for _, w := range warnings {
		fmt.Fprintf(stderr, "soulstream: WARNING: directory profile %q is invalid and was skipped (republish it): %v\n", w.Persona, w.Err)
	}
	if len(profiles) == 0 && len(pins.Personas) == 0 {
		return nil
	}

	kr, newPins := registry.BuildKeyring(profiles, pins.Personas)
	pins.Personas = newPins
	_ = keystore.SavePins(pinsPath, pins) // best effort; reading must not fail on pin I/O
	return kr
}
