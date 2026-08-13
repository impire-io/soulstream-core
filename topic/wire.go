package topic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// publishOp builds an operation record for the client's persona (author, fresh op-id,
// timestamp, given parents), serialises the payload, and publishes it to subject.
// Stamping the author as the client's own persona is the write-side attribution
// guarantee: the library cannot post as another persona.
func publishOp(ctx context.Context, c *realm.Client, subject, opType string, payload any, parents []string) (opID string, err error) {
	return publishOpWith(ctx, c, subject, opType, payload, parents, "", nil, nil)
}

// publishOpWith is publishOp plus transport extras: a pre-generated op-id (rollup
// names its manifest object after the baseline's id before publishing; "" generates
// one), additional NATS headers (e.g. the rollup marker), and JetStream publish
// options (e.g. the expected-last-subject-sequence guard). It shares the exact
// record-build and signing path, so an op published with extras is signed and
// attributed identically to any other.
func publishOpWith(ctx context.Context, c *realm.Client, subject, opType string, payload any, parents []string, presetID string, extraHeaders map[string]string, opts []jetstream.PublishOpt) (opID string, err error) {
	msg, opID, err := buildOpMsg(c, subject, canonicalBinding(subject), opType, payload, parents, presetID)
	if err != nil {
		return "", err
	}
	for k, v := range extraHeaders {
		msg.Header.Set(k, v)
	}
	if _, err := c.JetStream().PublishMsg(ctx, msg, opts...); err != nil {
		return "", fmt.Errorf("topic: publish %s: %w", opType, err)
	}
	return opID, nil
}

// buildOpMsg builds (and signs, when the client is keyed) an operation record as a
// ready-to-send NATS message. The canonical binding is explicit because it is not
// always derivable from the publish subject: a discovery reply travels on an
// ephemeral inbox but signs over the service name.
func buildOpMsg(c *realm.Client, subject, binding, opType string, payload any, parents []string, presetID string) (*nats.Msg, string, error) {
	author := c.Persona()
	if author == "" {
		return nil, "", fmt.Errorf("topic: a persona is required to post (client has none)")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("topic: marshal %s payload: %w", opType, err)
	}

	opID := presetID
	if opID == "" {
		opID = record.NewID()
	}
	rec := record.Record{
		ID:        opID,
		Author:    author,
		Parents:   parents,
		Type:      opType,
		Timestamp: time.Now().UTC(),
		Payload:   data,
	}

	// Sign when the client is configured to: the signature covers the canonical
	// record — the same bytes any reader can recompute from the wire form and the
	// binding — with the Signature field still empty (the canonical form omits an
	// empty sig). A configured signer that cannot produce a signature fails the
	// operation outright: publishing never falls back to unsigned, and an EMPTY
	// signature counts as a failure because it would silently travel as "unsigned".
	if signer := c.Signer(); signer != nil {
		canonical, cerr := rec.Canonical(c.Realm(), binding)
		if cerr != nil {
			return nil, "", fmt.Errorf("topic: canonicalise %s for signing: %w", opType, cerr)
		}
		sig, serr := signer.Sign(canonical)
		if serr != nil {
			return nil, "", fmt.Errorf("topic: sign %s: %w", opType, serr)
		}
		if sig == "" {
			return nil, "", fmt.Errorf("topic: sign %s: signer returned an empty signature", opType)
		}
		rec.Signature = sig
	}

	headers, body, err := rec.Build()
	if err != nil {
		return nil, "", fmt.Errorf("topic: build %s record: %w", opType, err)
	}
	return &nats.Msg{Subject: subject, Header: nats.Header(headers), Data: body}, opID, nil
}
