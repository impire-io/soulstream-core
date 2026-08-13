package mcpserver

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/impire-io/soulstream-core/topic"
)

// 010-work tools: versioned artefacts (revise, list, read) and work items
// (open, claim with verdict, done, abandon). Content stays text-bounded like
// soulstream_attach_text; binary artefacts are the CLI's business.

type openWorkInput struct {
	Path  string `json:"path" jsonschema:"the topic path"`
	Title string `json:"title" jsonschema:"the work item's title"`
	Body  string `json:"body,omitempty" jsonschema:"optional description; use @name to mention a persona"`
}

func (h *handlers) openWork(ctx context.Context, _ *mcp.CallToolRequest, in openWorkInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.Title == "" {
		return nil, nil, fmt.Errorf("path and title are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	id, err := th.OpenWork(ctx, in.Title, in.Body)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}

type workRefInput struct {
	Path   string `json:"path" jsonschema:"the topic path"`
	ItemID string `json:"item_id" jsonschema:"the work item's id (its work.open op-id)"`
}

// claimVerdict is what a claimant most needs to know: did the log pick me?
type claimVerdict struct {
	Claimed bool             `json:"claimed"`
	Owner   string           `json:"owner,omitempty"`
	Status  topic.WorkStatus `json:"status"`
	Note    string           `json:"note"`
}

func (h *handlers) claimWork(ctx context.Context, _ *mcp.CallToolRequest, in workRefInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.ItemID == "" {
		return nil, nil, fmt.Errorf("path and item_id are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	claimID, err := th.ClaimWork(ctx, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	// The log decides who won — materialise and report the verdict.
	v, err := th.Materialise(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, item := range v.WorkItems {
		if item.ID != in.ItemID {
			continue
		}
		for _, ev := range item.Timeline {
			if ev.OpID != claimID {
				continue
			}
			verdict := claimVerdict{Claimed: !ev.Void, Owner: item.Owner, Status: item.Status}
			switch {
			case !ev.Void:
				verdict.Note = "you own this item"
			case item.Status == topic.WorkDone:
				verdict.Note = "void — the item is already done"
			case item.Owner != "":
				verdict.Note = "void — " + item.Owner + " claimed it first (your claim is recorded, changes nothing)"
			default:
				verdict.Note = "void — the item was not open"
			}
			return jsonResult(verdict)
		}
	}
	return nil, nil, fmt.Errorf("work item %s not found in %s", in.ItemID, in.Path)
}

func (h *handlers) completeWork(ctx context.Context, _ *mcp.CallToolRequest, in workRefInput) (*mcp.CallToolResult, any, error) {
	return h.workRef(ctx, in, (*topic.Handle).CompleteWork)
}

func (h *handlers) abandonWork(ctx context.Context, _ *mcp.CallToolRequest, in workRefInput) (*mcp.CallToolResult, any, error) {
	return h.workRef(ctx, in, (*topic.Handle).AbandonWork)
}

func (h *handlers) workRef(ctx context.Context, in workRefInput,
	publish func(*topic.Handle, context.Context, string) (string, error)) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.ItemID == "" {
		return nil, nil, fmt.Errorf("path and item_id are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	id, err := publish(th, ctx, in.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}

type reviseTextInput struct {
	Path        string `json:"path" jsonschema:"the topic path"`
	Artefact    string `json:"artefact" jsonschema:"the artefact to revise: root op-id, any revision op-id, or display name"`
	Body        string `json:"body" jsonschema:"the full new text content (whole-file revision)"`
	Name        string `json:"name,omitempty" jsonschema:"new file name (default: keep the tip's)"`
	ContentType string `json:"content_type,omitempty" jsonschema:"content type (default: the tip's)"`
}

func (h *handlers) reviseText(ctx context.Context, _ *mcp.CallToolRequest, in reviseTextInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.Artefact == "" {
		return nil, nil, fmt.Errorf("path and artefact are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	v, err := th.Materialise(ctx)
	if err != nil {
		return nil, nil, err
	}
	a, err := topic.FindArtefact(v, in.Artefact)
	if err != nil {
		return nil, nil, err
	}
	name, ct := in.Name, in.ContentType
	if name == "" {
		name = a.Tip.Name
	}
	if ct == "" {
		ct = a.Tip.ContentType
	}
	opID, err := th.Revise(ctx, name, ct, []byte(in.Body), a.Tip.OpID)
	if err != nil {
		return nil, nil, err
	}
	return textResult(opID)
}

type listArtefactsInput struct {
	Path string `json:"path" jsonschema:"the topic path"`
}

func (h *handlers) listArtefacts(ctx context.Context, _ *mcp.CallToolRequest, in listArtefactsInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	kr := h.keyring(ctx)
	th := topic.Open(h.c, in.Path)
	th.UseKeyring(kr)
	v, err := th.Materialise(ctx)
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(v.Artefacts())
}

type readArtefactInput struct {
	Path     string `json:"path" jsonschema:"the topic path"`
	Artefact string `json:"artefact" jsonschema:"the artefact: root op-id, any revision op-id, or display name"`
	Revision string `json:"revision,omitempty" jsonschema:"a specific revision's op-id (default: the tip)"`
}

func (h *handlers) readArtefact(ctx context.Context, _ *mcp.CallToolRequest, in readArtefactInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.Artefact == "" {
		return nil, nil, fmt.Errorf("path and artefact are required")
	}
	th := topic.Open(h.c, in.Path)
	v, err := th.Materialise(ctx)
	if err != nil {
		return nil, nil, err
	}
	a, err := topic.FindArtefact(v, in.Artefact)
	if err != nil {
		return nil, nil, err
	}
	rev := a.Tip
	if in.Revision != "" {
		found := false
		for _, r := range a.Revisions {
			if r.OpID == in.Revision {
				rev, found = r, true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("artefact %s has no revision %s", a.Root, in.Revision)
		}
	}
	data, err := topic.GetAttachment(ctx, h.c, rev.Object)
	if err != nil {
		return nil, nil, err
	}
	if !topic.VerifyDigest(data, rev.Digest) {
		return nil, nil, fmt.Errorf("revision %s does not match its recorded digest", rev.OpID)
	}
	if !utf8.Valid(data) {
		return nil, nil, fmt.Errorf("revision %s is not text — fetch it with the CLI: soulstream get %s --artefact %s", rev.OpID, in.Path, a.Root)
	}
	return textResult(string(data))
}
