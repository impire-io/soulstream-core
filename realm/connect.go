package realm

import (
	"context"
	"fmt"
	"reflect"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/natscontext"

	"github.com/impire-io/soulstream-core/identity"
)

// Config is the required input to construct a [Client].
type Config struct {
	// ContextName is the named NATS context to connect from (empty uses the selected
	// context).
	ContextName string
	// Realm is the realm name; validated as a slug and bound into canonical records.
	Realm string
	// Persona is optional; when set, write-side attribution is enforced against it.
	Persona string
	// Signer is optional; when set, every op this client publishes carries an
	// Ed25519 signature over its canonical record, and a signing failure fails
	// the operation — there is no unsigned fallback. Nil publishes unsigned,
	// exactly as before signing existed. A typed-nil signer (a nil
	// *identity.SigningKey assigned into the field) is refused by Connect and
	// NewClient with an error naming the fix — a missing key must leave the
	// field unset.
	Signer identity.Signer
}

// Client wraps a live NATS connection and JetStream handle for one realm.
type Client struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	cfg Config
}

// Connect validates cfg, connects via the named NATS context, and builds a JetStream
// handle. It fails fast — before touching any realm artefact — when a name is
// invalid, the context is missing, the server is unreachable, or JetStream is
// unavailable.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err // fail fast, before any server contact
	}

	nc, _, err := natscontext.Connect(cfg.ContextName, nats.Name("soulstream/"+cfg.Realm))
	if err != nil {
		return nil, fmt.Errorf("realm: connect via context %q: %w", cfg.ContextName, err)
	}

	return finishConnect(ctx, nc, cfg)
}

// NewClient builds a realm client from an existing NATS connection, for callers that
// already hold one (higher-level engines, tests). It validates the config and confirms
// JetStream is available. The client takes ownership of nc and closes it on Close.
func NewClient(ctx context.Context, nc *nats.Conn, cfg Config) (*Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return finishConnect(ctx, nc, cfg)
}

func validateConfig(cfg Config) error {
	if err := identity.CheckName(cfg.Realm); err != nil {
		return fmt.Errorf("realm: invalid realm name: %w", err)
	}
	if cfg.Persona != "" {
		if err := identity.CheckName(cfg.Persona); err != nil {
			return fmt.Errorf("realm: invalid persona name: %w", err)
		}
	}
	if cfg.Signer != nil && signerIsTypedNil(cfg.Signer) {
		return fmt.Errorf("realm: Config.Signer holds a typed-nil %T — a missing key must leave the field unset (assign a signer only when it is non-nil)", cfg.Signer)
	}
	return nil
}

// signerIsTypedNil reports whether a non-nil Signer interface wraps a nil
// concrete value — Go's typed-nil trap, which would pass every `!= nil` check
// and panic at the first Sign. Caught here, at construction, it is a clear
// configuration error instead of a crash mid-publish.
func signerIsTypedNil(s identity.Signer) bool {
	v := reflect.ValueOf(s)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Func, reflect.Chan, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}

func finishConnect(ctx context.Context, nc *nats.Conn, cfg Config) (*Client, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("realm: initialise jetstream: %w", err)
	}

	// Fail fast if JetStream is not actually available on the server.
	if _, err := js.AccountInfo(ctx); err != nil {
		nc.Close()
		return nil, fmt.Errorf("realm: jetstream unavailable: %w", err)
	}

	return &Client{nc: nc, js: js, cfg: cfg}, nil
}

// Provision brings this client's realm to the mandated shape (see [ProvisionOn]).
// An optional [Budgets] value (at most one) sets creation-time byte roofs.
func (c *Client) Provision(ctx context.Context, budgets ...Budgets) (*ProvisionReport, error) {
	return ProvisionOn(ctx, c.js, budgets...)
}

// EnforceAuthor is the write-side attribution guard. When the client is persona-bound
// (Config.Persona set), it returns an error unless author is that persona, so the
// client can only ever publish as itself. A read-only client (no persona) permits any
// author — enforcing attribution is not its job. Publish paths in later features call
// this before sending.
func (c *Client) EnforceAuthor(author string) error {
	if c.cfg.Persona == "" {
		return nil
	}
	return identity.EnforceAuthor(c.cfg.Persona, author)
}

// JetStream returns the client's JetStream handle, so higher-level engines (such as the
// topic package) can build on the realm without re-connecting.
func (c *Client) JetStream() jetstream.JetStream { return c.js }

// Conn returns the client's raw NATS connection, for the core request-reply
// surfaces (discovery scatter/gather) that live beside the stream.
func (c *Client) Conn() *nats.Conn { return c.nc }

// Realm returns the client's realm name.
func (c *Client) Realm() string { return c.cfg.Realm }

// Persona returns the client's configured persona (empty if read-only).
func (c *Client) Persona() string { return c.cfg.Persona }

// Signer returns the client's signer, or nil when this client publishes
// unsigned.
func (c *Client) Signer() identity.Signer { return c.cfg.Signer }

// Close releases the underlying NATS connection.
func (c *Client) Close() error {
	c.nc.Close()
	return nil
}
