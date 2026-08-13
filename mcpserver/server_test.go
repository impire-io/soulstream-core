package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

func clientOn(t *testing.T, url, persona string) *realm.Client {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	c, err := realm.NewClient(context.Background(), nc, realm.Config{Realm: "acme", Persona: persona})
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// setup starts a server, provisions the realm, and returns handlers for persona plus the
// server URL (so a test can add a second persona's client).
func setup(t *testing.T, persona string) (*handlers, string) {
	t.Helper()
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	c := clientOn(t, url, persona)
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	return newHandlers(c), url
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("result content is %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func TestNewServerRegistersTools(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")
	if s := NewServer(h.c); s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestBoardAndShow(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, err := h.board(ctx, nil, boardInput{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(resultText(t, res)) != "[]" {
		t.Errorf("empty board = %q, want []", resultText(t, res))
	}

	res, _, err = h.startTopic(ctx, nil, startTopicInput{Name: "VAT"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))

	res, _, _ = h.board(ctx, nil, boardInput{})
	if !strings.Contains(resultText(t, res), path) {
		t.Errorf("board missing the topic: %q", resultText(t, res))
	}

	res, _, _ = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if !strings.Contains(resultText(t, res), `"lifecycle"`) {
		t.Errorf("show missing view: %q", resultText(t, res))
	}

	if _, _, err = h.showTopic(ctx, nil, showTopicInput{}); err == nil {
		t.Error("show with an empty path should error")
	}
}

func TestContributeAttributedToPersona(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, _ := h.startTopic(ctx, nil, startTopicInput{Name: "VAT"})
	path := strings.TrimSpace(resultText(t, res))

	res, _, err := h.postTurn(ctx, nil, postTurnInput{Path: path, Body: "hi @daan"})
	if err != nil {
		t.Fatal(err)
	}
	turnID := strings.TrimSpace(resultText(t, res))

	if _, _, err := h.addComment(ctx, nil, addCommentInput{Path: path, AnchorOpID: turnID, Body: "noted"}); err != nil {
		t.Fatal(err)
	}

	// Every op is authored by the configured persona, and the mention was recorded.
	v, err := topic.Open(h.c, path).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mentioned := false
	for _, c := range v.Contributions {
		if c.Author != "bookkeeper-agent" {
			t.Errorf("op authored by %q, want bookkeeper-agent", c.Author)
		}
		for _, m := range c.Mentions {
			if m == "daan" {
				mentioned = true
			}
		}
	}
	if !mentioned {
		t.Error("mention was not recorded on the op")
	}
}

func TestCheckInbox(t *testing.T) {
	h, url := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, err := h.checkInbox(ctx, nil, checkInboxInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(t, res), `"notifications": []`) {
		t.Errorf("empty inbox = %q, want empty notifications list", resultText(t, res))
	}

	// Another persona mentions this agent.
	daan := clientOn(t, url, "daan")
	th, err := topic.StartTopic(ctx, daan, topic.StartTopicInput{Name: "VAT"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := th.PostTurn(ctx, "check box 5 @bookkeeper-agent"); err != nil {
		t.Fatal(err)
	}

	res, _, err = h.checkInbox(ctx, nil, checkInboxInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, res)
	if !strings.Contains(text, th.Path()) || !strings.Contains(text, "daan") {
		t.Errorf("inbox missing the mention: %q", text)
	}
}

func TestAttachTextAndClose(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, _ := h.startTopic(ctx, nil, startTopicInput{Name: "files"})
	path := strings.TrimSpace(resultText(t, res))

	res, _, err := h.attachText(ctx, nil, attachTextInput{Path: path, Name: "summary.txt", Body: "all good"})
	if err != nil {
		t.Fatal(err)
	}
	object := strings.TrimSpace(resultText(t, res))
	if !strings.HasPrefix(object, "attachments/") {
		t.Errorf("attach_text returned %q, want an object key", object)
	}

	res, _, _ = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if !strings.Contains(resultText(t, res), "summary.txt") {
		t.Errorf("attachment not listed: %q", resultText(t, res))
	}

	if _, _, err := h.closeTopic(ctx, nil, closeTopicInput{Path: path}); err != nil {
		t.Fatal(err)
	}
	res, _, _ = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if !strings.Contains(resultText(t, res), "closed") {
		t.Errorf("topic not closed: %q", resultText(t, res))
	}
}
