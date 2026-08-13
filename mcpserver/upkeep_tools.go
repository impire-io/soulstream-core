package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/impire-io/soulstream-core/topic"
)

// 011-vocab tools: conversation upkeep. Removal, dormancy marking, and the
// sweeps stay operator surfaces (CLI), like archive.

type replyCommentInput struct {
	Path       string `json:"path" jsonschema:"the topic path"`
	AnchorOpID string `json:"anchor_op_id" jsonschema:"the comment (or reply) this replies to"`
	Body       string `json:"body" jsonschema:"the reply text; use @name to mention a persona"`
}

func (h *handlers) replyComment(ctx context.Context, _ *mcp.CallToolRequest, in replyCommentInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.AnchorOpID == "" || in.Body == "" {
		return nil, nil, fmt.Errorf("path, anchor_op_id and body are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	id, err := th.Reply(ctx, in.Body, in.AnchorOpID)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}

type resolveCommentInput struct {
	Path string `json:"path" jsonschema:"the topic path"`
	OpID string `json:"op_id" jsonschema:"the comment (or reply) to mark settled"`
}

func (h *handlers) resolveComment(ctx context.Context, _ *mcp.CallToolRequest, in resolveCommentInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.OpID == "" {
		return nil, nil, fmt.Errorf("path and op_id are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	id, err := th.Resolve(ctx, in.OpID)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}

type editInput struct {
	Path string `json:"path" jsonschema:"the topic path"`
	OpID string `json:"op_id" jsonschema:"your turn/comment/reply to correct (or any prior edit of it)"`
	Body string `json:"body" jsonschema:"the full corrected text (whole-body replacement)"`
}

func (h *handlers) edit(ctx context.Context, _ *mcp.CallToolRequest, in editInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.OpID == "" || in.Body == "" {
		return nil, nil, fmt.Errorf("path, op_id and body are required")
	}
	th := topic.Open(h.c, in.Path)
	v, err := th.Materialise(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Best-effort pre-check: a foreign edit would land but change nothing —
	// tell the agent before it wastes the op.
	for _, c := range v.Contributions {
		hit := c.OpID == in.OpID
		for _, e := range c.Edits {
			if e.OpID == in.OpID {
				hit = true
			}
		}
		if hit && c.Author != h.c.Persona() {
			return nil, nil, fmt.Errorf("only the author may edit: %s belongs to %s — reply instead", c.OpID, c.Author)
		}
	}
	id, err := th.Edit(ctx, in.OpID, in.Body)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}
