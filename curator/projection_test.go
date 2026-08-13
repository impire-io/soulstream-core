package curator

import (
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/topic"
)

// TestLastRealCountsWorkOps (010, FR-019): opening, claiming, finishing, or
// abandoning a work item is real activity — a topic whose latest op is a work
// event is not dormant from that moment.
func TestLastRealCountsWorkOps(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	view := &topic.MaterializedTopic{
		BaselineTs: base,
		Contributions: []topic.Contribution{
			{OpID: "c1", Type: topic.TypeTurnPost, Body: "old talk", Timestamp: base.Add(1 * time.Hour)},
		},
		WorkItems: []topic.WorkItem{{
			ID: "w1", Title: "a chore", Timestamp: base.Add(2 * time.Hour),
			Timeline: []topic.WorkEvent{
				{OpID: "e1", Kind: "claim", Author: "scribe", Timestamp: base.Add(3 * time.Hour)},
			},
		}},
	}

	ct := buildCached("realm-topic", view)
	if !ct.lastReal.Equal(base.Add(3 * time.Hour)) {
		t.Errorf("lastReal = %v, want the claim event's time — work ops keep a topic alive", ct.lastReal)
	}
}
