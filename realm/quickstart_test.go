package realm

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/record"
)

// TestQuickstartFlow mirrors specs/001-foundation/quickstart.md end to end, using a
// direct in-process connection in place of a named context: provision the realm,
// build a record, put it on the stream, read it back, and canonicalise it. If any
// public signature drifts from the quickstart, this test breaks.
func TestQuickstartFlow(t *testing.T) {
	ctx := context.Background()
	js, cleanup := newJS(t)
	defer cleanup()

	// 1. Provision a realm.
	report, err := ProvisionOn(ctx, js)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !report.Conformant() {
		t.Fatalf("provision report not conformant: %+v", report.Results)
	}

	// 2. Build an operation record and put it on the stream.
	rec := record.Record{
		ID:        record.NewID(),
		Author:    "daan",
		Type:      "turn.post",
		Timestamp: time.Now().UTC(),
		Payload:   []byte(`{"body":"hello soulstream"}`),
	}
	headers, payload, err := rec.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	subject := "SOULSTREAM.TOPICS.OPS.vat-q2-2026-x7m2"
	if _, err := js.PublishMsg(ctx, &nats.Msg{Subject: subject, Header: nats.Header(headers), Data: payload}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// 3. Read it back exactly.
	got, err := record.Parse(headers, payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ID != rec.ID || got.Author != rec.Author || got.Type != rec.Type {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// 4. Canonicalise for future signing.
	if _, err := rec.Canonical("acme", "vat-q2-2026-x7m2"); err != nil {
		t.Fatalf("canonical: %v", err)
	}
}
