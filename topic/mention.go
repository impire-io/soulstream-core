package topic

import (
	"context"
	"regexp"

	"github.com/impire-io/soulstream-core/identity"
)

// mentionRe matches an @ followed by a valid persona slug (no trailing hyphen, no
// uppercase — the grammar is baked into the pattern).
var mentionRe = regexp.MustCompile(`@([a-z0-9]+(?:-[a-z0-9]+)*)`)

// ParseMentions returns the distinct, valid persona names @mentioned in body.
func ParseMentions(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		name := m[1]
		if seen[name] || !identity.ValidName(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// mergeMentions is the mention set an operation carries: the names the parser found in
// the body, then any the caller named outright, de-duplicated and held to the same
// validity rule either way — an invalid or empty supplied name taps nobody, exactly as
// an invalid one written into a body does. Nil when nothing names anybody.
//
// Supplying names is how a caller whose surface reads better than the grammar does —
// a picker offering "Daan" where the record's own name for that person is a fold-issued
// slug — records who was meant without rewriting a word of what was typed.
func mergeMentions(body string, supplied []string) []string {
	out := ParseMentions(body)
	seen := make(map[string]bool, len(out)+len(supplied))
	for _, name := range out {
		seen[name] = true
	}
	for _, name := range supplied {
		if seen[name] || !identity.ValidName(name) {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// notifyMentions publishes a mention.notify to each mentioned persona's inbox, pointing
// at the operation that mentioned them.
func (h *Handle) notifyMentions(ctx context.Context, opID string, mentions []string) error {
	for _, persona := range mentions {
		if err := publishNotify(ctx, h.client, persona, NotifyPayload{
			Topic:  h.path,
			OpID:   opID,
			Author: h.client.Persona(),
		}); err != nil {
			return err
		}
	}
	return nil
}
