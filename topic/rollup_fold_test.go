package topic

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/record"
)

// mkRec builds a SeqRecord for fold tests (no server, no wire).
func foldRec(seq uint64, id, author, opType string, payload any, parents ...string) SeqRecord {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return SeqRecord{
		Record: record.Record{
			ID: id, Author: author, Acting: author, Type: opType, Parents: parents,
			Timestamp: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC), Payload: data,
		},
		StreamSeq: seq,
	}
}

// fullLog builds a log with every element kind: baseline, turns, an anchored comment,
// an attachment plus a whole-file revision of it, a work item with a won and a lost
// claim, and a close transition.
func fullLog() []SeqRecord {
	return []SeqRecord{
		foldRec(1, "base-0", "daan", TypeBaseline, BaselinePayload{State: json.RawMessage(`{"doc":1}`), Frontier: []string{}}),
		foldRec(2, "turn-1", "daan", TypeTurnPost, TurnPayload{Body: "hello @architect", Mentions: []string{"architect"}}, "base-0"),
		foldRec(3, "turn-2", "architect", TypeTurnPost, TurnPayload{Body: "hi back"}, "turn-1"),
		foldRec(4, "cmnt-1", "daan", TypeCommentAdd, CommentPayload{Body: "re: hello", Anchor: Anchor{Kind: "op", OpID: "turn-1"}}, "turn-2"),
		foldRec(5, "attn-1", "daan", TypeAttachmentAdd, AttachmentPayload{Name: "n.txt", Object: "o", Digest: "d", Size: 3}, "cmnt-1"),
		foldRec(6, "attn-2", "architect", TypeAttachmentAdd, AttachmentPayload{Name: "n.txt", Object: "o2", Digest: "d2", Size: 4, Anchor: "attn-1"}, "attn-1"),
		foldRec(7, "work-1", "daan", TypeWorkOpen, WorkOpenPayload{Title: "draft the intro", Body: "who takes it?"}, "attn-2"),
		foldRec(8, "wclm-1", "architect", TypeWorkClaim, WorkRefPayload{Anchor: &Anchor{Kind: "op", OpID: "work-1"}}, "work-1"),
		foldRec(9, "wclm-2", "daan", TypeWorkClaim, WorkRefPayload{Anchor: &Anchor{Kind: "op", OpID: "work-1"}}, "wclm-1"),
		foldRec(10, "edit-1", "daan", TypeEdit, CommentPayload{Body: "hello there @architect", Anchor: Anchor{Kind: "op", OpID: "turn-1"}, Mentions: []string{"architect"}}, "wclm-2"),
		foldRec(11, "rply-1", "architect", TypeCommentReply, CommentPayload{Body: "re: re: hello", Anchor: Anchor{Kind: "op", OpID: "cmnt-1"}}, "edit-1"),
		foldRec(12, "rslv-1", "daan", TypeCommentResolve, RefPayload{Anchor: &Anchor{Kind: "op", OpID: "cmnt-1"}}, "rply-1"),
		foldRec(13, "life-1", "daan", TypeLifeTransition, TransitionPayload{To: Closed}, "rslv-1"),
	}
}

// rollupOf builds the rollup baseline record equivalent to folding recs — the same
// construction Handle.Rollup performs, expressed serverlessly.
func rollupOf(recs []SeqRecord, newID string) SeqRecord {
	mt := apply("t", recs)
	payload := BaselinePayload{
		State:    mt.BaselineState,
		Frontier: mt.Frontier,
		Baked: &BakedState{
			Contributions: mt.Contributions,
			Attachments:   mt.Attachments,
			WorkItems:     mt.WorkItems,
			Lifecycle:     mt.Lifecycle,
		},
	}
	return foldRec(100, newID, "compactor", TypeBaseline, payload, mt.Frontier...)
}

