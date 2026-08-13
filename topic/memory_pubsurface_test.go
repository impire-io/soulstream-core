// Package topic_test proves SC-005 mechanically: everything a memory witness
// needs is public. This file compiles against the topic package from OUTSIDE —
// exported identifiers only — and plays the external archivist's role end to end:
// keep evidence while it is live, serve answers and exhibits, survive compaction.
// The separate archivist repository builds against exactly this surface.
package topic_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
	"github.com/impire-io/soulstream-core/topic"
)

// archivist is a minimal external witness: a private store of exhibits captured
// while they were live, plus a scrap of prose memory. Any shape works — the
// convention never sees the store.
type archivist struct {
	coverageFrom time.Time
	notes        []topic.MemoryAnswerDraft
	exhibits     map[string]record.Exhibit // "topic/op-id" → kept exhibit
}

func (a *archivist) witness() topic.MemoryWitness {
	return topic.MemoryWitness{
		CoverageFrom: a.coverageFrom,
		Answer: func(topic.MemoryQueryRequest) []topic.MemoryAnswerDraft {
			return a.notes // a real archivist searches its index here
		},
		Fetch: func(topicPath, opID string) (record.Exhibit, bool) {
			ex, ok := a.exhibits[topicPath+"/"+opID]
			return ex, ok
		},
	}
}

func externalClient(t *testing.T, url, persona string, key *identity.SigningKey) *realm.Client {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	rcfg := realm.Config{Realm: "test-realm", Persona: persona}
	if key != nil { // never put a typed-nil key into the interface field
		rcfg.Signer = key
	}
	c, err := realm.NewClient(context.Background(), nc, rcfg)
	if err != nil {
		nc.Close()
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestOutsiderWitnessServesTheConvention: the full loop — query answered and
// graded, compaction destroys the op, the outsider's kept exhibit restores it as
// verifying evidence and the verdict maps to the fact-with-provenance grade.
func TestOutsiderWitnessServesTheConvention(t *testing.T) {
	ctx := context.Background()
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)

	ownerKey, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	owner := externalClient(t, url, "owner", ownerKey)
	if _, err := owner.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// A decision happens: a turn, a challenge, and the mark that settled it. The
	// resolve mark's id is exactly the kind of op-level forensics compaction
	// consumes: its effect bakes into the baseline, its id does not, and once the
	// conversation moves on it is interior — not even the frontier remembers it.
	h, err := topic.StartTopic(ctx, owner, topic.StartTopicInput{Name: "the decision", SubjectMatter: "cadence"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	turnID, err := h.PostTurn(ctx, "weekly cadence it is")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	commentID, err := h.AddComment(ctx, "should it be monthly instead?", turnID)
	if err != nil {
		t.Fatalf("comment: %v", err)
	}
	decisionID, err := h.Resolve(ctx, commentID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := h.PostTurn(ctx, "moving on"); err != nil {
		t.Fatalf("post follow-up: %v", err)
	}

	// The archivist keeps evidence WHILE IT IS LIVE — retention is not
	// retrofittable, and this is the moment that matters.
	keeper := &archivist{coverageFrom: time.Now().UTC()}
	kept, err := topic.CaptureExhibit(ctx, owner, h.Path(), decisionID)
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	keeper.exhibits = map[string]record.Exhibit{h.Path() + "/" + decisionID: kept}
	keeper.notes = []topic.MemoryAnswerDraft{{
		Answer:    "weekly cadence; the monthly challenge was resolved on the spot",
		Citations: []topic.MemoryCitation{{Topic: h.Path(), OpID: decisionID}},
	}}

	// The archivist serves — an ordinary persona on an ordinary connection.
	arch := externalClient(t, url, "archivist", nil)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = topic.RespondMemory(wctx, arch, keeper.witness()) }()

	// Rollup destroys the resolve op (its mark bakes, its id does not). The
	// stream has forgotten; only keepers remember.
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if _, err := topic.CaptureExhibit(ctx, owner, h.Path(), decisionID); !errors.Is(err, topic.ErrOpNotLive) {
		t.Fatalf("expected the op gone after rollup, got %v", err)
	}

	// Ask the realm. The citation no longer resolves live — graded unverifiable,
	// with caution, never as fact.
	kr := &identity.Keyring{Keys: map[string][]string{"owner": {ownerKey.PublicKey()}}}
	var res *topic.MemoryResult
	for range 20 { // witness liveness: retry until the subscription serves
		res, err = topic.MemoryQuery(ctx, owner, topic.MemoryQueryInput{
			Query: "what did we decide about cadence?", Topics: []string{"the-decision*"},
			Timeout: 300 * time.Millisecond,
		}, kr)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if len(res.Answers) > 0 {
			break
		}
	}
	if len(res.Answers) == 0 {
		t.Fatal("the archivist never answered")
	}
	ans := res.Answers[0]
	if ans.Witness != "archivist" || ans.CoverageFrom.IsZero() {
		t.Errorf("attribution/coverage lost: %+v", ans)
	}
	if len(ans.Citations) != 1 || ans.Citations[0].Grade != topic.GradeUnverifiable {
		t.Fatalf("citations = %+v, want one unverifiable (compacted) citation", ans.Citations)
	}

	// The explicit follow-up fetch: the kept exhibit comes back, verifies against
	// the owner's pinned key, and upgrades the claim to fact-with-provenance.
	fetched, err := topic.FetchExhibit(ctx, owner, h.Path(), decisionID, time.Second, kr)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if fetched == nil || fetched.Verdict != topic.SigVerified || fetched.Source != "archivist" {
		t.Fatalf("fetched = %+v, want verified from archivist", fetched)
	}
	if grade := topic.GradeForVerdict(fetched.Verdict); grade != topic.GradeProvenance {
		t.Errorf("grade = %s, want %s", grade, topic.GradeProvenance)
	}

	// And the recovered evidence is portable: serialise, re-parse, still verifies
	// with no realm in sight.
	doc, err := fetched.Exhibit.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := record.ParseExhibit(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, verr := topic.VerifyExhibit(back, kr); verr != nil || v != topic.SigVerified {
		t.Errorf("offline verdict = %v (err %v), want verified", v, verr)
	}
}
