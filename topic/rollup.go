package topic

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// Rollup outcome errors.
var (
	// ErrRollupLost means a concurrent write invalidated the attempt: something
	// landed on the topic after the tail this rollup consumed. Nothing changed;
	// retry if compaction still matters.
	ErrRollupLost = errors.New("topic: rollup lost the race (someone wrote concurrently); try again")
	// ErrNothingToCompact means the log is already just a baseline.
	ErrNothingToCompact = errors.New("topic: nothing to compact (the log is already just a baseline)")
	// ErrTopicArchived means the topic is archived — terminal, writes are refused.
	// Always wrapped with the topic path by its return sites.
	ErrTopicArchived = errors.New("archived is terminal, writes are refused")
)

// Rollup compacts the topic: it folds the current baseline plus every operation
// since into a new baseline and publishes it as the atomic replacement of both —
// the server's per-subject rollup purges the predecessors in the same stroke.
//
// Race safety is optimistic: the publish demands that the last message on the
// subject still be the last op this rollup consumed. Any concurrent write rejects
// the attempt wholesale (ErrRollupLost) and the log is untouched — first writer
// wins, nothing to clean up. Rollup is an optimisation: a topic nobody compacts is
// a valid topic.
//
// The new baseline is an ordinary operation — signed when the client holds a key —
// and its payload carries the frontier, so later ops parent across the boundary as
// if the rollup never happened. On success the handle adopts that frontier and the
// new baseline op-id is returned.
func (h *Handle) Rollup(ctx context.Context) (string, error) {
	return h.rollup(ctx, false)
}

// rollup is Rollup with the archival exception: Archive's final compaction must run
// on the topic it just archived, which every other caller is refused.
func (h *Handle) rollup(ctx context.Context, allowArchived bool) (string, error) {
	recs, err := drainOps(ctx, h.client, h.path)
	if err != nil {
		return "", err
	}
	// Capture the current baseline's manifest objects before resolution rewrites
	// its payload: they become garbage once this rollup commits (whatever form the
	// new baseline takes).
	superseded := manifestChunksOf(recs)
	if err := resolveBaseline(ctx, h.client, recs); err != nil {
		return "", fmt.Errorf("topic: refusing to compact: %s", err)
	}
	mt := apply(h.path, recs)
	if mt.Malformed != "" {
		return "", fmt.Errorf("topic: refusing to compact a malformed topic: %s", mt.Malformed)
	}
	if mt.Lifecycle == Archived && !allowArchived {
		return "", fmt.Errorf("topic: %s is archived — %w", h.path, ErrTopicArchived)
	}
	if len(recs) <= 1 {
		return "", ErrNothingToCompact
	}

	payload := BaselinePayload{
		State:    mt.BaselineState,
		Frontier: mt.Frontier,
		Baked: &BakedState{
			Contributions: cleanBakedContributions(mt.Contributions),
			Attachments:   cleanBakedAttachments(mt.Attachments),
			WorkItems:     cleanBakedWorkItems(mt.WorkItems),
			Lifecycle:     mt.Lifecycle,
		},
	}

	lastSeq := recs[len(recs)-1].StreamSeq
	opID, err := publishBaseline(ctx, h, payload, mt.Frontier, lastSeq)
	if err != nil {
		return "", err
	}

	// Happy-path cleanup: the superseded baseline's manifest objects are garbage
	// now. Best effort — a failure leaves sweepable orphans, nothing more.
	if len(superseded) > 0 {
		if store, serr := h.client.JetStream().ObjectStore(ctx, realm.ObjectBucket); serr == nil {
			for _, name := range superseded {
				_ = store.Delete(ctx, name)
			}
		}
	}

	h.frontier = payload.Frontier
	h.lifecycle = mt.Lifecycle
	return opID, nil
}

// manifestChunksOf returns the object names the log's current baseline references,
// or nil when it is inline or absent.
func manifestChunksOf(recs []SeqRecord) []string {
	if len(recs) == 0 || recs[0].Record.Type != TypeBaseline {
		return nil
	}
	var bp BaselinePayload
	if json.Unmarshal(recs[0].Record.Payload, &bp) != nil || bp.Manifest == nil {
		return nil
	}
	return bp.Manifest.Chunks
}

// publishBaseline publishes a rollup baseline (inline or, when the state document
// exceeds the inline threshold, as a manifest) with the rollup header and the
// expected-last-subject-sequence guard.
func publishBaseline(ctx context.Context, h *Handle, payload BaselinePayload, frontier []string, lastSeq uint64) (string, error) {
	doc, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("topic: encode baseline: %w", err)
	}
	if len(doc) > InlineBaselineThreshold {
		return publishManifestBaseline(ctx, h, payload, frontier, lastSeq)
	}

	opID, err := publishOpWith(ctx, h.client, OpsSubject(h.path), TypeBaseline, payload, frontier, "",
		map[string]string{jetstream.MsgRollup: jetstream.MsgRollupSubject},
		[]jetstream.PublishOpt{jetstream.WithExpectLastSequencePerSubject(lastSeq)},
	)
	if err != nil {
		if isWrongLastSequence(err) {
			return "", ErrRollupLost
		}
		return "", err
	}
	return opID, nil
}

// stateDoc is the manifest object's content: the parts of an inline baseline that
// moved to the object store — byte-for-byte the {state, baked} document.
type stateDoc struct {
	State json.RawMessage `json:"state,omitempty"`
	Baked *BakedState     `json:"baked,omitempty"`
}

