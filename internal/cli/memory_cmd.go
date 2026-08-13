package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
	"github.com/impire-io/soulstream-core/topic"
)

const memoryUsage = `usage: soulstream memory <subcommand>
  memory query <question> [--topics a,b] [--after RFC3339] [--timeout d] [--json]
  memory fetch <path> <op-id> [-o file] [--timeout d] [--force] [--json]
  memory exhibit <path> <op-id> [-o file] [--force] [--json]
  memory verify <file>
`

// cmdMemory dispatches the memory subcommands: ask the realm (query), obtain an
// op as evidence (fetch, exhibit), and check evidence offline (verify).
func cmdMemory(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, memoryUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "query":
		return cmdMemoryQuery(ctx, connect, cfg, rest, stdout, stderr)
	case "fetch":
		return cmdMemoryFetch(ctx, connect, cfg, rest, stdout, stderr)
	case "exhibit":
		return cmdMemoryExhibit(ctx, connect, cfg, rest, stdout, stderr)
	case "verify":
		// Deliberately connection-free: evidence must check anywhere, including on
		// a machine that has never seen the realm.
		return cmdMemoryVerify(cfg, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "soulstream: unknown memory subcommand %q\n\n", sub)
		fmt.Fprint(stderr, memoryUsage)
		return 2
	}
}

// cmdMemoryQuery asks whoever remembers and renders the answers graded: every
// citation is checked against the realm before it is shown, and an uncited answer
// is labelled the gossip it is.
func cmdMemoryQuery(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", topic.DefaultMemoryTimeout, "how long to listen for answers")
	topicsFlag := fs.String("topics", "", "comma-separated topic-name patterns (relevance hint for witnesses)")
	afterFlag := fs.String("after", "", "RFC3339 interest horizon (relevance hint for witnesses)")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream memory query <question> [--topics a,b] [--after RFC3339] [--timeout d] [--json]")
		return 2
	}
	query := fs.Arg(0)

	var topics []string
	if strings.TrimSpace(*topicsFlag) != "" {
		for _, t := range strings.Split(*topicsFlag, ",") {
			if t = strings.TrimSpace(t); t != "" {
				topics = append(topics, t)
			}
		}
	}
	var after time.Time
	if *afterFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *afterFlag)
		if err != nil {
			fmt.Fprintf(stderr, "soulstream: --after must be RFC3339 (e.g. 2026-04-01T00:00:00Z): %v\n", err)
			return 2
		}
		after = parsed
	}

	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		warnDistrusted(stdout, stderr, kr)

		res, err := topic.MemoryQuery(ctx, c, topic.MemoryQueryInput{
			Query: query, Topics: topics, After: after, Timeout: *timeout,
		}, kr)
		if err != nil {
			return err
		}
		if *asJSON {
			if res.Answers == nil {
				res.Answers = []topic.MemoryAnswer{}
			}
			return printJSON(stdout, res)
		}
		if len(res.Answers) == 0 {
			fmt.Fprintln(stdout, "no answers before the deadline (silence is an answer — perhaps nobody keeps memory yet)")
			return nil
		}
		for i, a := range res.Answers {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			coverage := "coverage undeclared"
			if !a.CoverageFrom.IsZero() {
				coverage = "coverage from " + a.CoverageFrom.Format("2006-01-02")
			}
			fmt.Fprintf(stdout, "WITNESS %s%s  (%s)\n", a.Witness, sigGlyph(a.Sig), coverage)
			fmt.Fprintf(stdout, "  %s\n", a.Answer)
			if len(a.Citations) == 0 {
				fmt.Fprintf(stdout, "  [%s]  (no citations — a lead, never a decision)\n", topic.GradeGossip)
				continue
			}
			for _, cit := range a.Citations {
				line := fmt.Sprintf("  [%s]  %s / %s", cit.Grade, cit.Topic, cit.OpID)
				if cit.Grade == topic.GradeUnverifiable {
					line += "   (compacted or fabricated — try: soulstream memory fetch " + cit.Topic + " " + cit.OpID + ")"
				}
				fmt.Fprintln(stdout, line)
			}
		}
		return nil
	})
}

// cmdMemoryFetch obtains one op as an exhibit: from the stream when it is still
// there, from whoever kept it otherwise. Exit 1 means nobody (still) holds it.
func cmdMemoryFetch(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory fetch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", topic.DefaultMemoryTimeout, "how long to wait for witnesses")
	outfile := fs.String("o", "", "write the exhibit document to a file")
	force := fs.Bool("force", false, "overwrite an existing output file")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: soulstream memory fetch <path> <op-id> [-o file] [--timeout d] [--force] [--json]")
		return 2
	}
	path, opID := fs.Arg(0), fs.Arg(1)

	notFound := false
	code := withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		warnDistrusted(stdout, stderr, kr)

		res, err := topic.FetchExhibit(ctx, c, path, opID, *timeout, kr)
		if err != nil {
			return err
		}
		if res == nil {
			notFound = true
			if *asJSON {
				return printJSON(stdout, map[string]any{"found": false})
			}
			fmt.Fprintln(stdout, "no exhibit before the deadline — the op is compacted and nobody (still) holds it")
			return nil
		}
		return renderExhibitResult(stdout, res, *outfile, *force, *asJSON)
	})
	if code != 0 {
		return code
	}
	if notFound {
		return 1
	}
	return 0
}

