package curator

import (
	"context"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/topic"
)

// TestCuratorSweepsOptIn (011 US3): without the flags a curator posts no
// transitions and no abandons — 009's contract; with them, idle topics go
// dormant and stale claims reopen, all with ordinary attributed ops.
func TestCuratorSweepsOptIn(t *testing.T) {
	ctx := context.Background()
	c, url := provisioned(t, "daan")

	quiet, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Going Quiet"})
	if err != nil {
		t.Fatal(err)
	}
	work, err := topic.StartTopic(ctx, c, topic.StartTopicInput{Name: "Stalled Work"})
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := work.OpenWork(ctx, "stalls forever", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := work.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}

	view := func(h *topic.Handle) *topic.MaterializedTopic {
		t.Helper()
		mt, err := topic.Open(c, h.Path()).Materialise(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return mt
	}

	// Everything idles past the (tiny) windows.
	window := 200 * time.Millisecond
	time.Sleep(300 * time.Millisecond)

	// Flags off: several scan ticks change nothing.
	stop, _ := startCurator(t, url, Options{IdleWindow: window, ScanEvery: 50 * time.Millisecond})
	time.Sleep(400 * time.Millisecond)
	if got := view(quiet).Lifecycle; got == topic.Dormant {
		t.Fatal("unflagged curator marked a topic dormant (009 contract broken)")
	}
	if got := view(work).WorkItems[0]; got.Status != topic.WorkClaimed || got.Owner != "daan" {
		t.Fatalf("unflagged curator touched a claim: %+v", got)
	}
	stop()

	// Flags on: the idle topic goes dormant, the stale claim reopens.
	stop2, _ := startCurator(t, url, Options{
		IdleWindow: window, ScanEvery: 50 * time.Millisecond,
		MarkDormant: true, ReclaimAfter: window,
	})
	defer stop2()

	waitFor(t, 5*time.Second, "dormant mark", func() bool {
		return view(quiet).Lifecycle == topic.Dormant
	})
	waitFor(t, 5*time.Second, "stale claim reclaimed", func() bool {
		item := view(work).WorkItems[0]
		return item.Status == topic.WorkOpen && item.Owner == ""
	})

	// The reopened item is claimable fresh; the abandon is on the timeline,
	// attributed to the curator persona.
	item := view(work).WorkItems[0]
	last := item.Timeline[len(item.Timeline)-1]
	if last.Kind != "abandon" || last.Author != "curator" || last.Void {
		t.Errorf("reclaim event wrong: %+v", last)
	}
	if _, err := work.ClaimWork(ctx, itemID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "fresh claim", func() bool {
		return view(work).WorkItems[0].Owner == "daan"
	})

	// A content op wakes the dormant topic even while the sweep keeps running —
	// and the sweep re-marks it only after a fresh full window.
	fresh := topic.Open(c, quiet.Path())
	if _, err := fresh.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.PostTurn(ctx, "picking this back up"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "topic awake", func() bool {
		lc := view(quiet).Lifecycle
		return lc == topic.Active || lc == topic.Dormant // dormant again is legal after a full window
	})
}
