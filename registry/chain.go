package registry

import (
	"fmt"

	"github.com/impire-io/soulstream-core/identity"
)

// Chain derives and validates a profile's key chain, oldest key first, current key
// last. The rules (all offline — a profile is self-validating):
//
//   - no signing key → empty chain (the persona publishes unsigned or unknown)
//   - no rotations   → chain is just the current key
//   - with rotations → the root is rotations[0].From; every link's Proof must be
//     From's signature over identity.RotationProofBytes(name, To); links must be
//     contiguous (each From equals the previous To); and the last To must equal the
//     current signing key.
//
// Any violation returns an error — the caller treats the persona as a possible
// substitution, never as "partially trusted".
func Chain(p Profile) ([]string, error) {
	if p.SigningKey == nil {
		return nil, nil
	}
	if len(p.Rotations) == 0 {
		return []string{p.SigningKey.Ed25519}, nil
	}

	chain := []string{p.Rotations[0].From}
	prev := p.Rotations[0].From
	for i, r := range p.Rotations {
		if r.From != prev {
			return nil, fmt.Errorf("registry: %s: rotations[%d] breaks the chain (from %.12q, expected %.12q)",
				p.Name, i, r.From, prev)
		}
		if !identity.VerifySignature(r.From, identity.RotationProofBytes(p.Name, r.To), r.Proof) {
			return nil, fmt.Errorf("registry: %s: rotations[%d] proof does not verify", p.Name, i)
		}
		chain = append(chain, r.To)
		prev = r.To
	}
	if prev != p.SigningKey.Ed25519 {
		return nil, fmt.Errorf("registry: %s: chain ends at %.12q but signing_key is %.12q",
			p.Name, prev, p.SigningKey.Ed25519)
	}
	return chain, nil
}

// BuildKeyring reconciles published profiles against a reader's pinned chains and
// returns the keyring plus the updated pin map:
//
//   - persona not pinned yet → validate the chain and pin it (trust on first use)
//   - pinned chain is a prefix of (or equal to) the published chain → trust it and
//     re-pin the longer chain
//   - anything else — diverged, shortened, or an invalid chain — → the persona is
//     distrusted; the pin is kept untouched as evidence, never overwritten
//
// The input pin map is not mutated. Personas that are pinned but absent from
// profiles keep their pins and stay trusted for their pinned keys.
func BuildKeyring(profiles []Profile, pinned map[string][]string) (*identity.Keyring, map[string][]string) {
	kr := &identity.Keyring{
		Keys:       make(map[string][]string),
		Distrusted: make(map[string]bool),
	}
	newPins := make(map[string][]string, len(pinned))
	for persona, chain := range pinned {
		newPins[persona] = append([]string(nil), chain...)
		kr.Keys[persona] = append([]string(nil), chain...)
	}

	for _, p := range profiles {
		chain, err := Chain(p)
		if err != nil {
			kr.Distrusted[p.Name] = true
			delete(kr.Keys, p.Name)
			continue
		}
		if len(chain) == 0 {
			// A profile without a key neither trusts nor distrusts; an existing pin
			// (from when the persona had a key) would vanish from the directory —
			// that is a shortened chain, i.e. substitution territory.
			if len(newPins[p.Name]) > 0 {
				kr.Distrusted[p.Name] = true
				delete(kr.Keys, p.Name)
			}
			continue
		}

		pin := newPins[p.Name]
		if !isPrefix(pin, chain) {
			kr.Distrusted[p.Name] = true
			delete(kr.Keys, p.Name)
			continue
		}
		kr.Keys[p.Name] = append([]string(nil), chain...)
		newPins[p.Name] = append([]string(nil), chain...)
	}
	return kr, newPins
}

// isPrefix reports whether pin is a (possibly empty, possibly equal) prefix of chain.
func isPrefix(pin, chain []string) bool {
	if len(pin) > len(chain) {
		return false
	}
	for i := range pin {
		if pin[i] != chain[i] {
			return false
		}
	}
	return true
}
