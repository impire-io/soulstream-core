package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
)

// attestedProfile builds an operated persona's profile whose claim on operator is
// countersigned by operatorKey, bound to boundKey ("" = persona had no key).
func attestedProfile(t *testing.T, name, operator string, operatorKey *identity.SigningKey, boundKey string) Profile {
	t.Helper()
	return Profile{
		Name:       name,
		OperatedBy: operator,
		OperatorAttestation: &OperatorAttestation{
			OperatedKey: boundKey,
			Sig:         mustSign(t, operatorKey, identity.AttestationBytes(operator, name, boundKey)),
		},
	}
}

func TestAttestationStatus(t *testing.T) {
	opKey, opKey2, otherKey, myKey := testKey(t), testKey(t), testKey(t), testKey(t)
	opChain := []string{opKey.PublicKey()}
	rotatedOpChain := []string{opKey.PublicKey(), opKey2.PublicKey()}
	myChain := []string{myKey.PublicKey()}

	cases := []struct {
		name               string
		p                  Profile
		operatorChain      []string
		operatorDistrusted bool
		operatedChain      []string
		want               string
	}{
		{"no claim", Profile{Name: "solo"}, opChain, false, myChain, ""},
		{"claim without attestation", Profile{Name: "drafter", OperatedBy: "daan"}, opChain, false, nil, ClaimUnverified},
		{"attested", attestedProfile(t, "scribe", "daan", opKey, myKey.PublicKey()), opChain, false, myChain, ClaimAttested},
		{"attested via older chain key after operator rotation", attestedProfile(t, "scribe", "daan", opKey, myKey.PublicKey()), rotatedOpChain, false, myChain, ClaimAttested},
		{"unkeyed operated binds to name alone", attestedProfile(t, "scribe", "daan", opKey, ""), opChain, false, nil, ClaimAttested},
		{"empty binding stays good after the persona gains a key", attestedProfile(t, "scribe", "daan", opKey, ""), opChain, false, myChain, ClaimAttested},
		{"wrong signer", attestedProfile(t, "scribe", "daan", otherKey, myKey.PublicKey()), opChain, false, myChain, ClaimFailed},
		{"bound key outside the operated chain", attestedProfile(t, "scribe", "daan", opKey, otherKey.PublicKey()), opChain, false, myChain, ClaimFailed},
		{"operator has no published chain", attestedProfile(t, "scribe", "daan", opKey, myKey.PublicKey()), nil, false, myChain, ClaimUnverified},
		{"operator distrusted", attestedProfile(t, "scribe", "daan", opKey, myKey.PublicKey()), opChain, true, myChain, ClaimFailed},
	}
	for _, c := range cases {
		if got := AttestationStatus(c.p, c.operatorChain, c.operatorDistrusted, c.operatedChain); got != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAttestationTokenRoundTrip(t *testing.T) {
	opKey, myKey := testKey(t), testKey(t)

	s, err := NewAttestationToken(opKey, "daan", "scribe", myKey.PublicKey())
	if err != nil {
		t.Fatalf("NewAttestationToken: %v", err)
	}
	tok, err := ParseAttestationToken(s)
	if err != nil {
		t.Fatalf("ParseAttestationToken: %v", err)
	}
	if tok.Operator != "daan" || tok.Operated != "scribe" || tok.OperatedKey != myKey.PublicKey() {
		t.Fatalf("token round-trip = %+v", tok)
	}

	// The token's signature actually vouches.
	p := Profile{Name: "scribe", OperatedBy: "daan",
		OperatorAttestation: &OperatorAttestation{OperatedKey: tok.OperatedKey, Sig: tok.Sig}}
	if got := AttestationStatus(p, []string{opKey.PublicKey()}, false, []string{myKey.PublicKey()}); got != ClaimAttested {
		t.Fatalf("token-built attestation status = %q, want attested", got)
	}

	// Refusals.
	if _, err := NewAttestationToken(nil, "daan", "scribe", ""); err == nil {
		t.Error("token without a signer accepted")
	}
	if _, err := NewAttestationToken(opKey, "daan", "daan", ""); err == nil {
		t.Error("self-attestation accepted")
	}
	if _, err := ParseAttestationToken("not base64!"); err == nil {
		t.Error("garbage token accepted")
	}
	if _, err := ParseAttestationToken("aGVsbG8="); err == nil { // base64("hello")
		t.Error("non-JSON token accepted")
	}
}

// TestAttestationTokenDelegatedSigner (US3/T024, SC-003): a token whose
// signature came through the Signer seam vouches exactly like a locally
// signed one, and a failing delegate propagates its cause instead of
// producing a token.
func TestAttestationTokenDelegatedSigner(t *testing.T) {
	opKey, myKey := testKey(t), testKey(t)

	s, err := NewAttestationToken(delegated{key: opKey}, "daan", "scribe", myKey.PublicKey())
	if err != nil {
		t.Fatalf("NewAttestationToken via delegate: %v", err)
	}
	tok, err := ParseAttestationToken(s)
	if err != nil {
		t.Fatalf("ParseAttestationToken: %v", err)
	}
	p := Profile{Name: "scribe", OperatedBy: "daan",
		OperatorAttestation: &OperatorAttestation{OperatedKey: tok.OperatedKey, Sig: tok.Sig}}
	if got := AttestationStatus(p, []string{opKey.PublicKey()}, false, []string{myKey.PublicKey()}); got != ClaimAttested {
		t.Fatalf("delegated attestation status = %q, want attested", got)
	}

	_, err = NewAttestationToken(delegated{key: opKey, fail: errors.New("vault unreachable")}, "daan", "scribe", "")
	if err == nil || !strings.Contains(err.Error(), "vault unreachable") {
		t.Errorf("failing delegate: err = %v, want the custodian's cause", err)
	}
}

func TestOperatorChain(t *testing.T) {
	profiles := map[string]Profile{
		"daan":    {Name: "daan"},
		"scribe":  {Name: "scribe", OperatedBy: "daan"},
		"copyist": {Name: "copyist", OperatedBy: "scribe"},
		"lost":    {Name: "lost", OperatedBy: "nobody"},
		"ouro":    {Name: "ouro", OperatedBy: "boros"},
		"boros":   {Name: "boros", OperatedBy: "ouro"},
	}

	cases := []struct {
		start    string
		want     []string
		terminal ChainTerminal
	}{
		{"daan", []string{"daan"}, TerminalPrincipal},
		{"scribe", []string{"scribe", "daan"}, TerminalPrincipal},
		{"copyist", []string{"copyist", "scribe", "daan"}, TerminalPrincipal},
		{"lost", []string{"lost", "nobody"}, TerminalDangling},
		{"ouro", []string{"ouro", "boros", "ouro"}, TerminalCycle},
	}
	for _, c := range cases {
		chain, terminal := OperatorChain(profiles, c.start)
		if terminal != c.terminal || strings.Join(chain, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: chain=%v terminal=%s, want %v %s", c.start, chain, terminal, c.want, c.terminal)
		}
	}
}
