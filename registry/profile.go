package registry

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/impire-io/soulstream-core/identity"
)

// SigningKeyInfo is a persona's current public signing key. Since is author-claimed
// and informational — like Soulstream-Ts, it never decides verification outcomes.
type SigningKeyInfo struct {
	Ed25519 string    `json:"ed25519"`
	Since   time.Time `json:"since"`
}

// Rotation is one link in a persona's key chain: the new key (To) endorsed by the
// previous key (From) via Proof — From's Ed25519 signature over
// identity.RotationProofBytes(persona, To).
type Rotation struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Proof string `json:"proof"`
}

// Profile is a persona's directory entry — the KV value stored under the persona's
// name in the soulstream-personas bucket. A persona is a voice with a key: the entry
// carries presentation text and key material, never a classification — the protocol
// cannot verify what sort of entity controls the key, so it does not pretend to know.
type Profile struct {
	Name                string               `json:"name"`
	DisplayName         string               `json:"display_name,omitempty"`
	Description         string               `json:"description,omitempty"`
	OperatedBy          string               `json:"operated_by,omitempty"`
	OperatorAttestation *OperatorAttestation `json:"operator_attestation,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	SigningKey          *SigningKeyInfo      `json:"signing_key,omitempty"`
	Rotations           []Rotation           `json:"rotations,omitempty"`
}

// OperatorAttestation is the operator's countersignature over the operator claim:
// Sig is the operator's Ed25519 signature over
// identity.AttestationBytes(operated_by, name, operated_key). The operator and
// operated names are NOT duplicated here — they are OperatedBy and Name on the same
// profile, so there is exactly one copy that can never disagree with itself.
// OperatedKey is the operated persona's public key as the operator saw it, "" when
// the persona had none.
type OperatorAttestation struct {
	OperatedKey string `json:"operated_key,omitempty"`
	Sig         string `json:"sig"`
}

// Validate checks the profile's shape: slug names and well-formed key material.
// It does not verify rotation proofs — that is Chain's job.
func (p Profile) Validate() error {
	if err := identity.CheckName(p.Name); err != nil {
		return fmt.Errorf("registry: profile name: %w", err)
	}
	if p.OperatedBy != "" {
		if err := identity.CheckName(p.OperatedBy); err != nil {
			return fmt.Errorf("registry: profile %q: operated_by: %w", p.Name, err)
		}
		if p.OperatedBy == p.Name {
			return fmt.Errorf("registry: profile %q: operated_by must name another persona", p.Name)
		}
	}
	if p.OperatorAttestation != nil {
		if p.OperatedBy == "" {
			return fmt.Errorf("registry: profile %q: operator_attestation without operated_by", p.Name)
		}
		if p.OperatorAttestation.Sig == "" {
			return fmt.Errorf("registry: profile %q: operator_attestation: missing sig", p.Name)
		}
		if k := p.OperatorAttestation.OperatedKey; k != "" {
			if err := checkKeyB64(k); err != nil {
				return fmt.Errorf("registry: profile %q: operator_attestation.operated_key: %w", p.Name, err)
			}
		}
	}
	if p.SigningKey != nil {
		if err := checkKeyB64(p.SigningKey.Ed25519); err != nil {
			return fmt.Errorf("registry: profile %q: signing_key: %w", p.Name, err)
		}
	}
	for i, r := range p.Rotations {
		if err := checkKeyB64(r.From); err != nil {
			return fmt.Errorf("registry: profile %q: rotations[%d].from: %w", p.Name, i, err)
		}
		if err := checkKeyB64(r.To); err != nil {
			return fmt.Errorf("registry: profile %q: rotations[%d].to: %w", p.Name, i, err)
		}
		if r.Proof == "" {
			return fmt.Errorf("registry: profile %q: rotations[%d]: missing proof", p.Name, i)
		}
	}
	if p.SigningKey == nil && len(p.Rotations) > 0 {
		return fmt.Errorf("registry: profile %q: rotations without a signing_key", p.Name)
	}
	return nil
}

// checkKeyB64 checks that s is standard base64 of a 32-byte Ed25519 public key.
func checkKeyB64(s string) error {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return fmt.Errorf("not base64: %w", err)
	}
	if len(raw) != 32 {
		return fmt.Errorf("decodes to %d bytes, want 32", len(raw))
	}
	return nil
}
