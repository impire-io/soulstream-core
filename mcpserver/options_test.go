package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/keystore"
)

// TestWhoamiUnsigned: an unsigned session reports persona + realm and no key.
func TestWhoamiUnsigned(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")

	res, _, err := h.whoami(context.Background(), nil, whoamiInput{})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, `"persona": "bookkeeper-agent"`) || !strings.Contains(out, `"realm": "acme"`) {
		t.Errorf("whoami missing identity fields:\n%s", out)
	}
	if strings.Contains(out, "signer_public_key") {
		t.Errorf("unsigned session must not report a signer key:\n%s", out)
	}
}

// TestWhoamiSigned: a signed session reports the exact public key in use —
// through the remote node this is how a user sees who the edge admitted.
func TestWhoamiSigned(t *testing.T) {
	url, key := signedSetupURL(t)
	c := signedClientOn(t, url, "bookkeeper-agent", key)
	h := newHandlers(c)

	res, _, err := h.whoami(context.Background(), nil, whoamiInput{})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, key.PublicKey()) {
		t.Errorf("whoami missing the session's signer public key:\n%s", out)
	}
}

// TestWithKeyringOverridesPinsFile: an injected provider is the keyring —
// the default pins file is never needed, so verification works even where
// the file path is unusable (the multi-principal host shape).
func TestWithKeyringOverridesPinsFile(t *testing.T) {
	url, key := signedSetupURL(t)
	// Point the default pins path somewhere unusable: if the injected
	// provider were ignored, verification would degrade to unknown-key.
	t.Setenv(keystore.EnvPinsFile, t.TempDir()+"/missing-dir/acme.json")

	c := signedClientOn(t, url, "bookkeeper-agent", key)
	h := newHandlers(c)
	WithKeyring(func(context.Context) (*identity.Keyring, error) {
		return &identity.Keyring{Keys: map[string][]string{"bookkeeper-agent": {key.PublicKey()}}}, nil
	})(h)
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Injected"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	if _, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "keyed in memory"}); err != nil {
		t.Fatal(err)
	}

	res, _, err = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if out := resultText(t, res); !strings.Contains(out, `"sig": "verified"`) {
		t.Errorf("injected keyring not used for verification:\n%s", out)
	}
}

// TestWithKeyringErrorDegrades: a failing provider degrades to no keyring
// (unknown-key), exactly like a missing pins file — it never blocks a read.
func TestWithKeyringErrorDegrades(t *testing.T) {
	url, key := signedSetupURL(t)
	c := signedClientOn(t, url, "bookkeeper-agent", key)
	h := newHandlers(c)
	WithKeyring(func(context.Context) (*identity.Keyring, error) {
		return nil, errors.New("directory unreachable")
	})(h)
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Degraded"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	if _, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "still readable"}); err != nil {
		t.Fatal(err)
	}

	res, _, err = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, "still readable") {
		t.Errorf("read blocked by failing keyring provider:\n%s", out)
	}
	if strings.Contains(out, `"sig": "verified"`) || strings.Contains(out, `"sig": "failed"`) {
		t.Errorf("failing provider must degrade to unknown-key, got:\n%s", out)
	}
}
