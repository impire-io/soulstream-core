package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/impire-io/soulstream-core/registry"
	"github.com/impire-io/soulstream-core/topic"
)

// openTopic opens and materialises a handle so posts parent onto the current tip and
// closed-topic warnings fire.
func (h *handlers) openTopic(ctx context.Context, path string) (*topic.Handle, error) {
	th := topic.Open(h.c, path)
	if _, err := th.Materialise(ctx); err != nil {
		return nil, err
	}
	return th, nil
}

type boardInput struct{}

func (h *handlers) board(ctx context.Context, _ *mcp.CallToolRequest, _ boardInput) (*mcp.CallToolResult, any, error) {
	entries, err := topic.Board(ctx, h.c)
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(entries)
}

type showTopicInput struct {
	Path string `json:"path" jsonschema:"the topic path to read"`
}

// topicResult is a materialised view plus the session's distrust warnings — the
// per-op sig statuses live on the view's elements, the substitution alarm on top.
type topicResult struct {
	*topic.MaterializedTopic
	DistrustedPersonas []string `json:"distrusted_personas,omitempty"`
}

func (h *handlers) showTopic(ctx context.Context, _ *mcp.CallToolRequest, in showTopicInput) (*mcp.CallToolResult, any, error) {
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
	return jsonResult(topicResult{MaterializedTopic: v, DistrustedPersonas: distrusted(kr)})
}

type startTopicInput struct {
	Name    string   `json:"name" jsonschema:"the display name of the topic"`
	Subject string   `json:"subject,omitempty" jsonschema:"what the topic is about"`
	Tags    []string `json:"tags,omitempty" jsonschema:"tags for the topic"`
	Parent  string   `json:"parent,omitempty" jsonschema:"parent topic path, for a sub-topic"`
}

