package topic

import (
	"context"
	"reflect"
	"testing"
)

func TestParseMentions(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"hi @daan", []string{"daan"}},
		{"@bookkeeper-agent please check", []string{"bookkeeper-agent"}},
		{"trailing punct @bookkeeper-agent!", []string{"bookkeeper-agent"}},
		{"@Daan @@ @ x", nil},                       // uppercase, bare @@, and @ space → nothing
		{"@daan and @daan again", []string{"daan"}}, // de-duplicated
		{"@a talks to @b-c", []string{"a", "b-c"}},
		{"nobody here", nil},
		{"self @daan mentions", []string{"daan"}}, // self-mention is still parsed
	}
	for _, tc := range cases {
		got := ParseMentions(tc.body)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseMentions(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

// What a caller supplies joins what the body says, rather than replacing it: the
// grammar keeps working for anyone writing it, and a surface that resolves a name
// its people recognise adds to the same set.
func TestMergeMentions(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		supplied []string
		want     []string
	}{
		{"body only", "hi @daan", nil, []string{"daan"}},
		{"supplied only", "hi @Daan", []string{"daan"}, []string{"daan"}},
		{"union, body first", "@daan and @b-c", []string{"avery"},
			[]string{"daan", "b-c", "avery"}},
		{"the same name both ways is one", "@daan", []string{"daan"}, []string{"daan"}},
		{"supplied twice is one", "nobody", []string{"avery", "avery"}, []string{"avery"}},
		{"invalid supplied names are dropped", "nobody",
			[]string{"", "Daan", "-leading", "trailing-", "has space"}, nil},
		{"the valid ones survive the invalid", "nobody",
			[]string{"Daan", "daan", ""}, []string{"daan"}},
		{"nothing at all", "nobody here", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeMentions(tc.body, tc.supplied)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeMentions(%q, %v) = %v, want %v",
					tc.body, tc.supplied, got, tc.want)
			}
		})
	}
}

// The supplied arms write the union onto the op and tap every persona in it — and
// the body reaches the record exactly as it was written, display name and all.
func TestPostingMentioningSuppliedPersonas(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "the picker"})
	if err != nil {
		t.Fatal(err)
	}

	// A body written the way a person reads it: the grammar sees "@bookkeeper-agent"
	// and nothing else, and "Avery" rides along as a supplied name.
	const said = "@bookkeeper-agent and @Avery — box 5, please"
	turn, err := h.PostTurnMentioning(ctx, said, []string{"avery"})
	if err != nil {
		t.Fatal(err)
	}
	comment, err := h.AddCommentMentioning(ctx, "and you too, @Avery", turn, []string{"avery"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ReplyMentioning(ctx, "one more, @Avery", comment, []string{"avery", "bad name"}); err != nil {
		t.Fatal(err)
	}

	mt, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mt.Contributions) != 3 {
		t.Fatalf("contributions = %d, want 3", len(mt.Contributions))
	}
	if got := mt.Contributions[0].Body; got != said {
		t.Errorf("the body was rewritten: %q, want %q", got, said)
	}
	if got := mt.Contributions[0].Mentions; !reflect.DeepEqual(got,
		[]string{"bookkeeper-agent", "avery"}) {
		t.Errorf("turn mentions = %v, want the union", got)
	}
	for _, c := range mt.Contributions[1:] {
		if !reflect.DeepEqual(c.Mentions, []string{"avery"}) {
			t.Errorf("%s mentions = %v, want [avery] — an invalid supplied name taps nobody",
				c.Type, c.Mentions)
		}
	}

	// The fan-out follows the union: three slips for the supplied persona, one for
	// the one the grammar found.
	avery, err := FetchInbox(ctx, c, "avery", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(avery) != 3 {
		t.Errorf("avery holds %d slips, want 3 (turn, comment, reply)", len(avery))
	}
	agent, err := FetchInbox(ctx, c, "bookkeeper-agent", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(agent) != 1 || agent[0].OpID != turn {
		t.Errorf("the parsed mention was not notified: %+v", agent)
	}
	if bad, err := FetchInbox(ctx, c, "bad name", 10, nil); err == nil && len(bad) != 0 {
		t.Errorf("an invalid supplied name reached an inbox: %+v", bad)
	}
}