// stripVolatile zeroes the fields round-tripping legitimately does not preserve:
// stream sequences and per-op sig statuses die with the compacted tail, and the
// baseline timestamp moves to the compaction time (rollup is real activity).
func stripVolatile(mt *MaterializedTopic) *MaterializedTopic {
	for i := range mt.Contributions {
		mt.Contributions[i].StreamSeq = 0
		mt.Contributions[i].Sig = ""
	}
	for i := range mt.Attachments {
		mt.Attachments[i].StreamSeq = 0
		mt.Attachments[i].Sig = ""
	}
	for i := range mt.WorkItems {
		mt.WorkItems[i].StreamSeq = 0
		mt.WorkItems[i].Sig = ""
		for j := range mt.WorkItems[i].Timeline {
			mt.WorkItems[i].Timeline[j].StreamSeq = 0
			mt.WorkItems[i].Timeline[j].Sig = ""
		}
	}
	mt.Warnings = nil
	mt.BaselineTs = time.Time{}
	mt.BaselineID = "" // the checkpoint op changes identity at every rollup
	return mt
}

func viewsEqual(t *testing.T, a, b *MaterializedTopic) {
	t.Helper()
	aj, _ := json.Marshal(stripVolatile(a))
	bj, _ := json.Marshal(stripVolatile(b))
	if string(aj) != string(bj) {
		t.Errorf("views differ:\n before: %s\n after:  %s", aj, bj)
	}
}

// TestFoldRoundTrip is SC-001's core: apply(rollup(L)) ≡ apply(L), and with any tail
// appended, apply(rollup(L)+tail) ≡ apply(L+tail).
func TestFoldRoundTrip(t *testing.T) {
	logRecs := fullLog()
	before := apply("t", logRecs)
	rolled := rollupOf(logRecs, "base-1")

	after := apply("t", []SeqRecord{rolled})
	viewsEqual(t, apply("t", fullLog()), after)

	// Continue across the boundary: a post-rollup tail folds identically onto both.
	tail := []SeqRecord{
		foldRec(101, "turn-3", "daan", TypeTurnPost, TurnPayload{Body: "after the rollup"}, before.Frontier...),
		foldRec(102, "cmnt-2", "architect", TypeCommentAdd, CommentPayload{Body: "anchors to baked", Anchor: Anchor{Kind: "op", OpID: "turn-1"}}, "turn-3"),
	}
	fullPlusTail := append(fullLog(), tail...)
	rolledPlusTail := append([]SeqRecord{rollupOf(fullLog(), "base-1")}, tail...)
	viewsEqual(t, apply("t", fullPlusTail), apply("t", rolledPlusTail))
}

// TestFoldRoundTripFrontier: the frontier after rollup equals the frontier before,
// and tail ops referencing it retire it exactly as in the full log.
func TestFoldRoundTripFrontier(t *testing.T) {
	before := apply("t", fullLog())
	after := apply("t", []SeqRecord{rollupOf(fullLog(), "base-1")})
	if fmt.Sprint(before.Frontier) != fmt.Sprint(after.Frontier) {
		t.Errorf("frontier: before %v, after %v", before.Frontier, after.Frontier)
	}
	if len(after.Frontier) == 0 {
		t.Fatal("empty frontier after rollup")
	}
	// The baseline op-id must NOT be a frontier leaf post-rollup (it is a checkpoint).
	for _, id := range after.Frontier {
		if id == "base-1" {
			t.Error("rollup baseline op-id surfaced as frontier")
		}
	}
}

// TestFoldBakedAnchorsResolve: comments anchored to baked op-ids are not dangling
// (US1 scenario 6).
func TestFoldBakedAnchorsResolve(t *testing.T) {
	rolled := rollupOf(fullLog(), "base-1")
	before := apply("t", fullLog())
	recs := []SeqRecord{
		rolled,
		foldRec(101, "cmnt-2", "daan", TypeCommentAdd, CommentPayload{Body: "late anchor", Anchor: Anchor{Kind: "op", OpID: "turn-2"}}, before.Frontier...),
	}
	mt := apply("t", recs)
	for _, c := range mt.Contributions {
		if c.OpID == "cmnt-2" && c.Dangling {
			t.Error("comment anchored to a baked op-id reported dangling")
		}
	}
}

