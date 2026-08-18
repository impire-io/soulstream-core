package realm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/record"
)

// The operation identity doubles as the idempotency key: republishing a record with
// the same Nats-Msg-Id inside the duplicate window lands exactly one message.
func TestPublishDedupByOpID(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	if _, err := ProvisionOn(ctx, js); err != nil {
		t.Fatalf("provision: %v", err)
	}

	rec := record.Record{
		ID:     record.NewID(),
		Author: "daan", Acting: "daan",
		Type:      "turn.post",
		Timestamp: time.Now().UTC(),
		Payload:   []byte(`{"body":"hello"}`),
	}
	headers, payload, err := rec.Build()
	if err != nil {
		t.Fatalf("build record: %v", err)
	}

	subject := "SOULSTREAM.TOPICS.OPS.dedup-test-x1y2"
	msg := &nats.Msg{Subject: subject, Header: nats.Header(headers), Data: payload}

	if _, err := js.PublishMsg(ctx, msg); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	ack2, err := js.PublishMsg(ctx, msg)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if !ack2.Duplicate {
		t.Error("second publish: PubAck.Duplicate = false, want true")
	}

	stream, err := js.Stream(ctx, StreamName)
	if err != nil {
		t.Fatalf("look up stream: %v", err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	if info.State.Msgs != 1 {
		t.Errorf("stream holds %d messages, want 1 (dedup by op-id failed)", info.State.Msgs)
	}
}
