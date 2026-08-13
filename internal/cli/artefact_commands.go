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

func cmdArtefacts(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("artefacts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream artefacts <path> [ref] [--json]")
		return 2
	}
	path := fs.Arg(0)
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		v, err := materialiseForRead(ctx, c, cfg, path, stderr)
		if err != nil {
			return err
		}
		if fs.NArg() >= 2 {
			// One artefact's history.
			a, err := topic.FindArtefact(v, fs.Arg(1))
			if err != nil {
				return err
			}
			if *asJSON {
				return printJSON(stdout, a)
			}
			renderArtefactHistory(stdout, a)
			return nil
		}
		arts := v.Artefacts()
		if *asJSON {
			return printJSON(stdout, arts)
		}
		for _, a := range arts {
			fmt.Fprintf(stdout, "%-36s %-24s %2d revisions  tip by %s%s\n",
				a.Root, a.Name, len(a.Revisions), a.Tip.Author, sigGlyph(a.Tip.Sig))
		}
		return nil
	})
}

func cmdRevise(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("revise", flag.ContinueOnError)
	fs.SetOutput(stderr)
	of := fs.String("of", "", "artefact to revise (root op-id, revision op-id, or name)")
	ctype := fs.String("type", "", "content type (default: the tip's)")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 2 || *of == "" {
		fmt.Fprintln(stderr, "usage: soulstream revise <path> <file> --of <artefact> [--type ct]")
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
		v, err := h.Materialise(ctx)
		if err != nil {
			return err
		}
		a, err := topic.FindArtefact(v, *of)
		if err != nil {
			return err
		}
		contentType := *ctype
		if contentType == "" {
			contentType = a.Tip.ContentType
		}
		opID, err := h.Revise(ctx, filepath.Base(file), contentType, data, a.Tip.OpID)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "revised %s (op %s, supersedes %s)\n", a.Name, opID, a.Tip.OpID)
		return nil
	})
}

// getArtefact is `get <path> --artefact <ref>`: fetch the tip (or a chosen
// revision) of an artefact, digest-verified, into outfile.
func getArtefact(ctx context.Context, c *realm.Client, cfg Config, stdout, stderr io.Writer,
	path, ref, revision, outfile string, force bool) error {
	v, err := materialiseForRead(ctx, c, cfg, path, stderr)
	if err != nil {
		return err
	}
	a, err := topic.FindArtefact(v, ref)
	if err != nil {
		return err
	}
	rev := a.Tip
	if revision != "" {
		found := false
		for _, r := range a.Revisions {
			if r.OpID == revision {
				rev, found = r, true
				break
			}
		}
		if !found {
			return fmt.Errorf("artefact %s has no revision %s", a.Root, revision)
		}
	}
	if outfile == "" {
		outfile = filepath.Base(rev.Name)
	}
	if !force {
		if _, err := os.Stat(outfile); err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", outfile)
		}
	}
	data, err := topic.GetAttachment(ctx, c, rev.Object)
	if err != nil {
		return err
	}
	if !topic.VerifyDigest(data, rev.Digest) {
		return fmt.Errorf("revision %s does not match its recorded digest — refusing to write", rev.OpID)
	}
	if err := os.WriteFile(outfile, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (%d bytes, revision %s, integrity verified)\n", outfile, len(data), shortID(rev.OpID))
	return nil
}

func renderArtefactHistory(w io.Writer, a topic.Artefact) {
	fmt.Fprintf(w, "artefact:  %s (root %s)\n", a.Name, a.Root)
	fmt.Fprintf(w, "revisions: %d\n", len(a.Revisions))
	for _, r := range a.Revisions {
		marker := attachmentMark(r)
		if r.OpID == a.Tip.OpID {
			marker += "  <- tip"
		}
		fmt.Fprintf(w, "  [%s]%s %-12s %s  %s (%d bytes)%s\n",
			shortID(r.OpID), sigGlyph(r.Sig), r.Author, r.Timestamp.Format("2006-01-02 15:04"), r.Name, r.Size, marker)
	}
}
