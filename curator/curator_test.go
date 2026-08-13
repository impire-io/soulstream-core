package curator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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

func provisioned(t *testing.T, persona string) (*realm.Client, string) {
	t.Helper()
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	c := clientOn(t, url, persona)
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, url
}

// startCurator runs a curator and waits for its projection. Returns stop + events.
func startCurator(t *testing.T, url string, opts Options) (func(), *eventLog) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := clientOn(t, url, "curator")
	ev := &eventLog{}
	ready := make(chan struct{})
	userOnEvent := opts.OnEvent
	opts.OnEvent = func(e string) {
		ev.add(e)
		if strings.HasPrefix(e, "projection ready") {
			select {
			case <-ready:
			default:
				close(ready)
			}
		}
		if userOnEvent != nil {
			userOnEvent(e)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, c, opts)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("curator projection never became ready")
	}
	return func() { cancel(); <-done }, ev
}

type eventLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	l.lines = append(l.lines, e)
	l.mu.Unlock()
}

// suggestionCount counts suggestions of one kind in a topic.
func suggestionCount(t *testing.T, c *realm.Client, path string, isKind func(string) bool) int {
	t.Helper()
	v, err := topic.Open(c, path).Materialise(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, cb := range v.Contributions {
		if isKind(cb.Body) {
			n++
		}
	}
	return n
}

// waitFor polls until cond or deadline.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCuratorAnswersFromContent (US1): body-only phrases find topics; the projection
// is live; stopping the curator restores 008 behaviour.
func TestCuratorAnswersFromContent(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "daan")

	h, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Design Review", SubjectMatter: "architecture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "the xylophone gate question is thorny"); err != nil {
		t.Fatal(err)
	}

	stop, _ := startCurator(t, url, Options{IdleWindow: DefaultIdleWindow, ScanEvery: 50 * time.Millisecond})
	defer stop()

	// Scenario 2: a phrase that appears only in a turn body. The curator's first
	// answer materialises the topic lazily, which can miss a single gather window
	// on a slow machine — retry like scenario 3 does.
	var results []topic.DiscoverResult
	waitFor(t, 5*time.Second, "content answer", func() bool {
		r, err := topic.Discover(ctx, c, topic.DiscoverInput{Query: "xylophone gate", Timeout: 700 * time.Millisecond}, nil)
		if err != nil {
			t.Fatal(err)
		}
		results = r
		return len(r) == 1 && r[0].Path == h.Path()
	})
	if results[0].Answers[0].Persona != "curator" {
		t.Errorf("answered by %q, want curator", results[0].Answers[0].Persona)
	}

	// Scenario 1: name matching works like any responder.
	results, err = topic.Discover(ctx, c, topic.DiscoverInput{Query: "design review", Timeout: 700 * time.Millisecond}, nil)
	if err != nil || len(results) != 1 {
		t.Fatalf("name query results = %+v, err %v", results, err)
	}

	// Scenario 3: live projection — content posted after the curator started.
	h2, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Fresh Topic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h2.PostTurn(ctx, "the zeppelin budget is approved"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "live projection pickup", func() bool {
		r, err := topic.Discover(ctx, c, topic.DiscoverInput{Query: "zeppelin budget", Timeout: 400 * time.Millisecond}, nil)
		return err == nil && len(r) == 1 && r[0].Path == h2.Path()
	})

	// Scenario 5: stop the curator — content queries fall silent, the board works.
	stop()
	results, err = topic.Discover(ctx, c, topic.DiscoverInput{Query: "xylophone gate", Timeout: 300 * time.Millisecond}, nil)
	if err != nil || results != nil {
		t.Errorf("after stop: results=%v err=%v, want silence", results, err)
	}
	entries, err := topic.Board(ctx, c)
	if err != nil || len(entries) != 2 {
		t.Errorf("board after stop: %d entries, %v", len(entries), err)
	}
}

// TestCuratorAndPlainResponderCoexist (US1 scenario 4): both answer, both credited.
func TestCuratorAndPlainResponderCoexist(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "daan")
	if _, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Q2 VAT filing"}); err != nil {
		t.Fatal(err)
	}

	stop, _ := startCurator(t, url, Options{ScanEvery: time.Hour}) // habits idle; answering only
	defer stop()

	respCtx, stopResp := context.WithCancel(context.Background())
	defer stopResp()
	plain := clientOn(t, url, "plain-responder")
	go func() { _ = topic.RespondDiscovery(respCtx, plain, nil) }()
	time.Sleep(100 * time.Millisecond)

	// Both must answer within one gather window; retry so a slow first
	// materialisation by the curator can't fail the round.
	waitFor(t, 5*time.Second, "curator and plain responder both answering", func() bool {
		results, err := topic.Discover(ctx, c, topic.DiscoverInput{Query: "vat", Timeout: 700 * time.Millisecond}, nil)
		if err != nil || len(results) != 1 {
			return false
		}
		personas := map[string]bool{}
		for _, a := range results[0].Answers {
			personas[a.Persona] = true
		}
		return personas["curator"] && personas["plain-responder"]
	})
}

