// Package mcpserver exposes Soulstream operations as MCP tools so an AI persona can
// participate through tool calls. The server is bound to one persona for its lifetime;
// every tool acts as that persona over the realm + topic library.
//
// The package is public so any host can embed the tool surface by bringing its
// own connected, signer-wired realm client — the stdio adapter, the remote
// node, and single-binary distributions all serve the SAME surface; none may
// fork it. Per-session identity comes entirely from the client passed to
// NewServer; hosts that multiplex principals construct one server per session.
//
// Tool logic lives in handler methods on a struct holding the session client, so it is
// testable directly against an in-process server without stdio or a live MCP client.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/internal/version"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/registry"
)

type handlers struct {
	c *realm.Client
	// keyringFn, when set, replaces the default file-backed pins keyring.
	// A shared pins file on disk is the wrong shape for hosts serving many
	// principals at once.
	keyringFn func(context.Context) (*identity.Keyring, error)
}

func newHandlers(c *realm.Client) *handlers { return &handlers{c: c} }

// Option customizes a server for its host. Zero options is the stdio
// adapter's exact behavior.
type Option func(*handlers)

// WithKeyring injects the reader-verification keyring provider, replacing the
// default per-realm pins file. Hosts that multiplex principals must inject
// one. The provider may return (nil, nil): verification degrades to
// unknown-key exactly as a missing pins file does — it never blocks a read.
func WithKeyring(provider func(context.Context) (*identity.Keyring, error)) Option {
	return func(h *handlers) { h.keyringFn = provider }
}

// keyring builds the session's reader keyring per call: pins + directory → keyring,
// persisting extended pins. Rebuilt per read (not cached) so a rotation published
// mid-session is honoured; the directory is one KV list at dogfood scale. Every
// failure degrades to nil — verification never blocks a read.
func (h *handlers) keyring(ctx context.Context) *identity.Keyring {
	if h.keyringFn != nil {
		kr, err := h.keyringFn(ctx)
		if err != nil {
			return nil
		}
		return kr
	}
	pinsPath, err := keystore.ResolvePinsFile("", h.c.Realm())
	if err != nil {
		return nil
	}
	pins, err := keystore.LoadPins(pinsPath, h.c.Realm())
	if err != nil {
		return nil
	}
	profiles, warnings, err := registry.All(ctx, h.c)
	if err != nil {
		return nil
	}
	for _, w := range warnings {
		// stderr is the stdio server's log channel — loud without corrupting MCP traffic.
		fmt.Fprintf(os.Stderr, "soulstream-mcp: WARNING: directory profile %q is invalid and was skipped (republish it): %v\n", w.Persona, w.Err)
	}
	if len(profiles) == 0 && len(pins.Personas) == 0 {
		return nil
	}
	kr, newPins := registry.BuildKeyring(profiles, pins.Personas)
	pins.Personas = newPins
	_ = keystore.SavePins(pinsPath, pins)
	return kr
}

