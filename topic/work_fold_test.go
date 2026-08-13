package topic

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/record"
)

// workLog builds: baseline, one open item, then the given work ops.
func workLog(tail ...SeqRecord) []SeqRecord {
	recs := []SeqRecord{
		foldRec(1, "base-0", "daan", TypeBaseline, BaselinePayload{State: json.RawMessage(`{}`), Frontier: []string{}}),
		foldRec(2, "item-1", "daan", TypeWorkOpen, WorkOpenPayload{Title: "draft the intro", Body: "any takers?"}, "base-0"),
	}
	return append(recs, tail...)
}

func workRef(seq uint64, id, author, opType, itemID string, parents ...string) SeqRecord {
	return foldRec(seq, id, author, opType, WorkRefPayload{Anchor: &Anchor{Kind: "op", OpID: itemID}}, parents...)
}

func soleItem(t *testing.T, mt *MaterializedTopic) WorkItem {
	t.Helper()
	if len(mt.WorkItems) != 1 {
		t.Fatalf("work items = %d, want 1 (%v)", len(mt.WorkItems), mt.Warnings)
	}
	return mt.WorkItems[0]
}

// TestWorkFoldOpen: work.open creates an open, unowned, attributed item — and
// activates a proposed topic (work ops are ordinary content).
func TestWorkFoldOpen(t *testing.T) {
	mt := apply("t", workLog())
	item := soleItem(t, mt)
	if item.ID != "item-1" || item.Author != "daan" || item.Title != "draft the intro" {
		t.Errorf("item = %+v", item)
	}
	if item.Status != WorkOpen || item.Owner != "" || len(item.Timeline) != 0 {
		t.Errorf("fresh item not open/unowned: %+v", item)
	}
	if mt.Lifecycle != Active {
		t.Errorf("lifecycle = %s, want active (a work item is content)", mt.Lifecycle)
	}
}

// TestWorkFoldClaimRace: the first claim in stream order wins; the later claim is
// void on the timeline — same verdict from the same log, every time.
func TestWorkFoldClaimRace(t *testing.T) {
	mt := apply("t", workLog(
		workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
		workRef(4, "clm-2", "scribe", TypeWorkClaim, "item-1", "item-1"),
	))
	item := soleItem(t, mt)
	if item.Status != WorkClaimed || item.Owner != "architect" {
		t.Fatalf("item = %s/%q, want claimed/architect", item.Status, item.Owner)
	}
	if len(item.Timeline) != 2 {
		t.Fatalf("timeline = %d events, want 2", len(item.Timeline))
	}
	if item.Timeline[0].Void || item.Timeline[0].Author != "architect" {
		t.Errorf("winning claim wrong: %+v", item.Timeline[0])
	}
	if !item.Timeline[1].Void || item.Timeline[1].Author != "scribe" {
		t.Errorf("losing claim not void: %+v", item.Timeline[1])
	}
}

// TestWorkFoldStateMachine walks the transition table's rejections: claim on
// claimed/done, duplicate done, abandon on open/done — all void, never an error.
func TestWorkFoldStateMachine(t *testing.T) {
	t.Run("done without claim", func(t *testing.T) {
		mt := apply("t", workLog(workRef(3, "dn-1", "scribe", TypeWorkDone, "item-1", "item-1")))
		item := soleItem(t, mt)
		if item.Status != WorkDone || item.Owner != "" {
			t.Errorf("item = %s/%q, want done with no owner", item.Status, item.Owner)
		}
	})
	t.Run("claim by current owner is void", func(t *testing.T) {
		mt := apply("t", workLog(
			workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
			workRef(4, "clm-2", "architect", TypeWorkClaim, "item-1", "clm-1"),
		))
		item := soleItem(t, mt)
		if !item.Timeline[1].Void {
			t.Error("owner's re-claim was not void")
		}
	})
	t.Run("done is terminal", func(t *testing.T) {
		mt := apply("t", workLog(
			workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
			workRef(4, "dn-1", "architect", TypeWorkDone, "item-1", "clm-1"),
			workRef(5, "clm-2", "scribe", TypeWorkClaim, "item-1", "dn-1"),
			workRef(6, "dn-2", "scribe", TypeWorkDone, "item-1", "clm-2"),
			workRef(7, "ab-1", "scribe", TypeWorkAbandon, "item-1", "dn-2"),
		))
		item := soleItem(t, mt)
		if item.Status != WorkDone || item.Owner != "architect" {
			t.Fatalf("item = %s/%q, want done, owner kept", item.Status, item.Owner)
		}
		for _, ev := range item.Timeline[2:] {
			if !ev.Void {
				t.Errorf("post-done %s not void", ev.Kind)
			}
		}
	})
	t.Run("abandon on open is void", func(t *testing.T) {
		mt := apply("t", workLog(workRef(3, "ab-1", "daan", TypeWorkAbandon, "item-1", "item-1")))
		item := soleItem(t, mt)
		if item.Status != WorkOpen || !item.Timeline[0].Void {
			t.Errorf("abandon on open changed state or was not void: %+v", item)
		}
	})
}

