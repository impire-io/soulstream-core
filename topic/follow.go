package topic

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// Follow materialises the topic, then keeps applying live operations, calling onOp with
// the updated view after each applied op. It uses a single ordered consumer that
// delivers history then live messages in one continuous stream, so there is no
// replay/live seam. It blocks until ctx is cancelled, then returns nil.
func (h *Handle) Follow(ctx context.Context, onOp func(*MaterializedTopic)) error {
	stream, err := h.client.JetStream().Stream(ctx, realm.StreamName)
	if err != nil {
		return fmt.Errorf("topic: look up stream: %w", err)
	}

	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{OpsSubject(h.path)},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("topic: create consumer: %w", err)
	}
	it, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("topic: consume: %w", err)
	}

	var stopOnce sync.Once
	stop := func() { stopOnce.Do(it.Stop) }
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // unblocks Next()
	}()

	var recs []SeqRecord
	var baselineBroken string
	statuses := map[string]SigStatus{}
	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				if ctx.Err() != nil {
					return nil // cancelled — normal termination
				}
				return err
			}
			return fmt.Errorf("topic: read op: %w", err)
		}

		md, err := msg.Metadata()
		if err != nil {
			return fmt.Errorf("topic: message metadata: %w", err)
		}
		rec, perr := record.Parse(msg.Headers(), msg.Data())
		if perr != nil {
			continue // skip an unparseable op
		}

		// Verify each op as it arrives, against its wire form (manifest resolution
		// below may rewrite the first record's payload).
		statuses[rec.ID] = VerifyRecord(rec, h.client.RealmKey(), h.path, h.keyring)

		recs = append(recs, SeqRecord{Record: rec, StreamSeq: md.Sequence.Stream})
		if len(recs) == 1 {
			if rerr := resolveBaseline(ctx, h.client, recs); rerr != nil {
				baselineBroken = rerr.Error()
			}
		}
		if baselineBroken != "" {
			// An unresolvable manifest baseline poisons the whole view: emitting a
			// fold without its baked content would be silent partial state.
			mt := &MaterializedTopic{Path: h.path, Lifecycle: Proposed, Malformed: baselineBroken}
			h.adopt(mt)
			if onOp != nil {
				onOp(mt)
			}
			continue
		}
		mt := apply(h.path, recs)
		annotateView(mt, statuses, recs[0].Record.ID)
		h.adopt(mt)
		if onOp != nil {
			onOp(mt)
		}
	}
}