func (h *handlers) startTopic(ctx context.Context, _ *mcp.CallToolRequest, in startTopicInput) (*mcp.CallToolResult, any, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	th, err := topic.StartTopic(ctx, h.c, topic.StartTopicInput{
		Name: in.Name, SubjectMatter: in.Subject, Tags: in.Tags, Parent: in.Parent,
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(th.Path())
}

type postTurnInput struct {
	Path string `json:"path" jsonschema:"the topic path"`
	Body string `json:"body" jsonschema:"the message text; use @name to mention a persona"`
}

func (h *handlers) postTurn(ctx context.Context, _ *mcp.CallToolRequest, in postTurnInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.Body == "" {
		return nil, nil, fmt.Errorf("path and body are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	id, err := th.PostTurn(ctx, in.Body)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}

type addCommentInput struct {
	Path       string `json:"path" jsonschema:"the topic path"`
	AnchorOpID string `json:"anchor_op_id" jsonschema:"the op-id this comment is anchored to"`
	Body       string `json:"body" jsonschema:"the comment text"`
}

func (h *handlers) addComment(ctx context.Context, _ *mcp.CallToolRequest, in addCommentInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.AnchorOpID == "" || in.Body == "" {
		return nil, nil, fmt.Errorf("path, anchor_op_id and body are required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	id, err := th.AddComment(ctx, in.Body, in.AnchorOpID)
	if err != nil {
		return nil, nil, err
	}
	return textResult(id)
}

type closeTopicInput struct {
	Path string `json:"path" jsonschema:"the topic path"`
}

func (h *handlers) closeTopic(ctx context.Context, _ *mcp.CallToolRequest, in closeTopicInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	if _, err := th.Close(ctx); err != nil {
		return nil, nil, err
	}
	return textResult("closed " + in.Path)
}

type attachTextInput struct {
	Path        string `json:"path" jsonschema:"the topic path"`
	Name        string `json:"name" jsonschema:"the attachment's file name"`
	ContentType string `json:"content_type,omitempty" jsonschema:"the content type (default text/plain)"`
	Body        string `json:"body" jsonschema:"the text content to attach"`
}

func (h *handlers) attachText(ctx context.Context, _ *mcp.CallToolRequest, in attachTextInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" || in.Name == "" {
		return nil, nil, fmt.Errorf("path and name are required")
	}
	ct := in.ContentType
	if ct == "" {
		ct = "text/plain"
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	opID, err := th.Attach(ctx, in.Name, ct, []byte(in.Body), "")
	if err != nil {
		return nil, nil, err
	}
	// Return the object key (looked up from the fresh view) for later retrieval.
	if v, err := th.Materialise(ctx); err == nil {
		for _, a := range v.Attachments {
			if a.OpID == opID {
				return textResult(a.Object)
			}
		}
	}
	return textResult(opID)
}

type rollupTopicInput struct {
	Path string `json:"path" jsonschema:"the topic path to compact"`
}

// rollupTopic compacts a topic's history into a fresh baseline. The view is
// unchanged; a lost race is a retryable error; an already-compact topic is a no-op.
func (h *handlers) rollupTopic(ctx context.Context, _ *mcp.CallToolRequest, in rollupTopicInput) (*mcp.CallToolResult, any, error) {
	if in.Path == "" {
		return nil, nil, fmt.Errorf("path is required")
	}
	th, err := h.openTopic(ctx, in.Path)
	if err != nil {
		return nil, nil, err
	}
	baselineID, err := th.Rollup(ctx)
	if errors.Is(err, topic.ErrNothingToCompact) {
		return textResult("nothing to compact")
	}
	if err != nil {
		return nil, nil, err
	}
	return textResult(baselineID)
}

type discoverInput struct {
	Query string `json:"query" jsonschema:"what to look for; matched against topic names, subject matter, and tags (empty matches everything)"`
	Limit int    `json:"limit,omitempty" jsonschema:"per-answerer result cap (default 10)"`
}

// discover asks the realm's live discovery layer. Silence resolves to an empty
// list — the board tool remains the always-works fallback.
func (h *handlers) discover(ctx context.Context, _ *mcp.CallToolRequest, in discoverInput) (*mcp.CallToolResult, any, error) {
	kr := h.keyring(ctx)
	results, err := topic.Discover(ctx, h.c, topic.DiscoverInput{Query: in.Query, Limit: in.Limit}, kr)
	if err != nil {
		return nil, nil, err
	}
	if results == nil {
		results = []topic.DiscoverResult{}
	}
	return jsonResult(results)
}

type publishProfileInput struct {
	DisplayName string `json:"display_name,omitempty" jsonschema:"presentation name"`
	Description string `json:"description,omitempty" jsonschema:"one-line description of this persona"`
	OperatedBy  string `json:"operated_by,omitempty" jsonschema:"the persona that operates (answers for) this one"`
	Attestation string `json:"attestation,omitempty" jsonschema:"attestation token from the operator (their countersignature of the operated_by claim)"`
}

// publishProfile publishes (or metadata-updates) the session persona's directory
// entry, including its public signing key when the session holds one. Stored key
// material stays authoritative; key changes are rotation, done via the CLI.
func (h *handlers) publishProfile(ctx context.Context, _ *mcp.CallToolRequest, in publishProfileInput) (*mcp.CallToolResult, any, error) {
	now := time.Now().UTC()
	p := registry.Profile{
		Name:        h.c.Persona(),
		DisplayName: in.DisplayName,
		Description: in.Description,
		OperatedBy:  in.OperatedBy,
		CreatedAt:   now,
	}
	if in.Attestation != "" {
		tok, err := registry.ParseAttestationToken(in.Attestation)
		if err != nil {
			return nil, nil, err
		}
		if in.OperatedBy == "" {
			return nil, nil, fmt.Errorf("attestation requires operated_by %q", tok.Operator)
		}
		if tok.Operator != in.OperatedBy {
			return nil, nil, fmt.Errorf("attestation token is from %q but operated_by names %q", tok.Operator, in.OperatedBy)
		}
		if tok.Operated != h.c.Persona() {
			return nil, nil, fmt.Errorf("attestation token vouches for %q, not this persona (%q)", tok.Operated, h.c.Persona())
		}
		p.OperatorAttestation = &registry.OperatorAttestation{OperatedKey: tok.OperatedKey, Sig: tok.Sig}
	}
	if s := h.c.Signer(); s != nil {
		p.SigningKey = &registry.SigningKeyInfo{Ed25519: s.PublicKey(), Since: now}
	}
	if err := registry.Publish(ctx, h.c, p); err != nil {
		return nil, nil, err
	}
	published, _, err := registry.Lookup(ctx, h.c, h.c.Persona())
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(published)
}

type checkInboxInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum notifications to return (default 50)"`
}

// inboxResult carries the notifications (each with its sig status) plus the
// session's distrust warnings.
type inboxResult struct {
	Notifications      []topic.Notification `json:"notifications"`
	DistrustedPersonas []string             `json:"distrusted_personas,omitempty"`
}

func (h *handlers) checkInbox(ctx context.Context, _ *mcp.CallToolRequest, in checkInboxInput) (*mcp.CallToolResult, any, error) {
	kr := h.keyring(ctx)
	notes, err := topic.FetchInbox(ctx, h.c, h.c.Persona(), in.Limit, kr)
	if err != nil {
		return nil, nil, err
	}
	if notes == nil {
		notes = []topic.Notification{}
	}
	return jsonResult(inboxResult{Notifications: notes, DistrustedPersonas: distrusted(kr)})
}