// TestWorkFoldAbandonReopens (US3): abandon clears the owner and re-arms the race;
// the previous owner may reclaim like anyone else.
func TestWorkFoldAbandonReopens(t *testing.T) {
	mt := apply("t", workLog(
		workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
		workRef(4, "ab-1", "architect", TypeWorkAbandon, "item-1", "clm-1"),
	))
	item := soleItem(t, mt)
	if item.Status != WorkOpen || item.Owner != "" {
		t.Fatalf("after abandon: %s/%q, want open/unowned", item.Status, item.Owner)
	}
	if item.Timeline[1].Void {
		t.Error("owner's abandon reported void")
	}

	// A fresh claim wins fresh — including the previous owner's.
	mt = apply("t", workLog(
		workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
		workRef(4, "ab-1", "architect", TypeWorkAbandon, "item-1", "clm-1"),
		workRef(5, "clm-2", "architect", TypeWorkClaim, "item-1", "ab-1"),
	))
	item = soleItem(t, mt)
	if item.Status != WorkClaimed || item.Owner != "architect" {
		t.Errorf("reclaim after abandon: %s/%q", item.Status, item.Owner)
	}
}

// TestWorkFoldMalformedVsVoid: unreadable ops warn and vanish (malformed);
// readable ops that lose fold as void; unknown item references warn.
func TestWorkFoldMalformedVsVoid(t *testing.T) {
	t.Run("empty title is malformed", func(t *testing.T) {
		recs := []SeqRecord{
			foldRec(1, "base-0", "daan", TypeBaseline, BaselinePayload{Frontier: []string{}}),
			foldRec(2, "item-x", "daan", TypeWorkOpen, WorkOpenPayload{Title: "  "}, "base-0"),
		}
		mt := apply("t", recs)
		if len(mt.WorkItems) != 0 {
			t.Fatalf("malformed open produced an item")
		}
		if len(mt.Warnings) == 0 || !strings.Contains(mt.Warnings[0], "malformed work.open") {
			t.Errorf("warnings = %v", mt.Warnings)
		}
		if mt.Lifecycle != Proposed {
			t.Errorf("malformed open activated the topic")
		}
	})
	t.Run("missing anchor is malformed", func(t *testing.T) {
		mt := apply("t", workLog(foldRec(3, "clm-x", "daan", TypeWorkClaim, WorkRefPayload{}, "item-1")))
		item := soleItem(t, mt)
		if len(item.Timeline) != 0 {
			t.Error("malformed claim reached the timeline")
		}
		if !warningsContain(mt, "malformed work.claim") {
			t.Errorf("warnings = %v", mt.Warnings)
		}
	})
	t.Run("unreadable payload is malformed", func(t *testing.T) {
		bad := SeqRecord{Record: record.Record{
			ID: "clm-x", Author: "daan", Type: TypeWorkClaim, Parents: []string{"item-1"},
			Timestamp: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
			Payload:   []byte(`{"anchor":"not-an-object"}`),
		}, StreamSeq: 3}
		mt := apply("t", append(workLog(), bad))
		if len(soleItem(t, mt).Timeline) != 0 {
			t.Error("unreadable claim reached the timeline")
		}
	})
	t.Run("unknown item is a void warning", func(t *testing.T) {
		mt := apply("t", workLog(workRef(3, "clm-x", "daan", TypeWorkClaim, "ghost", "item-1")))
		if !warningsContain(mt, "unknown work item") {
			t.Errorf("warnings = %v", mt.Warnings)
		}
		if len(soleItem(t, mt).Timeline) != 0 {
			t.Error("claim on a ghost item reached a real item's timeline")
		}
	})
}

