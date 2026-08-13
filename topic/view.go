package topic

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/impire-io/soulstream-core/record"
)

// Announcement is a topic's info metadata.
//
// The JSON tags on the view structs are wire contract: baked state inside rollup
// baselines serialises these exact shapes, so the keys are pinned here, not left to
// Go's default casing.
type Announcement struct {
	OpID          string    `json:"op_id,omitempty"` // the announce op's id ("" pre-015 baked views)
	TopicID       string    `json:"topic_id"`
	Name          string    `json:"name"`
	SubjectMatter string    `json:"subject_matter,omitempty"`
	Parent        string    `json:"parent,omitempty"`
	Expected      []string  `json:"expected,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	Sig           SigStatus `json:"sig,omitempty"` // verification status of the announce op
}

// EditStamp records one applied edit on a contribution: the chain that lets a
// later edit anchor a compacted predecessor, and the visible "edited" trail.
type EditStamp struct {
	OpID      string    `json:"op_id"`
	Author    string    `json:"author"` // always the contribution's author (the same-author rule)
	Ts        time.Time `json:"ts"`
	Sig       SigStatus `json:"sig,omitempty"`        // volatile; recomputed at read
	StreamSeq uint64    `json:"stream_seq,omitempty"` // volatile; 0 for baked stamps
}

// Contribution is a materialised turn, comment, or reply. An edited contribution
// renders the newest applied edit's body and mentions; its Edits trail says so.
type Contribution struct {
	OpID       string      `json:"op_id"`
	Author     string      `json:"author"`
	Timestamp  time.Time   `json:"ts"`
	Type       string      `json:"type"` // TypeTurnPost | TypeCommentAdd | TypeCommentReply
	Body       string      `json:"body"`
	Mentions   []string    `json:"mentions,omitempty"`    // persona names mentioned in the body
	Anchor     string      `json:"anchor,omitempty"`      // comment/reply anchored op-id ("" for turns)
	Dangling   bool        `json:"dangling,omitempty"`    // anchor not present in the topic
	Resolved   bool        `json:"resolved,omitempty"`    // comment.resolve mark — closed, never deleted
	ResolvedBy string      `json:"resolved_by,omitempty"` // first resolver
	ResolvedTs time.Time   `json:"resolved_ts,omitzero"`  // when — counts as topic activity
	Edits      []EditStamp `json:"edits,omitempty"`       // applied edits, stream order
	Sig        SigStatus   `json:"sig,omitempty"`         // verification status of this op's signature
	StreamSeq  uint64      `json:"stream_seq,omitempty"`  // 0 for elements baked into a baseline
}

// Attachment is a materialised attachment.add — a reference to a blob in the object store.
// A removed attachment is withdrawn, not erased: the mark is visible, the bytes stay
// fetchable until the topic is archived (the one act that reclaims withdrawn blobs).
type Attachment struct {
	OpID        string    `json:"op_id"`
	Author      string    `json:"author"`
	Timestamp   time.Time `json:"ts"`
	Name        string    `json:"name"`
	Object      string    `json:"object"`
	Digest      string    `json:"digest"`
	Size        uint64    `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	Anchor      string    `json:"anchor,omitempty"`
	Dangling    bool      `json:"dangling,omitempty"`
	Removed     bool      `json:"removed,omitempty"`
	RemovedBy   string    `json:"removed_by,omitempty"`
	RemovedTs   time.Time `json:"removed_ts,omitzero"`
	Sig         SigStatus `json:"sig,omitempty"`
	StreamSeq   uint64    `json:"stream_seq,omitempty"`
}

// MaterializedTopic is the pure projection of a topic's op-log.
type MaterializedTopic struct {
	Path          string          `json:"path"`
	Announcement  *Announcement   `json:"announcement,omitempty"`
	BaselineState json.RawMessage `json:"baseline_state,omitempty"`
	// BaselineTs is the baseline op's (author-claimed) timestamp: the topic's birth
	// time, or — after a rollup — the compaction time. Informational, like every
	// timestamp; useful as "when did this topic's current zero-point happen".
	BaselineTs time.Time `json:"baseline_ts,omitempty"`
	// BaselineID is the current baseline op's id — the topic's zero-point checkpoint.
	BaselineID    string         `json:"baseline_id,omitempty"`
	Lifecycle     Lifecycle      `json:"lifecycle"`
	Contributions []Contribution `json:"contributions,omitempty"`
	Attachments   []Attachment   `json:"attachments,omitempty"`
	WorkItems     []WorkItem     `json:"work_items,omitempty"`
	Frontier      []string       `json:"frontier"`            // leaf op-ids
	Malformed     string         `json:"malformed,omitempty"` // non-empty reason if the log has no usable baseline
	Warnings      []string       `json:"warnings,omitempty"`  // e.g. ignored unknown op types
}

