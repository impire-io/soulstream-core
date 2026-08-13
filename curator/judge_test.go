package curator

import (
	"strings"
	"testing"
	"time"

	"github.com/impire-io/soulstream-core/topic"
)

func entry(name, subject string, tags ...string) topic.DiscoverEntry {
	return topic.DiscoverEntry{Path: "x", Name: name, SubjectMatter: subject, Tags: tags}
}

func TestSimilarity(t *testing.T) {
	cases := []struct {
		name      string
		a, b      topic.DiscoverEntry
		duplicate bool
	}{
		{"near-identical names", entry("Q2 VAT filing", "the filing"), entry("VAT filing Q2", "filing"), true},
		{"same words different case", entry("Onboarding Guide", ""), entry("onboarding guide", ""), true},
		{"tag overlap counts", entry("Quarterly numbers", "finance", "vat", "q2"), entry("Q2 VAT", "", "finance"), true},
		{"unrelated", entry("Q2 VAT filing", "finance"), entry("Team offsite planning", "fun"), false},
		{"shared stopword only", entry("The VAT plan", ""), entry("The offsite", ""), false},
	}
	for _, c := range cases {
		s := Similarity(c.a, c.b)
		if c.duplicate && s < DuplicateThreshold {
			t.Errorf("%s: similarity %.2f, want >= %.2f", c.name, s, DuplicateThreshold)
		}
		if !c.duplicate && s >= DuplicateThreshold {
			t.Errorf("%s: similarity %.2f, want < %.2f", c.name, s, DuplicateThreshold)
		}
	}

	if s := Similarity(entry("", ""), entry("something", "")); s != 0 {
		t.Errorf("empty entry similarity = %.2f, want 0", s)
	}
}

func TestSuggestionConvention(t *testing.T) {
	dup := DuplicateSuggestion("vat-q2-x7m2")
	if !IsDuplicateSuggestion(dup) || !IsSuggestion(dup) || IsDormantSuggestion(dup) {
		t.Errorf("duplicate suggestion misrecognised: %q", dup)
	}
	if !strings.Contains(dup, "vat-q2-x7m2") {
		t.Errorf("flag does not name the older topic: %q", dup)
	}

	dorm := DormantSuggestion(14 * 24 * time.Hour)
	if !IsDormantSuggestion(dorm) || !IsSuggestion(dorm) || IsDuplicateSuggestion(dorm) {
		t.Errorf("dormancy suggestion misrecognised: %q", dorm)
	}
	if !strings.Contains(dorm, "14 days") {
		t.Errorf("proposal does not say the span plainly: %q", dorm)
	}

	// Ordinary conversation mentioning curators is NOT a suggestion.
	for _, body := range []string{
		"the curator flagged this yesterday",
		"curator: what do you think?",
		"[curator-ish] not the convention",
		"no activity for weeks, sadly", // missing the marker prefix
	} {
		if IsSuggestion(body) {
			t.Errorf("false positive suggestion: %q", body)
		}
	}
}

func TestHumanSpan(t *testing.T) {
	cases := map[time.Duration]string{
		15 * 24 * time.Hour: "15 days",
		5 * time.Hour:       "5 hours",
		7 * time.Minute:     "7 minutes",
	}
	for d, want := range cases {
		if got := humanSpan(d); got != want {
			t.Errorf("humanSpan(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestSearchText(t *testing.T) {
	view := &topic.MaterializedTopic{
		Contributions: []topic.Contribution{{Body: "the ROLLUP gate question"}},
		Attachments:   []topic.Attachment{{Name: "Budget.CSV"}},
	}
	text := searchText(entry("Design Review", "architecture", "core"), view)
	for _, want := range []string{"design review", "architecture", "core", "rollup gate", "budget.csv"} {
		if !strings.Contains(text, want) {
			t.Errorf("searchText missing %q: %q", want, text)
		}
	}
}
