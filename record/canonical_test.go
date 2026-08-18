package record

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

var fixedTS = time.Date(2026, 7, 11, 14, 3, 22, 0, time.UTC)

// SC-004: equal content in different orders yields byte-identical canonical output.
func TestCanonicalDeterministic(t *testing.T) {
	id := NewID()
	rec := func(payload string) Record {
		return Record{ID: id, Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: fixedTS, Payload: []byte(payload)}
	}

	// Same payload, keys supplied in different orders.
	a, err := rec(`{"a":1,"b":2}`).Canonical("acme", "topic-x1y2")
	if err != nil {
		t.Fatalf("Canonical a: %v", err)
	}
	b, err := rec(`{"b":2,"a":1}`).Canonical("acme", "topic-x1y2")
	if err != nil {
		t.Fatalf("Canonical b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("payload key order changed canonical output:\n a=%s\n b=%s", a, b)
	}

	// Canonicalising the same record twice is stable.
	c, _ := rec(`{"a":1,"b":2}`).Canonical("acme", "topic-x1y2")
	if !bytes.Equal(a, c) {
		t.Error("canonical output is not stable across calls")
	}
}

// FR-022: realm and topic are bound — changing either changes the bytes.
func TestCanonicalBindsRealmAndTopic(t *testing.T) {
	rec := Record{ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: fixedTS, Payload: []byte(`{}`)}

	c1, err := rec.Canonical("acme", "topic-a")
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := rec.Canonical("other-realm", "topic-a")
	c3, _ := rec.Canonical("acme", "topic-b")

	if bytes.Equal(c1, c2) {
		t.Error("changing realm did not change canonical output (realm not bound)")
	}
	if bytes.Equal(c1, c3) {
		t.Error("changing topic did not change canonical output (topic not bound)")
	}
}

// FR-021: every wire field maps into exactly one canonical field, correctly.
func TestCanonicalLossless(t *testing.T) {
	parent := NewID()
	rec := Record{
		ID: NewID(), Author: "architect", Acting: "architect", Parents: []string{parent}, Type: "comment.add",
		Timestamp: fixedTS, Signature: "sig-123", Payload: []byte(`{"body":"x"}`),
	}

	c, err := rec.Canonical("acme", "vat.pricing")
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	var got canonicalRecord
	if err := json.Unmarshal(c, &got); err != nil {
		t.Fatalf("unmarshal canonical: %v", err)
	}
	if got.V != Version {
		t.Errorf("v = %d, want %d", got.V, Version)
	}
	if got.Realm != "acme" || got.Topic != "vat.pricing" {
		t.Errorf("realm/topic = %q/%q, want acme/vat.pricing", got.Realm, got.Topic)
	}
	if got.ID != rec.ID || got.Author != rec.Author || got.Type != rec.Type {
		t.Errorf("id/author/type mismatch: %+v", got)
	}
	if got.Sig != rec.Signature {
		t.Errorf("sig = %q, want %q", got.Sig, rec.Signature)
	}
	if len(got.Parents) != 1 || got.Parents[0] != parent {
		t.Errorf("parents = %v, want [%s]", got.Parents, parent)
	}
	if got.Ts != rec.Timestamp.Format(time.RFC3339Nano) {
		t.Errorf("ts = %q, want %q", got.Ts, rec.Timestamp.Format(time.RFC3339Nano))
	}
	if string(got.Data) != `{"body":"x"}` {
		t.Errorf("data = %s, want {\"body\":\"x\"}", got.Data)
	}
}

// FR-023: an empty signature is omitted; empty parents encode as [] not null.
func TestCanonicalOmitsEmptySigAndEmptyParents(t *testing.T) {
	rec := Record{ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: fixedTS, Payload: []byte(`{}`)}
	c, err := rec.Canonical("acme", "topic-a")
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(c, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["sig"]; present {
		t.Error("sig key present when signature is empty")
	}
	if string(m["parents"]) != "[]" {
		t.Errorf("parents = %s, want []", m["parents"])
	}
}

func TestCanonicalRejectsNonJSONPayload(t *testing.T) {
	rec := Record{ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: fixedTS, Payload: []byte("not json")}
	if _, err := rec.Canonical("acme", "topic-a"); err == nil {
		t.Error("Canonical with non-JSON payload: got nil, want ErrBadPayload")
	}
}
