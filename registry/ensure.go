// F1's closing act (hq 02-DESIGN/soulstream-core/extensions/tenancy.md):
// a persona signing key that materializes in an external custodian on
// first touch is unverifiable by every reader until its public half
// reaches this directory — readers build keyrings from profiles and
// pins, nothing else. The party constructing a signer therefore owns
// making the key resolvable, by calling EnsureSigningKey beside the
// construction.

package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
)

// EnsureSigningKey makes the client persona's signing key resolvable by
// readers: a persona with no directory entry gets a minimal profile
// carrying the key; an entry without key material gains it, display
// metadata untouched; an entry already carrying the same key is left
// alone. A different stored key returns [ErrKeyConflict] — key changes
// go through [Rotate], never an overwrite. Idempotent by construction:
// call it at every signer construction.
func EnsureSigningKey(ctx context.Context, c *realm.Client, signer identity.Signer) error {
	pub := signer.PublicKey()
	if pub == "" {
		return errors.New("registry: ensure signing key: signer has no public key")
	}
	persona := c.Persona()
	stored, found, err := Lookup(ctx, c, persona)
	if err != nil {
		return fmt.Errorf("registry: ensure signing key for %q: %w", persona, err)
	}
	if found && stored.SigningKey != nil {
		if stored.SigningKey.Ed25519 == pub {
			return nil
		}
		return fmt.Errorf("%w: persona %q", ErrKeyConflict, persona)
	}
	p := stored
	if !found {
		p = Profile{Name: persona}
	}
	p.SigningKey = &SigningKeyInfo{Ed25519: pub, Since: time.Now().UTC()}
	return Publish(ctx, c, p)
}
