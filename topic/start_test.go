package topic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

func TestStartTopicPublishesAnnounceAndBaseline(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")

	h, err := StartTopic(ctx, c, StartTopicInput{
		Name: "Q2 VAT filing", SubjectMatter: "filing the Q2 return", Tags: []string{"finance"},
	})
	if err != nil {
		t.Fatalf("StartTopic: %v", err)
	}
	if !identity.ValidName(IDFromPath(h.Path())) {
		t.Errorf("topic path segment %q is not a valid slug", h.Path())
	}

	// INFO carries the announcement.
	stream, err := c.JetStream().Stream(ctx, realm.StreamName)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, InfoSubject(h.Path()))
	if err != nil {
		t.Fatalf("no announcement on INFO: %v", err)
	}
	arec, err := record.Parse(raw.Header, raw.Data)
	if err != nil {
		t.Fatalf("parse announce: %v", err)
	}
	if arec.Type != TypeAnnounce {
		t.Errorf("info op type = %q, want %q", arec.Type, TypeAnnounce)
	}

	// OPS first message is the baseline; the handle frontier is the baseline id.
	recs, err := drainOps(ctx, c, h.Path())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(recs) != 1 || recs[0].Record.Type != TypeBaseline {
		t.Fatalf("first op is not a lone baseline: %+v", recs)
	}
	if len(h.Frontier()) != 1 || h.Frontier()[0] != recs[0].Record.ID {
		t.Errorf("frontier = %v, want [%s]", h.Frontier(), recs[0].Record.ID)
	}
}

func TestStartTopicRejectsOversizeBaseline(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")

	state := json.RawMessage(`"` + strings.Repeat("x", InlineBaselineThreshold+1) + `"`)
	_, err := StartTopic(ctx, c, StartTopicInput{Name: "big", State: state})
	if err == nil {
		t.Fatal("expected oversize-baseline rejection")
	}
	if !strings.Contains(err.Error(), "inline limit") {
		t.Errorf("error = %v, want it to mention the inline limit", err)
	}
}

func TestStartTopicRequiresName(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")
	if _, err := StartTopic(ctx, c, StartTopicInput{}); err == nil {
		t.Error("StartTopic with no name should error")
	}
}
