package cli

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/topic"
)

// safeBuf is a concurrency-safe buffer for the streaming-command tests, which read
// output while the command goroutine writes it.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitContains(t *testing.T, buf *safeBuf, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q; got %q", want, buf.String())
}

func TestWatchStreamsLiveThenExits(t *testing.T) {
	connect := testConnector(t)
	ctx := context.Background()

	c, err := connect(ctx, Config{Realm: "acme", Persona: "daan"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "live"})
	if err != nil {
		t.Fatal(err)
	}

	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out safeBuf
	done := make(chan int, 1)
	go func() {
		done <- Run(wctx, []string{"--realm", "acme", "--persona", "daan", "watch", h.Path()}, &out, &out, connect)
	}()

	time.Sleep(300 * time.Millisecond) // let watch attach
	if _, err := h.PostTurn(ctx, "live turn"); err != nil {
		t.Fatal(err)
	}
	waitContains(t, &out, "live turn", 3*time.Second)

	cancel()
	if code := <-done; code != 0 {
		t.Errorf("watch exit %d after cancel, want 0", code)
	}
}

func TestInboxStreamsMentionThenExits(t *testing.T) {
	connect := testConnector(t)
	ctx := context.Background()

	c, err := connect(ctx, Config{Realm: "acme", Persona: "daan"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "vat"})
	if err != nil {
		t.Fatal(err)
	}

	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out safeBuf
	done := make(chan int, 1)
	go func() {
		done <- Run(ictx, []string{"--realm", "acme", "--persona", "bookkeeper-agent", "inbox"}, &out, &out, connect)
	}()

	time.Sleep(300 * time.Millisecond)
	if _, err := h.PostTurn(ctx, "check box 5 @bookkeeper-agent"); err != nil {
		t.Fatal(err)
	}
	waitContains(t, &out, "mention in", 3*time.Second)

	cancel()
	if code := <-done; code != 0 {
		t.Errorf("inbox exit %d after cancel, want 0", code)
	}
}
