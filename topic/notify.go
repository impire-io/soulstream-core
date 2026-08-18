package topic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// NotifySubjectPrefix is the prefix of a persona's notify (inbox) subject.
const NotifySubjectPrefix = "SOULSTREAM.PERSONA.NOTIFY."

// NotifySubject returns a persona's notify subject.
func NotifySubject(persona string) string { return NotifySubjectPrefix + persona }

// Notification is a received mention.notify.
type Notification struct {
	Topic  string    `json:"topic"`
	OpID   string    `json:"op_id"`
	Author string    `json:"author"`
	Sig    SigStatus `json:"sig,omitempty"` // verification status of the notify op itself
}

// publishNotify publishes a mention.notify record to a persona's inbox.
func publishNotify(ctx context.Context, c *realm.Client, persona string, payload NotifyPayload) error {
	if _, err := publishOp(ctx, c, NotifySubject(persona), TypeMentionNotify, payload, nil); err != nil {
		return fmt.Errorf("topic: notify %s: %w", persona, err)
	}
	return nil
}

// notifyStream looks up the realm's inbox stream. A realm provisioned before the
// inbox stream existed gets a clear pointer at the fix rather than a bare not-found.
func notifyStream(ctx context.Context, c *realm.Client) (jetstream.Stream, error) {
	stream, err := c.JetStream().Stream(ctx, realm.NotifyStreamName)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, fmt.Errorf("topic: this realm has no inbox stream yet — run `soulstream provision` to converge it: %w", err)
		}
		return nil, fmt.Errorf("topic: look up inbox stream: %w", err)
	}
	return stream, nil
}

// FetchInbox returns the persona's mention notifications, newest-first, capped at limit
// (a limit of 0 or less means the default of 50). It returns an empty slice (no error)
// when the inbox is empty. Unlike FollowInbox, it is a bounded one-shot read — the shape
// a request/response caller (such as the MCP adapter) needs. Each notification carries
// its verification status against kr (nil kr: signed notifies report unknown-key).
func FetchInbox(ctx context.Context, c *realm.Client, persona string, limit int, kr *identity.Keyring) ([]Notification, error) {
	if limit <= 0 {
		limit = 50
	}

	stream, err := notifyStream(ctx, c)
	if err != nil {
		return nil, err
	}
	subject := NotifySubject(persona)

	// Empty guard (an ordered consumer's Next() would otherwise block on an empty inbox).
	if _, err := stream.GetLastMsgForSubject(ctx, subject); err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("topic: probe inbox: %w", err)
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

	var all []Notification
	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				break
			}
			return nil, fmt.Errorf("topic: read inbox: %w", err)
		}
		md, err := msg.Metadata()
		if err != nil {
			return nil, fmt.Errorf("topic: message metadata: %w", err)
		}
		if rec, perr := record.Parse(msg.Headers(), msg.Data()); perr == nil && rec.Type == TypeMentionNotify {
			var np NotifyPayload
			if json.Unmarshal(rec.Payload, &np) == nil {
				all = append(all, Notification{
					Topic: np.Topic, OpID: np.OpID, Author: np.Author,
					Sig: VerifyRecord(rec, c.RealmKey(), persona, kr),
				})
			}
		}
		if md.NumPending == 0 {
			break
		}
	}

	// Newest-first, then cap at the limit.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// FollowInbox subscribes to persona's notify subject and calls onNotify for each
// mention.notify (history then live). It blocks until ctx is cancelled, then returns
// nil. Each notification carries its verification status against kr.
func FollowInbox(ctx context.Context, c *realm.Client, persona string, kr *identity.Keyring, onNotify func(Notification)) error {
	stream, err := notifyStream(ctx, c)
	if err != nil {
		return err
	}

	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{NotifySubject(persona)},
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
		stop()
	}()

	for {
		msg, err := it.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			return fmt.Errorf("topic: read notify: %w", err)
		}

		rec, perr := record.Parse(msg.Headers(), msg.Data())
		if perr != nil || rec.Type != TypeMentionNotify {
			continue
		}
		var np NotifyPayload
		if json.Unmarshal(rec.Payload, &np) != nil {
			continue
		}
		if onNotify != nil {
			onNotify(Notification{
				Topic: np.Topic, OpID: np.OpID, Author: np.Author,
				Sig: VerifyRecord(rec, c.RealmKey(), persona, kr),
			})
		}
	}
}