// SeqRecord pairs a record with its JetStream stream sequence — the ordering key.
type SeqRecord struct {
	Record    record.Record
	StreamSeq uint64
}

// apply folds an ordered sequence of records (already sorted by stream sequence) into a
// materialised view. It is a pure function of the log: the same input always yields the
// same output.
func apply(path string, recs []SeqRecord) *MaterializedTopic {
	mt := &MaterializedTopic{Path: path, Lifecycle: Proposed}

	if len(recs) == 0 {
		mt.Malformed = "topic has no operations (no baseline)"
		return mt
	}
	if recs[0].Record.Type != TypeBaseline {
		mt.Malformed = "first operation is not a baseline (got " + recs[0].Record.Type + ")"
		return mt
	}

	// The baseline: its state, whatever a rollup baked in, and the DAG bookkeeping.
	mt.BaselineTs = recs[0].Record.Timestamp
	mt.BaselineID = recs[0].Record.ID
	var bp BaselinePayload
	if err := json.Unmarshal(recs[0].Record.Payload, &bp); err == nil {
		mt.BaselineState = bp.State
	}
	seen := map[string]bool{recs[0].Record.ID: true}
	referenced := map[string]bool{}
	for _, p := range recs[0].Record.Parents {
		referenced[p] = true
	}

	// Seed from the baked conversation (present after a rollup). Baked op-ids stay
	// anchor-resolvable; the ones not on the frontier are interior — already built
	// upon — so they must never resurface as frontier leaves.
	workIdx := map[string]int{}    // item id → index in mt.WorkItems
	editTarget := map[string]int{} // contribution op-id (or applied-edit op-id) → index
	attIdx := map[string]int{}     // attachment op-id → index in mt.Attachments
	if bp.Baked != nil {
		mt.Contributions = append(mt.Contributions, bp.Baked.Contributions...)
		mt.Attachments = append(mt.Attachments, bp.Baked.Attachments...)
		mt.WorkItems = append(mt.WorkItems, bp.Baked.WorkItems...)
		if bp.Baked.Lifecycle != "" {
			mt.Lifecycle = bp.Baked.Lifecycle
		}
		for i, c := range mt.Contributions {
			editTarget[c.OpID] = i
			seen[c.OpID] = true
			referenced[c.OpID] = true
			// Baked edit stamps keep the chain joinable: a post-rollup edit may
			// anchor an edit op-id the compaction consumed.
			for _, e := range c.Edits {
				editTarget[e.OpID] = i
				seen[e.OpID] = true
				referenced[e.OpID] = true
			}
		}
		for i, a := range mt.Attachments {
			attIdx[a.OpID] = i
			seen[a.OpID] = true
			referenced[a.OpID] = true
		}
		for i, w := range mt.WorkItems {
			workIdx[w.ID] = i
			seen[w.ID] = true
			referenced[w.ID] = true
			for _, ev := range w.Timeline {
				seen[ev.OpID] = true
				referenced[ev.OpID] = true
			}
		}
	}

	// Frontier continuity: a non-empty payload frontier names the leaves the topic
	// continues from (the baseline op itself becomes a checkpoint, not a leaf). An
	// empty frontier is birth: the baseline op-id is the sole leaf, as always.
	if len(bp.Frontier) > 0 {
		referenced[recs[0].Record.ID] = true
		for _, id := range bp.Frontier {
			seen[id] = true
			delete(referenced, id)
		}
	}

	contentOps := 0
	// content marks one applied content op: it counts toward proposed→active and
	// wakes a dormant topic on the spot (reactivation is order-sensitive — only
	// content after the dormant mark wakes it).
	content := func() {
		contentOps++
		if mt.Lifecycle == Dormant {
			mt.Lifecycle = Active
		}
	}
	for _, sr := range recs[1:] {
		r := sr.Record
		seen[r.ID] = true
		for _, p := range r.Parents {
			referenced[p] = true
		}

		switch r.Type {
		case TypeTurnPost:
			content()
			var tp TurnPayload
			_ = json.Unmarshal(r.Payload, &tp)
			editTarget[r.ID] = len(mt.Contributions)
			mt.Contributions = append(mt.Contributions, Contribution{
				OpID: r.ID, Author: r.Author, Timestamp: r.Timestamp, Type: r.Type,
				Body: tp.Body, Mentions: tp.Mentions, StreamSeq: sr.StreamSeq,
			})
		case TypeCommentAdd, TypeCommentReply:
			content()
			var cp CommentPayload
			_ = json.Unmarshal(r.Payload, &cp)
			editTarget[r.ID] = len(mt.Contributions)
			mt.Contributions = append(mt.Contributions, Contribution{
				OpID: r.ID, Author: r.Author, Timestamp: r.Timestamp, Type: r.Type,
				Body: cp.Body, Mentions: cp.Mentions, Anchor: cp.Anchor.OpID, StreamSeq: sr.StreamSeq,
			})
		case TypeEdit:
			var cp CommentPayload
			if err := json.Unmarshal(r.Payload, &cp); err != nil || strings.TrimSpace(cp.Body) == "" || cp.Anchor.OpID == "" {
				mt.Warnings = append(mt.Warnings, "ignored malformed edit "+r.ID+" (needs a body and a target)")
				continue
			}
			idx, ok := editTarget[cp.Anchor.OpID]
			if !ok {
				mt.Warnings = append(mt.Warnings, "edit "+r.ID+" targets an unknown or non-editable op "+cp.Anchor.OpID+" — ignored")
				continue
			}
			c := &mt.Contributions[idx]
			if c.Author != r.Author {
				mt.Warnings = append(mt.Warnings, "ignored edit "+r.ID+" by "+r.Author+" of "+c.OpID+" — only the author may edit their words")
				continue
			}
			content()
			c.Body = cp.Body
			c.Mentions = cp.Mentions
			c.Edits = append(c.Edits, EditStamp{OpID: r.ID, Author: r.Author, Ts: r.Timestamp, StreamSeq: sr.StreamSeq})
			editTarget[r.ID] = idx // later edits may anchor this one — same chain
		case TypeCommentResolve:
			var rp RefPayload
			if err := json.Unmarshal(r.Payload, &rp); err != nil || rp.Anchor == nil || rp.Anchor.OpID == "" {
				mt.Warnings = append(mt.Warnings, "ignored malformed comment.resolve "+r.ID+" (missing target)")
				continue
			}
			idx, ok := editTarget[rp.Anchor.OpID]
			if !ok || mt.Contributions[idx].Type == TypeTurnPost {
				mt.Warnings = append(mt.Warnings, "comment.resolve "+r.ID+" targets "+rp.Anchor.OpID+" which is not a comment — ignored")
				continue
			}
			c := &mt.Contributions[idx]
			if c.Resolved {
				continue // idempotent: the mark stands, duplicates are harmless
			}
			content()
			c.Resolved = true
			c.ResolvedBy = r.Author
			c.ResolvedTs = r.Timestamp
		case TypeAttachmentAdd:
			content()
			var ap AttachmentPayload
			_ = json.Unmarshal(r.Payload, &ap)
			attIdx[r.ID] = len(mt.Attachments)
			mt.Attachments = append(mt.Attachments, Attachment{
				OpID: r.ID, Author: r.Author, Timestamp: r.Timestamp,
				Name: ap.Name, Object: ap.Object, Digest: ap.Digest, Size: ap.Size,
				ContentType: ap.ContentType, Anchor: ap.Anchor, StreamSeq: sr.StreamSeq,
			})
		case TypeAttachmentRemove:
			var rp RefPayload
			if err := json.Unmarshal(r.Payload, &rp); err != nil || rp.Anchor == nil || rp.Anchor.OpID == "" {
				mt.Warnings = append(mt.Warnings, "ignored malformed attachment.remove "+r.ID+" (missing target)")
				continue
			}
			idx, ok := attIdx[rp.Anchor.OpID]
			if !ok {
				mt.Warnings = append(mt.Warnings, "attachment.remove "+r.ID+" targets "+rp.Anchor.OpID+" which is not an attachment — ignored")
				continue
			}
			a := &mt.Attachments[idx]
			if a.Removed {
				continue // idempotent
			}
			content()
			a.Removed = true
			a.RemovedBy = r.Author
			a.RemovedTs = r.Timestamp
		case TypeWorkOpen:
			var wp WorkOpenPayload
			if err := json.Unmarshal(r.Payload, &wp); err != nil || strings.TrimSpace(wp.Title) == "" {
				mt.Warnings = append(mt.Warnings, "ignored malformed work.open "+r.ID+" (missing title)")
				continue
			}
			content()
			workIdx[r.ID] = len(mt.WorkItems)
			mt.WorkItems = append(mt.WorkItems, WorkItem{
				ID: r.ID, Author: r.Author, Timestamp: r.Timestamp,
				Title: wp.Title, Body: wp.Body, Mentions: wp.Mentions,
				Status: WorkOpen, StreamSeq: sr.StreamSeq,
			})
		case TypeWorkClaim, TypeWorkDone, TypeWorkAbandon:
			var rp WorkRefPayload
			if err := json.Unmarshal(r.Payload, &rp); err != nil || rp.Anchor == nil || rp.Anchor.OpID == "" {
				mt.Warnings = append(mt.Warnings, "ignored malformed "+r.Type+" "+r.ID+" (missing item anchor)")
				continue
			}
			content()
			idx, ok := workIdx[rp.Anchor.OpID]
			if !ok {
				mt.Warnings = append(mt.Warnings, r.Type+" "+r.ID+" references unknown work item "+rp.Anchor.OpID+" — void")
				continue
			}
			item := &mt.WorkItems[idx]
			ev := WorkEvent{
				OpID: r.ID, Kind: workEventKind(r.Type), Author: r.Author,
				Timestamp: r.Timestamp, StreamSeq: sr.StreamSeq,
			}
			// The state machine, not the author, decides. The fold's in-order
			// traversal is the arbiter: the first claim that finds the item open
			// wins, and everything the machine rejects stays visible as void.
			switch r.Type {
			case TypeWorkClaim:
				if item.Status == WorkOpen {
					item.Status = WorkClaimed
					item.Owner = r.Author
				} else {
					ev.Void = true
				}
			case TypeWorkDone:
				if item.Status == WorkDone {
					ev.Void = true
				} else {
					item.Status = WorkDone
				}
			case TypeWorkAbandon:
				if item.Status == WorkClaimed {
					item.Status = WorkOpen
					item.Owner = ""
				} else {
					ev.Void = true
				}
			}
			item.Timeline = append(item.Timeline, ev)
		case TypeLifeTransition:
			if mt.Lifecycle == Archived {
				mt.Warnings = append(mt.Warnings, "ignored transition after archived (archived is terminal)")
				continue
			}
			var lp TransitionPayload
			if err := json.Unmarshal(r.Payload, &lp); err == nil {
				switch lp.To {
				case Closed:
					mt.Lifecycle = Closed
				case Archived:
					mt.Lifecycle = Archived
				case Dormant:
					// Closed rests already; dormancy is for topics still in play.
					if mt.Lifecycle == Closed {
						mt.Warnings = append(mt.Warnings, "ignored dormant transition on a closed topic")
					} else {
						mt.Lifecycle = Dormant
					}
				}
			}
		case TypeBaseline:
			// A live follower that retained pre-rollup history sees the landed
			// rollup as its next message: a checkpoint whose content this view
			// already holds. Skip its content, but keep the frontier consistent
			// with a cold read: the checkpoint itself is never a leaf, and the
			// leaves it recorded stay the topic's frontier.
			referenced[r.ID] = true
			var cp BaselinePayload
			if json.Unmarshal(r.Payload, &cp) == nil {
				for _, id := range cp.Frontier {
					seen[id] = true
					delete(referenced, id)
				}
			}
			mt.Warnings = append(mt.Warnings, "observed a rollup checkpoint mid-log (view already contains its content)")
		default:
			mt.Warnings = append(mt.Warnings, "ignored unknown op type: "+r.Type)
		}
	}

	// Lifecycle: closed/archived win; otherwise active once there is content.
	if mt.Lifecycle == Proposed && contentOps > 0 {
		mt.Lifecycle = Active
	}

	// Flag comments, replies, and attachments whose anchor op-id is not present
	// in the topic (turns carry no anchor).
	for i := range mt.Contributions {
		c := &mt.Contributions[i]
		if c.Anchor != "" && !seen[c.Anchor] {
			c.Dangling = true
		}
	}
	for i := range mt.Attachments {
		a := &mt.Attachments[i]
		if a.Anchor != "" && !seen[a.Anchor] {
			a.Dangling = true
		}
	}

	// Frontier = observed op-ids minus those referenced as some op's parent.
	for id := range seen {
		if !referenced[id] {
			mt.Frontier = append(mt.Frontier, id)
		}
	}
	sort.Strings(mt.Frontier)

	return mt
}
