package registry

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/impire-io/soulstream-core/identity"
)

// Operator-claim statuses, as reported wherever a profile is displayed. A claim
// (operated_by) with no attestation, or one whose operator has no validated chain to
// check against, is unverified — visible, never hidden, never presented as vouched.
const (
	ClaimAttested   = "attested"
	ClaimUnverified = "unverified"
	ClaimFailed     = "failed"
)

// AttestationStatus reports the operator claim's status for profile p: "" when the
// profile makes no claim, otherwise one of the Claim* constants.
//
// The rule mirrors op-signature verification: the countersignature must verify
// against ANY key in the operator's validated chain, and the bound operated key must
// be "" (attested before the persona had a key) or a member of the operated persona's
// own chain — so routine rotation on either side never breaks a vouch, while a vouch
// can never be replayed onto a substituted key. A distrusted operator fails the
// claim: substitution territory poisons vouches too.
func AttestationStatus(p Profile, operatorChain []string, operatorDistrusted bool, operatedChain []string) string {
	if p.OperatedBy == "" {
		return ""
	}
	if p.OperatorAttestation == nil {
		return ClaimUnverified
	}
	if operatorDistrusted {
		return ClaimFailed
	}
	if len(operatorChain) == 0 {
		return ClaimUnverified
	}
	att := p.OperatorAttestation
	if att.OperatedKey != "" && !slices.Contains(operatedChain, att.OperatedKey) {
		return ClaimFailed
	}
	statement := identity.AttestationBytes(p.OperatedBy, p.Name, att.OperatedKey)
	for _, key := range operatorChain {
		if identity.VerifySignature(key, statement, att.Sig) {
			return ClaimAttested
		}
	}
	return ClaimFailed
}

// AttestationToken is the portable form of an attestation: the operator generates it
// on their own side (the secret key never moves) and hands it to the operated
// persona, who includes it when publishing its own profile. Profiles stay strictly
// self-published.
type AttestationToken struct {
	Operator    string `json:"operator"`
	Operated    string `json:"operated"`
	OperatedKey string `json:"operated_key,omitempty"`
	Sig         string `json:"sig"`
}

// NewAttestationToken signs the attestation statement with the operator's signer —
// a local key or a delegate whose key lives with a custodian; only the capability
// to sign is needed — and returns the token as base64 JSON, safe to paste through
// chat or a topic.
func NewAttestationToken(signer identity.Signer, operator, operated, operatedKeyB64 string) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("registry: attesting requires the operator's signing key")
	}
	if err := identity.CheckName(operator); err != nil {
		return "", fmt.Errorf("registry: attestation operator: %w", err)
	}
	if err := identity.CheckName(operated); err != nil {
		return "", fmt.Errorf("registry: attestation operated persona: %w", err)
	}
	if operator == operated {
		return "", fmt.Errorf("registry: a persona cannot attest to operating itself")
	}
	sig, err := signer.Sign(identity.AttestationBytes(operator, operated, operatedKeyB64))
	if err != nil {
		return "", fmt.Errorf("registry: sign attestation: %w", err)
	}
	tok := AttestationToken{
		Operator:    operator,
		Operated:    operated,
		OperatedKey: operatedKeyB64,
		Sig:         sig,
	}
	raw, err := json.Marshal(tok)
	if err != nil {
		return "", fmt.Errorf("registry: encode attestation token: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// ParseAttestationToken decodes a portable attestation token. It checks shape only —
// whether the signature actually vouches is AttestationStatus's job on the read side.
func ParseAttestationToken(s string) (AttestationToken, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return AttestationToken{}, fmt.Errorf("registry: attestation token is not base64: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var tok AttestationToken
	if err := dec.Decode(&tok); err != nil {
		return AttestationToken{}, fmt.Errorf("registry: attestation token is malformed: %w", err)
	}
	if err := identity.CheckName(tok.Operator); err != nil {
		return AttestationToken{}, fmt.Errorf("registry: attestation token operator: %w", err)
	}
	if err := identity.CheckName(tok.Operated); err != nil {
		return AttestationToken{}, fmt.Errorf("registry: attestation token operated persona: %w", err)
	}
	if tok.Sig == "" {
		return AttestationToken{}, fmt.Errorf("registry: attestation token carries no signature")
	}
	return tok, nil
}

// ChainTerminal is where following operated_by links ends for OperatorChain.
type ChainTerminal string

const (
	// TerminalPrincipal means the walk reached a persona that answers for itself.
	TerminalPrincipal ChainTerminal = "principal"
	// TerminalDangling means a named operator has no directory entry.
	TerminalDangling ChainTerminal = "dangling"
	// TerminalCycle means the links loop (including self-reference) — an invalid chain.
	TerminalCycle ChainTerminal = "cycle"
)

// OperatorChain follows operated_by links from start until they terminate, returning
// the personas visited (start first) and how the walk ended. The visited set bounds
// the walk: a cycle is reported, never looped on. Individual profiles stay readable
// regardless of the terminal — this is presentation, not permission.
func OperatorChain(profiles map[string]Profile, start string) ([]string, ChainTerminal) {
	visited := map[string]bool{}
	chain := []string{start}
	current := start
	for {
		visited[current] = true
		p, ok := profiles[current]
		if !ok {
			return chain, TerminalDangling
		}
		if p.OperatedBy == "" {
			return chain, TerminalPrincipal
		}
		if visited[p.OperatedBy] {
			return append(chain, p.OperatedBy), TerminalCycle
		}
		current = p.OperatedBy
		chain = append(chain, current)
	}
}
