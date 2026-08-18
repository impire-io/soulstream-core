// The grant record vocabulary (C4 — hq
// 02-DESIGN/soulstream-core/extensions/tenancy.md): the op-log half of
// the S8 split. A grant is recorded where its issuer can review it and
// watch it being exercised; enforcement lives with whoever holds the
// capability, consuming THIS record only as a projection — replay the
// ops and you rebuild its state, never a second source of truth.

package topic

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// GrantIssuePayload is the grant.issue payload: the author (the granter)
// authorizes Grantee to do a specific, named thing on its behalf. The
// op's ID becomes the grant's identity. A grant exists when issued and
// not before (C6).
type GrantIssuePayload struct {
	Grantee string    `json:"grantee"`          // required; empty is malformed
	Scope   string    `json:"scope"`            // required: the resource or action class granted
	Expires time.Time `json:"expires,omitzero"` // zero = until revoked
}

// GrantAuthority names the standing grant an op acts under: the grant's
// issue op and its granter — so the acting persona (the op's author) and
// the granting persona are each readable without inferring one from the
// other (C3).
type GrantAuthority struct {
	Grant   string `json:"grant"`   // the grant.issue op-id
	Granter string `json:"granter"` // the granting persona, by name
}

// GrantStatus is a grant's log-derived state. Expiry is a clock fact,
// not a log fact: the projection stays a pure function of the log, and
// readers ask [GrantItem.ActiveAt] when "now" matters.
type GrantStatus string

// Grant states. Revoked is terminal: re-granting is a new grant.
const (
	GrantActive  GrantStatus = "active"
	GrantRevoked GrantStatus = "revoked"
)

// GrantEvent is one op that touched a grant — including the ones the
// state machine rejected, which stay visible as void.
type GrantEvent struct {
	OpID      string    `json:"op_id"`
	Kind      string    `json:"kind"` // "revoke"
	Author    string    `json:"author"`
	Timestamp time.Time `json:"ts"`
	Void      bool      `json:"void,omitempty"`
	Sig       SigStatus `json:"sig,omitempty"`
	StreamSeq uint64    `json:"stream_seq,omitempty"` // 0 for baked events
}

// GrantItem is a standing grant derived from the log: identity is the
// issuing op's ID, the granter is its author.
type GrantItem struct {
	ID        string       `json:"id"`
	Granter   string       `json:"granter"` // the issue op's author
	Grantee   string       `json:"grantee"`
	Scope     string       `json:"scope"`
	Timestamp time.Time    `json:"ts"` // issued at
	Expires   time.Time    `json:"expires,omitzero"`
	Status    GrantStatus  `json:"status"`
	Timeline  []GrantEvent `json:"timeline,omitempty"`
	Sig       SigStatus    `json:"sig,omitempty"`
	StreamSeq uint64       `json:"stream_seq,omitempty"` // 0 for baked grants
}

// ActiveAt reports whether the grant authorizes at the given moment:
// not revoked, and inside its expiry when one is set.
func (g GrantItem) ActiveAt(now time.Time) bool {
	if g.Status != GrantActive {
		return false
	}
	return g.Expires.IsZero() || now.Before(g.Expires)
}

// IssueGrant records the client persona's authorization: grantee may do
// scope on the granter's behalf, until expires (zero = until revoked).
// The returned op-id is the grant's identity.
func (h *Handle) IssueGrant(ctx context.Context, grantee, scope string, expires time.Time) (string, error) {
	if strings.TrimSpace(grantee) == "" {
		return "", fmt.Errorf("topic: grant.issue needs a grantee")
	}
	if strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("topic: grant.issue needs a scope")
	}
	return h.Post(ctx, TypeGrantIssue, GrantIssuePayload{Grantee: grantee, Scope: scope, Expires: expires})
}

// RevokeGrant publishes grant.revoke for the grant. Only the granter's
// revoke moves the state machine; anything else stays visible as void.
// Publishing cannot know whether it won — materialise and read.
func (h *Handle) RevokeGrant(ctx context.Context, grantID string) (string, error) {
	if strings.TrimSpace(grantID) == "" {
		return "", fmt.Errorf("topic: grant.revoke needs a grant id")
	}
	return h.Post(ctx, TypeGrantRevoke, RefPayload{Anchor: &Anchor{Kind: "op", OpID: grantID}})
}

// PostTurnOnBehalf posts a turn carrying its authority: the acting
// persona is the op's author, the granting persona and the grant it
// acts under ride the payload (C3's dual attribution). The library
// records the claim; whether the grant was live is the projection's
// answer and the enforcer's decision, never the author's say-so.
func (h *Handle) PostTurnOnBehalf(ctx context.Context, body string, authority GrantAuthority) (string, error) {
	if authority.Grant == "" || authority.Granter == "" {
		return "", fmt.Errorf("topic: on-behalf turn needs the grant id and the granter")
	}
	mentions := ParseMentions(body)
	opID, err := h.Post(ctx, TypeTurnPost, TurnPayload{Body: body, Mentions: mentions, Authority: &authority})
	if err != nil {
		return "", err
	}
	return opID, h.notifyMentions(ctx, opID, mentions)
}
