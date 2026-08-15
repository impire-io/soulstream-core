package topic

import (
	"context"
	"fmt"
	"log"
)

// warnf reports a non-fatal advisory. It is a package variable so tests can capture it.
var warnf = func(format string, args ...any) {
	log.Printf("soulstream/topic: "+format, args...)
}

// Post builds and publishes an operation to the topic's ops subject: it stamps the
// author (the client's persona), generates the op-id, and parents onto the frontier the
// handle has observed. It returns the new op-id and advances the handle's frontier to
// it. Posting to a topic the handle last saw as closed is warned, not blocked — closed
// is not-writable by convention. Archived is different: terminal, refused outright.
func (h *Handle) Post(ctx context.Context, opType string, payload any) (string, error) {
	if h.lifecycle == Archived {
		return "", fmt.Errorf("topic: %s is archived — %w", h.path, ErrTopicArchived)
	}
	if h.lifecycle == Closed {
		warnf("posting %s to closed topic %s (closed is not-writable by convention)", opType, h.path)
	}
	opID, err := publishOp(ctx, h.client, OpsSubject(h.path), opType, payload, h.frontier)
	if err != nil {
		return "", err
	}
	h.frontier = []string{opID}
	return opID, nil
}

// PostTurn posts a turn.post — a contribution to the conversation. It parses @mentions
// from the body, records them on the op, and notifies each mentioned persona's inbox.
func (h *Handle) PostTurn(ctx context.Context, body string) (string, error) {
	return h.PostTurnMentioning(ctx, body, nil)
}

// PostTurnMentioning posts a turn.post that also taps personas the body does not name in
// the grammar. The op carries — and the notifies reach — the union of what the parser
// read out of the body and what the caller supplied; the body is posted exactly as
// given, never rewritten to match. Invalid or empty supplied names are dropped.
//
// It is the arm for a surface that lets people write a name they recognise: the caller
// resolves the display text to persona names and hands them over, and the record still
// says what the person typed.
func (h *Handle) PostTurnMentioning(ctx context.Context, body string, mentions []string) (string, error) {
	all := mergeMentions(body, mentions)
	opID, err := h.Post(ctx, TypeTurnPost, TurnPayload{Body: body, Mentions: all})
	if err != nil {
		return "", err
	}
	return opID, h.notifyMentions(ctx, opID, all)
}

// PostTurnIdempotent posts a turn.post under a caller-supplied operation id — the
// exported arm of the library's retry-with-same-id duty. The id doubles as the
// message's duplicate-detection identity (Nats-Msg-Id), so reposting the same id
// within the stream's duplicate window is absorbed by the server rather than
// creating a second operation: a caller that never learned a publish's outcome
// retries with the same id and the record stays single. Mention handling matches
// [Handle.PostTurnMentioning]; a repost does re-publish the mention notifies —
// notifications are advisory pointers into a bounded inbox, so retry noise is
// bounded noise, never a second op. An empty id falls back to a fresh one.
func (h *Handle) PostTurnIdempotent(ctx context.Context, body string, mentions []string, opID string) (string, error) {
	if opID == "" {
		return h.PostTurnMentioning(ctx, body, mentions)
	}
	if h.lifecycle == Archived {
		return "", fmt.Errorf("topic: %s is archived — %w", h.path, ErrTopicArchived)
	}
	if h.lifecycle == Closed {
		warnf("posting %s to closed topic %s (closed is not-writable by convention)", TypeTurnPost, h.path)
	}
	all := mergeMentions(body, mentions)
	id, err := publishOpWith(ctx, h.client, OpsSubject(h.path), TypeTurnPost,
		TurnPayload{Body: body, Mentions: all}, h.frontier, opID, nil, nil)
	if err != nil {
		return "", err
	}
	h.frontier = []string{id}
	return id, h.notifyMentions(ctx, id, all)
}

// AddComment posts a comment.add anchored to anchorOpID, with the same @mention handling
// as PostTurn.
func (h *Handle) AddComment(ctx context.Context, body, anchorOpID string) (string, error) {
	return h.AddCommentMentioning(ctx, body, anchorOpID, nil)
}

// AddCommentMentioning posts a comment.add that also taps supplied personas, with the
// same union rule as [Handle.PostTurnMentioning].
func (h *Handle) AddCommentMentioning(ctx context.Context, body, anchorOpID string, mentions []string) (string, error) {
	all := mergeMentions(body, mentions)
	opID, err := h.Post(ctx, TypeCommentAdd, CommentPayload{
		Body: body, Anchor: Anchor{Kind: "op", OpID: anchorOpID}, Mentions: all,
	})
	if err != nil {
		return "", err
	}
	return opID, h.notifyMentions(ctx, opID, all)
}
