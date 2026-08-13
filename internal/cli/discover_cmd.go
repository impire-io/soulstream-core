package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

// cmdDiscover asks the realm "is there already a topic about X?" and renders the
// merged answers. Silence resolves to a friendly empty result — the board is the
// fallback that always works.
func cmdDiscover(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", topic.DefaultDiscoverTimeout, "how long to wait for answers")
	limit := fs.Int("limit", topic.DefaultDiscoverLimit, "per-answerer result cap")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream discover <query> [--timeout d] [--limit n] [--json]")
		return 2
	}
	query := fs.Arg(0)

	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		warnDistrusted(stdout, stderr, kr)

		results, err := topic.Discover(ctx, c, topic.DiscoverInput{
			Query: query, Limit: *limit, Timeout: *timeout,
		}, kr)
		if err != nil {
			return err
		}
		if *asJSON {
			if results == nil {
				results = []topic.DiscoverResult{}
			}
			return printJSON(stdout, results)
		}
		if len(results) == 0 {
			fmt.Fprintln(stdout, "no answers before the deadline (the board still works: soulstream board)")
			return nil
		}
		for _, r := range results {
			lc := string(r.Lifecycle)
			if lc == "" {
				lc = "-"
			}
			fmt.Fprintf(stdout, "%-9s %-40s %s\n", lc, r.Path, r.Name)
			fmt.Fprintf(stdout, "          answered by: %s\n", renderAnswers(r.Answers))
		}
		return nil
	})
}

// renderAnswers lists answerers with their verification glyphs.
func renderAnswers(answers []topic.DiscoverAnswer) string {
	out := ""
	for i, a := range answers {
		if i > 0 {
			out += ", "
		}
		out += a.Persona + sigGlyph(a.Sig)
	}
	return out
}

// cmdRespond serves discovery as this persona until interrupted: each request is
// answered from a fresh board projection, or met with silence when nothing matches.
func cmdRespond(ctx context.Context, connect Connector, cfg Config, _ []string, stdout, stderr io.Writer) int {
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		fmt.Fprintf(stdout, "responding to discovery as %q (Ctrl-C to stop)\n", cfg.Persona)
		return topic.RespondDiscovery(ctx, c, func(query string, sent int, err error) {
			if err != nil {
				fmt.Fprintf(stderr, "soulstream: could not serve %q: %v\n", query, err)
				return
			}
			if sent == 0 {
				fmt.Fprintf(stdout, "served %q: nothing to say\n", query)
				return
			}
			fmt.Fprintf(stdout, "served %q: %d matches\n", query, sent)
		})
	})
}
