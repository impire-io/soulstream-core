package registry

import (
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/identity"
)

// rotatedProfile builds a valid profile for persona that rotated through the given
// keys in order (keys[0] is the root, keys[len-1] the current key).
func rotatedProfile(t *testing.T, persona string, keys ...*identity.SigningKey) Profile {
	t.Helper()
	p := Profile{
		Name:      persona,
		CreatedAt: time.Now().UTC(),
		SigningKey: &SigningKeyInfo{
			Ed25519: keys[len(keys)-1].PublicKey(),
			Since:   time.Now().UTC(),
		},
	}
	for i := 1; i < len(keys); i++ {
		p.Rotations = append(p.Rotations, Rotation{
			From:  keys[i-1].PublicKey(),
			To:    keys[i].PublicKey(),
			Proof: mustSign(t, keys[i-1], identity.RotationProofBytes(persona, keys[i].PublicKey())),
		})
	}
	return p
}

func TestChainSingleKey(t *testing.T) {
	key := testKey(t)
	chain, err := Chain(rotatedProfile(t, "solo", key))
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if len(chain) != 1 || chain[0] != key.PublicKey() {
		t.Errorf("chain = %v", chain)
	}
}

func TestChainNoKey(t *testing.T) {
	chain, err := Chain(Profile{Name: "keyless"})
	if err != nil || chain != nil {
		t.Errorf("keyless profile: chain=%v err=%v, want nil/nil", chain, err)
	}
}

func TestChainValidRotations(t *testing.T) {
	a, b, c := testKey(t), testKey(t), testKey(t)
	chain, err := Chain(rotatedProfile(t, "architect", a, b, c))
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	want := []string{a.PublicKey(), b.PublicKey(), c.PublicKey()}
	if len(chain) != 3 || chain[0] != want[0] || chain[1] != want[1] || chain[2] != want[2] {
		t.Errorf("chain = %v, want %v", chain, want)
	}
}

func TestChainRejectsInvalid(t *testing.T) {
	a, b, c := testKey(t), testKey(t), testKey(t)

	brokenLink := rotatedProfile(t, "architect", a, b, c)
	brokenLink.Rotations[1].From = c.PublicKey() // no longer contiguous

	badProof := rotatedProfile(t, "architect", a, b)
	badProof.Rotations[0].Proof = mustSign(t, b, identity.RotationProofBytes("architect", b.PublicKey())) // signed by the wrong (new) key

	replayedPersona := rotatedProfile(t, "architect", a, b)
	replayedPersona.Name = "historian" // proof was bound to "architect"

	wrongEnd := rotatedProfile(t, "architect", a, b)
	wrongEnd.SigningKey.Ed25519 = c.PublicKey() // chain ends at b, key says c

	for name, p := range map[string]Profile{
		"broken link":           brokenLink,
		"bad proof":             badProof,
		"proof replayed":        replayedPersona,
		"chain ends at old key": wrongEnd,
	} {
		if _, err := Chain(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildKeyringTOFUAndExtension(t *testing.T) {
	a, b := testKey(t), testKey(t)
	single := rotatedProfile(t, "architect", a)
	rotated := rotatedProfile(t, "architect", a, b)

	// First sight: pin the chain.
	kr, pins := BuildKeyring([]Profile{single}, nil)
	if kr.Distrusted["architect"] {
		t.Fatal("first sight distrusted")
	}
	if chain, _ := kr.ChainFor("architect"); len(chain) != 1 || chain[0] != a.PublicKey() {
		t.Fatalf("chain after TOFU = %v", chain)
	}
	if len(pins["architect"]) != 1 {
		t.Fatalf("pin after TOFU = %v", pins["architect"])
	}

	// Valid rotation: published chain extends the pin → accept, re-pin.
	kr, pins = BuildKeyring([]Profile{rotated}, pins)
	if kr.Distrusted["architect"] {
		t.Fatal("valid rotation distrusted")
	}
	if chain, _ := kr.ChainFor("architect"); len(chain) != 2 {
		t.Fatalf("chain after rotation = %v", chain)
	}
	if len(pins["architect"]) != 2 {
		t.Fatalf("pin not extended: %v", pins["architect"])
	}
}

func TestBuildKeyringSubstitutionDistrusts(t *testing.T) {
	a, mallory := testKey(t), testKey(t)
	pins := map[string][]string{"architect": {a.PublicKey()}}

	// The directory now shows a different key with no rotation proof.
	substituted := rotatedProfile(t, "architect", mallory)
	kr, newPins := BuildKeyring([]Profile{substituted}, pins)
	if !kr.Distrusted["architect"] {
		t.Fatal("substituted key not distrusted")
	}
	if chain, _ := kr.ChainFor("architect"); chain != nil {
		t.Errorf("distrusted persona still has keys: %v", chain)
	}
	// The pin is evidence: kept, not overwritten.
	if len(newPins["architect"]) != 1 || newPins["architect"][0] != a.PublicKey() {
		t.Errorf("pin was rewritten to %v", newPins["architect"])
	}
	// Input map untouched.
	if len(pins["architect"]) != 1 || pins["architect"][0] != a.PublicKey() {
		t.Errorf("input pins mutated: %v", pins["architect"])
	}
}

func TestBuildKeyringShortenedAndVanishedChains(t *testing.T) {
	a, b := testKey(t), testKey(t)
	pins := map[string][]string{"architect": {a.PublicKey(), b.PublicKey()}}

	// Shortened: directory rolled back to just the root.
	kr, _ := BuildKeyring([]Profile{rotatedProfile(t, "architect", a)}, pins)
	if !kr.Distrusted["architect"] {
		t.Error("shortened chain not distrusted")
	}

	// Vanished key: profile now has no signing key at all while a pin exists.
	keyless := Profile{Name: "architect", CreatedAt: time.Now().UTC()}
	kr, _ = BuildKeyring([]Profile{keyless}, pins)
	if !kr.Distrusted["architect"] {
		t.Error("vanished key not distrusted")
	}
}

func TestBuildKeyringInvalidChainDistrusts(t *testing.T) {
	a, b := testKey(t), testKey(t)
	p := rotatedProfile(t, "architect", a, b)
	p.Rotations[0].Proof = "bm90IGEgcHJvb2Y=" // garbage proof

	kr, pins := BuildKeyring([]Profile{p}, nil)
	if !kr.Distrusted["architect"] {
		t.Error("invalid chain not distrusted")
	}
	if len(pins["architect"]) != 0 {
		t.Errorf("invalid chain was pinned: %v", pins["architect"])
	}
}

func TestBuildKeyringPinnedButAbsentStaysTrusted(t *testing.T) {
	a := testKey(t)
	pins := map[string][]string{"emeritus": {a.PublicKey()}}

	kr, newPins := BuildKeyring(nil, pins)
	if kr.Distrusted["emeritus"] {
		t.Error("absent-from-directory persona distrusted")
	}
	if chain, _ := kr.ChainFor("emeritus"); len(chain) != 1 {
		t.Errorf("pinned keys lost: %v", chain)
	}
	if len(newPins["emeritus"]) != 1 {
		t.Errorf("pin lost: %v", newPins["emeritus"])
	}
}
