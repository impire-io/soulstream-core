package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/topic"
)

// TestCloseToolCompactsAndArchivedRefusesWrites: close tidies; an archived topic
// refuses agent writes with a verbatim, explicable error.
func TestCloseToolCompactsAndArchivedRefusesWrites(t *testing.T) {
	h, url := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Agent Topic"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))
	if _, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "work happened"}); err != nil {
		t.Fatal(err)
	}

	// close via the tool → compacted closed topic that still reads fully.
	if _, _, err := h.closeTopic(ctx, nil, closeTopicInput{Path: path}); err != nil {
		t.Fatal(err)
	}
	res, _, err = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, `"lifecycle": "closed"`) || !strings.Contains(out, "work happened") {
		t.Errorf("closed topic reads wrong:\n%s", out)
	}

	// An operator archives it (library act — not an agent tool).
	operator := clientOn(t, url, "daan")
	if _, err := topic.Open(operator, path).Archive(ctx); err != nil {
		t.Fatalf("operator archive: %v", err)
	}

	// The agent's writes now refuse, with the archival named.
	if _, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "one more"}); err == nil {
		t.Error("postTurn succeeded on an archived topic")
	} else if !strings.Contains(err.Error(), "archived") {
		t.Errorf("refusal does not name archival: %v", err)
	}
	if _, _, err := h.rollupTopic(ctx, nil, rollupTopicInput{Path: path}); err == nil {
		t.Error("rollupTopic succeeded on an archived topic")
	}

	// Reading still works.
	res, _, err = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), `"lifecycle": "archived"`) {
		t.Errorf("archived topic unreadable:\n%s", resultText(t, res))
	}
}
