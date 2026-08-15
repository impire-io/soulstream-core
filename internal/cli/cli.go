// Package cli implements the soulstream terminal client. The logic lives here (not in
// cmd/) so it is testable: Run takes an explicit context, output writers, and an
// injectable Connector, letting tests drive every command against an in-process server.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"github.com/impire-io/soulstream-core/internal/config"
	"github.com/impire-io/soulstream-core/internal/version"
)

// Config is the resolved connection configuration.
type Config struct {
	Context string
	Realm   string
	Persona string
	// KeyFile is the persona's signing-seed file ("" resolves env → default path).
	// When the resolved file exists, every write command signs its ops.
	KeyFile string
	// PinsFile is the realm's key-pin file ("" resolves env → default path). Pins
	// are the client's durable trust-on-first-use state.
	PinsFile string
}

const usageText = `soulstream — a Soulstream client

Usage:
  soulstream [--context <name>] [--realm <name>] [--persona <name>] <command> [args...]

Commands:
  provision [--budgets] [--budget-oplog s] [--budget-notify s]
            [--budget-personas s] [--budget-objects s]
                                     ensure the realm's artefacts; --budgets applies
                                     default byte roofs (sizes like 1GiB) so
                                     limit-enforced accounts work out of the box
  board [--json]                     list the topics on the board
  start <name> [--subject s] [--tag t]... [--parent path]
                                     start a topic; prints its path
  show <path> [--json]               print a topic's current state
  post <path> <body>                 post a turn (use @name to mention)
  comment <path> <op-id> <body>      comment, anchored to an op
  reply <path> <op-id> <body>        reply under a comment
  edit <path> <op-id> <body>         correct your own turn/comment/reply
  resolve <path> <op-id>             mark a comment settled (stays readable)
  attach <path> <file> [--type ct] [--anchor op-id]
                                     attach a file; prints its object key
  revise <path> <file> --of <artefact> [--type ct]
                                     attach a whole-file revision of an artefact
  artefacts <path> [ref] [--json]    list a topic's artefacts, or one's history
  get <object> <outfile> [--force]   download an attachment
  get <path> --artefact <ref> [--revision op-id] [-o outfile] [--force]
                                     download an artefact's tip (or a revision)
  work open|claim|done|abandon|list|show ...
                                     work items: tasks the log itself arbitrates
                                     (first claim in stream order wins)
  detach <path> <attachment-op-id>   withdraw a file (bytes reclaimed at archival)
  mark-dormant <path> [--idle 336h]  apply the idle rule; posts only when eligible
  close <path>                       close a topic (and tidy it up)
  rollup <path>                      compact a topic's history into a fresh baseline
  archive <path>                     archive a topic: final compaction, read-only forever
  watch <path>                       stream a topic live (Ctrl-C to stop)
  inbox                              stream your notifications live (Ctrl-C to stop)
  discover <query> [--timeout d] [--limit n] [--json]
                                     ask the realm: is there a topic about this?
  respond                            answer discovery asks from your own board view
                                     (Ctrl-C to stop)
  memory query <question> [--topics a,b] [--after ts] [--timeout d] [--json]
                                     ask whoever remembers; answers arrive graded
                                     (fact / testimony / gossip — checked, not trusted)
  memory fetch <path> <op-id> [-o file] [--timeout d] [--force] [--json]
                                     get an op as evidence: the stream first,
                                     then whoever kept it
  memory exhibit <path> <op-id> [-o file] [--force] [--json]
                                     export a live op as a portable exhibit
  memory verify <file>               check an exhibit offline against your pins
  curate [--idle 336h] [--scan-every 1m] [--mark-dormant] [--reclaim <dur>]
                                     run the curator: best discovery answers,
                                     duplicate flags, dormancy nudges — plus the
                                     opt-in sweeps (Ctrl-C to stop)
  key init                           create this persona's signing key
  key show                           print this persona's public signing key
  key rotate                         switch to a new key (old key endorses it)
  profile publish [--display-name n] [--description d] [--operated-by p]
                  [--attestation t]  publish/update this persona's directory profile
  profile attest <persona>           countersign that this persona operates another
                                     (prints a token the other includes at publish)
  profile show <persona>             print a persona's profile, operator claim,
                                     key chain, pin state
  config                             show the effective identity and where each
                                     value came from (never connects)
  version                            print the client version

Any other verb runs a soulstream-<verb> binary from PATH with the resolved
identity in its environment (soulstream wrap → soulstream-wrap).

Configuration (per field, the first source that answers wins):
  flag > environment > nearest .soulstream.json walking up from the working
  directory > config.json in the user's soulstream config dir
  --context   / SOULSTREAM_CONTEXT    named NATS context
  --realm     / SOULSTREAM_REALM      realm name
  --persona   / SOULSTREAM_PERSONA    persona (required for write commands)
  --key-file  / SOULSTREAM_KEY_FILE   signing-seed file (default: config dir; when
                                      present, published ops are signed)
  --pins-file / SOULSTREAM_PINS_FILE  key-pin file (default: config dir)
Run "soulstream config" to see every value and its source.
`

// Main wires os streams, a SIGINT-cancellable context, and the natscontext connector.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return Run(ctx, args, os.Stdout, os.Stderr, realmConnect)
}