// TestWorkFoldLifecycleDecoupled: closing a topic completes and abandons nothing
// (FR-019) — and item ops keep folding after a close.
func TestWorkFoldLifecycleDecoupled(t *testing.T) {
	mt := apply("t", workLog(
		workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
		foldRec(4, "life-1", "daan", TypeLifeTransition, TransitionPayload{To: Closed}, "clm-1"),
		workRef(5, "dn-1", "architect", TypeWorkDone, "item-1", "life-1"),
	))
	item := soleItem(t, mt)
	if mt.Lifecycle != Closed {
		t.Fatalf("lifecycle = %s", mt.Lifecycle)
	}
	if item.Status != WorkDone || item.Owner != "architect" {
		t.Errorf("close disturbed the item: %s/%q", item.Status, item.Owner)
	}
	if item.Timeline[1].Void {
		t.Error("done after close was void — closed topics still take ops")
	}
}

// TestWorkFoldEvidenceAnchors: comments and attachments anchored to an item's id
// resolve (not dangling) — the item is part of the topic's DAG like anything else.
func TestWorkFoldEvidenceAnchors(t *testing.T) {
	mt := apply("t", workLog(
		foldRec(3, "cmnt-1", "scribe", TypeCommentAdd, CommentPayload{Body: "on it", Anchor: Anchor{Kind: "op", OpID: "item-1"}}, "item-1"),
		foldRec(4, "attn-1", "scribe", TypeAttachmentAdd, AttachmentPayload{Name: "draft.md", Object: "o", Digest: "d", Size: 1, Anchor: "item-1"}, "cmnt-1"),
	))
	for _, c := range mt.Contributions {
		if c.Dangling {
			t.Errorf("evidence comment dangling: %+v", c)
		}
	}
	for _, a := range mt.Attachments {
		if a.Dangling {
			t.Errorf("evidence attachment dangling: %+v", a)
		}
	}
}

// TestWorkFoldNoWorkOpsOmitsField (FR-018): a view without work ops serialises
// without a work_items key — byte-compatible with pre-010 views.
func TestWorkFoldNoWorkOpsOmitsField(t *testing.T) {
	recs := []SeqRecord{
		foldRec(1, "base-0", "daan", TypeBaseline, BaselinePayload{Frontier: []string{}}),
		foldRec(2, "turn-1", "daan", TypeTurnPost, TurnPayload{Body: "hi"}, "base-0"),
	}
	out, err := json.Marshal(apply("t", recs))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "work_items") {
		t.Errorf("work_items serialised on a work-free topic: %s", out)
	}
}

// TestWorkFoldBakedRoundTrip: items — statuses, owners, timelines with their void
// flags — survive compaction, and a pre-010 baseline (no work_items) seeds none.
func TestWorkFoldBakedRoundTrip(t *testing.T) {
	logRecs := workLog(
		workRef(3, "clm-1", "architect", TypeWorkClaim, "item-1", "item-1"),
		workRef(4, "clm-2", "scribe", TypeWorkClaim, "item-1", "clm-1"),
		workRef(5, "ab-1", "architect", TypeWorkAbandon, "item-1", "clm-2"),
		workRef(6, "clm-3", "scribe", TypeWorkClaim, "item-1", "ab-1"),
	)
	before := apply("t", logRecs)
	after := apply("t", []SeqRecord{rollupOf(logRecs, "base-1")})
	viewsEqual(t, apply("t", logRecs), after)

	item := soleItem(t, after)
	if item.Status != WorkClaimed || item.Owner != "scribe" {
		t.Errorf("baked item = %s/%q, want claimed/scribe", item.Status, item.Owner)
	}
	if len(item.Timeline) != 4 || !item.Timeline[1].Void || item.Timeline[2].Void {
		t.Errorf("baked timeline lost its shape: %+v", item.Timeline)
	}

	// Ops keep folding onto the baked item across the boundary.
	tail := workRef(101, "dn-1", "scribe", TypeWorkDone, "item-1", before.Frontier...)
	viewsEqual(t, apply("t", append(logRecs, tail)),
		apply("t", []SeqRecord{rollupOf(logRecs, "base-1"), tail}))

	// A baseline from before this feature simply has no items.
	old := []SeqRecord{foldRec(1, "base-0", "daan", TypeBaseline,
		BaselinePayload{Frontier: []string{"x"}, Baked: &BakedState{Lifecycle: Active}})}
	if got := apply("t", old); len(got.WorkItems) != 0 {
		t.Errorf("pre-010 baseline produced work items: %+v", got.WorkItems)
	}
}

func warningsContain(mt *MaterializedTopic, substr string) bool {
	for _, w := range mt.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
