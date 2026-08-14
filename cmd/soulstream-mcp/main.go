// Command soulstream-mcp is an MCP (Model Context Protocol) server over stdio that lets
// an AI persona participate in Soulstream through tool calls. It acts as one configured
// persona for its whole session.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/impire-io/soulstream-core/internal/config"
	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/internal/version"
	"github.com/impire-io/soulstream-core/mcpserver"
	"github.com/impire-io/soulstream-core/realm"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("soulstream-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctxName := fs.String("context", "", "named NATS context")
	realmName := fs.String("realm", "", "realm name")
	persona := fs.String("persona", "", "persona name")
	keyFile := fs.String("key-file", "", "signing-seed file (default: SOULSTREAM_KEY_FILE, then config dir)")
	url := fs.String("url", "", "server address to dial directly, instead of a context (env: SOULSTREAM_URL)")
	creds := fs.String("creds", "", "NATS credentials file — the deployment's sentinel on the token lane (env: SOULSTREAM_CREDS)")
	token := fs.String("token", "", "access token presented on connect; prefer SOULSTREAM_TOKEN, since a flag is visible to every process on the machine")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(os.Stdout, version.Version)
		return 0
	}

	// Identity resolves per field: flag > env > nearest .soulstream.json walking up
	// from the working directory > user config file. The MCP host sets our working
	// directory to the project, so per-project identity falls out of cwd.
	explicit := config.File{}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "context":
			explicit.Context = *ctxName
		case "realm":
			explicit.Realm = *realmName
		case "persona":
			explicit.Persona = *persona
		case "key-file":
			explicit.KeyFile = *keyFile
		}
	})
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "soulstream-mcp: %v\n", err)
		return 1
	}
	resolved, err := config.Resolve(explicit, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream-mcp: %v\n", err)
		return 1
	}
	if resolved.Persona.V == "" {
		fmt.Fprintln(stderr, "soulstream-mcp: a persona is required (--persona, SOULSTREAM_PERSONA, or a config file)")
		return 2
	}

	// Sign automatically when the persona's key exists; no key just means unsigned.
	keyPath, err := keystore.ResolveKeyFile(resolved.KeyFile.V, resolved.Realm.V, resolved.Persona.V)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream-mcp: %v\n", err)
		return 1
	}
	signer, err := keystore.LoadKey(keyPath)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream-mcp: %v\n", err)
		return 1
	}

	// The lane resolves from flag and environment only — never from a config
	// file, because a config file can never carry a credential: the sticker
	// names you, it can never be you.
	ctx := context.Background()
	rcfg := realm.Config{
		ContextName: resolved.Context.V, Realm: resolved.Realm.V, Persona: resolved.Persona.V,
		URL:       first(*url, os.Getenv("SOULSTREAM_URL")),
		CredsFile: first(*creds, os.Getenv("SOULSTREAM_CREDS")),
		Token:     first(*token, os.Getenv("SOULSTREAM_TOKEN")),
	}
	// An address handed over right now is a more specific answer than a context
	// saved on this machine, so it wins — and says so, rather than leaving
	// somebody to wonder which one they reached.
	if rcfg.URL != "" && rcfg.ContextName != "" {
		fmt.Fprintf(stderr, "soulstream-mcp: dialling %s; the %q context is not consulted\n",
			rcfg.URL, rcfg.ContextName)
		rcfg.ContextName = ""
	}
	// Assign only a real key: a typed-nil *SigningKey inside the interface
	// would read as "configured to sign" and panic at first use.
	if signer != nil {
		rcfg.Signer = signer
	}
	c, err := realm.Connect(ctx, rcfg)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream-mcp: %v\n", err)
		return 1
	}
	defer func() { _ = c.Close() }()

	if err := mcpserver.NewServer(c).Run(ctx, &mcp.StdioTransport{}); err != nil {
		fmt.Fprintf(stderr, "soulstream-mcp: %v\n", err)
		return 1
	}
	return 0
}

// first is the flag-then-environment precedence the lane resolves on, matching
// the identity fields' first-answer-wins rule. A variable that is set but empty
// counts as unset, as it does there.
func first(flagValue, env string) string {
	if flagValue != "" {
		return flagValue
	}
	return env
}
