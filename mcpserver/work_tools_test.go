package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/topic"
)

// TestWorkTools: an AI persona opens, claims (winning and losing verdicts),
// completes, and abandons work items through the MCP surface.
func TestWorkTools(t *testing.T) {
	h, url := setup(t, "bookkeeper-agent")
	ctx := context.Background()
	rival := newHandlers(clientOn(t, url, "rival-agent"))

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "gadget plan"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))

	res, _, err = h.openWork(ctx, nil, openWorkInput{Path: path, Title: "draft the intro", Body: "any takers?"})
	if err != nil {
		t.Fatal(err)
	}
	itemID := strings.TrimSpace(resultText(t, res))

	// First claim wins; the verdict says so.
	res, _, err = h.claimWork(ctx, nil, workRefInput{Path: path, ItemID: itemID})
	if err != nil {
		t.Fatal(err)
	}
	var v claimVerdict
	if err := json.Unmarshal([]byte(resultText(t, res)), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Claimed || v.Owner != "bookkeeper-agent" {
		t.Fatalf("winning verdict = %+v", v)
	}

	// The rival's claim is void and the verdict names the owner.
	res, _, err = rival.claimWork(ctx, nil, workRefInput{Path: path, ItemID: itemID})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &v); err != nil {
		t.Fatal(err)
	}
	if v.Claimed || v.Owner != "bookkeeper-agent" || !strings.Contains(v.Note, "claimed it first") {
		t.Fatalf("losing verdict = %+v", v)
	}

	// show_topic carries the items; complete finishes it.
	res, _, _ = h.showTopic(ctx, nil, showTopicInput{Path: path})
	if !strings.Contains(resultText(t, res), `"work_items"`) {
		t.Errorf("show_topic missing work_items: %q", resultText(t, res))
	}
	if _, _, err = h.completeWork(ctx, nil, workRefInput{Path: path, ItemID: itemID}); err != nil {
		t.Fatal(err)
	}

	// Abandon reopens a second item for the rival to claim fresh.
	res, _, _ = h.openWork(ctx, nil, openWorkInput{Path: path, Title: "polish diagrams"})
	item2 := strings.TrimSpace(resultText(t, res))
	if _, _, err = h.claimWork(ctx, nil, workRefInput{Path: path, ItemID: item2}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.abandonWork(ctx, nil, workRefInput{Path: path, ItemID: item2}); err != nil {
		t.Fatal(err)
	}
	res, _, err = rival.claimWork(ctx, nil, workRefInput{Path: path, ItemID: item2})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(resultText(t, res)), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Claimed || v.Owner != "rival-agent" {
		t.Fatalf("post-abandon verdict = %+v", v)
	}

	// Input validation.
	if _, _, err := h.openWork(ctx, nil, openWorkInput{Path: path}); err == nil {
		t.Error("openWork accepted an empty title")
	}
	if _, _, err := h.claimWork(ctx, nil, workRefInput{Path: path}); err == nil {
		t.Error("claimWork accepted an empty item id")
	}
}

// TestArtefactTools: revise a text artefact, list lineages, read tip and an old
// revision back — with the binary guard pointing at the CLI.
func TestArtefactTools(t *testing.T) {
	h, _ := setup(t, "bookkeeper-agent")
	ctx := context.Background()

	res, _, err := h.startTopic(ctx, nil, startTopicInput{Name: "design notes"})
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(resultText(t, res))

	if _, _, err = h.attachText(ctx, nil, attachTextInput{Path: path, Name: "notes.md", Body: "draft one"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = h.reviseText(ctx, nil, reviseTextInput{Path: path, Artefact: "notes.md", Body: "draft two"}); err != nil {
		t.Fatal(err)
	}

	res, _, err = h.listArtefacts(ctx, nil, listArtefactsInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var arts []topic.Artefact
	if err := json.Unmarshal([]byte(resultText(t, res)), &arts); err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || len(arts[0].Revisions) != 2 || arts[0].Name != "notes.md" {
		t.Fatalf("artefacts = %+v", arts)
	}

	// Tip by default; any revision by op-id.
	res, _, err = h.readArtefact(ctx, nil, readArtefactInput{Path: path, Artefact: "notes.md"})
	if err != nil || resultText(t, res) != "draft two" {
		t.Errorf("read tip = %q (%v)", resultText(t, res), err)
	}
	res, _, err = h.readArtefact(ctx, nil, readArtefactInput{
		Path: path, Artefact: arts[0].Root, Revision: arts[0].Revisions[0].OpID,
	})
	if err != nil || resultText(t, res) != "draft one" {
		t.Errorf("read old revision = %q (%v)", resultText(t, res), err)
	}

	// Binary content is refused with a pointer at the CLI.
	th := topic.Open(h.c, path)
	if _, err := th.Materialise(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := th.Revise(ctx, "notes.bin", "application/octet-stream", []byte{0xff, 0xfe, 0x00, 0x80}, arts[0].Tip.OpID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.readArtefact(ctx, nil, readArtefactInput{Path: path, Artefact: arts[0].Root}); err == nil || !strings.Contains(err.Error(), "not text") {
		t.Errorf("binary read error = %v", err)
	}
}
