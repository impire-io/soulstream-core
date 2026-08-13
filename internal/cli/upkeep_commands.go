package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/impire-io/soulstream-core/curator"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

func cmdReply(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "usage: soulstream reply <path> <comment-op-id> <body>")
		return 2
	}
	path, anchor, body := args[0], args[1], args[2]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		id, err := h.Reply(ctx, body, anchor)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, id)
		return nil
	})
}

func cmdEdit(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintln(stderr, "usage: soulstream edit <path> <op-id> <new body>")
		return 2
	}
	path, target, body := args[0], args[1], args[2]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		id, err := h.Edit(ctx, target, body)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, id)
		return nil
	})
}

func cmdResolve(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: soulstream resolve <path> <comment-op-id>")
		return 2
	}
	path, target := args[0], args[1]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		if _, err := h.Resolve(ctx, target); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "resolved", target)
		return nil
	})
}

func cmdDetach(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: soulstream detach <path> <attachment-op-id>")
		return 2
	}
	path, target := args[0], args[1]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h, err := openAndMaterialise(ctx, c, path)
		if err != nil {
			return err
		}
		if _, err := h.RemoveAttachment(ctx, target); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "withdrawn", target, "(bytes reclaimed when the topic is archived)")
		return nil
	})
}

func cmdMarkDormant(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mark-dormant", flag.ContinueOnError)
	fs.SetOutput(stderr)
	idle := fs.Duration("idle", curator.DefaultIdleWindow, "the realm's idle window")
	if err := parseInterspersed(fs, args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: soulstream mark-dormant <path> [--idle 336h]")
		return 2
	}
	path := fs.Arg(0)
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h := topic.Open(c, path)
		mt, err := h.Materialise(ctx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if !topic.DormantEligible(mt, *idle, now) {
			switch mt.Lifecycle {
			case topic.Dormant:
				fmt.Fprintln(stdout, "already dormant")
			case topic.Closed, topic.Archived:
				fmt.Fprintf(stdout, "not eligible: the topic is %s\n", mt.Lifecycle)
			default:
				fmt.Fprintf(stdout, "not idle: newest op %s ago, window %s — nothing posted\n",
					now.Sub(topic.NewestOpTs(mt)).Round(time.Minute), *idle)
			}
			return nil
		}
		if _, err := h.MarkDormant(ctx); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "marked dormant — any content op wakes it")
		return nil
	})
}
