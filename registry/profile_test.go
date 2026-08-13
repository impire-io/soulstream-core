package registry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/identity"
)

func testKey(t *testing.T) *identity.SigningKey {
	t.Helper()
	k, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

func mustSign(t *testing.T, k identity.Signer, msg []byte) string {
	t.Helper()
	sig, err := k.Sign(msg)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

// delegated wraps a key behind the Signer seam — a stand-in for a custodian
// that signs on request and never releases the key. fail, when set, makes
// every Sign fail (the custodian is unreachable).
type delegated struct {
	key  *identity.SigningKey
	fail error
}

func (d delegated) PublicKey() string { return d.key.PublicKey() }

func (d delegated) Sign(b []byte) (string, error) {
	if d.fail != nil {
		return "", d.fail
	}
	return d.key.Sign(b)
}

func TestProfileValidate(t *testing.T) {
	key := testKey(t)
	good := Profile{
		Name:       "architect",
		OperatedBy: "daan",
		CreatedAt:  time.Now().UTC(),
		SigningKey: &SigningKeyInfo{Ed25519: key.PublicKey(), Since: time.Now().UTC()},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(p *Profile)
	}{
		{"bad name", func(p *Profile) { p.Name = "Not A Slug" }},
		{"bad operated_by", func(p *Profile) { p.OperatedBy = "Bad.Name" }},
		{"self-operated", func(p *Profile) { p.OperatedBy = p.Name }},
		{"bad key encoding", func(p *Profile) { p.SigningKey = &SigningKeyInfo{Ed25519: "%%%"} }},
		{"bad key length", func(p *Profile) { p.SigningKey = &SigningKeyInfo{Ed25519: "c2hvcnQ="} }},
		{"rotation missing proof", func(p *Profile) {
			p.Rotations = []Rotation{{From: p.SigningKey.Ed25519, To: p.SigningKey.Ed25519}}
		}},
		{"rotations without key", func(p *Profile) {
			p.Rotations = []Rotation{{From: p.SigningKey.Ed25519, To: p.SigningKey.Ed25519, Proof: "x"}}
			p.SigningKey = nil
		}},
	}
	for _, c := range cases {
		p := good
		c.mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// The JSON shape is a wire contract (contracts/wire-and-kv.md): fixed key names,
// optionals omitted when empty.
func TestProfileJSONShape(t *testing.T) {
	key := testKey(t)
	p := Profile{
		Name:      "architect",
		CreatedAt: time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC),
		SigningKey: &SigningKeyInfo{
			Ed25519: key.PublicKey(),
			Since:   time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC),
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"name":"architect"`, `"created_at"`, `"signing_key"`, `"ed25519"`, `"since"`} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON missing %s: %s", want, s)
		}
	}
	for _, absent := range []string{"kind", "display_name", "description", "operated_by", "rotations"} {
		if strings.Contains(s, absent) {
			t.Errorf("field %q present in JSON but must be absent: %s", absent, s)
		}
	}

	var back Profile
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.SigningKey == nil || back.SigningKey.Ed25519 != key.PublicKey() {
		t.Error("round-trip lost the signing key")
	}
}

// A stored document carrying any unknown field — the retired "kind" above all — is
// invalid: rejected loudly naming the field, never silently accepted or stripped.
func TestDecodeProfileStrict(t *testing.T) {
	legacy := []byte(`{"name":"architect","kind":"agent","created_at":"2026-07-21T09:00:00Z"}`)
	if _, err := decodeProfile(legacy); err == nil {
		t.Fatal("legacy document with kind accepted")
	} else if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("error does not name the offending field: %v", err)
	}

	unknown := []byte(`{"name":"architect","favourite_colour":"blue"}`)
	if _, err := decodeProfile(unknown); err == nil {
		t.Fatal("document with unknown field accepted")
	} else if !strings.Contains(err.Error(), "favourite_colour") {
		t.Fatalf("error does not name the offending field: %v", err)
	}

	good := []byte(`{"name":"architect","created_at":"2026-07-21T09:00:00Z"}`)
	if p, err := decodeProfile(good); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	} else if p.Name != "architect" {
		t.Fatalf("decoded name = %q", p.Name)
	}
}
