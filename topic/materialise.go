package topic

import "context"

// Materialise drains the topic's ops backlog and returns the current view, updating the
// handle's observed frontier and lifecycle so subsequent posts parent correctly. It is a
// pure replay: two consumers replaying the same log produce the same view.
func (h *Handle) Materialise(ctx context.Context) (*MaterializedTopic, error) {
	recs, err := drainOps(ctx, h.client, h.path)
	if err != nil {
		return nil, err
	}

	// Verify against the wire form BEFORE manifest resolution rewrites the
	// baseline's payload — the signature covers the bytes that travelled.
	statuses := annotate(recs, h.client.RealmKey(), h.path, h.keyring)
	baselineID := ""
	if len(recs) > 0 {
		baselineID = recs[0].Record.ID
	}

	var mt *MaterializedTopic
	if rerr := resolveBaseline(ctx, h.client, recs); rerr != nil {
		// A manifest baseline that cannot be resolved makes the topic malformed
		// with a reason — reading never crashes and never shows partial state.
		mt = &MaterializedTopic{Path: h.path, Lifecycle: Proposed, Malformed: rerr.Error()}
	} else {
		mt = apply(h.path, recs)
		annotateView(mt, statuses, baselineID)
	}

	// Enrich the view with the topic's announcement (from its INFO subject).
	if ann, annRec, err := fetchAnnouncement(ctx, h.client, h.path); err == nil && ann != nil {
		ann.Sig = VerifyRecord(*annRec, h.client.RealmKey(), h.path, h.keyring)
		mt.Announcement = ann
	}

	h.adopt(mt)
	return mt, nil
}
