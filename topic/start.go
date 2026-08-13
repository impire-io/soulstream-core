package topic

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/impire-io/soulstream-core/realm"
)

// InlineBaselineThreshold is the maximum size, in bytes, of an inline baseline state.
// Larger baselines require manifest (chunked) baselines, which are deferred to a later
// cycle.
const InlineBaselineThreshold = 128 * 1024

// StartTopicInput is the intent to start a topic.
type StartTopicInput struct {
	Name          string
	SubjectMatter string
	Tags          []string
	Expected      []string // a hint for clients, never a posting gate
	Parent        string   // "" for a top-level topic; else the parent's topic-path
	State         json.RawMessage
}

// StartTopic generates a topic-id, publishes the topic.announce to the INFO subject and
// the initial inline baseline as the first op on the OPS subject, and returns a handle
// to the new topic. It errors if the initial state exceeds the inline threshold.
func StartTopic(ctx context.Context, c *realm.Client, in StartTopicInput) (*Handle, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("topic: StartTopic requires a name")
	}

	state := in.State
	if len(state) == 0 {
		state = json.RawMessage(`{}`)
	}
	if len(state) > InlineBaselineThreshold {
		return nil, fmt.Errorf(
			"topic: initial baseline state is %d bytes, over the %d-byte inline limit; "+
				"manifest (chunked) baselines are not yet supported",
			len(state), InlineBaselineThreshold)
	}

	id := NewTopicID(in.Name)
	path := ChildPath(in.Parent, id)

	// 1. Announce the topic on its INFO subject.
	announce := AnnouncePayload{
		TopicID:       id,
		Name:          in.Name,
		SubjectMatter: in.SubjectMatter,
		Expected:      in.Expected,
		Tags:          in.Tags,
		Parent:        in.Parent,
	}
	if _, err := publishOp(ctx, c, InfoSubject(path), TypeAnnounce, announce, nil); err != nil {
		return nil, fmt.Errorf("topic: announce: %w", err)
	}

	// 2. Publish the initial baseline as the FIRST op on the OPS subject.
	baselineID, err := publishOp(ctx, c, OpsSubject(path), TypeBaseline,
		BaselinePayload{State: state, Frontier: []string{}}, nil)
	if err != nil {
		return nil, fmt.Errorf("topic: initial baseline: %w", err)
	}

	return &Handle{client: c, path: path, frontier: []string{baselineID}}, nil
}
