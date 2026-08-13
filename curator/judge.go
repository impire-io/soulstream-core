package curator

import (
	"strings"

	"github.com/impire-io/soulstream-core/topic"
)

// DuplicateThreshold is the similarity above which two topics look like the same
// conversation: half their announcement words in common.
const DuplicateThreshold = 0.5

// Similarity scores how alike two topics' announcements are: the Jaccard overlap of
// their lowercased alphanumeric token sets over name + subject matter + tags, in
// [0, 1]. The topic-id's random suffix is excluded — it exists precisely to differ.
// Deterministic and explainable in one sentence, as a suggestion's reason must be.
func Similarity(a, b topic.DiscoverEntry) float64 {
	ta, tb := entryTokens(a), entryTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for tok := range ta {
		if tb[tok] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	return float64(inter) / float64(union)
}

// entryTokens tokenises what the announcement says the topic is about.
func entryTokens(e topic.DiscoverEntry) map[string]bool {
	out := map[string]bool{}
	addTokens(out, e.Name)
	addTokens(out, e.SubjectMatter)
	for _, tag := range e.Tags {
		addTokens(out, tag)
	}
	// The path's final random suffix would poison the overlap; identity comes from
	// the words, not the dice roll. (Tokens from the name already cover the slug.)
	return out
}

// addTokens splits s into lowercased alphanumeric runs.
func addTokens(set map[string]bool, s string) {
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			set[strings.ToLower(b.String())] = true
			b.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
}

// searchText builds the lowercased haystack for content-aware matching: what the
// announcement says plus what was actually said inside the topic.
func searchText(entry topic.DiscoverEntry, view *topic.MaterializedTopic) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(entry.Name))
	b.WriteByte(' ')
	b.WriteString(strings.ToLower(entry.SubjectMatter))
	for _, tag := range entry.Tags {
		b.WriteByte(' ')
		b.WriteString(strings.ToLower(tag))
	}
	if view != nil {
		for _, c := range view.Contributions {
			b.WriteByte(' ')
			b.WriteString(strings.ToLower(c.Body))
		}
		for _, a := range view.Attachments {
			b.WriteByte(' ')
			b.WriteString(strings.ToLower(a.Name))
		}
	}
	return b.String()
}
