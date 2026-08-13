package cli

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/internal/keystore"
)

func TestProfilePublishAndShow(t *testing.T) {
	connect := testConnector(t)
	keyFile := filepath.Join(t.TempDir(), "acme-daan.ed25519")
	pinsFile := filepath.Join(t.TempDir(), "acme.json")
	base := []string{"--realm", "acme", "--persona", "daan", "--key-file", keyFile, "--pins-file", pinsFile}

	run(connect, append(base, "provision")...)
	if code, _, errs := run(connect, append(base, "key", "init")...); code != 0 {
		t.Fatalf("key init: %s", errs)
	}
	key, err := keystore.LoadKey(keyFile)
	if err != nil || key == nil {
		t.Fatalf("load key: %v", err)
	}

	code, out, errs := run(connect, append(base, "profile", "publish", "--display-name", "Daan")...)
	if code != 0 {
		t.Fatalf("profile publish exit %d: %s", code, errs)
	}
	if !strings.Contains(out, key.PublicKey()) {
		t.Errorf("publish output missing the public key: %q", out)
	}

	code, out, errs = run(connect, append(base, "profile", "show", "daan")...)
	if code != 0 {
		t.Fatalf("profile show exit %d: %s", code, errs)
	}
	for _, want := range []string{"name:         daan", "display name: Daan", key.PublicKey(), "(current)"} {
		if !strings.Contains(out, want) {
			t.Errorf("profile show missing %q:\n%s", want, out)
		}
	}

	// Metadata update: same persona republished with new metadata keeps the key.
	code, _, errs = run(connect, append(base, "profile", "publish", "--display-name", "Daan G")...)
	if code != 0 {
		t.Fatalf("metadata update exit %d: %s", code, errs)
	}
	_, out, _ = run(connect, append(base, "profile", "show", "daan")...)
	if !strings.Contains(out, "Daan G") || !strings.Contains(out, key.PublicKey()) {
		t.Errorf("metadata update lost data:\n%s", out)
	}

	// Unknown persona: helpful failure.
	if code, _, _ = run(connect, append(base, "profile", "show", "stranger")...); code == 0 {
		t.Error("profile show for an absent persona should fail")
	}
}

// The full accountability flow (quickstart.md): the operator attests, the operated
// persona publishes with the token, and show reports attested; a claim without a
// token shows unverified; a corrupted countersignature shows FAILED with the profile
// still readable.
func TestProfileAttestFlow(t *testing.T) {
	connect := testConnector(t)
	dir := t.TempDir()
	daanKey := filepath.Join(dir, "acme-daan.ed25519")
	scribeKey := filepath.Join(dir, "acme-scribe.ed25519")
	pinsFile := filepath.Join(dir, "acme.json")
	daan := []string{"--realm", "acme", "--persona", "daan", "--key-file", daanKey, "--pins-file", pinsFile}
	scribe := []string{"--realm", "acme", "--persona", "scribe", "--key-file", scribeKey, "--pins-file", pinsFile}

	run(connect, append(daan, "provision")...)
	for _, base := range [][]string{daan, scribe} {
		if code, _, errs := run(connect, append(base, "key", "init")...); code != 0 {
			t.Fatalf("key init: %s", errs)
		}
		if code, _, errs := run(connect, append(base, "profile", "publish")...); code != 0 {
			t.Fatalf("profile publish: %s", errs)
		}
	}

	// Attesting without a key is refused loudly.
	if code, _, errs := run(connect, append([]string{"--realm", "acme", "--persona", "keyless", "--key-file", filepath.Join(dir, "none.ed25519"), "--pins-file", pinsFile}, "profile", "attest", "scribe")...); code == 0 {
		t.Error("attest without a signing key succeeded")
	} else if !strings.Contains(errs, "key") {
		t.Errorf("keyless attest error unhelpful: %s", errs)
	}

	// daan attests scribe; scribe republishes with the token.
	code, token, errs := run(connect, append(daan, "profile", "attest", "scribe")...)
	if code != 0 {
		t.Fatalf("attest exit %d: %s", code, errs)
	}
	token = strings.TrimSpace(token)
	code, _, errs = run(connect, append(scribe, "profile", "publish", "--operated-by", "daan", "--attestation", token)...)
	if code != 0 {
		t.Fatalf("publish with attestation exit %d: %s", code, errs)
	}

	code, out, _ := run(connect, append(daan, "profile", "show", "scribe")...)
	if code != 0 {
		t.Fatalf("show exit %d", code)
	}
	for _, want := range []string{"operated by:  daan  [attested]", "principal:    daan  (scribe → daan)"} {
		if !strings.Contains(out, want) {
			t.Errorf("show missing %q:\n%s", want, out)
		}
	}

	// A claim with no token is unverified.
	if code, _, errs := run(connect, append(scribe, "profile", "publish", "--operated-by", "daan")...); code != 0 {
		t.Fatalf("republish without token: %s", errs)
	}
	_, out, _ = run(connect, append(daan, "profile", "show", "scribe")...)
	if !strings.Contains(out, "operated by:  daan  [unverified]") {
		t.Errorf("show missing unverified claim:\n%s", out)
	}

	// A token from the wrong operator, or for another persona, is refused at publish.
	if code, _, _ := run(connect, append(scribe, "profile", "publish", "--operated-by", "keyless", "--attestation", token)...); code == 0 {
		t.Error("token accepted for a mismatched --operated-by")
	}
	if code, _, _ := run(connect, append(daan, "profile", "publish", "--operated-by", "scribe", "--attestation", token)...); code == 0 {
		t.Error("token accepted for the wrong operated persona")
	}

	// A corrupted countersignature: FAILED, loudly, profile still readable.
	badToken, err := corruptTokenSig(token)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run(connect, append(scribe, "profile", "publish", "--operated-by", "daan", "--attestation", badToken)...); code != 0 {
		t.Fatalf("publish with corrupt sig should store (verification is read-side): %s", errs)
	}
	code, out, errs = run(connect, append(daan, "profile", "show", "scribe")...)
	if code != 0 {
		t.Fatalf("show of failed claim must still work: %s", errs)
	}
	if !strings.Contains(out, "operated by:  daan  [FAILED]") || !strings.Contains(out, "name:         scribe") {
		t.Errorf("show missing FAILED claim or profile body:\n%s", out)
	}
	if !strings.Contains(errs, "FAILED") {
		t.Errorf("failed claim not warned on stderr: %s", errs)
	}
}

// corruptTokenSig flips the signature inside a portable attestation token.
func corruptTokenSig(token string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	var tok map[string]string
	if err := json.Unmarshal(raw, &tok); err != nil {
		return "", err
	}
	sig, err := base64.StdEncoding.DecodeString(tok["sig"])
	if err != nil {
		return "", err
	}
	sig[0] ^= 0xff
	tok["sig"] = base64.StdEncoding.EncodeToString(sig)
	out, err := json.Marshal(tok)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

func TestProfileProvisionListsPersonasArtefact(t *testing.T) {
	connect := testConnector(t)
	code, out, errs := run(connect, "--realm", "acme", "--persona", "daan", "provision")
	if code != 0 {
		t.Fatalf("provision exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "personas") {
		t.Errorf("provision output missing the personas artefact: %q", out)
	}
}
