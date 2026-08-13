package curator

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

// cachedTopic is one topic as the curator knows it.
type cachedTopic struct {
	view       *topic.MaterializedTopic
	entry      topic.DiscoverEntry
	searchText string
	lastReal   time.Time // newest non-suggestion activity; never before birth
	lastAny    time.Time // newest op of any kind — core's dormancy clock
	birth      time.Time // BaselineTs
	malformed  bool      // skip-marker: never matched, never commented on
}

// projection is the curator's warm view of the realm's topics: a cache of
// materialised views seeded from the board, invalidated by one core subscription on
// the topic subjects, re-materialised lazily. A missed signal can only delay
// freshness (the next refresh sweeps dirty paths), never corrupt.
type projection struct {
	c *realm.Client

	mu      sync.Mutex
	entries map[string]*cachedTopic
	dirty   map[string]bool

	sub *nats.Subscription
}

// newProjection subscribes for liveness signals FIRST, then seeds from the board —
// that order closes the startup race (a message between seed and subscribe would
// otherwise leave silent staleness).
func newProjection(ctx context.Context, c *realm.Client) (*projection, error) {
	p := &projection{
		c:       c,
		entries: map[string]*cachedTopic{},
		dirty:   map[string]bool{},
	}

	sub, err := c.Conn().Subscribe("SOULSTREAM.TOPICS.>", func(msg *nats.Msg) {
		path := pathOfTopicSubject(msg.Subject)
		if path == "" {
			return
		}
		p.mu.Lock()
		p.dirty[path] = true
		p.mu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	p.sub = sub
	// Flush before seeding: once the server has registered the interest, nothing
	// published after this point can slip between seed and subscription.
	if err := c.Conn().Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}

	entries, err := topic.Board(ctx, c)
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	p.mu.Lock()
	for _, e := range entries {
		p.dirty[e.Path] = true
	}
	p.mu.Unlock()
	if err := p.refresh(ctx); err != nil {
		_ = sub.Unsubscribe()
		return nil, err
	}
	return p, nil
}

func (p *projection) close() {
	if p.sub != nil {
		_ = p.sub.Unsubscribe()
	}
}

// pathOfTopicSubject extracts the topic path from an OPS or INFO subject.
func pathOfTopicSubject(subject string) string {
	if strings.HasPrefix(subject, topic.OpsSubjectPrefix) {
		return strings.TrimPrefix(subject, topic.OpsSubjectPrefix)
	}
	if strings.HasPrefix(subject, topic.InfoSubjectPrefix) {
		return strings.TrimPrefix(subject, topic.InfoSubjectPrefix)
	}
	return ""
}

// refresh re-materialises every dirty path.
func (p *projection) refresh(ctx context.Context) error {
	p.mu.Lock()
	paths := make([]string, 0, len(p.dirty))
	for path := range p.dirty {
		paths = append(paths, path)
	}
	p.dirty = map[string]bool{}
	p.mu.Unlock()

	for _, path := range paths {
		view, err := topic.Open(p.c, path).Materialise(ctx)
		if err != nil {
			// Transient read failure: keep whatever we had, try again next sweep.
			p.mu.Lock()
			p.dirty[path] = true
			p.mu.Unlock()
			continue
		}
		p.mu.Lock()
		p.entries[path] = buildCached(path, view)
		p.mu.Unlock()
	}
	return ctx.Err()
}

// buildCached derives everything the habits need from one materialised view.
func buildCached(path string, view *topic.MaterializedTopic) *cachedTopic {
	ct := &cachedTopic{view: view, birth: view.BaselineTs, malformed: view.Malformed != ""}
	if ct.malformed {
		return ct
	}

	entry := topic.DiscoverEntry{Path: path, Lifecycle: view.Lifecycle}
	if view.Announcement != nil {
		entry.Name = view.Announcement.Name
		entry.SubjectMatter = view.Announcement.SubjectMatter
		entry.Tags = view.Announcement.Tags
	}
	ct.entry = entry
	ct.searchText = searchText(entry, view)

	// Last real activity: the newest thing that is not a curator suggestion.
	// Curator chatter keeps nothing alive.
	ct.lastReal = view.BaselineTs
	for _, c := range view.Contributions {
		if IsSuggestion(c.Body) {
			continue
		}
		if c.Timestamp.After(ct.lastReal) {
			ct.lastReal = c.Timestamp
		}
	}
	for _, a := range view.Attachments {
		if a.Timestamp.After(ct.lastReal) {
			ct.lastReal = a.Timestamp
		}
	}
	// Work items are ordinary content: opening, claiming, finishing, or abandoning
	// a task is real activity, never curator chatter.
	for _, w := range view.WorkItems {
		if w.Timestamp.After(ct.lastReal) {
			ct.lastReal = w.Timestamp
		}
		for _, ev := range w.Timeline {
			if ev.Timestamp.After(ct.lastReal) {
				ct.lastReal = ev.Timestamp
			}
		}
	}
	// lastAny is core's dormancy clock: the newest op of ANY kind, curator
	// chatter included (a suggestion defers dormancy one window at most).
	ct.lastAny = topic.NewestOpTs(view)
	return ct
}

// search answers a discovery query from the cache: identity fields plus what was
// said inside the topics.
func (p *projection) search(query string, limit int) []topic.DiscoverEntry {
	if limit <= 0 {
		limit = topic.DefaultDiscoverLimit
	}
	q := strings.ToLower(strings.TrimSpace(query))

	p.mu.Lock()
	defer p.mu.Unlock()
	var out []topic.DiscoverEntry
	for _, ct := range p.entries {
		if ct.malformed {
			continue
		}
		if q != "" && !strings.Contains(ct.searchText, q) {
			continue
		}
		out = append(out, ct.entry)
		if len(out) == limit {
			break
		}
	}
	return out
}

// snapshot returns the cached topics for a scan pass.
func (p *projection) snapshot() []*cachedTopic {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*cachedTopic, 0, len(p.entries))
	for _, ct := range p.entries {
		out = append(out, ct)
	}
	return out
}
