package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/topic"
)

func TestDiscoverTool(t *testing.T) {
	h, url := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	if _, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "Q2 VAT filing"}); err != nil {
		t.Fatal(err)
	}

	// A responder under another persona.
	respCtx, stopResponder := context.WithCancel(context.Background())
	defer stopResponder()
	responder := clientOn(t, url, "architect")
	go func() { _ = topic.RespondDiscovery(respCtx, responder, nil) }()
	time.Sleep(100 * time.Millisecond)

	res, _, err := h.discover(ctx, nil, discoverInput{Query: "vat"})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, "Q2 VAT filing") || !strings.Contains(out, `"persona": "architect"`) {
		t.Errorf("discover result:\n%s", out)
	}

	// Silence: stop the responder; the tool returns an empty list, not an error.
	stopResponder()
	time.Sleep(50 * time.Millisecond)
	res, _, err = h.discover(ctx, nil, discoverInput{Query: "quantum weaving"})
	if err != nil {
		t.Fatalf("silent discover: %v", err)
	}
	if strings.TrimSpace(resultText(t, res)) != "[]" {
		t.Errorf("silent discover = %q, want []", resultText(t, res))
	}
}
