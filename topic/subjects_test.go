package topic

import (
	"testing"

	"github.com/impire-io/soulstream-core/identity"
)

func TestSubjectBuilders(t *testing.T) {
	if got := OpsSubject("vat-x7m2"); got != "SOULSTREAM.TOPICS.OPS.vat-x7m2" {
		t.Errorf("OpsSubject = %q", got)
	}
	if got := InfoSubject("vat-x7m2"); got != "SOULSTREAM.TOPICS.INFO.vat-x7m2" {
		t.Errorf("InfoSubject = %q", got)
	}
	if got := OpsSubject("vat-x7m2.pricing-c2d9"); got != "SOULSTREAM.TOPICS.OPS.vat-x7m2.pricing-c2d9" {
		t.Errorf("nested OpsSubject = %q", got)
	}
}

func TestPathHelpers(t *testing.T) {
	if got := ChildPath("", "vat-x7m2"); got != "vat-x7m2" {
		t.Errorf("ChildPath top-level = %q", got)
	}
	if got := ChildPath("vat-x7m2", "pricing-c2d9"); got != "vat-x7m2.pricing-c2d9" {
		t.Errorf("ChildPath nested = %q", got)
	}
	if got := ParentPath("vat-x7m2"); got != "" {
		t.Errorf("ParentPath top-level = %q, want empty", got)
	}
	if got := ParentPath("vat-x7m2.pricing-c2d9"); got != "vat-x7m2" {
		t.Errorf("ParentPath = %q", got)
	}
	if got := IDFromPath("vat-x7m2.pricing-c2d9"); got != "pricing-c2d9" {
		t.Errorf("IDFromPath = %q", got)
	}
	if got := IDFromPath("vat-x7m2"); got != "vat-x7m2" {
		t.Errorf("IDFromPath top-level = %q", got)
	}
}

func TestNewTopicID(t *testing.T) {
	cases := []string{"Q2 VAT filing", "Hello, World!", "   spaces   ", "AlreadySlugish", "***", ""}
	seen := map[string]bool{}
	for _, name := range cases {
		for range 50 {
			id := NewTopicID(name)
			if !identity.ValidName(id) {
				t.Fatalf("NewTopicID(%q) = %q, not a valid slug", name, id)
			}
			if len(id) < idSuffixLen+2 { // at least "x-" + 4? loosely
				t.Fatalf("NewTopicID(%q) = %q too short", name, id)
			}
			if seen[id] {
				t.Fatalf("NewTopicID produced a duplicate: %q", id)
			}
			seen[id] = true
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Q2 VAT filing": "q2-vat-filing",
		"Hello, World!": "hello-world",
		"  trim  me  ":  "trim-me",
		"a--b":          "a-b",
		"***":           "",
		"MixedCASE123":  "mixedcase123",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
