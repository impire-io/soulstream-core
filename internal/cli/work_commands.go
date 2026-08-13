package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

const workUsage = `usage: soulstream work <subcommand>

  work open <path> <title> [--body text]   open a work item; prints its id
  work claim <path> <item-id>              claim it — the log decides who won
  work done <path> <item-id>               mark it done
  work abandon <path> <item-id>            let it go; the item reopens
  work list <path> [--json]                the topic's items: status, owner, title
  work show <path> <item-id> [--json]      one item: timeline and evidence
`

func cmdWork(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, workUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "open":
		return cmdWorkOpen(ctx, connect, cfg, rest, stdout, stderr)
	case "claim":
		return cmdWorkClaim(ctx, connect, cfg, rest, stdout, stderr)
	case "done":
		return cmdWorkDone(ctx, connect, cfg, rest, stdout, stderr)
	case "abandon":
		return cmdWorkAbandon(ctx, connect, cfg, rest, stdout, stderr)
	case "list":
		return cmdWorkList(ctx, connect, cfg, rest, stdout, stderr)
	case "show":
		return cmdWorkShow(ctx, connect, cfg, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "soulstream work: unknown subcommand %q\n\n", sub)
		fmt.Fprint(stderr, workUsage)
		return 2
	}
}

func cmdWorkOpen(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	body := fs.String("body", "", "item description (use @name to mention)")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: soulstream work open <path> <title> [--body text]")
		return 2
	}
	path, title := fs.Arg(0), fs.Arg(1)
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		id, err := h.OpenWork(ctx, title, *body)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, id)
		return nil
	})
}

func cmdWorkClaim(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: soulstream work claim <path> <item-id>")
		return 2
	}
	path, itemID := args[0], args[1]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		claimID, err := h.ClaimWork(ctx, itemID)
		if err != nil {
			return err
		}
		// Publishing cannot know it won — the log decides. Materialise and report.
		v, err := h.Materialise(ctx)
		if err != nil {
			return err
		}
		item, ok := findWorkItem(v, itemID)
		if !ok {
			return fmt.Errorf("work item %s not found in %s", itemID, path)
		}
		for _, ev := range item.Timeline {
			if ev.OpID != claimID {
				continue
			}
			switch {
			case !ev.Void:
				fmt.Fprintln(stdout, "claimed — you own it")
			case item.Status == topic.WorkDone:
				fmt.Fprintln(stdout, "void — the item is already done")
			case item.Owner != "":
				fmt.Fprintf(stdout, "void — owned by %s (your claim is recorded, changes nothing)\n", item.Owner)
			default:
				fmt.Fprintln(stdout, "void — the item was not open")
			}
			return nil
		}
		return fmt.Errorf("claim %s not visible in %s yet", claimID, path)
	})
}

func cmdWorkDone(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	return workRefCommand(ctx, connect, cfg, args, stdout, stderr, "done",
		func(h *topic.Handle, itemID string) (string, error) { return h.CompleteWork(ctx, itemID) })
}

func cmdWorkAbandon(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	return workRefCommand(ctx, connect, cfg, args, stdout, stderr, "abandoned (reopened)",
		func(h *topic.Handle, itemID string) (string, error) { return h.AbandonWork(ctx, itemID) })
}

func workRefCommand(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer,
	verdict string, publish func(*topic.Handle, string) (string, error)) int {
	if len(args) < 2 {
		fmt.Fprintf(stderr, "usage: soulstream work %s <path> <item-id>\n", verdictWord(verdict))
		return 2
	}
	path, itemID := args[0], args[1]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		if _, err := publish(h, itemID); err != nil {
			return err
		}
		fmt.Fprintln(stdout, verdict, itemID)
		return nil
	})
}

func verdictWord(verdict string) string {
	if verdict == "done" {
		return "done"
	}
	return "abandon"
}

func cmdWorkList(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream work list <path> [--json]")
		return 2
	}
	path := fs.Arg(0)
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		v, err := materialiseForRead(ctx, c, cfg, path, stderr)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(stdout, v.WorkItems)
		}
		for _, item := range v.WorkItems {
			owner := item.Owner
			if owner == "" {
				owner = "-"
			}
			fmt.Fprintf(stdout, "%-36s %-8s %-12s %s%s\n", item.ID, item.Status, owner, item.Title, sigGlyph(item.Sig))
		}
		return nil
	})
}

func cmdWorkShow(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: soulstream work show <path> <item-id> [--json]")
		return 2
	}
	path, itemID := fs.Arg(0), fs.Arg(1)
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		v, err := materialiseForRead(ctx, c, cfg, path, stderr)
		if err != nil {
			return err
		}
		item, ok := findWorkItem(v, itemID)
		if !ok {
			return fmt.Errorf("work item %s not found in %s", itemID, path)
		}
		if *asJSON {
			return printJSON(stdout, item)
		}
		renderWorkItem(stdout, v, item)
		return nil
	})
}

// findWorkItem accepts a full or shortened (unique-prefix) item id.
func findWorkItem(v *topic.MaterializedTopic, ref string) (topic.WorkItem, bool) {
	var match topic.WorkItem
	matches := 0
	for _, item := range v.WorkItems {
		if item.ID == ref {
			return item, true
		}
		if len(ref) >= 8 && len(item.ID) >= len(ref) && item.ID[:len(ref)] == ref {
			match, matches = item, matches+1
		}
	}
	return match, matches == 1
}

// materialiseForRead opens the topic with the realm keyring for a read-only view.
func materialiseForRead(ctx context.Context, c *realm.Client, cfg Config, path string, stderr io.Writer) (*topic.MaterializedTopic, error) {
	h := topic.Open(c, path)
	h.UseKeyring(realmKeyring(ctx, c, cfg, stderr))
	return h.Materialise(ctx)
}
