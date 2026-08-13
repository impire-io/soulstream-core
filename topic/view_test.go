package topic

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/record"
)

func mkRec(id, typ string, parents []string, payload any) record.Record {
	var p []byte
	switch v := payload.(type) {
	case nil:
	case string:
		p = []byte(v)
	default:
		p, _ = json.Marshal(v)
	}
	return record.Record{
		ID: id, Author: "daan", Type: typ, Parents: parents,
		Timestamp: time.Unix(0, 0).UTC(), Payload: p,
	}
}

func baseline(id string) record.Record {
	return mkRec(id, TypeBaseline, nil, BaselinePayload{State: json.RawMessage(`{}`)})
}

func seq(recs ...record.Record) []SeqRecord {
	out := make([]SeqRecord, len(recs))
	for i, r := range recs {
		out[i] = SeqRecord{Record: r, StreamSeq: uint64(i + 1)}
	}
	return out
}

func TestApplyBaselineOnly(t *testing.T) {
	mt := apply("t", seq(baseline("base")))
	if mt.Malformed != "" {
		t.Fatalf("malformed: %q", mt.Malformed)
	}
	if mt.Lifecycle != Proposed {
		t.Errorf("lifecycle = %q, want proposed", mt.Lifecycle)
	}
	if len(mt.Contributions) != 0 {
		t.Errorf("contributions = %d, want 0", len(mt.Contributions))
	}
	if len(mt.Frontier) != 1 || mt.Frontier[0] != "base" {
		t.Errorf("frontier = %v, want [base]", mt.Frontier)
	}
}

func TestApplyTurnsAndComments(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("turn1", TypeTurnPost, []string{"base"}, TurnPayload{Body: "hello"}),
		mkRec("c1", TypeCommentAdd, []string{"turn1"}, CommentPayload{Body: "re", Anchor: Anchor{Kind: "op", OpID: "turn1"}}),
	))
	if mt.Lifecycle != Active {
		t.Errorf("lifecycle = %q, want active", mt.Lifecycle)
	}
	if len(mt.Contributions) != 2 {
		t.Fatalf("contributions = %d, want 2", len(mt.Contributions))
	}
	if mt.Contributions[0].Body != "hello" || mt.Contributions[0].Type != TypeTurnPost {
		t.Errorf("contribution 0 = %+v", mt.Contributions[0])
	}
	if mt.Contributions[1].Anchor != "turn1" || mt.Contributions[1].Dangling {
		t.Errorf("comment anchor/dangling wrong: %+v", mt.Contributions[1])
	}
}

func TestApplyOrderingByStreamSeq(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("turn1", TypeTurnPost, []string{"base"}, TurnPayload{Body: "first"}),
		mkRec("turn2", TypeTurnPost, []string{"turn1"}, TurnPayload{Body: "second"}),
	))
	if mt.Contributions[0].Body != "first" || mt.Contributions[1].Body != "second" {
		t.Errorf("out of order: %+v", mt.Contributions)
	}
	// Sequential parents → single leaf.
	if len(mt.Frontier) != 1 || mt.Frontier[0] != "turn2" {
		t.Errorf("frontier = %v, want [turn2]", mt.Frontier)
	}
}

func TestApplyConcurrentFrontier(t *testing.T) {
	// Two turns both parenting the baseline → two leaves.
	mt := apply("t", seq(
		baseline("base"),
		mkRec("turnA", TypeTurnPost, []string{"base"}, TurnPayload{Body: "a"}),
		mkRec("turnB", TypeTurnPost, []string{"base"}, TurnPayload{Body: "b"}),
	))
	if len(mt.Frontier) != 2 || mt.Frontier[0] != "turnA" || mt.Frontier[1] != "turnB" {
		t.Errorf("frontier = %v, want [turnA turnB]", mt.Frontier)
	}
}

func TestApplyDanglingComment(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("c1", TypeCommentAdd, []string{"base"}, CommentPayload{Body: "orphan", Anchor: Anchor{Kind: "op", OpID: "nope"}}),
	))
	if !mt.Contributions[0].Dangling {
		t.Error("comment with missing anchor not flagged dangling")
	}
}

func TestApplyUnknownTypeIgnored(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("x1", "future.op", []string{"base"}, `{"whatever":1}`),
	))
	if len(mt.Contributions) != 0 {
		t.Errorf("unknown op became a contribution: %+v", mt.Contributions)
	}
	if len(mt.Warnings) != 1 {
		t.Errorf("warnings = %v, want one", mt.Warnings)
	}
	if mt.Lifecycle != Proposed {
		t.Errorf("unknown op should not activate: %q", mt.Lifecycle)
	}
}

func TestApplyLifecycleClosed(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("turn1", TypeTurnPost, []string{"base"}, TurnPayload{Body: "hi"}),
		mkRec("l1", TypeLifeTransition, []string{"turn1"}, TransitionPayload{To: Closed}),
	))
	if mt.Lifecycle != Closed {
		t.Errorf("lifecycle = %q, want closed", mt.Lifecycle)
	}
}

func TestApplyAttachment(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("a1", TypeAttachmentAdd, []string{"base"}, AttachmentPayload{
			Name: "q2.csv", Object: "attachments/t/obj-1", Digest: "SHA-256=xyz", Size: 42, ContentType: "text/csv",
		}),
	))
	if mt.Lifecycle != Active {
		t.Errorf("attachment should activate the topic: %q", mt.Lifecycle)
	}
	if len(mt.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(mt.Attachments))
	}
	a := mt.Attachments[0]
	if a.Name != "q2.csv" || a.Object != "attachments/t/obj-1" || a.Size != 42 || a.ContentType != "text/csv" {
		t.Errorf("attachment metadata wrong: %+v", a)
	}
	if len(mt.Contributions) != 0 {
		t.Errorf("attachment should not appear as a contribution: %+v", mt.Contributions)
	}
}

func TestApplyAttachmentDanglingAnchor(t *testing.T) {
	mt := apply("t", seq(
		baseline("base"),
		mkRec("a1", TypeAttachmentAdd, []string{"base"}, AttachmentPayload{
			Name: "x", Object: "o", Anchor: "no-such-op",
		}),
	))
	if len(mt.Attachments) != 1 || !mt.Attachments[0].Dangling {
		t.Errorf("attachment with missing anchor not flagged dangling: %+v", mt.Attachments)
	}
}

func TestApplyMalformedFirstOp(t *testing.T) {
	mt := apply("t", seq(
		mkRec("turn1", TypeTurnPost, nil, TurnPayload{Body: "no baseline"}),
	))
	if mt.Malformed == "" {
		t.Error("expected malformed when first op is not a baseline")
	}
}

func TestApplyDeterministic(t *testing.T) {
	build := func() *MaterializedTopic {
		return apply("t", seq(
			baseline("base"),
			mkRec("turn1", TypeTurnPost, []string{"base"}, TurnPayload{Body: "hello"}),
			mkRec("c1", TypeCommentAdd, []string{"turn1"}, CommentPayload{Body: "re", Anchor: Anchor{Kind: "op", OpID: "turn1"}}),
		))
	}
	a, b := build(), build()
	if a.Lifecycle != b.Lifecycle || len(a.Contributions) != len(b.Contributions) {
		t.Fatal("apply not deterministic")
	}
	for i := range a.Contributions {
		if a.Contributions[i].OpID != b.Contributions[i].OpID || a.Contributions[i].Body != b.Contributions[i].Body {
			t.Errorf("contribution %d differs", i)
		}
	}
}
