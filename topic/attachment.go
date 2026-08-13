package topic

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
)

// Attach stores data in the realm's object store under attachments/<topic-path>/<uuid>
// and publishes an attachment.add referencing it by name, object key, digest, size, and
// content type. anchor may be "" (unanchored). An empty name is rejected; a zero-byte
// file is allowed.
func (h *Handle) Attach(ctx context.Context, name, contentType string, data []byte, anchor string) (opID string, err error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("topic: attachment name must not be empty")
	}
	// Check before the upload, not just at Post: refusing early spares the store an
	// orphan object.
	if h.lifecycle == Archived {
		return "", fmt.Errorf("topic: %s is archived — %w", h.path, ErrTopicArchived)
	}

	store, err := h.client.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		return "", fmt.Errorf("topic: open object store: %w", err)
	}

	object := "attachments/" + h.path + "/" + record.NewID()
	info, err := store.PutBytes(ctx, object, data)
	if err != nil {
		return "", fmt.Errorf("topic: store attachment: %w", err)
	}

	return h.Post(ctx, TypeAttachmentAdd, AttachmentPayload{
		Name:        name,
		Object:      object,
		Digest:      info.Digest,
		Size:        info.Size,
		ContentType: contentType,
		Anchor:      anchor,
	})
}

// GetAttachment fetches an attachment's bytes from the realm's object store by its object
// key. A missing object returns a clear not-found error.
func GetAttachment(ctx context.Context, c *realm.Client, object string) ([]byte, error) {
	store, err := c.JetStream().ObjectStore(ctx, realm.ObjectBucket)
	if err != nil {
		return nil, fmt.Errorf("topic: open object store: %w", err)
	}
	data, err := store.GetBytes(ctx, object)
	if err != nil {
		if errors.Is(err, jetstream.ErrObjectNotFound) {
			return nil, fmt.Errorf("topic: attachment %q not found: %w", object, err)
		}
		return nil, fmt.Errorf("topic: fetch attachment: %w", err)
	}
	return data, nil
}

// VerifyDigest reports whether data matches an object-store digest of the form
// "SHA-256=<base64url>".
func VerifyDigest(data []byte, digest string) bool {
	sum := sha256.Sum256(data)
	return digest == "SHA-256="+base64.URLEncoding.EncodeToString(sum[:])
}
