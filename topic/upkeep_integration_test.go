package topic

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/identity"
)

// TestUpkeepWalkthrough (US1): edit → reply → resolve over a live server, with
// mention notification from a reply, signed edit stamps, and archived refusal.
func TestUpkeepWalkthrough(t *testing.T) {
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
	scribe := connectClient(t, url, "scribe")

	h, err := StartTopic(ctx, daan, StartTopicInput{Name: "gadget plan"})
	if err != nil {
		t.Fatal(err)
	}
	turnID, err := h.PostTurn(ctx, "lets ship thursdy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Edit(ctx, turnID, "let's ship Thursday"); err != nil {
		t.Fatal(err)
	}

	sh := Open(scribe, h.Path())
	if _, err := sh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	cmntID, err := sh.AddComment(ctx, "which Thursday?", turnID)
	if err != nil {
		t.Fatal(err)
	}

	// A reply pings its mention like any comment.
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	notes := make(chan Notification, 4)
	go func() { _ = FollowInbox(fctx, scribe, "scribe", nil, func(n Notification) { notes <- n }) }()
	if _, err := h.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Reply(ctx, "@scribe the 30th", cmntID); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-notes:
		if n.Topic != h.Path() {
			t.Errorf("notification = %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply mention not notified")
	}
	if _, err := h.Resolve(ctx, cmntID); err != nil {
		t.Fatal(err)
	}

	// A cold, keyring-equipped reader sees it all — with verified stamps.
	reader := Open(scribe, h.Path())
	reader.UseKeyring(&identity.Keyring{Keys: map[string][]string{"daan": {key.PublicKey()}}})
	mt, err := reader.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	turn := contribution(t, mt, turnID)
	if turn.Body != "let's ship Thursday" || len(turn.Edits) != 1 {
		t.Fatalf("edited turn wrong: %+v", turn)
	}
	if turn.Edits[0].Sig != SigVerified {
		t.Errorf("signed edit stamp status = %s", turn.Edits[0].Sig)
	}
	cmnt := contribution(t, mt, cmntID)
	if !cmnt.Resolved || cmnt.ResolvedBy != "daan" {
		t.Errorf("resolve mark wrong: %+v", cmnt)
	}

	// Empty edits are refused at the library door too.
	if _, err := h.Edit(ctx, turnID, "  "); err == nil {
		t.Error("empty edit accepted")
	}

	// Archived refuses the upkeep words like everything else.
	if _, err := h.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Edit(ctx, turnID, "too late"); !errors.Is(err, ErrTopicArchived) {
		t.Errorf("archived accepted edit: %v", err)
	}
	if _, err := h.Resolve(ctx, cmntID); !errors.Is(err, ErrTopicArchived) {
		t.Errorf("archived accepted resolve: %v", err)
	}
}

// TestRemoveAndArchivalGC (US2): detach keeps bytes fetchable; archival deletes
// exactly the withdrawn blobs.
func TestRemoveAndArchivalGC(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "cabinet"})
	if err != nil {
		t.Fatal(err)
	}
	keepID, err := h.Attach(ctx, "keep.md", "text/markdown", []byte("keep me"), "")
	if err != nil {
		t.Fatal(err)
	}
	dropID, err := h.Revise(ctx, "keep.md", "text/markdown", []byte("drop me"), keepID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.RemoveAttachment(ctx, dropID); err != nil {
		t.Fatal(err)
	}

	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keep, drop Attachment
	for _, a := range mt.Attachments {
		switch a.OpID {
		case keepID:
			keep = a
		case dropID:
			drop = a
		}
	}
	if !drop.Removed || drop.RemovedBy != "daan" || keep.Removed {
		t.Fatalf("marks wrong: keep=%+v drop=%+v", keep, drop)
	}
	// Withdrawn bytes stay fetchable until archival — replay never dangles.
	if data, err := GetAttachment(ctx, c, drop.Object); err != nil || string(data) != "drop me" {
		t.Fatalf("withdrawn blob unreadable before archival: %v", err)
	}
	// The artefact serves the fallback tip.
	arts := mt.Artefacts()
	if len(arts) != 1 || arts[0].Tip.OpID != keepID {
		t.Fatalf("tip did not fall back: %+v", arts)
	}

	// Marks survive compaction.
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	mt, err = Open(c, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range mt.Attachments {
		if a.OpID == dropID && (!a.Removed || a.RemovedBy != "daan") {
			t.Fatalf("baked removal mark lost: %+v", a)
		}
	}

	// Archival reclaims the withdrawn blob — and only it.
	if _, err := h.Archive(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := GetAttachment(ctx, c, drop.Object); err == nil {
		t.Error("withdrawn blob survived archival")
	}
	if data, err := GetAttachment(ctx, c, keep.Object); err != nil || string(data) != "keep me" {
		t.Errorf("surviving blob lost at archival: %v", err)
	}
}

// TestDormantLive (US3): a follower of a dormant topic sees Active the moment a
// content op lands; MarkDormant round-trips; concurrent marks converge.
func TestDormantLive(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	daan := connectClient(t, url, "daan")
	if _, err := daan.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	scribe := connectClient(t, url, "scribe")

	h, err := StartTopic(ctx, daan, StartTopicInput{Name: "nap"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "something, then silence"); err != nil {
		t.Fatal(err)
	}
	// Two personas apply the rule concurrently — harmless, same state.
	if _, err := h.MarkDormant(ctx); err != nil {
		t.Fatal(err)
	}
	sh := Open(scribe, h.Path())
	if _, err := sh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if sh.Lifecycle() != Dormant {
		t.Fatalf("lifecycle = %s, want dormant", sh.Lifecycle())
	}
	if _, err := sh.MarkDormant(ctx); err != nil {
		t.Fatal(err)
	}

	// A live follower watches it wake.
	states := make(chan Lifecycle, 16)
	fctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		fh := Open(scribe, h.Path())
		_ = fh.Follow(fctx, func(mt *MaterializedTopic) { states <- mt.Lifecycle })
	}()

	awaitState := func(want Lifecycle) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-states:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("follower never saw %s", want)
			}
		}
	}
	awaitState(Dormant)
	if _, err := h.PostTurn(ctx, "awake!"); err != nil {
		t.Fatal(err)
	}
	awaitState(Active)
	cancel()
	wg.Wait()

	// Cold agrees, and DormantEligible refuses a freshly active topic.
	mt, err := Open(daan, h.Path()).Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if mt.Lifecycle != Active {
		t.Errorf("cold lifecycle = %s", mt.Lifecycle)
	}
	if DormantEligible(mt, 14*24*time.Hour, time.Now().UTC()) {
		t.Error("active fresh topic eligible for dormancy")
	}
}
