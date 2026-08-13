package topic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
)

// buildFullTopic creates a topic with every element kind and returns its handle.
func buildFullTopic(t *testing.T, c *realm.Client) *Handle {
	t.Helper()
	ctx := context.Background()
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "full", SubjectMatter: "everything", State: json.RawMessage(`{"doc":"artefact"}`)})
	if err != nil {
		t.Fatal(err)
	}
	turn1, err := h.PostTurn(ctx, "hello @reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "second turn"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddComment(ctx, "a comment", turn1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Attach(ctx, "notes.txt", "text/plain", []byte("attached body"), turn1); err != nil {
		t.Fatal(err)
	}
	return h
}

// countOps returns the number of messages currently on the topic's ops subject.
func countOps(t *testing.T, c *realm.Client, path string) int {
	t.Helper()
	recs, err := drainOps(context.Background(), c, path)
	if err != nil {
		t.Fatal(err)
	}
	return len(recs)
}

// TestRollupReplayEquivalence is US1's independent test: compact a full-featured
// topic and the cold view is identical (SC-001), from exactly one message (SC-002).
func TestRollupReplayEquivalence(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := buildFullTopic(t, c)

	before, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opsBefore := countOps(t, c, h.Path())
	if opsBefore < 5 {
		t.Fatalf("test topic too small: %d ops", opsBefore)
	}

	baselineID, err := h.Rollup(ctx)
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if baselineID == "" {
		t.Fatal("empty baseline op-id")
	}

	if n := countOps(t, c, h.Path()); n != 1 {
		t.Errorf("ops after rollup = %d, want exactly 1 (SC-002)", n)
	}

	// A completely cold reader (fresh handle, no prior state) sees the identical view.
	after, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	viewsEqual(t, before, after)
	if after.Lifecycle != Active {
		t.Errorf("lifecycle after rollup = %s", after.Lifecycle)
	}
}

// TestRollupContinuity: posting after a rollup parents onto the payload frontier and
// anchors to baked op-ids resolve (US1 scenarios 2 and 6).
func TestRollupContinuity(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := buildFullTopic(t, c)

	before, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bakedTurn := before.Contributions[0].OpID

	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}

	postID, err := h.PostTurn(ctx, "after the boundary")
	if err != nil {
		t.Fatalf("post after rollup: %v", err)
	}
	if _, err := h.AddComment(ctx, "anchored to baked", bakedTurn); err != nil {
		t.Fatalf("comment after rollup: %v", err)
	}

	// The tail op parents onto the pre-rollup frontier, not the baseline op-id.
	recs, err := drainOps(ctx, c, h.Path())
	if err != nil {
		t.Fatal(err)
	}
	var postRec *SeqRecord
	for i := range recs {
		if recs[i].Record.ID == postID {
			postRec = &recs[i]
		}
	}
	if postRec == nil {
		t.Fatal("post not found in tail")
	}
	wantParents := map[string]bool{}
	for _, id := range before.Frontier {
		wantParents[id] = true
	}
	for _, p := range postRec.Record.Parents {
		if !wantParents[p] {
			t.Errorf("post parent %q is not a pre-rollup frontier member %v", p, before.Frontier)
		}
	}

	v, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, cb := range v.Contributions {
		if cb.Anchor == bakedTurn && cb.Dangling {
			t.Error("anchor to a baked op-id reported dangling")
		}
	}
}

