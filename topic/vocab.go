package topic

import (
	"encoding/json"
	"time"

	"github.com/impire-io/soulstream-core/record"
)

// Operation types defined this cycle. Types outside this set are ignored with a
// warning during materialisation (additive vocabulary growth).
const (
	TypeAnnounce         = "topic.announce"
	TypeBaseline         = "baseline"
	TypeTurnPost         = "turn.post"
	TypeCommentAdd       = "comment.add"
	TypeLifeTransition   = "life.transition"
	TypeAttachmentAdd    = "attachment.add"
	TypeMentionNotify    = "mention.notify"
	TypeDiscover         = "topic.discover"
	TypeDiscoverReply    = "topic.discover.reply"
	TypeWorkOpen         = "work.open"
	TypeWorkClaim        = "work.claim"
	TypeWorkDone         = "work.done"
	TypeWorkAbandon      = "work.abandon"
	TypeCommentReply     = "comment.reply"
	TypeCommentResolve   = "comment.resolve"
	TypeEdit             = "edit"
	TypeAttachmentRemove = "attachment.remove"
	TypeMemoryQuery      = "memory.query"
	TypeMemoryAnswer     = "memory.answer"
	TypeMemoryFetch      = "memory.fetch"
	TypeMemoryExhibit    = "memory.exhibit"
)

// Lifecycle is a topic's derived state.
type Lifecycle string

// Lifecycle states.
const (
	Proposed Lifecycle = "proposed"
	Active   Lifecycle = "active"
	// Dormant is idle past the realm's window; resumable — any content op makes
	// the topic active again. Any persona may apply the deterministic idle rule
	// and post the transition.
	Dormant Lifecycle = "dormant"
	Closed  Lifecycle = "closed"
	// Archived is terminal: the final re-baseline has run, the topic is readable
	// forever and refuses all writes.
	Archived Lifecycle = "archived"
)

// AnnouncePayload is the topic.announce payload, carried on the INFO subject.
type AnnouncePayload struct {
	TopicID       string   `json:"topic_id"`
	Name          string   `json:"name"`
	SubjectMatter string   `json:"subject_matter,omitempty"`
	Expected      []string `json:"expected,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Parent        string   `json:"parent,omitempty"`
}

// BaselinePayload is the baseline op payload: the topic's zero-point. At birth it
// carries the opaque workbench state and an empty frontier. After a rollup it also
// carries the conversation folded in (Baked) — or, when the state document exceeds
// the inline threshold, a Manifest referencing the object store instead of
// State/Baked. Exactly one of {State(+Baked), Manifest} is present.
type BaselinePayload struct {
	State    json.RawMessage `json:"state,omitempty"`
	Frontier []string        `json:"frontier"`
	Baked    *BakedState     `json:"baked,omitempty"`
	Manifest *ManifestRef    `json:"manifest,omitempty"`
}

// BakedState is the conversation a rollup folded into its baseline: the elements a
// reader would have materialised from the compacted tail, in original stream order.
// Derived facts (dangling flags, sig statuses, active-vs-proposed) are recomputed at
// read time, never stored.
type BakedState struct {
	Contributions []Contribution `json:"contributions,omitempty"`
	Attachments   []Attachment   `json:"attachments,omitempty"`
	WorkItems     []WorkItem     `json:"work_items,omitempty"`
	Lifecycle     Lifecycle      `json:"lifecycle,omitempty"`
}

// ManifestRef names an oversized state document in the object store: chunk objects in
// fetch order (one, this cycle), a digest over the full document, and its size.
type ManifestRef struct {
	Chunks []string `json:"chunks"`
	Digest string   `json:"digest"`
	Size   uint64   `json:"size"`
}

// TurnPayload is the turn.post payload.
type TurnPayload struct {
	Body     string   `json:"body"`
	Mentions []string `json:"mentions,omitempty"`
}

// Anchor references another operation by its op-id.
type Anchor struct {
	Kind string `json:"kind"` // "op"
	OpID string `json:"op_id"`
}

// CommentPayload is the comment.add payload.
type CommentPayload struct {
	Body     string   `json:"body"`
	Anchor   Anchor   `json:"anchor"`
	Mentions []string `json:"mentions,omitempty"`
}

// TransitionPayload is the life.transition payload.
type TransitionPayload struct {
	To   Lifecycle `json:"to"`
	From Lifecycle `json:"from,omitempty"`
}

// AttachmentPayload is the attachment.add payload: a small, verifiable reference to a
// blob in the realm's object store.
type AttachmentPayload struct {
	Name        string `json:"name"`
	Object      string `json:"object"`
	Digest      string `json:"digest"`
	Size        uint64 `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	Anchor      string `json:"anchor,omitempty"`
}