// TestCuratorFlagsDuplicates (US2): one flag in the newer topic, idempotent across
// restart and a second curator; unrelated topics untouched.
func TestCuratorFlagsDuplicates(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "daan")

	older, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Q2 VAT filing", SubjectMatter: "the filing", Tags: []string{"finance"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond) // distinct birth times
	newer, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "VAT filing Q2", SubjectMatter: "filing", Tags: []string{"finance"}})
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Team offsite planning", SubjectMatter: "fun"})
	if err != nil {
		t.Fatal(err)
	}

	stop, _ := startCurator(t, url, Options{IdleWindow: DefaultIdleWindow, ScanEvery: 50 * time.Millisecond})

	waitFor(t, 5*time.Second, "duplicate flag", func() bool {
		return suggestionCount(t, c, newer.Path(), IsDuplicateSuggestion) == 1
	})

	// The flag names the older path and sits in the NEWER topic only.
	v, err := topic.Open(c, newer.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cb := range v.Contributions {
		if IsDuplicateSuggestion(cb.Body) {
			found = true
			if !strings.Contains(cb.Body, older.Path()) {
				t.Errorf("flag does not name the older topic: %q", cb.Body)
			}
			if cb.Author != "curator" {
				t.Errorf("flag author = %q", cb.Author)
			}
			if cb.Dangling {
				t.Error("flag anchor dangling")
			}
		}
	}
	if !found {
		t.Fatal("no flag in the newer topic")
	}
	if n := suggestionCount(t, c, older.Path(), IsDuplicateSuggestion); n != 0 {
		t.Errorf("older topic flagged too (%d flags)", n)
	}
	if n := suggestionCount(t, c, unrelated.Path(), IsDuplicateSuggestion); n != 0 {
		t.Errorf("unrelated topic flagged (%d flags)", n)
	}

	// Scenario 2+3: restart the curator AND run a second one — still exactly one flag.
	stop()
	stop2, _ := startCurator(t, url, Options{IdleWindow: DefaultIdleWindow, ScanEvery: 50 * time.Millisecond})
	defer stop2()
	time.Sleep(400 * time.Millisecond) // several scan ticks
	if n := suggestionCount(t, c, newer.Path(), IsDuplicateSuggestion); n != 1 {
		t.Errorf("flags after restart = %d, want exactly 1 (SC-002)", n)
	}
}

// TestCuratorProposesDormancy (US3): one proposal per quiet spell; activity re-arms
// exactly one more; curator chatter never counts; closed topics get none.
func TestCuratorProposesDormancy(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "daan")

	quiet, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Going Quiet"})
	if err != nil {
		t.Fatal(err)
	}
	closedTopic, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Already Done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := closedTopic.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Let both pass the (short) idle window before the curator starts.
	window := 300 * time.Millisecond
	time.Sleep(400 * time.Millisecond)

	stop, _ := startCurator(t, url, Options{IdleWindow: window, ScanEvery: 50 * time.Millisecond})

	waitFor(t, 5*time.Second, "dormancy proposal", func() bool {
		return suggestionCount(t, c, quiet.Path(), IsDormantSuggestion) == 1
	})

	// Scenario 2 + 4: repeated scans add nothing — the proposal itself is the only
	// recent op, and curator chatter never resets the clock.
	time.Sleep(400 * time.Millisecond)
	if n := suggestionCount(t, c, quiet.Path(), IsDormantSuggestion); n != 1 {
		t.Errorf("proposals after more scans = %d, want exactly 1", n)
	}

	// Scenario 5: the closed topic got nothing.
	if n := suggestionCount(t, c, closedTopic.Path(), IsDormantSuggestion); n != 0 {
		t.Errorf("closed topic proposed (%d)", n)
	}

	// Scenario 3: fresh real activity, quiet again ⇒ exactly one more.
	fresh := topic.Open(c, quiet.Path())
	if _, err := fresh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.PostTurn(ctx, "still alive after all"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "re-armed proposal", func() bool {
		return suggestionCount(t, c, quiet.Path(), IsDormantSuggestion) == 2
	})
	time.Sleep(300 * time.Millisecond)
	if n := suggestionCount(t, c, quiet.Path(), IsDormantSuggestion); n != 2 {
		t.Errorf("proposals after re-arm = %d, want exactly 2 (SC-003)", n)
	}

	// Restart: still nothing new (scenario 2 across restarts).
	stop()
	stop2, _ := startCurator(t, url, Options{IdleWindow: window, ScanEvery: 50 * time.Millisecond})
	defer stop2()
	time.Sleep(400 * time.Millisecond)
	if n := suggestionCount(t, c, quiet.Path(), IsDormantSuggestion); n != 2 {
		t.Errorf("proposals after restart = %d, want 2", n)
	}
}

// TestCuratorAnnouncementOnlyTopicEligible: a topic with no ops beyond its baseline
// is eligible for dormancy via its birth time.
func TestCuratorAnnouncementOnlyTopicEligible(t *testing.T) {
	c, url := provisioned(t, "daan")
	bare, err := topic.StartTopic(context.Background(), c, topic.StartTopicInput{Name: "Never Used"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)

	stop, _ := startCurator(t, url, Options{IdleWindow: 300 * time.Millisecond, ScanEvery: 50 * time.Millisecond})
	defer stop()

	waitFor(t, 5*time.Second, "bare-topic proposal", func() bool {
		return suggestionCount(t, c, bare.Path(), IsDormantSuggestion) == 1
	})
}