// TestRollupRaces: first writer wins, the loser changes nothing (SC-003).
func TestRollupRaces(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h := buildFullTopic(t, c)

	// Race 1: a post lands between the rollup's read and its publish. Simulate by
	// draining, posting, then attempting the publish with the stale guard — which is
	// exactly what Rollup does internally when raced. Easiest faithful simulation:
	// open two handles; one materialises, the other posts, then the first compacts.
	loser := Open(c, h.Path())
	if _, err := loser.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "sneaking in"); err != nil {
		t.Fatal(err)
	}
	opsBefore := countOps(t, c, h.Path())

	// The loser's Rollup re-drains internally, so to force the race we post from a
	// second goroutineless path: drain count check ensures the sneak is included in
	// its read — meaning Rollup would now succeed. To genuinely race the guard we
	// need the publish to happen against a stale sequence: do it directly.
	recs, err := drainOps(ctx, c, h.Path())
	if err != nil {
		t.Fatal(err)
	}
	staleSeq := recs[len(recs)-2].StreamSeq // pretend we never saw the last op
	mt := apply(h.Path(), recs[:len(recs)-1])
	payload := BaselinePayload{State: mt.BaselineState, Frontier: mt.Frontier,
		Baked: &BakedState{Contributions: cleanBakedContributions(mt.Contributions), Attachments: cleanBakedAttachments(mt.Attachments), Lifecycle: mt.Lifecycle}}
	_, err = publishBaseline(ctx, loser, payload, mt.Frontier, staleSeq)
	if !errors.Is(err, ErrRollupLost) {
		t.Fatalf("stale rollup publish: err = %v, want ErrRollupLost", err)
	}
	if n := countOps(t, c, h.Path()); n != opsBefore {
		t.Errorf("lost rollup changed the log: %d → %d ops", opsBefore, n)
	}

	// Race 2: rollup vs rollup — the second attempt with the same guard loses.
	winner := Open(c, h.Path())
	if _, err := winner.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	recs, err = drainOps(ctx, c, h.Path())
	if err != nil {
		t.Fatal(err)
	}
	lastSeq := recs[len(recs)-1].StreamSeq
	mtFull := apply(h.Path(), recs)
	full := BaselinePayload{State: mtFull.BaselineState, Frontier: mtFull.Frontier,
		Baked: &BakedState{Contributions: cleanBakedContributions(mtFull.Contributions), Attachments: cleanBakedAttachments(mtFull.Attachments), Lifecycle: mtFull.Lifecycle}}

	if _, err := publishBaseline(ctx, winner, full, mtFull.Frontier, lastSeq); err != nil {
		t.Fatalf("winner rollup: %v", err)
	}
	if _, err := publishBaseline(ctx, loser, full, mtFull.Frontier, lastSeq); !errors.Is(err, ErrRollupLost) {
		t.Fatalf("second rollup with the same guard: err = %v, want ErrRollupLost", err)
	}
	if n := countOps(t, c, h.Path()); n != 1 {
		t.Errorf("ops after raced rollups = %d, want 1", n)
	}

	// Zero ops lost: the sneak survived into the baked state.
	v, err := Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cb := range v.Contributions {
		if cb.Body == "sneaking in" {
			found = true
		}
	}
	if !found {
		t.Error("the racing post was lost by the rollup")
	}
}

// TestRollupSignedProvenance (US1 scenario 5): a signing compactor's baseline
// verifies, and baked elements report the baseline's status.
func TestRollupSignedProvenance(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)

	// An unsigned persona builds history.
	plain := connectClient(t, url, "plain")
	if _, err := plain.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := StartTopic(ctx, plain, StartTopicInput{Name: "provenance", SubjectMatter: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "unsigned words"); err != nil {
		t.Fatal(err)
	}

	// A signed compactor rolls it up.
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	compactor := connectClientSigned(t, url, "compactor", key)
	ch := Open(compactor, h.Path())
	if _, err := ch.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Rollup(ctx); err != nil {
		t.Fatal(err)
	}

	kr := &identity.Keyring{Keys: map[string][]string{"compactor": {key.PublicKey()}}}
	rh := Open(connectClient(t, url, "reader"), h.Path())
	rh.UseKeyring(kr)
	v, err := rh.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Contributions) != 1 {
		t.Fatalf("contributions = %d", len(v.Contributions))
	}
	if v.Contributions[0].Sig != SigVerified {
		t.Errorf("baked element sig = %s, want verified (inherits the signed baseline)", v.Contributions[0].Sig)
	}

	// Without the compactor's key: the baked element degrades with the baseline.
	bare := Open(connectClient(t, url, "reader2"), h.Path())
	vBare, err := bare.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vBare.Contributions[0].Sig != SigUnknownKey {
		t.Errorf("keyring-less baked sig = %s, want unknown-key", vBare.Contributions[0].Sig)
	}
}

// TestRollupNothingToCompact: a fresh topic refuses politely.
func TestRollupNothingToCompact(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Rollup(ctx); !errors.Is(err, ErrNothingToCompact) {
		t.Errorf("fresh topic rollup: err = %v, want ErrNothingToCompact", err)
	}
}

// TestRollupLiveFollowerConsistent: a follower that watched the whole history stays
// consistent when a rollup lands mid-follow.
func TestRollupLiveFollowerConsistent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := provisionedClient(t, "daan")
	h := buildFullTopic(t, c)

	views := make(chan *MaterializedTopic, 32)
	fh := Open(c, h.Path())
	go func() { _ = fh.Follow(ctx, func(v *MaterializedTopic) { views <- v }) }()

	// Wait until the follower has the full history.
	waitFor := func(cond func(*MaterializedTopic) bool) *MaterializedTopic {
		deadline := time.After(5 * time.Second)
		for {
			select {
			case v := <-views:
				if cond(v) {
					return v
				}
			case <-deadline:
				t.Fatal("follower timed out")
				return nil
			}
		}
	}
	waitFor(func(v *MaterializedTopic) bool { return len(v.Contributions) == 3 })

	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "post-rollup live"); err != nil {
		t.Fatal(err)
	}

	v := waitFor(func(v *MaterializedTopic) bool { return len(v.Contributions) == 4 })
	for _, cb := range v.Contributions {
		if cb.Body == "post-rollup live" && cb.Dangling {
			t.Error("live post-rollup op dangling")
		}
	}
	// The mid-log checkpoint left a benign warning, not an unknown-type one.
	for _, w := range v.Warnings {
		if w == "ignored unknown op type: baseline" {
			t.Error("rollup checkpoint treated as unknown type by the follower")
		}
	}
}
