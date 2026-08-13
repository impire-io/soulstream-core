package topic

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/realm"
)

// bigTopic builds a topic whose materialised state document exceeds the inline
// threshold: a large workbench artefact plus a couple of turns.
func bigTopic(t *testing.T, c *realm.Client) *Handle {
	t.Helper()
	ctx := context.Background()

	big := strings.Repeat("x", InlineBaselineThreshold-1024)
	state, err := json.Marshal(map[string]string{"artefact": big})
	if err != nil {
		t.Fatal(err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "big", State: state})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, strings.Repeat("padding so the doc crosses the threshold ", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "and one more"); err != nil {
		t.Fatal(err)
	}
	return h
}

// manifestOf returns the current baseline's manifest ref (fails the test if inline).
func manifestOf(t *testing.T, c *realm.Client, path string) ManifestRef {
	t.Helper()
	recs, err := drainOps(context.Background(), c, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("empty log")
	}
	var bp BaselinePayload
	if err := json.Unmarshal(recs[0].Record.Payload, &bp); err != nil {
		t.Fatal(err)
	}
	if bp.Manifest == nil {
		t.Fatal("baseline is inline, expected manifest")
	}
	return *bp.Manifest
}

// TestManifestRollupRoundTrip (SC-004): an oversized state compacts to exactly one
// manifest message, and a cold reader reconstructs the identical view.
func TestManifestRollupRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := bigTopic(t, c)

	before, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatalf("manifest rollup: %v", err)
	}

	if n := countOps(t, c, h.Path()); n != 1 {
		t.Errorf("ops after manifest rollup = %d, want 1", n)
	}
	m := manifestOf(t, c, h.Path())
	if len(m.Chunks) != 1 || m.Digest == "" || m.Size == 0 {
		t.Errorf("manifest ref = %+v", m)
	}

	after, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Malformed != "" {
		t.Fatalf("manifest read malformed: %s", after.Malformed)
	}
	viewsEqual(t, before, after)
	if string(after.BaselineState) != string(before.BaselineState) {
		t.Error("workbench state lost through the manifest")
	}

	// Life continues over a manifest boundary too.
	if _, err := h.PostTurn(ctx, "after the manifest"); err != nil {
		t.Fatal(err)
	}
	v, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Contributions) != len(before.Contributions)+1 {
		t.Errorf("contributions after post = %d", len(v.Contributions))
	}
}

// TestManifestCrashBeforeCommit: an object written but never committed (crash or
// lost race before the publish) leaves the original log replaying exactly, with only
// a harmless orphan behind.
func TestManifestCrashBeforeCommit(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := bigTopic(t, c)

	before, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opsBefore := countOps(t, c, h.Path())

	// Simulate the crash: write an orphan state object exactly where a rollup
	// would, and never publish.
	store, err := c.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutBytes(ctx, "baseline/"+h.Path()+"/orphaned-attempt", []byte(`{"state":{}}`)); err != nil {
		t.Fatal(err)
	}

	if n := countOps(t, c, h.Path()); n != opsBefore {
		t.Errorf("orphan changed the log: %d → %d", opsBefore, n)
	}
	after, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	viewsEqual(t, before, after)

	// A lost race after the object write behaves the same: publish rejected, log
	// untouched (proven at the guard level; the orphan stays sweepable garbage).
	recs, err := drainOps(ctx, c, h.Path())
	if err != nil {
		t.Fatal(err)
	}
	staleSeq := recs[0].StreamSeq // hopelessly stale
	mt := apply(h.Path(), recs)
	payload := BaselinePayload{State: mt.BaselineState, Frontier: mt.Frontier,
		Baked: &BakedState{Contributions: cleanBakedContributions(mt.Contributions), Lifecycle: mt.Lifecycle}}
	if _, err := publishManifestBaseline(ctx, h, payload, mt.Frontier, staleSeq); !errors.Is(err, ErrRollupLost) {
		t.Fatalf("stale manifest publish: %v, want ErrRollupLost", err)
	}
	if n := countOps(t, c, h.Path()); n != opsBefore {
		t.Error("lost manifest race changed the log")
	}
}

// TestManifestUnreadableIsMalformedNotCrash (FR-011): missing or corrupt chunks make
// the topic report malformed with a reason — reads never fail, never show partial state.
func TestManifestUnreadableIsMalformedNotCrash(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := bigTopic(t, c)
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	m := manifestOf(t, c, h.Path())

	store, err := c.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt: replace the object's bytes.
	if _, err := store.PutBytes(ctx, m.Chunks[0], []byte(`{"state":"tampered"}`)); err != nil {
		t.Fatal(err)
	}
	v, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatalf("read of a corrupt manifest errored instead of reporting malformed: %v", err)
	}
	if v.Malformed == "" || !strings.Contains(v.Malformed, "digest") {
		t.Errorf("corrupt manifest: Malformed = %q", v.Malformed)
	}
	if len(v.Contributions) != 0 {
		t.Error("corrupt manifest leaked partial state")
	}

	// Missing: delete the object entirely.
	if err := store.Delete(ctx, m.Chunks[0]); err != nil {
		t.Fatal(err)
	}
	v, err = Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatalf("read of a missing manifest errored: %v", err)
	}
	if v.Malformed == "" || !strings.Contains(v.Malformed, "chunk") {
		t.Errorf("missing chunk: Malformed = %q", v.Malformed)
	}
}

// TestManifestSupersededCleanup (US2 scenario 4): a successful re-rollup deletes the
// superseded baseline's object.
func TestManifestSupersededCleanup(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := bigTopic(t, c)
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	first := manifestOf(t, c, h.Path())

	// Grow and compact again.
	if _, err := h.PostTurn(ctx, strings.Repeat("more growth ", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	second := manifestOf(t, c, h.Path())
	if first.Chunks[0] == second.Chunks[0] {
		t.Fatal("second rollup reused the first object name")
	}

	store, err := c.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetBytes(ctx, first.Chunks[0]); err == nil {
		t.Error("superseded manifest object still present after successful re-rollup")
	}
	if _, err := store.GetBytes(ctx, second.Chunks[0]); err != nil {
		t.Errorf("current manifest object missing: %v", err)
	}
}
