package record

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// recordsEqual compares two records field-by-field, treating nil and empty
// slices/maps as equal and comparing timestamps by instant.
func recordsEqual(a, b Record) bool {
	if a.ID != b.ID || a.Author != b.Author || a.Type != b.Type || a.Signature != b.Signature {
		return false
	}
	if !a.Timestamp.Equal(b.Timestamp) {
		return false
	}
	if len(a.Parents) != len(b.Parents) {
		return false
	}
	for i := range a.Parents {
		if a.Parents[i] != b.Parents[i] {
			return false
		}
	}
	if !bytes.Equal(a.Payload, b.Payload) {
		return false
	}
	if len(a.Extras) != len(b.Extras) {
		return false
	}
	for k, v := range a.Extras {
		if b.Extras[k] != v {
			return false
		}
	}
	return true
}

func TestRecordRoundTrip(t *testing.T) {
	ts := time.Date(2026, 7, 11, 14, 3, 22, 0, time.UTC)
	p1 := NewID()
	p2 := NewID()

	cases := []struct {
		name string
		rec  Record
	}{
		{"no-parents", Record{
			ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: ts,
			Payload: []byte(`{"body":"hello"}`),
		}},
		{"one-parent", Record{
			ID: NewID(), Author: "architect", Acting: "architect", Parents: []string{p1}, Type: "comment.add",
			Timestamp: ts, Payload: []byte(`{"body":"noted"}`),
		}},
		{"many-parents", Record{
			ID: NewID(), Author: "bookkeeper-agent", Acting: "bookkeeper-agent", Parents: []string{p1, p2}, Type: "baseline",
			Timestamp: ts, Payload: []byte(`{"state":{}}`),
		}},
		{"with-signature", Record{
			ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: ts,
			Signature: "ed25519-placeholder-sig", Payload: []byte(`{}`),
		}},
		{"with-unknown-headers", Record{
			ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: ts,
			Payload: []byte(`{}`),
			Extras:  map[string]string{"Soulstream-Experimental": "yes", "Soulstream-Trace": "abc"},
		}},
		{"subsecond-timestamp", Record{
			ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post",
			Timestamp: time.Date(2026, 7, 11, 14, 3, 22, 500_000_000, time.UTC),
			Payload:   []byte(`{}`),
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers, payload, err := tc.rec.Build()
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			got, err := Parse(headers, payload)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !recordsEqual(tc.rec, got) {
				t.Fatalf("round-trip mismatch:\n orig = %+v\n got  = %+v", tc.rec, got)
			}
		})
	}
}

// Empty parents must produce NO header; an absent header must parse to empty parents.
func TestParentsAbsentVsEmpty(t *testing.T) {
	ts := time.Date(2026, 7, 11, 14, 3, 22, 0, time.UTC)

	rec := Record{ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: ts, Payload: []byte(`{}`)}
	headers, _, err := rec.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, present := headers[HeaderParents]; present {
		t.Errorf("empty parents produced a %s header, want it absent", HeaderParents)
	}

	got, err := Parse(headers, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Parents) != 0 {
		t.Errorf("absent parents header parsed to %v, want empty", got.Parents)
	}
}

// Unknown Soulstream-* headers must survive a round-trip; Nats-Msg-Id must not leak
// into Extras.
func TestUnknownHeadersPreserved(t *testing.T) {
	ts := time.Date(2026, 7, 11, 14, 3, 22, 0, time.UTC)
	rec := Record{
		ID: NewID(), Author: "daan", Acting: "daan", Type: "turn.post", Timestamp: ts, Payload: []byte(`{}`),
		Extras: map[string]string{"Soulstream-Future": "42"},
	}
	headers, payload, err := rec.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, err := Parse(headers, payload)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Extras["Soulstream-Future"] != "42" {
		t.Errorf("unknown header not preserved: %v", got.Extras)
	}
	if _, leaked := got.Extras[HeaderMsgID]; leaked {
		t.Error("Nats-Msg-Id leaked into Extras")
	}
	if _, leaked := got.Extras[HeaderAuthor]; leaked {
		t.Error("known Soulstream header leaked into Extras")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	valid := func() map[string][]string {
		return map[string][]string{
			HeaderMsgID:   {NewID()},
			HeaderVersion: {"1"},
			HeaderAuthor:  {"daan"},
			HeaderType:    {"turn.post"},
			HeaderTs:      {"2026-07-11T14:03:22Z"},
		}
	}

	cases := []struct {
		name    string
		mutate  func(h map[string][]string)
		wantErr error
	}{
		{"missing-id", func(h map[string][]string) { delete(h, HeaderMsgID) }, ErrMissingField},
		{"missing-author", func(h map[string][]string) { delete(h, HeaderAuthor) }, ErrMissingField},
		{"missing-type", func(h map[string][]string) { delete(h, HeaderType) }, ErrMissingField},
		{"missing-ts", func(h map[string][]string) { delete(h, HeaderTs) }, ErrMissingField},
		{"missing-version", func(h map[string][]string) { delete(h, HeaderVersion) }, ErrMissingField},
		{"bad-version-value", func(h map[string][]string) { h[HeaderVersion] = []string{"3"} }, ErrBadVersion},
		{"non-integer-version", func(h map[string][]string) { h[HeaderVersion] = []string{"one"} }, ErrBadVersion},
		{"bad-timestamp", func(h map[string][]string) { h[HeaderTs] = []string{"yesterday"} }, ErrBadTimestamp},
		{"bad-author", func(h map[string][]string) { h[HeaderAuthor] = []string{"Bad Author"} }, ErrBadAuthor},
		{"bad-id", func(h map[string][]string) { h[HeaderMsgID] = []string{"not-a-uuid"} }, ErrBadID},
		{"bad-parent", func(h map[string][]string) { h[HeaderParents] = []string{"not-a-uuid"} }, ErrBadID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := valid()
			tc.mutate(h)
			_, err := Parse(h, nil)
			if err == nil {
				t.Fatalf("Parse(%s) = nil error, want %v", tc.name, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Parse(%s) error = %v, want errors.Is %v", tc.name, err, tc.wantErr)
			}
			var fe *FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("Parse(%s) error type = %T, want *FieldError", tc.name, err)
			}
		})
	}
}
