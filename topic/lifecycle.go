package topic

import (
	"context"
	"errors"
	"fmt"

	"github.com/impire-io/soulstream-core/realm"
)

// definedLifecycle reports whether l is a state a persona can transition to.
func definedLifecycle(l Lifecycle) bool {
	switch l {
	case Proposed, Active, Dormant, Closed, Archived:
		return true
	default:
		return false
	}
}

// Transition posts a life.transition to the given state. It rejects a state the
// protocol does not define, naming the allowed states.
//
// The resulting lifecycle is derived from the log when the topic is materialised, so
// two personas transitioning to the same state concurrently converge — the ops merge
// harmlessly and there is no arbiter. Note: a bare transition to Archived records
// terminality without the final compaction; Archive is the mandated act.
func (h *Handle) Transition(ctx context.Context, to Lifecycle) (string, error) {
	if !definedLifecycle(to) {
		return "", fmt.Errorf(
			"topic: undefined lifecycle transition %q; allowed states are %q, %q, %q, %q, %q",
			to, Proposed, Active, Dormant, Closed, Archived)
	}
	return h.Post(ctx, TypeLifeTransition, TransitionPayload{To: to})
}

// MarkDormant posts the dormant transition — the write half of core's idle rule.
// Callers apply the rule first (DormantEligible); marking is idempotent, so two
// personas applying it concurrently converge. Any content op wakes the topic.
func (h *Handle) MarkDormant(ctx context.Context) (string, error) {
	return h.Transition(ctx, Dormant)
}

// Close records the closed transition and then tidies the topic with one
// best-effort compaction attempt. Closed-and-uncompacted is a valid resting state
// (rollup is optional for correctness), so a lost race or an empty tail is
// swallowed; only unexpected failures surface — with the close already standing.
func (h *Handle) Close(ctx context.Context) (string, error) {
	opID, err := h.Transition(ctx, Closed)
	if err != nil {
		return "", err
	}
	h.lifecycle = Closed

	if _, err := h.rollup(ctx, false); err != nil &&
		!errors.Is(err, ErrRollupLost) && !errors.Is(err, ErrNothingToCompact) {
		return opID, fmt.Errorf("topic: closed %s, but the tidy-up compaction failed: %w", h.path, err)
	}
	return opID, nil
}

// archiveRetries bounds Archive's compaction attempts: enough to absorb a racing
// writer or two, never a livelock against a hostile one.
const archiveRetries = 3

// Archive is the one deliberate reclamation act: it records the archived transition
// and performs the final compaction, ending the topic as a single terminal baseline
// — readable forever, writable never. The compaction retries a bounded number of
// times against racing writers; if retries exhaust, the archived transition stands
// (writes are already refused on observation), the error says so loudly, and a later
// Archive call finishes the job. Archiving an archived-and-compact topic reports
// "already archived" and changes nothing.
//
// It returns the final baseline's op-id.
func (h *Handle) Archive(ctx context.Context) (string, error) {
	mt, err := h.Materialise(ctx)
	if err != nil {
		return "", err
	}
	if mt.Malformed != "" {
		return "", fmt.Errorf("topic: refusing to archive a malformed topic: %s", mt.Malformed)
	}

	if mt.Lifecycle == Archived {
		// Either fully archived (nothing to do) or a half-done archival whose
		// final compaction lost its races earlier — finish it.
		baselineID, cerr := h.archiveCompact(ctx)
		if errors.Is(cerr, ErrNothingToCompact) {
			// Already compact — but a prior archival may have died before its
			// blob sweep; finishing that is why re-archiving must GC too.
			h.sweepRemovedBlobs(ctx)
			return "", fmt.Errorf("topic: %s is already archived — %w", h.path, ErrTopicArchived)
		}
		if cerr == nil {
			h.sweepRemovedBlobs(ctx)
		}
		return baselineID, cerr
	}

	if _, err := h.Transition(ctx, Archived); err != nil {
		return "", err
	}
	h.lifecycle = Archived

	baselineID, cerr := h.archiveCompact(ctx)
	if errors.Is(cerr, ErrNothingToCompact) {
		// Someone else's compaction baked our transition concurrently: archived
		// and compact — the goal state, however reached.
		h.sweepRemovedBlobs(ctx)
		recs, derr := drainOps(ctx, h.client, h.path)
		if derr != nil || len(recs) == 0 {
			return "", derr
		}
		return recs[0].Record.ID, nil
	}
	if cerr == nil {
		h.sweepRemovedBlobs(ctx)
	}
	return baselineID, cerr
}

// sweepRemovedBlobs deletes withdrawn attachments' objects — the archival half of
// attachment.remove ("the blob itself is deleted only at topic archival"). It
// runs only after the final baseline is in place, so replay never dangles within
// the topic's lifetime; failures leave sweepable garbage, never corruption.
func (h *Handle) sweepRemovedBlobs(ctx context.Context) {
	recs, err := drainOps(ctx, h.client, h.path)
	if err != nil || len(recs) == 0 {
		return
	}
	if resolveBaseline(ctx, h.client, recs) != nil {
		return
	}
	mt := apply(h.path, recs)
	store, err := h.client.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		return
	}
	for _, a := range mt.Attachments {
		if a.Removed {
			_ = store.Delete(ctx, a.Object)
		}
	}
}

// archiveCompact runs the final compaction with bounded retries against racing
// writers. ErrNothingToCompact passes through for the caller to interpret.
func (h *Handle) archiveCompact(ctx context.Context) (string, error) {
	var lastErr error
	for attempt := 0; attempt < archiveRetries; attempt++ {
		baselineID, err := h.rollup(ctx, true)
		switch {
		case err == nil:
			return baselineID, nil
		case errors.Is(err, ErrRollupLost):
			lastErr = err // a writer raced in; re-drain and try again
		case errors.Is(err, ErrNothingToCompact):
			return "", err
		default:
			return "", fmt.Errorf("topic: archived %s, but the final compaction failed: %w", h.path, err)
		}
	}
	return "", fmt.Errorf("topic: archived %s, but the final compaction lost %d races: %w (the transition stands; archive again to finish)",
		h.path, archiveRetries, lastErr)
}