// cmdMemoryExhibit exports a live op as a portable exhibit — the deliberate
// "export what we have" verb. A compacted op points at memory fetch instead.
func cmdMemoryExhibit(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory exhibit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outfile := fs.String("o", "", "write the exhibit document to a file")
	force := fs.Bool("force", false, "overwrite an existing output file")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: soulstream memory exhibit <path> <op-id> [-o file] [--force] [--json]")
		return 2
	}
	path, opID := fs.Arg(0), fs.Arg(1)

	notLive := false
	code := withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		warnDistrusted(stdout, stderr, kr)

		ex, err := topic.CaptureExhibit(ctx, c, path, opID)
		if errors.Is(err, topic.ErrOpNotLive) {
			notLive = true
			fmt.Fprintf(stderr, "soulstream: %v\n", err)
			fmt.Fprintf(stderr, "soulstream: a witness may still hold it — try: soulstream memory fetch %s %s\n", path, opID)
			return nil
		}
		if err != nil {
			return err
		}
		verdict, err := topic.VerifyExhibit(ex, kr)
		if err != nil {
			return err
		}
		return renderExhibitResult(stdout, &topic.ExhibitResult{Exhibit: ex, Verdict: verdict, Source: "live"}, *outfile, *force, *asJSON)
	})
	if code != 0 {
		return code
	}
	if notLive {
		return 1
	}
	return 0
}

// cmdMemoryVerify checks an exhibit file offline: no connection, no directory —
// only the document and the user's pinned keys. Evidence must check anywhere.
func cmdMemoryVerify(cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream memory verify <file>")
		return 2
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	ex, err := record.ParseExhibit(data)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}

	kr := pinsKeyring(cfg)
	if cfg.Realm != "" && ex.Realm != cfg.Realm {
		fmt.Fprintf(stderr, "soulstream: note: the exhibit is from realm %q; your pins are for %q\n", ex.Realm, cfg.Realm)
	}
	verdict, err := topic.VerifyExhibit(ex, kr)
	if err != nil {
		fmt.Fprintf(stderr, "soulstream: %v\n", err)
		return 2
	}
	printExhibitFacts(stdout, ex, verdict)
	switch verdict {
	case topic.SigFailed:
		fmt.Fprintln(stderr, "soulstream: the signature does NOT verify — the document was altered or never said this")
		return 1
	case topic.SigUnknownKey:
		fmt.Fprintln(stderr, "soulstream: the author's key is not among your pins — verify on a machine that knows this realm, or pass --pins-file")
	case topic.SigUnsigned:
		fmt.Fprintln(stderr, "soulstream: the op was never signed — the content is only as trustworthy as whoever kept it")
	}
	return 0
}

// pinsKeyring builds an offline keyring from the user's pin file alone — the
// verifier's own key knowledge, no directory, no connection. Nothing pinned (or
// no realm configured) yields nil: signed evidence degrades to unknown-key.
func pinsKeyring(cfg Config) *identity.Keyring {
	if cfg.Realm == "" {
		return nil
	}
	path, err := keystore.ResolvePinsFile(cfg.PinsFile, cfg.Realm)
	if err != nil {
		return nil
	}
	pins, err := keystore.LoadPins(path, cfg.Realm)
	if err != nil || len(pins.Personas) == 0 {
		return nil
	}
	return &identity.Keyring{Keys: pins.Personas}
}

// renderExhibitResult prints the verdict and provenance, optionally writing the
// document to a file (overwrite-guarded, like every download in this client).
func renderExhibitResult(stdout io.Writer, res *topic.ExhibitResult, outfile string, force, asJSON bool) error {
	if asJSON {
		return printJSONExhibit(stdout, res, outfile, force)
	}
	printExhibitFacts(stdout, res.Exhibit, res.Verdict)
	fmt.Fprintf(stdout, "source:  %s\n", res.Source)
	if outfile != "" {
		if err := writeExhibitFile(outfile, res.Exhibit, force); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", outfile)
	}
	return nil
}

func printJSONExhibit(stdout io.Writer, res *topic.ExhibitResult, outfile string, force bool) error {
	if outfile != "" {
		if err := writeExhibitFile(outfile, res.Exhibit, force); err != nil {
			return err
		}
	}
	return printJSON(stdout, map[string]any{
		"found": true, "verdict": res.Verdict, "source": res.Source, "exhibit": res.Exhibit,
	})
}

// printExhibitFacts prints what the document itself establishes.
func printExhibitFacts(stdout io.Writer, ex record.Exhibit, verdict topic.SigStatus) {
	rec, err := ex.Record()
	if err != nil {
		return // callers verify first; an unreadable record never reaches here
	}
	fmt.Fprintf(stdout, "verdict: %s\n", verdict)
	fmt.Fprintf(stdout, "author:  %s\n", rec.Author)
	fmt.Fprintf(stdout, "realm:   %s\n", ex.Realm)
	fmt.Fprintf(stdout, "topic:   %s\n", ex.Binding)
	fmt.Fprintf(stdout, "type:    %s\n", rec.Type)
	fmt.Fprintf(stdout, "ts:      %s\n", rec.Timestamp.Format(time.RFC3339))
	fmt.Fprintf(stdout, "op:      %s\n", rec.ID)
}

// writeExhibitFile writes the exhibit's document form, pretty-printed for humans,
// refusing to overwrite unless forced.
func writeExhibitFile(outfile string, ex record.Exhibit, force bool) error {
	if !force {
		if _, err := os.Stat(outfile); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", outfile)
		}
	}
	compact, err := ex.Marshal()
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact, "", "  "); err != nil {
		return err
	}
	pretty.WriteByte('\n')
	return os.WriteFile(outfile, pretty.Bytes(), 0o644)
}
