package topic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// BoardEntry is one topic on the discovery board.
type BoardEntry struct {
	Path         string       `json:"path"`
	Announcement Announcement `json:"announcement"`
	Parent       string       `json:"parent,omitempty"`
	ParentKnown  bool         `json:"parent_known"`
	Lifecycle    Lifecycle    `json:"lifecycle,omitempty"`
}

// Board replays the realm's info board and returns one entry per topic — the latest
// announcement per info subject. An empty realm yields an empty board, not an error. A
// sub-topic whose parent is absent is flagged (ParentKnown == false), never dropped.
func Board(ctx context.Context, c *realm.Client) ([]BoardEntry, error) {
	stream, err := c.JetStream().Stream(ctx, realm.StreamName)
	if err != nil {
		return nil, fmt.Errorf("topic: look up stream: %w", err)
	}

	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(InfoSubjectWildcard))
	if err != nil {
		return nil, fmt.Errorf("topic: stream info: %w", err)
	}

	paths := make([]string, 0, len(info.State.Subjects))
	known := make(map[string]bool, len(info.State.Subjects))
	for subj := range info.State.Subjects {
		path := strings.TrimPrefix(subj, InfoSubjectPrefix)
		paths = append(paths, path)
		known[path] = true
	}
	sort.Strings(paths)

	entries := make([]BoardEntry, 0, len(paths))
	for _, path := range paths {
		ann, _, err := fetchAnnouncement(ctx, c, path)
		if err != nil {
			return nil, err
		}
		if ann == nil {
			continue // no (or malformed) announcement — skip rather than fail the board
		}

		parent := ParentPath(path)
		entry := BoardEntry{
			Path:         path,
			Announcement: *ann,
			Parent:       parent,
			ParentKnown:  parent == "" || known[parent],
		}

		// Lifecycle where derivable: materialise the topic's ops (resolving a
		// manifest baseline first — its lifecycle lives in the state document).
		if recs, err := drainOps(ctx, c, path); err == nil {
			if rerr := resolveBaseline(ctx, c, recs); rerr == nil {
				entry.Lifecycle = apply(path, recs).Lifecycle
			}
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// fetchAnnouncement returns the latest announcement for a topic-path from its INFO
// subject — plus the wire record it came from, so callers can verify its signature —
// or nils (no error) when there is none or it is malformed.
func fetchAnnouncement(ctx context.Context, c *realm.Client, path string) (*Announcement, *record.Record, error) {
	stream, err := c.JetStream().Stream(ctx, realm.StreamName)
	if err != nil {
		return nil, nil, fmt.Errorf("topic: look up stream: %w", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, InfoSubject(path))
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("topic: last announce for %s: %w", path, err)
	}
	rec, err := record.Parse(raw.Header, raw.Data)
	if err != nil {
		return nil, nil, nil
	}
	var ap AnnouncePayload
	if json.Unmarshal(rec.Payload, &ap) != nil {
		return nil, nil, nil
	}
	return &Announcement{
		OpID:          rec.ID,
		TopicID:       ap.TopicID,
		Name:          ap.Name,
		SubjectMatter: ap.SubjectMatter,
		Parent:        ap.Parent,
		Expected:      ap.Expected,
		Tags:          ap.Tags,
	}, &rec, nil
}
