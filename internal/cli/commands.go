package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

func cmdProvision(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	fs.SetOutput(stderr)
	useDefaults := fs.Bool("budgets", false, "apply the default storage budgets (limit-enforced accounts work out of the box)")
	var opLog, notify, personas, objects sizeFlag
	fs.Var(&opLog, "budget-oplog", "op-log stream byte roof (e.g. 1GiB)")
	fs.Var(&notify, "budget-notify", "inbox stream byte roof (e.g. 64MiB)")
	fs.Var(&personas, "budget-personas", "persona directory byte roof (e.g. 64MiB)")
	fs.Var(&objects, "budget-objects", "attachment store byte roof (e.g. 512MiB)")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}

	// The switch fills exactly the artefacts no explicit flag names; flags alone
	// leave the rest unlimited (spec 016, clarification 2026-07-27).
	var budgets realm.Budgets
	if *useDefaults {
		budgets = realm.DefaultBudgets()
	}
	for _, f := range []struct {
		flag  *sizeFlag
		field *int64
	}{{&opLog, &budgets.OpLog}, {&notify, &budgets.Notify}, {&personas, &budgets.Personas}, {&objects, &budgets.Objects}} {
		if f.flag.set {
			*f.field = f.flag.bytes
		}
	}
	budgeted := *useDefaults || opLog.set || notify.set || personas.set || objects.set

	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		var report *realm.ProvisionReport
		var err error
		if budgeted {
			report, err = c.Provision(ctx, budgets)
		} else {
			report, err = c.Provision(ctx)
		}
		if err != nil {
			return err
		}
		renderReport(stdout, report)
		return nil
	})
}

func cmdBoard(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("board", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		entries, err := topic.Board(ctx, c)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(stdout, entries)
		}
		renderBoard(stdout, entries)
		return nil
	})
}

func cmdStart(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	subject := fs.String("subject", "", "subject matter")
	parent := fs.String("parent", "", "parent topic path")
	var tags multiFlag
	fs.Var(&tags, "tag", "tag (repeatable)")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream start <name> [--subject s] [--tag t]... [--parent path]")
		return 2
	}
	name := fs.Arg(0)
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := topic.StartTopic(ctx, c, topic.StartTopicInput{
			Name: name, SubjectMatter: *subject, Tags: tags, Parent: *parent,
		})
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, h.Path())
		return nil
	})
}

func cmdShow(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream show <path> [--json]")
		return 2
	}
	path := fs.Arg(0)
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		h := topic.Open(c, path)
		h.UseKeyring(kr)
		v, err := h.Materialise(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(stdout, v)
		}
		warnDistrusted(stdout, stderr, kr)
		renderView(stdout, v)
		return nil
	})
}

func cmdPost(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: soulstream post <path> <body>")
		return 2
	}
	path, body := args[0], args[1]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		id, err := h.PostTurn(ctx, body)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, id)
		return nil
	})
}

func cmdComment(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "usage: soulstream comment <path> <op-id> <body>")
		return 2
	}
	path, anchor, body := args[0], args[1], args[2]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		id, err := h.AddComment(ctx, body, anchor)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, id)
		return nil
	})
}

func cmdAttach(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ctype := fs.String("type", "application/octet-stream", "content type")
	anchor := fs.String("anchor", "", "anchor op-id")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: soulstream attach <path> <file> [--type ct] [--anchor op-id]")
		return 2
	}
	path, file := fs.Arg(0), fs.Arg(1)
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		opID, err := h.Attach(ctx, filepath.Base(file), *ctype, data, *anchor)
		if err != nil {
			return err
		}
		// Surface the object key (looked up from the fresh view) for later `get`.
		if v, err := h.Materialise(ctx); err == nil {
			for _, a := range v.Attachments {
				if a.OpID == opID {
					fmt.Fprintln(stdout, a.Object)
					return nil
				}
			}
		}
		fmt.Fprintln(stdout, opID)
		return nil
	})
}

func cmdGet(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "overwrite an existing file")
	artefact := fs.String("artefact", "", "fetch by artefact (root op-id, revision op-id, or name)")
	revision := fs.String("revision", "", "a specific revision's op-id (default: the tip)")
	out := fs.String("o", "", "output file (default: the revision's name)")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if *artefact != "" {
		if fs.NArg() < 1 {
			fmt.Fprintln(stderr, "usage: soulstream get <path> --artefact <ref> [--revision op-id] [-o outfile] [--force]")
			return 2
		}
		path := fs.Arg(0)
		return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
			return getArtefact(ctx, c, cfg, stdout, stderr, path, *artefact, *revision, *out, *force)
		})
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: soulstream get <object> <outfile> [--force]")
		return 2
	}
	object, outfile := fs.Arg(0), fs.Arg(1)
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		if !*force {
			if _, err := os.Stat(outfile); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", outfile)
			}
		}
		// GetAttachment returning success means the object store verified the digest.
		data, err := topic.GetAttachment(ctx, c, object)
		if err != nil {
			return err
		}
		if err := os.WriteFile(outfile, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s (%d bytes, integrity verified)\n", outfile, len(data))
		return nil
	})
}

func cmdClose(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: soulstream close <path>")
		return 2
	}
	path := args[0]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		if _, err := h.Close(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "closed", path)
		return nil
	})
}

func cmdArchive(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: soulstream archive <path>")
		return 2
	}
	path := args[0]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h := topic.Open(c, path)
		if _, err := h.Archive(ctx); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "archived %s: final baseline published; the topic is now read-only\n", path)
		return nil
	})
}