// RefPayload is the payload of ops whose whole meaning is "about that op":
// comment.resolve and attachment.remove reference their target via the usual
// anchor convention. A missing anchor is malformed.
type RefPayload struct {
	Anchor *Anchor `json:"anchor"`
}

// NotifyPayload is the mention.notify payload, carried on a persona's notify subject.
type NotifyPayload struct {
	Topic  string `json:"topic"`
	OpID   string `json:"op_id"`
	Author string `json:"author"`
}

// DiscoverPayload is the topic.discover request: the question a persona shouts at
// the realm. The deadline is advisory for answerers (skip stale work); enforcement
// is the asker's — it simply stops listening.
type DiscoverPayload struct {
	Query    string    `json:"query"`
	Limit    int       `json:"limit,omitempty"`
	Deadline time.Time `json:"deadline,omitempty"`
}

// DiscoverEntry is one topic as one answerer knows it, from its own projection.
type DiscoverEntry struct {
	Path          string    `json:"path"`
	Name          string    `json:"name"`
	SubjectMatter string    `json:"subject_matter,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Lifecycle     Lifecycle `json:"lifecycle,omitempty"`
}

// DiscoverReplyPayload is a topic.discover.reply: one answerer's matches. A
// responder with nothing to say sends nothing — silence is cheaper than noise.
type DiscoverReplyPayload struct {
	Matches []DiscoverEntry `json:"matches"`
}

// MemoryScope is a memory.query's relevance hint: which topics (name patterns,
// witness-interpreted) and what time horizon the asker cares about. Witnesses
// decide relevance; the asker's protection is grading, never filtering.
type MemoryScope struct {
	Topics []string  `json:"topics,omitempty"`
	After  time.Time `json:"after,omitzero"`
}

// MemoryQueryPayload is the memory.query request: a free-text question shouted at
// whoever remembers. The deadline is advisory for witnesses (skip stale work);
// enforcement is the asker's — it simply stops listening.
type MemoryQueryPayload struct {
	Query    string       `json:"query"`
	Scope    *MemoryScope `json:"scope,omitempty"`
	Deadline time.Time    `json:"deadline,omitzero"`
}

// MemoryCitation points at one operation offered as evidence for a claim.
type MemoryCitation struct {
	Topic string `json:"topic"`
	OpID  string `json:"op_id"`
}

// MemoryAnswerPayload is one witness's testimony: prose, citations (never inline
// exhibits — provenance is a deliberate follow-up fetch), and optionally the
// moment the witness's op-granularity memory starts (retention is not
// retrofittable, so the blind spot travels with the testimony).
type MemoryAnswerPayload struct {
	Answer       string           `json:"answer"`
	Citations    []MemoryCitation `json:"citations,omitempty"`
	CoverageFrom time.Time        `json:"coverage_from,omitzero"`
}

// MemoryFetchPayload is the memory.fetch request: exactly one operation, wanted
// back as an exhibit.
type MemoryFetchPayload struct {
	Topic    string    `json:"topic"`
	OpID     string    `json:"op_id"`
	Deadline time.Time `json:"deadline,omitzero"`
}

// MemoryExhibitPayload is a memory.exhibit reply: the requested operation as a
// self-authenticating exhibit from whatever store the witness keeps.
type MemoryExhibitPayload struct {
	Exhibit record.Exhibit `json:"exhibit"`
}
