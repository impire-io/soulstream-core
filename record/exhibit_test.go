package record

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func testExhibit(t *testing.T) Exhibit {
	t.Helper()
	rec := Record{
		ID:        NewID(),
		Author:    "daan",
		Acting:    "daan",
		Type:      "turn.post",
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Signature: "c2lnbmF0dXJl",
		Payload:   []byte(`{"body":"weekly cadence"}`),
		Extras:    map[string]string{"Soulstream-Future": "kept"},
	}
	headers, payload, err := rec.Build()
	if err != nil {
		t.Fatalf("build record: %v", err)
	}
	return Exhibit{
		Version: ExhibitVersion,
		Realm:   "test-realm",
		Binding: "vat-q2-x7m2",
		Subject: "SOULSTREAM.TOPICS.OPS.vat-q2-x7m2",
		Headers: headers,
		Payload: payload,
	}
}

func TestExhibitRoundTripIsByteIdentical(t *testing.T) {
	e := testExhibit(t)
	first, err := e.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseExhibit(first)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	second, err := parsed.Marshal()
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("round-trip not byte-identical:\n%s\n%s", first, second)
	}
}

func TestExhibitRecordReconstructs(t *testing.T) {
	e := testExhibit(t)
	rec, err := e.Record()
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if rec.Author != "daan" || rec.Type != "turn.post" || rec.Signature != "c2lnbmF0dXJl" {
		t.Errorf("reconstructed record lost fields: %+v", rec)
	}
	if string(rec.Payload) != `{"body":"weekly cadence"}` {
		t.Errorf("payload = %s", rec.Payload)
	}
	if rec.Extras["Soulstream-Future"] != "kept" {
		t.Errorf("unknown Soulstream-* headers must survive capture: %+v", rec.Extras)
	}
}

func TestParseExhibitIsStrict(t *testing.T) {
	valid, err := testExhibit(t).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	cases := []struct {
		name string
		doc  string
		want string // substring of the error
	}{
		{"unknown field", strings.Replace(string(valid), `"realm"`, `"sneaky":1,"realm"`, 1), "unknown field"},
		{"wrong version", strings.Replace(string(valid), `"version":1`, `"version":2`, 1), "version 2 unsupported"},
		{"missing realm", strings.Replace(string(valid), `"test-realm"`, `""`, 1), "missing realm"},
		{"missing binding", strings.Replace(string(valid), `"vat-q2-x7m2"`, `""`, 1), "missing binding"},
		{"missing headers", `{"version":1,"realm":"r","binding":"b","payload_b64":""}`, "missing headers"},
		{"trailing data", string(valid) + `{"more":true}`, "trailing data"},
		{"not json", "definitely not json", "parse exhibit"},
	}
	for _, c := range cases {
		if _, err := ParseExhibit([]byte(c.doc)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention of %q", c.name, err, c.want)
		}
	}
}
