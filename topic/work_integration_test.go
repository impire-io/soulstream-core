package topic

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/identity"
)

// TestWorkWalkthrough (US2): open with a mention, race two claims from two
// clients, attach evidence, finish — every reader derives the same owner and the
// same void set, before and after a rollup.
func TestWorkWalkthrough(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	daan := connectClient(t, url, "daan")
	if _, err := daan.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	scribe := connectClient(t, url, "scribe")

	h, err := StartTopic(ctx, daan, StartTopicInput{Name: "gadget plan"})
	if err != nil {
		t.Fatal(err)
	}

	// Open with a mention — the item is a conversation from the first breath.
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	notes := make(chan Notification, 4)
	go func() { _ = FollowInbox(fctx, scribe, "scribe", nil, func(n Notification) { notes <- n }) }()

	itemID, err := h.OpenWork(ctx, "draft the intro", "@scribe want to take this?")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notes:
		if n.Topic != h.Path() || n.Author != "daan" {
			t.Errorf("notification = %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("work.open mention not notified")
	}

	// Two clients claim back-to-back: stream order decides, both replicas agree.
	sh := Open(scribe, h.Path())
	if _, err := sh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	check := func(c interface {
		Materialise(context.Context) (*MaterializedTopic, error)
	}) WorkItem {
		t.Helper()
		mt, err := c.Materialise(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(mt.WorkItems) != 1 {
			t.Fatalf("work items = %d", len(mt.WorkItems))
		}
		return mt.WorkItems[0]
	}
	for _, reader := range []*Handle{Open(daan, h.Path()), Open(scribe, h.Path())} {
		item := check(reader)
		if item.Status != WorkClaimed || item.Owner != "scribe" {
			t.Fatalf("item = %s/%q, want claimed/scribe", item.Status, item.Owner)
		}
		if len(item.Timeline) != 2 || item.Timeline[0].Void || !item.Timeline[1].Void {
			t.Fatalf("timeline = %+v", item.Timeline)
		}
	}

	// Evidence anchors to the item; completing is one op.
	if _, err := sh.Attach(ctx, "intro-draft.md", "text/markdown", []byte("# intro"), itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.CompleteWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	mt, err := Open(daan, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item := mt.WorkItems[0]
	if item.Status != WorkDone || item.Owner != "scribe" {
		t.Fatalf("finished item = %s/%q", item.Status, item.Owner)
	}
	if mt.Attachments[0].Anchor != itemID || mt.Attachments[0].Dangling {
		t.Error("evidence not anchored to the item")
	}

	// Compaction preserves all of it.
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	cold := check(Open(scribe, h.Path()))
	if cold.Status != WorkDone || cold.Owner != "scribe" || len(cold.Timeline) != 3 {
		t.Errorf("baked item = %s/%q with %d events", cold.Status, cold.Owner, len(cold.Timeline))
	}
	if !cold.Timeline[1].Void {
		t.Error("baked timeline lost the void claim")
	}
}

// TestWorkAbandonReclaim (US3): abandon reopens; the fresh race is won like a
// first claim — here by the previous owner's rival.
func TestWorkAbandonReclaim(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	daan := connectClient(t, url, "daan")
	if _, err := daan.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	scribe := connectClient(t, url, "scribe")

	h, err := StartTopic(ctx, daan, StartTopicInput{Name: "polish diagrams"})
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := h.OpenWork(ctx, "polish the diagrams", "")
	if err != nil {
		t.Fatal(err)
	}

	sh := Open(scribe, h.Path())
	if _, err := sh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.AbandonWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := mt.WorkItems[0]; got.Status != WorkOpen || got.Owner != "" {
		t.Fatalf("after abandon = %s/%q", got.Status, got.Owner)
	}

	if _, err := h.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	mt, err = sh.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item := mt.WorkItems[0]
	if item.Status != WorkClaimed || item.Owner != "daan" {
		t.Errorf("reclaimed item = %s/%q, want claimed/daan", item.Status, item.Owner)
	}
	if len(item.Timeline) != 3 {
		t.Errorf("timeline = %d events, want claim+abandon+claim", len(item.Timeline))
	}
}

// TestWorkLifecycleBoundaries: a closed topic still takes work ops (warned, like
// every write) and closing disturbs no item; an archived topic refuses them.
func TestWorkLifecycleBoundaries(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "boundaries"})
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := h.OpenWork(ctx, "a chore", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Close(ctx); err != nil {
		t.Fatal(err)
	}

	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := mt.WorkItems[0]; got.Status != WorkClaimed || got.Owner != "daan" {
		t.Fatalf("close disturbed the item: %s/%q", got.Status, got.Owner)
	}

	// Closed: permitted (convention warns, the log accepts).
	if _, err := h.CompleteWork(ctx, itemID); err != nil {
		t.Fatalf("closed topic refused a work op: %v", err)
	}

	if _, err := h.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.OpenWork(ctx, "too late", ""); !errors.Is(err, ErrTopicArchived) {
		t.Errorf("archived topic accepted work.open: %v", err)
	}
	if _, err := h.ClaimWork(ctx, itemID); !errors.Is(err, ErrTopicArchived) {
		t.Errorf("archived topic accepted work.claim: %v", err)
	}
}

// TestWorkSignedStatuses (FR-015): work items, timeline events, and revisions
// carry per-op signature status exactly like existing elements.
func TestWorkSignedStatuses(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	daan := connectClientSigned(t, url, "daan", key)
	if _, err := daan.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	scribe := connectClient(t, url, "scribe") // unsigned

	h, err := StartTopic(ctx, daan, StartTopicInput{Name: "signed work"})
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := h.OpenWork(ctx, "verify me", "")
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := h.Attach(ctx, "spec.md", "text/markdown", []byte("v1"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Revise(ctx, "spec.md", "text/markdown", []byte("v2"), rootID); err != nil {
		t.Fatal(err)
	}

	sh := Open(scribe, h.Path())
	if _, err := sh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	reader := Open(scribe, h.Path())
	reader.UseKeyring(&identity.Keyring{Keys: map[string][]string{"daan": {key.PublicKey()}}})
	mt, err := reader.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item := mt.WorkItems[0]
	if item.Sig != SigVerified {
		t.Errorf("signed work.open status = %s", item.Sig)
	}
	if len(item.Timeline) != 1 || item.Timeline[0].Sig != SigUnsigned {
		t.Errorf("unsigned claim status = %+v", item.Timeline)
	}
	for _, a := range mt.Attachments {
		if a.Sig != SigVerified {
			t.Errorf("signed revision %s status = %s", a.OpID, a.Sig)
		}
	}
}