// publishManifestBaseline handles the over-threshold form with the crash-safe write
// order: put the state document as one object (named after the pre-generated
// baseline op-id), publish the manifest as the atomic commit point under the same
// guard, then delete the superseded baseline's objects. A crash or lost race before
// the publish leaves the old log intact plus a harmless orphaned object.
func publishManifestBaseline(ctx context.Context, h *Handle, payload BaselinePayload, frontier []string, lastSeq uint64) (string, error) {
	doc, err := json.Marshal(stateDoc{State: payload.State, Baked: payload.Baked})
	if err != nil {
		return "", fmt.Errorf("topic: encode state document: %w", err)
	}

	store, err := h.client.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		return "", fmt.Errorf("topic: open object store: %w", err)
	}

	// 1. Chunks first. Named after the baseline op-id that will commit them.
	baselineID := record.NewID()
	object := "baseline/" + h.path + "/" + baselineID
	if _, err := store.PutBytes(ctx, object, doc); err != nil {
		return "", fmt.Errorf("topic: store baseline state: %w", err)
	}

	// 2. The manifest publish is the commit point.
	manifest := BaselinePayload{
		Frontier: payload.Frontier,
		Manifest: &ManifestRef{Chunks: []string{object}, Digest: digestOf(doc), Size: uint64(len(doc))},
	}
	opID, err := publishOpWith(ctx, h.client, OpsSubject(h.path), TypeBaseline, manifest, frontier, baselineID,
		map[string]string{jetstream.MsgRollup: jetstream.MsgRollupSubject},
		[]jetstream.PublishOpt{jetstream.WithExpectLastSequencePerSubject(lastSeq)},
	)
	if err != nil {
		if isWrongLastSequence(err) {
			return "", ErrRollupLost // the put object is now an orphan: garbage, never corruption
		}
		return "", err
	}
	return opID, nil
}

// resolveBaseline rewrites a manifest baseline (always the first record) into its
// inline form before folding: fetch the chunks in order, verify the digest, and
// substitute the state document. Any failure is a malformation of the topic — the
// caller reports it as such; it must never crash a read or show partial state.
func resolveBaseline(ctx context.Context, c *realm.Client, recs []SeqRecord) error {
	if len(recs) == 0 || recs[0].Record.Type != TypeBaseline {
		return nil
	}
	var bp BaselinePayload
	if err := json.Unmarshal(recs[0].Record.Payload, &bp); err != nil || bp.Manifest == nil {
		return nil // inline (or unparseable, which apply reports on its own)
	}

	store, err := c.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		return fmt.Errorf("manifest baseline unreadable: open object store: %v", err)
	}
	var doc []byte
	for _, name := range bp.Manifest.Chunks {
		part, err := store.GetBytes(ctx, name)
		if err != nil {
			return fmt.Errorf("manifest baseline unreadable: chunk %q: %v", name, err)
		}
		doc = append(doc, part...)
	}
	if !VerifyDigest(doc, bp.Manifest.Digest) {
		return fmt.Errorf("manifest baseline corrupt: state document does not match its digest")
	}

	var sd stateDoc
	if err := json.Unmarshal(doc, &sd); err != nil {
		return fmt.Errorf("manifest baseline corrupt: state document is not valid JSON: %v", err)
	}
	inline, err := json.Marshal(BaselinePayload{State: sd.State, Frontier: bp.Frontier, Baked: sd.Baked})
	if err != nil {
		return fmt.Errorf("manifest baseline unreadable: %v", err)
	}
	recs[0].Record.Payload = inline
	return nil
}

// digestOf computes the object-store digest form over data ("SHA-256=<base64url>"),
// matching VerifyDigest.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "SHA-256=" + base64.URLEncoding.EncodeToString(sum[:])
}

// isWrongLastSequence reports whether err is the server rejecting the
// expected-last-subject-sequence guard — the rollup's lost race.
func isWrongLastSequence(err error) bool {
	var apiErr *jetstream.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence
}

// cleanBakedContributions strips the fields baked state never stores: stream
// sequences die with the compacted tail, and sig statuses are recomputed at read
// time (baked elements inherit the baseline's).
func cleanBakedContributions(cs []Contribution) []Contribution {
	out := make([]Contribution, len(cs))
	copy(out, cs)
	for i := range out {
		out[i].StreamSeq = 0
		out[i].Sig = ""
		out[i].Dangling = false // derived at read time
		if len(out[i].Edits) > 0 {
			// Stamps are history (they keep edit chains joinable across the
			// compaction); only their volatile fields die with the tail.
			es := make([]EditStamp, len(out[i].Edits))
			copy(es, out[i].Edits)
			for j := range es {
				es[j].StreamSeq = 0
				es[j].Sig = ""
			}
			out[i].Edits = es
		}
	}
	return out
}

func cleanBakedAttachments(as []Attachment) []Attachment {
	out := make([]Attachment, len(as))
	copy(out, as)
	for i := range out {
		out[i].StreamSeq = 0
		out[i].Sig = ""
		out[i].Dangling = false
	}
	return out
}

// cleanBakedWorkItems strips the volatile fields from items and their timelines.
// Void flags are kept: a lost claim is history, not volatility.
func cleanBakedWorkItems(ws []WorkItem) []WorkItem {
	out := make([]WorkItem, len(ws))
	copy(out, ws)
	for i := range out {
		out[i].StreamSeq = 0
		out[i].Sig = ""
		if len(out[i].Timeline) > 0 {
			tl := make([]WorkEvent, len(out[i].Timeline))
			copy(tl, out[i].Timeline)
			for j := range tl {
				tl[j].StreamSeq = 0
				tl[j].Sig = ""
			}
			out[i].Timeline = tl
		}
	}
	return out
}
