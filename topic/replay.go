package topic

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// drainOps reads all existing operations for a topic-path in stream order and returns
// them as SeqRecords. It returns an empty slice (no error) when the topic has no ops.
// Shared by Materialise, Follow (its initial replay), and the discovery board.
func drainOps(ctx context.Context, c *realm.Client, path string) ([]SeqRecord, error) {
	stream, err := c.JetStream().Stream(ctx, realm.StreamName)
	if err != nil {
		return nil, fmt.Errorf("topic: look up stream: %w", err)
	}
	subject := OpsSubject(path)

	// Empty guard: with no messages, an ordered consumer's Next() would block forever.
	if _, err := stream.GetLastMsgForSubject(ctx, subject); err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("topic: probe ops subject: %w", err)
	}

	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subject},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("topic: create consumer: %w", err)
	}
	it, err := cons.Messages()
	if err != nil {
		return nil, fmt.Errorf("topic: consume: %w", err)
	}
	defer it.Stop()

	var out []SeqRecord
	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				break
			}
			return nil, fmt.Errorf("topic: read op: %w", err)
		}
		md, err := msg.Metadata()
		if err != nil {
			return nil, fmt.Errorf("topic: message metadata: %w", err)
		}
		if rec, perr := record.Parse(msg.Headers(), msg.Data()); perr == nil {
			out = append(out, SeqRecord{Record: rec, StreamSeq: md.Sequence.Stream})
		}
		if md.NumPending == 0 { // last of the backlog
			break
		}
	}
	return out, nil
}