// Run parses args, resolves config, connects via connect, and dispatches to a command,
// returning the process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, connect Connector) int {
	global := flag.NewFlagSet("soulstream", flag.ContinueOnError)
	global.SetOutput(stderr)
	global.Usage = func() { fmt.Fprint(stderr, usageText) }
	ctxName := global.String("context", "", "named NATS context")
	realmName := global.String("realm", "", "realm name")
	persona := global.String("persona", "", "persona name")
	keyFile := global.String("key-file", "", "signing-seed file (default: env, then config dir)")
	pinsFile := global.String("pins-file", "", "key-pin file (default: env, then config dir)")
	if err := global.Parse(args); err != nil {
		return 2
	}

	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	cmd, cmdArgs := rest[0], rest[1:]

	// version and help must always answer — they are the diagnostics people reach
	// for when configuration is broken, so they dispatch before resolution can fail.
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return 0
	}

	// Only flags the user actually passed enter the chain (a default no longer
	// swallows the environment, and files sit below both).
	explicit := config.File{}
	global.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "context":
			explicit.Context = *ctxName
		case "realm":
			explicit.Realm = *realmName
		case "persona":
			explicit.Persona = *persona
		case "key-file":
			explicit.KeyFile = *keyFile
		case "pins-file":
			explicit.PinsFile = *pinsFile
		}
	})
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 1
	}
	resolved, err := config.Resolve(explicit, cwd)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 1
	}
	cfg := Config{
		Context:  resolved.Context.V,
		Realm:    resolved.Realm.V,
		Persona:  resolved.Persona.V,
		KeyFile:  resolved.KeyFile.V,
		PinsFile: resolved.PinsFile.V,
	}

	switch cmd {
	case "provision":
		return cmdProvision(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "board":
		return cmdBoard(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "start":
		return cmdStart(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "show":
		return cmdShow(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "post":
		return cmdPost(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "comment":
		return cmdComment(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "reply":
		return cmdReply(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "edit":
		return cmdEdit(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "resolve":
		return cmdResolve(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "detach":
		return cmdDetach(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "mark-dormant":
		return cmdMarkDormant(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "attach":
		return cmdAttach(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "revise":
		return cmdRevise(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "artefacts":
		return cmdArtefacts(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "get":
		return cmdGet(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "work":
		return cmdWork(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "close":
		return cmdClose(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "rollup":
		return cmdRollup(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "archive":
		return cmdArchive(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "discover":
		return cmdDiscover(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "respond":
		return cmdRespond(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "memory":
		return cmdMemory(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "curate":
		return cmdCurate(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "watch":
		return cmdWatch(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "inbox":
		return cmdInbox(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "key":
		return cmdKey(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "profile":
		return cmdProfile(ctx, connect, cfg, cmdArgs, stdout, stderr)
	case "config":
		return cmdConfig(resolved, stdout)
	default:
		if code, ok := runExternal(ctx, cfg, cmd, cmdArgs, stdout, stderr); ok {
			return code
		}
		fmt.Fprintf(stderr, "soulstream: unknown command %q\n\n", cmd)
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

// runExternal is the CLI's external-subcommand seam (the git convention): a
// verb the switch above does not know is looked up as soulstream-<verb> on
// PATH and run with the resolved identity projected into its environment —
// resolution happened once, above, by the precedence rules people already
// configure, and the child reads the same SOULSTREAM_* names every other
// door reads. Built-ins always win (this runs only from the default arm),
// and a verb without a binary falls through to the usual usage error: the
// seam never turns a typo into a silent search.
func runExternal(ctx context.Context, cfg Config, verb string, args []string, stdout, stderr io.Writer) (int, bool) {
	bin, err := exec.LookPath("soulstream-" + verb)
	if err != nil {
		return 0, false
	}
	child := exec.CommandContext(ctx, bin, args...)
	child.Stdin = os.Stdin
	child.Stdout = stdout
	child.Stderr = stderr
	// Non-empty resolved fields layer over the parent environment; what the
	// parent carried and the CLI did not resolve passes through untouched.
	env := os.Environ()
	for name, v := range map[string]string{
		"SOULSTREAM_CONTEXT":   cfg.Context,
		"SOULSTREAM_REALM":     cfg.Realm,
		"SOULSTREAM_PERSONA":   cfg.Persona,
		"SOULSTREAM_KEY_FILE":  cfg.KeyFile,
		"SOULSTREAM_PINS_FILE": cfg.PinsFile,
	} {
		if v != "" {
			env = append(env, name+"="+v)
		}
	}
	child.Env = env
	if err := child.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), true
		}
		fmt.Fprintf(stderr, "soulstream: %s: %v\n", verb, err)
		return 1, true
	}
	return 0, true
}

// parseInterspersed parses fs allowing flags to appear before or after positional
// arguments (the stdlib flag package otherwise stops at the first positional). It uses
// the FlagSet's own definitions to know which flags consume a following value.
func parseInterspersed(fs *flag.FlagSet, args []string) error {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if !strings.Contains(a, "=") {
				if f := fs.Lookup(strings.TrimLeft(a, "-")); f != nil && !isBoolFlag(f) && i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
			continue
		}
		positionals = append(positionals, a)
	}
	return fs.Parse(append(flags, positionals...))
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// multiFlag collects a repeatable string flag (e.g. --tag).
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
