package topic

import (
	"context"
	"testing"
	"time"
)

// TestGrantWalkthrough (C4): issue, exercise with dual attribution,
// revoke — every reader derives the same states, the granter-only rule
// voids everyone else, and the standing grant survives a rollup (a
// compaction that dropped consent would erase authority its issuer
// never withdrew).
func TestGrantWalkthrough(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	daan := connectClient(t, url, "daan")
	if _, err := daan.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	scribe := connectClient(t, url, "scribe")

	h, err := StartTopic(ctx, daan, StartTopicInput{Name: "delegated errands"})
	if err != nil {
		t.Fatal(err)
	}

	// daan grants scribe a scope, bounded.
	grantID, err := h.IssueGrant(ctx, "scribe", "resource:github", time.Now().Add(time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}

	// The grantee exercises it: acting persona is the author, the granter
	// rides the authority claim — each readable without the other (C3).
	sh := Open(scribe, h.Path())
	if _, err := sh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.PostTurnOnBehalf(ctx, "fetched the release notes", GrantAuthority{Grant: grantID, Granter: "daan"}); err != nil {
		t.Fatal(err)
	}

	// A non-granter's revoke is void; the granter's lands.
	if _, err := sh.RevokeGrant(ctx, grantID); err != nil {
		t.Fatal(err)
	}
	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mt.Grants) != 1 {
		t.Fatalf("grants = %d", len(mt.Grants))
	}
	g := mt.Grants[0]
	if g.Status != GrantActive || !g.ActiveAt(time.Now()) {
		t.Fatalf("after foreign revoke: status=%s", g.Status)
	}
	if len(g.Timeline) != 1 || !g.Timeline[0].Void || g.Timeline[0].Author != "scribe" {
		t.Fatalf("foreign revoke not void-visible: %+v", g.Timeline)
	}
	var exercised *Contribution
	for i := range mt.Contributions {
		if mt.Contributions[i].Authority != nil {
			exercised = &mt.Contributions[i]
		}
	}
	if exercised == nil || exercised.Author != "scribe" ||
		exercised.Authority.Granter != "daan" || exercised.Authority.Grant != grantID {
		t.Fatalf("dual attribution missing: %+v", exercised)
	}

	if _, err := h.RevokeGrant(ctx, grantID); err != nil {
		t.Fatal(err)
	}

	// Both replicas agree; the projection is a pure function of the log.
	for _, c := range []*Handle{h, sh} {
		mt, err := c.Materialise(ctx)
		if err != nil {
			t.Fatal(err)
		}
		g := mt.Grants[0]
		if g.Status != GrantRevoked || g.ActiveAt(time.Now()) {
			t.Fatalf("after revoke: %+v", g)
		}
		if len(g.Timeline) != 2 || g.Timeline[1].Void {
			t.Fatalf("granter revoke not landed: %+v", g.Timeline)
		}
	}

	// The rollup bakes the grant; the revoked state and the void history
	// survive compaction, and a second revoke stays void.
	if _, err := h.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.RevokeGrant(ctx, grantID); err != nil {
		t.Fatal(err)
	}
	mt, err = h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mt.Grants) != 1 {
		t.Fatalf("grants after rollup = %d", len(mt.Grants))
	}
	g = mt.Grants[0]
	if g.Status != GrantRevoked || g.Granter != "daan" || g.Grantee != "scribe" || g.Scope != "resource:github" {
		t.Fatalf("baked grant lost state: %+v", g)
	}
	if last := g.Timeline[len(g.Timeline)-1]; !last.Void {
		t.Fatal("post-rollup re-revoke should be void")
	}
}

// TestGrantExpiryIsAClockFact: the projection never consults a clock —
// an expired-but-unrevoked grant stays GrantActive in the log-derived
// state, and ActiveAt is where "now" enters.
func TestGrantExpiryIsAClockFact(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClient(t, url, "daan")
	if _, err := c.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "short leash"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.IssueGrant(ctx, "scribe", "resource:calendar", time.Now().Add(-time.Minute).UTC()); err != nil {
		t.Fatal(err)
	}
	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g := mt.Grants[0]
	if g.Status != GrantActive {
		t.Fatalf("log-derived status = %s, want active (expiry is not a log fact)", g.Status)
	}
	if g.ActiveAt(time.Now()) {
		t.Fatal("expired grant reported active at now")
	}
	if !g.ActiveAt(g.Expires.Add(-time.Second)) {
		t.Fatal("grant not active inside its own window")
	}
}
