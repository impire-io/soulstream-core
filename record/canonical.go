package record

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gowebpki/jcs"
)

// ErrBadPayload indicates the payload is not valid JSON, so it cannot be represented
// as the canonical record's data value.
var ErrBadPayload = errors.New("record: payload is not valid JSON")

// canonicalRecord is the logical object that gets canonicalised. Field order here is
// irrelevant — JCS sorts keys — but the json tags fix the key names.
type canonicalRecord struct {
	V       int             `json:"v"`
	Realm   string          `json:"realm"`
	Acting  string          `json:"acting"`
	Topic   string          `json:"topic"`
	ID      string          `json:"id"`
	Author  string          `json:"author"`
	Parents []string        `json:"parents"`
	Ts      string          `json:"ts"`
	Type    string          `json:"type"`
	Data    json.RawMessage `json:"data"`
	Sig     string          `json:"sig,omitempty"`
}

// Canonical produces the RFC 8785 (JCS) canonical byte sequence of the record bound
// to the realm IDENTITY (its cryptographic key since v2 — A10: a
// name-scoped signature could be replayed into a realm reusing the
// name; the key scopes it to the trust root) and topic. Two records equal in content produce byte-identical output
// regardless of the order their fields (or their payload's JSON keys) were supplied —
// that stability is what makes the bytes a stable signing input. Binding realm and
// topic prevents an operation being re-presented as belonging to another realm or
// topic.
//
// The payload becomes the canonical record's data value, so it must be valid JSON (an
// empty payload becomes null). Producing or verifying a signature is out of scope; the
// Signature field, if set, is carried into the "sig" key.
func (r Record) Canonical(realm, topic string) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if realm == "" {
		return nil, missingField("realm")
	}
	if topic == "" {
		return nil, missingField("topic")
	}

	var data json.RawMessage
	if len(r.Payload) > 0 {
		if !json.Valid(r.Payload) {
			return nil, ErrBadPayload
		}
		data = json.RawMessage(r.Payload)
	}

	parents := r.Parents
	if parents == nil {
		parents = []string{} // encode no-parents as [], never null
	}

	raw, err := json.Marshal(canonicalRecord{
		V:       Version,
		Realm:   realm,
		Acting:  r.Acting,
		Topic:   topic,
		ID:      r.ID,
		Author:  r.Author,
		Parents: parents,
		Ts:      r.Timestamp.Format(time.RFC3339Nano),
		Type:    r.Type,
		Data:    data,
		Sig:     r.Signature,
	})
	if err != nil {
		return nil, err
	}

	return jcs.Transform(raw)
}
