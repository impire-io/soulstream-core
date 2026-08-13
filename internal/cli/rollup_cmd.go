package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/topic"
)

// cmdRollup compacts a topic: history folds into a fresh baseline, the view stays
// identical, and a lost race is a retryable non-error state of the world.
func cmdRollup(ctx context.Context, connect Connector, cfg Config, args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: soulstream rollup <path>")
		return 2
	}
	path := args[0]
	return withClient(ctx, connect, cfg, true, stderr, func(c *realm.Client) error {
		h := topic.Open(c, path)
		before, err := h.Materialise(ctx)
		if err != nil {
			return err
		}
		folded := countTopicOps(before)

		_, err = h.Rollup(ctx)
		switch {
		case errors.Is(err, topic.ErrNothingToCompact):
			fmt.Fprintf(stdout, "nothing to compact in %s\n", path)
			return nil
		case err != nil:
			return err // ErrRollupLost carries its own retry advice
		}
		fmt.Fprintf(stdout, "compacted %s: %d ops folded into a new baseline\n", path, folded)
		return nil
	})
}

// countTopicOps approximates the ops a rollup folds: the visible elements plus the
// baseline itself. Informational only — the honest count lives in the log.
func countTopicOps(v *topic.MaterializedTopic) int {
	return 1 + len(v.Contributions) + len(v.Attachments)
}