// distrusted lists the keyring's distrusted personas, sorted — the loud surface an
// AI persona can act on (empty means all clear).
func distrusted(kr *identity.Keyring) []string {
	if kr == nil {
		return nil
	}
	var names []string
	for name, bad := range kr.Distrusted {
		if bad {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// NewServer builds an MCP server exposing the Soulstream tools, all acting as c's persona.
// Options customize host concerns (see WithKeyring); zero options is the stdio adapter's
// exact behavior.
func NewServer(c *realm.Client, opts ...Option) *mcp.Server {
	h := newHandlers(c)
	for _, opt := range opts {
		opt(h)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "soulstream", Version: version.Version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_whoami",
		Description: "Who this session is: the persona the realm admitted, the realm name, and the public signing key in use (empty when the session is unsigned). What you publish is attributed to exactly this identity.",
	}, h.whoami)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_board",
		Description: "List every topic on the realm's board (path, name, lifecycle).",
	}, h.board)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_show_topic",
		Description: "Read a topic: its announcement, contributions (with mentions), attachments, and lifecycle.",
	}, h.showTopic)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_start_topic",
		Description: "Start a new topic and return its path.",
	}, h.startTopic)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_post_turn",
		Description: "Post a turn to a topic. Use @name in the body to mention a persona.",
	}, h.postTurn)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_add_comment",
		Description: "Post a comment anchored to an operation.",
	}, h.addComment)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_close_topic",
		Description: "Close a topic (record a close transition).",
	}, h.closeTopic)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_attach_text",
		Description: "Attach text content to a topic; returns the attachment's object key.",
	}, h.attachText)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_check_inbox",
		Description: "Return your mention notifications (topic, op-id, author), newest first.",
	}, h.checkInbox)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_publish_profile",
		Description: "Publish or update your persona's directory profile (display metadata; includes your public signing key when this session holds one).",
	}, h.publishProfile)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_rollup_topic",
		Description: "Compact a topic: fold its history into a fresh baseline. The conversation reads identically afterwards; replay just gets cheap.",
	}, h.rollupTopic)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_discover",
		Description: "Ask the realm whether topics about something already exist; whoever is answering discovery replies from their own view. An empty result just means nobody answered — the board tool always works.",
	}, h.discover)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_open_work",
		Description: "Open a work item in a topic: a task with a title, visible to everyone. Returns the item id. Use @name in the body to mention a persona.",
	}, h.openWork)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_claim_work",
		Description: "Claim a work item. The log decides who won: first claim in stream order owns it, later claims are void. Returns the verdict — check `claimed` before starting the work.",
	}, h.claimWork)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_complete_work",
		Description: "Mark a work item done (terminal). Attach evidence first with comments or attachments anchored to the item id.",
	}, h.completeWork)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_abandon_work",
		Description: "Abandon a claimed work item: it reopens, unowned, for anyone to claim fresh.",
	}, h.abandonWork)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_revise_text",
		Description: "Attach a whole-file text revision of an artefact (an attachment lineage). The revision supersedes the current tip; history keeps every version.",
	}, h.reviseText)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_list_artefacts",
		Description: "List a topic's artefacts: each is a lineage of whole-file revisions with a root id, display name, full history, and current tip.",
	}, h.listArtefacts)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_read_artefact",
		Description: "Read an artefact's current text content (or a specific revision's), digest-verified. Text only — binary content needs the CLI.",
	}, h.readArtefact)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_reply_comment",
		Description: "Reply under a comment — threaded commentary anchored to it. Use @name to mention.",
	}, h.replyComment)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_resolve_comment",
		Description: "Mark a comment settled: still readable, visibly closed. Anyone may resolve; duplicates are harmless.",
	}, h.resolveComment)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_edit",
		Description: "Correct your OWN turn, comment, or reply with a whole-body replacement. Readers render the newest version; the edit trail stays visible. Only the author's edits take effect — disagree with a reply, not a rewrite.",
	}, h.edit)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_memory_query",
		Description: "Ask whoever keeps memory of the realm. Answers arrive attributed and graded by verifiability (fact / unverifiable / gossip) — citations are checked against the realm, never trusted. Empty means silence, which is an honest answer.",
	}, h.memoryQuery)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "soulstream_memory_fetch",
		Description: "Fetch one operation as a self-authenticating exhibit: from the stream when it is still there, from whoever kept a copy when compaction removed it. The verdict says whether the author's signature verifies.",
	}, h.memoryFetch)

	return s
}

type whoamiInput struct{}

// whoamiResult is the session's server-side identity as this client holds it.
// Through the remote node the persona is the principal the admission edge
// asserted — whoami is how a remote user sees who the realm decided they are.
type whoamiResult struct {
	Persona         string `json:"persona"`
	Realm           string `json:"realm"`
	SignerPublicKey string `json:"signer_public_key,omitempty"`
}

func (h *handlers) whoami(_ context.Context, _ *mcp.CallToolRequest, _ whoamiInput) (*mcp.CallToolResult, any, error) {
	out := whoamiResult{Persona: h.c.Persona(), Realm: h.c.Realm()}
	if s := h.c.Signer(); s != nil {
		out.SignerPublicKey = s.PublicKey()
	}
	return jsonResult(out)
}

func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
}

func textResult(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}
