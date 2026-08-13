package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

func cmdWatch(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: soulstream watch <path>")
		return 2
	}
	path := args[0]
	return withClient(ctx, connect, cfg, false, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		warnDistrusted(stdout, stderr, kr)
		h := topic.Open(c, path)
		h.UseKeyring(kr)
		printed := 0
		return h.Follow(ctx, func(v *topic.MaterializedTopic) {
			for i := printed; i < len(v.Contributions); i++ {
				renderContribution(stdout, v.Contributions[i])
			}
			printed = len(v.Contributions)
		})
	})
}

func cmdInbox(ctx context.Context, connect Connector, cfg Config, _ []string, stdout, stderr io.Writer) int {
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		kr := realmKeyring(ctx, c, cfg, stderr)
		warnDistrusted(stdout, stderr, kr)
		return topic.FollowInbox(ctx, c, cfg.Persona, kr, func(n topic.Notification) {
			fmt.Fprintf(stdout, "mention in %s (op %s) by%s %s\n", n.Topic, shortID(n.OpID), sigGlyph(n.Sig), n.Author)
		})
	})
}
