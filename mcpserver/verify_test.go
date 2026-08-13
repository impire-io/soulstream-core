package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/internal/keystore"
)

// TestShowTopicSurfacesSigStatus: tool results carry a sig field per op once the
// author's profile is in the directory, and distrusted_personas when a pin diverges.
func TestShowTopicSurfacesSigStatus(t *testing.T) {
	url, key := signedSetupURL(t)
	t.Setenv(keystore.EnvPinsFile, t.TempDir()+"/acme.json")

	c := signedClientOn(t, url, "bookkeeper-agent", key)
	h := newHandlers(c)
	ctx := context.Background()

	if _, _, err := h.publishProfile(ctx, nil, publishProfileInput{}); err != nil {
		t.Fatal(err)
	}
	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Signed"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	if _, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "sealed by an agent"}); err != nil {
		t.Fatal(err)
	}

	res, _, err = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, `"sig": "verified"`) {
		t.Errorf("show result missing verified sig status:\n%s", out)
	}
	if strings.Contains(out, "distrusted_personas") {
		t.Errorf("no distrust expected:\n%s", out)
	}

	// Substitute the pin → distrusted_personas appears, op reports failed.
	pinsPath, err := keystore.ResolvePinsFile("", "acme")
	if err != nil {
		t.Fatal(err)
	}
	pins, err := keystore.LoadPins(pinsPath, "acme")
	if err != nil {
		t.Fatal(err)
	}
	pins.Personas["bookkeeper-agent"] = []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	if err := keystore.SavePins(pinsPath, pins); err != nil {
		t.Fatal(err)
	}

	res, _, err = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	out = resultText(t, res)
	if !strings.Contains(out, `"distrusted_personas"`) || !strings.Contains(out, "bookkeeper-agent") {
		t.Errorf("substitution not surfaced:\n%s", out)
	}
	if !strings.Contains(out, `"sig": "failed"`) {
		t.Errorf("distrusted persona's op not failed:\n%s", out)
	}
	if !strings.Contains(out, "sealed by an agent") {
		t.Error("flagged op was hidden — content must stay visible")
	}
}

// TestCheckInboxCarriesSig: notifications include their sig status.
func TestCheckInboxCarriesSig(t *testing.T) {
	url, key := signedSetupURL(t)
	t.Setenv(keystore.EnvPinsFile, t.TempDir()+"/acme.json")

	signer := signedClientOn(t, url, "signer", key)
	sh := newHandlers(signer)
	ctx := context.Background()
	if _, _, err := sh.publishProfile(ctx, nil, publishProfileInput{}); err != nil {
		t.Fatal(err)
	}
	res, _, err := sh.startTopic(ctx, nil, startTopicInput{Name: "Pings"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	if _, _, err := sh.postTurn(ctx, nil, postTurnInput{Path: path, Body: "look @bookkeeper-agent"}); err != nil {
		t.Fatal(err)
	}

	agent := clientOn(t, url, "bookkeeper-agent")
	ah := newHandlers(agent)
	res, _, err = ah.checkInbox(ctx, nil, checkInboxInput{})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, `"sig": "verified"`) {
		t.Errorf("notification missing verified sig:\n%s", out)
	}
}