// TestFoldMidLogBaselineIsCheckpoint: a live follower retaining pre-rollup history
// sees the landed rollup as a mid-log baseline — skipped as a checkpoint, view
// unchanged, benign warning.
func TestFoldMidLogBaselineIsCheckpoint(t *testing.T) {
	recs := append(fullLog(), rollupOf(fullLog(), "base-1"))
	mt := apply("t", recs)
	viewsEqual(t, apply("t", fullLog()), mt)

	warned := false
	for _, w := range apply("t", recs).Warnings {
		if w == "observed a rollup checkpoint mid-log (view already contains its content)" {
			warned = true
		}
		if w == "ignored unknown op type: baseline" {
			t.Error("mid-log baseline treated as unknown type")
		}
	}
	if !warned {
		t.Error("mid-log baseline produced no checkpoint warning")
	}
}

// TestFoldArchivedIsTerminal: transitions after archived are ignored; content after
// archived does not resurrect the topic.
func TestFoldArchivedIsTerminal(t *testing.T) {
	recs := []SeqRecord{
		foldRec(1, "base-0", "daan", TypeBaseline, BaselinePayload{State: json.RawMessage(`{}`), Frontier: []string{}}),
		foldRec(2, "life-1", "daan", TypeLifeTransition, TransitionPayload{To: Archived}, "base-0"),
		foldRec(3, "life-2", "daan", TypeLifeTransition, TransitionPayload{To: Closed}, "life-1"),
		foldRec(4, "turn-1", "daan", TypeTurnPost, TurnPayload{Body: "raced in"}, "life-2"),
	}
	mt := apply("t", recs)
	if mt.Lifecycle != Archived {
		t.Errorf("lifecycle = %s, want archived (terminal)", mt.Lifecycle)
	}
	// The raced-in turn is still visible (never dropped), just on an archived topic.
	if len(mt.Contributions) != 1 {
		t.Errorf("raced-in content dropped: %d contributions", len(mt.Contributions))
	}

	// Baked archived is equally terminal.
	rolled := rollupOf(recs[:2], "base-1")
	recs2 := []SeqRecord{rolled, foldRec(101, "life-3", "daan", TypeLifeTransition, TransitionPayload{To: Closed}, "base-1")}
	if got := apply("t", recs2).Lifecycle; got != Archived {
		t.Errorf("baked archived overridden to %s", got)
	}
}

// TestFoldPre007BaselineUnchanged: a birth baseline (no baked, empty frontier) folds
// exactly as before 007 — the baseline op-id is the frontier.
func TestFoldPre007BaselineUnchanged(t *testing.T) {
	recs := []SeqRecord{
		foldRec(1, "base-0", "daan", TypeBaseline, BaselinePayload{State: json.RawMessage(`{"doc":1}`), Frontier: []string{}}),
	}
	mt := apply("t", recs)
	if len(mt.Frontier) != 1 || mt.Frontier[0] != "base-0" {
		t.Errorf("birth frontier = %v, want [base-0]", mt.Frontier)
	}
	if mt.Lifecycle != Proposed {
		t.Errorf("birth lifecycle = %s", mt.Lifecycle)
	}
	if string(mt.BaselineState) != `{"doc":1}` {
		t.Errorf("baseline state = %s", mt.BaselineState)
	}
	if !mt.BaselineTs.Equal(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("birth BaselineTs = %v", mt.BaselineTs)
	}
}

// TestFoldBaselineTsMovesWithRollup: post-rollup, BaselineTs is the compaction
// baseline's time — rollup is real activity by a persona.
func TestFoldBaselineTsMovesWithRollup(t *testing.T) {
	before := apply("t", fullLog())
	after := apply("t", []SeqRecord{rollupOf(fullLog(), "base-1")})
	if !before.BaselineTs.Equal(after.BaselineTs) {
		// Same in this synthetic log (foldRec pins one timestamp) — assert the
		// field is at least populated post-rollup.
		t.Logf("baseline ts moved: %v → %v", before.BaselineTs, after.BaselineTs)
	}
	if after.BaselineTs.IsZero() {
		t.Error("post-rollup BaselineTs is zero")
	}
}
