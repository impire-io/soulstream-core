package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
)

// decodeProfile decodes a stored profile document strictly: any unknown field —
// including the retired "kind" — makes the whole document invalid, never silently
// accepted or stripped. The json error names the offending field; callers add the
// persona. We are the only users: an out-of-date document is republished, not
// tolerated.
func decodeProfile(data []byte) (Profile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p Profile
	if err := dec.Decode(&p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// ProfileWarning names a directory entry a bulk read skipped because its stored
// document is invalid (strict decode). Bulk readers warn loudly and continue: the
// persona's signatures then report unknown-key, and the surrounding read never fails
// — losing testimony is worse than reading it with a warning.
type ProfileWarning struct {
	Persona string
	Err     error
}

// BucketName is the persona directory's KV bucket (provisioned by realm.Provision).
const BucketName = realm.PersonasBucket

// ErrKeyConflict means a publish tried to change a persona's stored key material
// without a rotation proof. Key changes go through rotation — anything else would be
// indistinguishable from a substitution attack.
var ErrKeyConflict = errors.New("registry: profile already holds a different key (rotate instead)")

// Publish creates or updates own profile — the client persona's directory entry.
//
// It is create-or-metadata-update: a persona with no entry is created (KV Create);
// an existing entry has its display metadata replaced while the stored signing_key
// and rotations remain authoritative — an incoming nil or identical key preserves
// them, an incoming different key returns [ErrKeyConflict]. Both paths use the KV's
// own optimistic concurrency, so racing clients get an error, never a lost write.
func Publish(ctx context.Context, c *realm.Client, p Profile) error {
	if err := c.EnforceAuthor(p.Name); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	kv, err := bucket(ctx, c)
	if err != nil {
		return err
	}

	entry, err := kv.Get(ctx, p.Name)
	switch {
	case errors.Is(err, jetstream.ErrKeyNotFound):
		data, merr := json.Marshal(p)
		if merr != nil {
			return fmt.Errorf("registry: encode profile: %w", merr)
		}
		if _, cerr := kv.Create(ctx, p.Name, data); cerr != nil {
			return fmt.Errorf("registry: create profile %q: %w", p.Name, cerr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("registry: read profile %q: %w", p.Name, err)
	}

	stored, uerr := decodeProfile(entry.Value())
	if uerr != nil {
		// Republishing is the ONE documented recovery from an invalid stored
		// document (e.g. a pre-014 profile still carrying "kind"): fall back to a
		// lenient decode of the old document so the key-conflict guard below still
		// holds — the stored key material stays authoritative even during recovery.
		// Only unreadable JSON remains a hard error.
		if lerr := json.Unmarshal(entry.Value(), &stored); lerr != nil {
			return fmt.Errorf("registry: stored profile %q is unreadable: %w", p.Name, lerr)
		}
	}

	// Stored key material is authoritative. A different incoming key without a
	// rotation is refused loudly.
	if p.SigningKey != nil && stored.SigningKey != nil && p.SigningKey.Ed25519 != stored.SigningKey.Ed25519 {
		return fmt.Errorf("%w: persona %q", ErrKeyConflict, p.Name)
	}
	if stored.SigningKey != nil {
		p.SigningKey = stored.SigningKey
		p.Rotations = stored.Rotations
	}
	// Creation time is set on first publish and preserved ever after.
	if !stored.CreatedAt.IsZero() {
		p.CreatedAt = stored.CreatedAt
	}

	data, merr := json.Marshal(p)
	if merr != nil {
		return fmt.Errorf("registry: encode profile: %w", merr)
	}
	if _, uerr := kv.Update(ctx, p.Name, data, entry.Revision()); uerr != nil {
		return fmt.Errorf("registry: update profile %q: %w", p.Name, uerr)
	}
	return nil
}

// Rotate replaces the client persona's signing key: it appends a rotation entry —
// the new key endorsed by the old key's signature over the domain-separated proof
// bytes — sets the new key as current, and writes with the read revision, so a lost
// race is an error, never a blind overwrite. It returns the updated profile.
//
// Rotation requires an existing published profile whose current key matches oldKey:
// there is nothing to rotate *from* otherwise, and endorsing a key the directory
// does not hold would be indistinguishable from substitution.
//
// Both keys are Signers, not concrete key material: the old key needs only to
// sign the proof and name itself, the new key only to name itself — so either
// may live with a custodian. A signing failure aborts before any directory write.
func Rotate(ctx context.Context, c *realm.Client, oldKey, newKey identity.Signer) (Profile, error) {
	persona := c.Persona()
	if persona == "" {
		return Profile{}, errors.New("registry: rotation requires a persona-bound client")
	}
	kv, err := bucket(ctx, c)
	if err != nil {
		return Profile{}, err
	}
	entry, err := kv.Get(ctx, persona)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return Profile{}, fmt.Errorf("registry: no published profile for %q — publish one before rotating", persona)
		}
		return Profile{}, fmt.Errorf("registry: read profile %q: %w", persona, err)
	}
	p, err := decodeProfile(entry.Value())
	if err != nil {
		return Profile{}, fmt.Errorf("registry: stored profile %q is invalid: %w", persona, err)
	}
	if p.SigningKey == nil {
		return Profile{}, fmt.Errorf("registry: profile %q has no signing key to rotate from", persona)
	}
	if p.SigningKey.Ed25519 != oldKey.PublicKey() {
		return Profile{}, fmt.Errorf("registry: profile %q holds a different current key than the one rotating from", persona)
	}

	newPub := newKey.PublicKey()
	proof, err := oldKey.Sign(identity.RotationProofBytes(persona, newPub))
	if err != nil {
		return Profile{}, fmt.Errorf("registry: sign rotation proof: %w", err)
	}
	p.Rotations = append(p.Rotations, Rotation{
		From:  oldKey.PublicKey(),
		To:    newPub,
		Proof: proof,
	})
	p.SigningKey = &SigningKeyInfo{Ed25519: newPub, Since: time.Now().UTC()}

	data, err := json.Marshal(p)
	if err != nil {
		return Profile{}, fmt.Errorf("registry: encode profile: %w", err)
	}
	if _, err := kv.Update(ctx, persona, data, entry.Revision()); err != nil {
		return Profile{}, fmt.Errorf("registry: rotate %q: %w", persona, err)
	}
	return p, nil
}

// Lookup reads one persona's profile. A realm without a directory, or a persona
// without an entry, is (Profile{}, false, nil) — absence is a normal state, never
// an error.
func Lookup(ctx context.Context, c *realm.Client, persona string) (Profile, bool, error) {
	kv, err := bucket(ctx, c)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return Profile{}, false, nil
		}
		return Profile{}, false, err
	}
	entry, err := kv.Get(ctx, persona)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return Profile{}, false, nil
		}
		return Profile{}, false, fmt.Errorf("registry: read profile %q: %w", persona, err)
	}
	p, err := decodeProfile(entry.Value())
	if err != nil {
		return Profile{}, false, fmt.Errorf("registry: profile %q is invalid: %w", persona, err)
	}
	return p, true, nil
}

// All reads every profile in the directory, plus a warning per entry whose stored
// document is invalid (skipped, never silently). A realm without a directory yields
// an empty slice — readers degrade, they do not fail.
func All(ctx context.Context, c *realm.Client) ([]Profile, []ProfileWarning, error) {
	kv, err := bucket(ctx, c)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: list personas: %w", err)
	}

	var profiles []Profile
	var warnings []ProfileWarning
	for name := range lister.Keys() {
		entry, err := kv.Get(ctx, name)
		if err != nil {
			if errors.Is(err, jetstream.ErrKeyNotFound) {
				continue // deleted between list and get
			}
			return nil, nil, fmt.Errorf("registry: read profile %q: %w", name, err)
		}
		p, derr := decodeProfile(entry.Value())
		if derr != nil {
			// One invalid entry must not hide the rest of the directory — but it
			// must not pass silently either.
			warnings = append(warnings, ProfileWarning{Persona: name, Err: derr})
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles, warnings, nil
}

func bucket(ctx context.Context, c *realm.Client) (jetstream.KeyValue, error) {
	kv, err := c.JetStream().KeyValue(ctx, BucketName)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("registry: open persona directory: %w", err)
	}
	return kv, nil
}
