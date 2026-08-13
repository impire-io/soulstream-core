package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/impire-io/soulstream-core/topic"
)

type memoryQueryInput struct {
	Query     string   `json:"query" jsonschema:"the free-text question to ask whoever keeps memory"`
	Topics    []string `json:"topics,omitempty" jsonschema:"topic-name patterns as a relevance hint for witnesses (never enforced)"`
	After     string   `json:"after,omitempty" jsonschema:"RFC3339 interest horizon hint"`
	TimeoutMs int      `json:"timeout_ms,omitempty" jsonschema:"how long to listen for answers (default 3000, clamped 100..30000)"`
}

// memoryQuery asks the realm's witnesses and returns their answers merged,
// attributed, and graded — every citation checked against the realm, never
// trusted. An empty list means silence, which is an honest answer.
func (h *handlers) memoryQuery(ctx context.Context, _ *mcp.CallToolRequest, in memoryQueryInput) (*mcp.CallToolResult, any, error) {
	var after time.Time
	if in.After != "" {
		parsed, err := time.Parse(time.RFC3339, in.After)
		if err != nil {
			return nil, nil, err
		}
		after = parsed
	}
	kr := h.keyring(ctx)
	res, err := topic.MemoryQuery(ctx, h.c, topic.MemoryQueryInput{
		Query:   in.Query,
		Topics:  in.Topics,
		After:   after,
		Timeout: time.Duration(in.TimeoutMs) * time.Millisecond,
	}, kr)
	if err != nil {
		return nil, nil, err
	}
	if res.Answers == nil {
		res.Answers = []topic.MemoryAnswer{}
	}
	return jsonResult(res)
}

type memoryFetchInput struct {
	Topic     string `json:"topic" jsonschema:"the topic path the operation belongs to"`
	OpID      string `json:"op_id" jsonschema:"the operation id to fetch as an exhibit"`
	TimeoutMs int    `json:"timeout_ms,omitempty" jsonschema:"how long to wait for witnesses when the op is compacted (default 3000, clamped 100..30000)"`
}

type memoryFetchResult struct {
	Found   bool            `json:"found"`
	Verdict topic.SigStatus `json:"verdict,omitempty"`
	Source  string          `json:"source,omitempty"`
	Exhibit any             `json:"exhibit,omitempty"`
}

// memoryFetch obtains one operation as a self-authenticating exhibit: from the
// stream when it is still there, from whoever kept it otherwise. The verdict is
// the exhibit's own embedded-signature check — fetch-and-verify in one call.
func (h *handlers) memoryFetch(ctx context.Context, _ *mcp.CallToolRequest, in memoryFetchInput) (*mcp.CallToolResult, any, error) {
	kr := h.keyring(ctx)
	res, err := topic.FetchExhibit(ctx, h.c, in.Topic, in.OpID, time.Duration(in.TimeoutMs)*time.Millisecond, kr)
	if err != nil {
		return nil, nil, err
	}
	if res == nil {
		return jsonResult(memoryFetchResult{Found: false})
	}
	return jsonResult(memoryFetchResult{
		Found: true, Verdict: res.Verdict, Source: res.Source, Exhibit: res.Exhibit,
	})
}
