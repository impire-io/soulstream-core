package topic

import (
	"crypto/rand"
	"strings"

	"github.com/impire-io/soulstream-core/identity"
)

// Subject prefixes and wildcards for the topic taxonomy.
const (
	OpsSubjectPrefix    = "SOULSTREAM.TOPICS.OPS."
	InfoSubjectPrefix   = "SOULSTREAM.TOPICS.INFO."
	InfoSubjectWildcard = "SOULSTREAM.TOPICS.INFO.>"
	// SvcSubjectPrefix is the request-reply service namespace: core NATS
	// request-reply, never stored state.
	SvcSubjectPrefix = "SOULSTREAM.SVC."
	// SvcDiscoverSubject is where topic.discover requests are published.
	SvcDiscoverSubject = SvcSubjectPrefix + "DISCOVER"
	// SvcMemorySubject is where memory.query and memory.fetch requests are published.
	SvcMemorySubject = SvcSubjectPrefix + "MEMORY"
)

// OpsSubject returns the ops subject for a topic-path.
func OpsSubject(path string) string { return OpsSubjectPrefix + path }

// InfoSubject returns the info subject for a topic-path.
func InfoSubject(path string) string { return InfoSubjectPrefix + path }

// canonicalBinding returns the topic value bound into an op's canonical record for
// the subject it is published on: the topic path for ops/info subjects, the persona
// name for notify subjects. The rule is deliberately derivable from the subject alone,
// so any reader can recompute the signing input from the subject it consumed the op
// on. An unknown subject shape returns "" (canonicalisation will refuse it).
func canonicalBinding(subject string) string {
	switch {
	case strings.HasPrefix(subject, OpsSubjectPrefix):
		return strings.TrimPrefix(subject, OpsSubjectPrefix)
	case strings.HasPrefix(subject, InfoSubjectPrefix):
		return strings.TrimPrefix(subject, InfoSubjectPrefix)
	case strings.HasPrefix(subject, NotifySubjectPrefix):
		return strings.TrimPrefix(subject, NotifySubjectPrefix)
	case strings.HasPrefix(subject, SvcSubjectPrefix):
		// Service messages bind to the service name. Replies travel on ephemeral
		// inboxes but sign over the same service binding — see the discovery code.
		return strings.TrimPrefix(subject, SvcSubjectPrefix)
	default:
		return ""
	}
}

// ChildPath joins a parent topic-path and a child topic-id ("" parent → top-level).
func ChildPath(parent, childID string) string {
	if parent == "" {
		return childID
	}
	return parent + "." + childID
}

// ParentPath returns the parent portion of a topic-path, or "" if it is top-level.
func ParentPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[:i]
	}
	return ""
}

// IDFromPath returns this topic's own id (the last segment of the path).
func IDFromPath(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

const (
	idSuffixLen = 4
	idAlphabet  = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// NewTopicID builds a topic-id from a display name: a slug of the name plus a 4-char
// random suffix. The result always satisfies the foundation's slug grammar, so it is a
// valid path segment, and the suffix makes coordination-free uniqueness overwhelmingly
// likely without a registry.
func NewTopicID(name string) string {
	slug := slugify(name)
	if slug == "" {
		slug = "topic"
	}
	// Leave room for "-" + suffix within the name-length budget.
	if maxSlug := identity.MaxNameLen - idSuffixLen - 1; len(slug) > maxSlug {
		slug = strings.Trim(slug[:maxSlug], "-")
		if slug == "" {
			slug = "topic"
		}
	}
	return slug + "-" + randomSuffix()
}

// slugify lowercases s and collapses every run of non-[a-z0-9] into a single hyphen,
// with no leading/trailing hyphen — matching the slug grammar.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if b.Len() > 0 && !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func randomSuffix() string {
	buf := make([]byte, idSuffixLen)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; fall back to a fixed marker rather than panic.
		return "0000"
	}
	for i := range buf {
		buf[i] = idAlphabet[int(buf[i])%len(idAlphabet)]
	}
	return string(buf)
}
