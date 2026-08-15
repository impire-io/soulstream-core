package topic

import (
	"context"
	"testing"

	"github.com/impire-io/soulstream-core/record"
)

// A repost under the same operation id is absorbed by the stream's duplicate
// window: one op in the record, the same id returned both times. This is the
// retry-with-same-id duty the library documents, exported.
func TestPostTurnIdempotentDedupes(t *testing.T) {
	c := provisionedClient(t, "poster")
	ctx := context.Background()

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "idempotent"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}

	opID := record.NewID()
	first, err := h.PostTurnIdempotent(ctx, "the one reply", nil, opID)
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	second, err := h.PostTurnIdempotent(ctx, "the one reply", nil, opID)
	if err != nil {
		t.Fatalf("repost: %v", err)
	}
	if first != opID || second != opID {
		t.Fatalf("op ids = %q, %q, want the supplied %q both times", first, second, opID)
	}

	view, err := h.Materialise(ctx)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	turns := 0
	for _, contrib := range view.Contributions {
		if contrib.Type == TypeTurnPost {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("turns in topic = %d, want exactly 1 after a same-id repost", turns)
	}
}

// An empty id keeps the ordinary fresh-id behavior: two posts are two ops.
func TestPostTurnIdempotentEmptyIDIsFresh(t *testing.T) {
	c := provisionedClient(t, "poster")
	ctx := context.Background()

	h, err := StartTopic(ctx, c, StartTopicInput{Name: "fresh"})
	if err != nil {
		t.Fatalf("start topic: %v", err)
	}
	first, err := h.PostTurnIdempotent(ctx, "one", nil, "")
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	second, err := h.PostTurnIdempotent(ctx, "two", nil, "")
	if err != nil {
		t.Fatalf("second post: %v", err)
	}
	if first == second {
		t.Fatalf("fresh posts share id %q, want distinct ids", first)
	}
}
