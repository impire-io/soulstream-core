package topic

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// ErrOpNotLive means the requested op is not in the stream — compacted by a rollup,
// or it never existed. Callers that want it anyway ask the realm's witnesses
// (FetchExhibit does both in order).
var ErrOpNotLive = errors.New("topic: op is not in the stream (compacted or never existed)")

// CaptureExhibit captures one live operation as a self-authenticating exhibit: a
// bounded ordered scan of the topic's own ops and info subjects for the op-id,
// keeping headers and payload verbatim. Verbatim bytes plus the canonical binding
// are exactly what makes the author's signature keep verifying wherever the
// document travels. Export never launders: a bad signature captures as a bad
// signature.
func CaptureExhibit(ctx context.Context, c *realm.Client, path, opID string) (record.Exhibit, error) {
	if path == "" || opID == "" {
		return record.Exhibit{}, fmt.Errorf("topic: capturing an exhibit needs a topic and an op-id")
	}
	stream, err := c.JetStream().Stream(ctx, realm.StreamName)
	if err != nil {
		return record.Exhibit{}, fmt.Errorf("topic: look up stream: %w", err)
	}

	// Empty guard per subject: with no messages at all, an ordered consumer's Next()
	// would block forever. Only subjects that hold something are scanned.
	var subjects []string
	for _, subject := range []string{OpsSubject(path), InfoSubject(path)} {
		if _, err := stream.GetLastMsgForSubject(ctx, subject); err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				continue
			}
			return record.Exhibit{}, fmt.Errorf("topic: probe %s: %w", subject, err)
		}
		subjects = append(subjects, subject)
	}
	if len(subjects) == 0 {
		return record.Exhibit{}, ErrOpNotLive
	}

	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: subjects,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return record.Exhibit{}, fmt.Errorf("topic: create consumer: %w", err)
	}
	it, err := cons.Messages()
	if err != nil {
		return record.Exhibit{}, fmt.Errorf("topic: consume: %w", err)
	}
	defer it.Stop()

	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				break
			}
			return record.Exhibit{}, fmt.Errorf("topic: read op: %w", err)
		}
		md, err := msg.Metadata()
		if err != nil {
			return record.Exhibit{}, fmt.Errorf("topic: message metadata: %w", err)
		}
		if h := msg.Headers(); h != nil && h.Get(record.HeaderMsgID) == opID {
			subject := msg.Subject()
			headers := make(map[string][]string, len(h))
			for k, v := range h {
				headers[k] = append([]string(nil), v...)
			}
			return record.Exhibit{
				Version: record.ExhibitVersion,
				Realm:   c.RealmKey(),
				Binding: canonicalBinding(subject),
				Subject: subject,
				Headers: headers,
				Payload: append([]byte(nil), msg.Data()...),
			}, nil
		}
		if md.NumPending == 0 { // last of the backlog
			break
		}
	}
	return record.Exhibit{}, ErrOpNotLive
}

// VerifyExhibit reconstructs the exhibit's operation and verifies its embedded
// signature against the author's validated key chain in kr — a pure check needing
// no realm connectivity (kr can come from a pins file alone). The error is
// non-nil only for a document that does not contain a readable operation;
// signature problems are verdicts, never errors.
func VerifyExhibit(e record.Exhibit, kr *identity.Keyring) (SigStatus, error) {
	rec, err := e.Record()
	if err != nil {
		return "", fmt.Errorf("topic: exhibit does not contain a readable operation: %w", err)
	}
	return VerifyRecord(rec, e.Realm, e.Binding, kr), nil
}
