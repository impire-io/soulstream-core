package topic

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Conversation upkeep and the idle rules: the housekeeping words. Reply, edit,
// resolve, and remove are ordinary ops; dormancy and stale claims are pure rules
// any persona may apply — the clock is the caller's, the log stays the truth.

// Reply posts a comment.reply anchored to a comment (or another reply), with the
// same @mention handling as comments.
func (h *Handle) Reply(ctx context.Context, body, anchorOpID string) (string, error) {
	return h.ReplyMentioning(ctx, body, anchorOpID, nil)
}

// ReplyMentioning posts a comment.reply that also taps supplied personas, with the same
// union rule as [Handle.PostTurnMentioning].
func (h *Handle) ReplyMentioning(ctx context.Context, body, anchorOpID string, mentions []string) (string, error) {
	if strings.TrimSpace(anchorOpID) == "" {
		return "", fmt.Errorf("topic: a reply needs the comment's op-id")
	}
	all := mergeMentions(body, mentions)
	opID, err := h.Post(ctx, TypeCommentReply, CommentPayload{
		Body: body, Anchor: Anchor{Kind: "op", OpID: anchorOpID}, Mentions: all,
	})
	if err != nil {
		return "", err
	}
	return opID, h.notifyMentions(ctx, opID, all)
}

// Edit publishes a whole-body correction of the caller's own turn, comment, or
// reply. Supersession is a projection rule: readers render the newest edit in
// the chain, and an edit by anyone but the original author changes nothing (it
// folds as a visible warning). targetOpID may be the contribution or any prior
// edit of it. Mentions in the new body notify, like everywhere else.
func (h *Handle) Edit(ctx context.Context, targetOpID, newBody string) (string, error) {
	if strings.TrimSpace(targetOpID) == "" {
		return "", fmt.Errorf("topic: an edit needs its target's op-id")
	}
	if strings.TrimSpace(newBody) == "" {
		return "", fmt.Errorf("topic: an edit needs a body — an empty correction corrects nothing")
	}
	mentions := ParseMentions(newBody)
	opID, err := h.Post(ctx, TypeEdit, CommentPayload{
		Body: newBody, Anchor: Anchor{Kind: "op", OpID: targetOpID}, Mentions: mentions,
	})
	if err != nil {
		return "", err
	}
	return opID, h.notifyMentions(ctx, opID, mentions)
}

// Resolve marks a comment (or reply) settled — closed without being deleted.
// Any persona may resolve; duplicates are harmless no-ops by projection.
func (h *Handle) Resolve(ctx context.Context, targetOpID string) (string, error) {
	if strings.TrimSpace(targetOpID) == "" {
		return "", fmt.Errorf("topic: resolve needs the comment's op-id")
	}
	return h.Post(ctx, TypeCommentResolve, RefPayload{Anchor: &Anchor{Kind: "op", OpID: targetOpID}})
}

// RemoveAttachment withdraws an attachment: the mark is visible, the bytes stay
// fetchable until the topic is archived (which reclaims withdrawn blobs). Any
// persona may remove; duplicates are harmless no-ops by projection.
func (h *Handle) RemoveAttachment(ctx context.Context, addOpID string) (string, error) {
	if strings.TrimSpace(addOpID) == "" {
		return "", fmt.Errorf("topic: attachment.remove needs the attachment's op-id")
	}
	return h.Post(ctx, TypeAttachmentRemove, RefPayload{Anchor: &Anchor{Kind: "op", OpID: addOpID}})
}

// NewestOpTs returns the timestamp of the newest operation the view carries —
// contributions and their edits, resolves, attachments and their removals, work
// items and their timelines — with the baseline as the floor. This is the clock
// the dormancy rule reads: the last time *anything* happened.
func NewestOpTs(mt *MaterializedTopic) time.Time {
	newest := mt.BaselineTs
	bump := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	for _, c := range mt.Contributions {
		bump(c.Timestamp)
		bump(c.ResolvedTs)
		for _, e := range c.Edits {
			bump(e.Ts)
		}
	}
	for _, a := range mt.Attachments {
		bump(a.Timestamp)
		bump(a.RemovedTs)
	}
	for _, w := range mt.WorkItems {
		bump(w.Timestamp)
		for _, ev := range w.Timeline {
			bump(ev.Timestamp)
		}
	}
	return newest
}

// DormantEligible applies core's idle rule: a proposed or active topic whose
// newest operation — of any kind — is older than the window may be marked
// dormant by any persona. Dormant, closed, archived, and malformed topics are
// never eligible. Pure: the clock is the caller's.
func DormantEligible(mt *MaterializedTopic, window time.Duration, now time.Time) bool {
	if mt == nil || mt.Malformed != "" {
		return false
	}
	if mt.Lifecycle != Proposed && mt.Lifecycle != Active {
		return false
	}
	return now.Sub(NewestOpTs(mt)) > window
}

// StaleClaims returns the ids of claimed work items whose newest related
// activity — the claim itself, any later timeline event, any comment or
// attachment anchored to the item — is older than the window. A sweep may
// reopen each with an ordinary work.abandon; the second sweep's abandon folds
// void, so races converge. Pure: the clock is the caller's.
func StaleClaims(mt *MaterializedTopic, window time.Duration, now time.Time) []string {
	if mt == nil || mt.Malformed != "" {
		return nil
	}
	var out []string
	for _, w := range mt.WorkItems {
		if w.Status != WorkClaimed {
			continue
		}
		last := w.Timestamp
		for _, ev := range w.Timeline {
			if ev.Timestamp.After(last) {
				last = ev.Timestamp
			}
		}
		for _, c := range mt.Contributions {
			if c.Anchor == w.ID && c.Timestamp.After(last) {
				last = c.Timestamp
			}
		}
		for _, a := range mt.Attachments {
			if a.Anchor == w.ID && a.Timestamp.After(last) {
				last = a.Timestamp
			}
		}
		if now.Sub(last) > window {
			out = append(out, w.ID)
		}
	}
	return out
}
