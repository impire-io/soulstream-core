package toolcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/realm"
)

// BucketName is the catalog's KV bucket. Deliberately NOT provisioned:
// this is an extension, and the bucket is created by whoever writes the
// first entry.
const BucketName = "soulstream-tools"

// history is how many revisions of an entry the bucket keeps — enough to
// see a recent edit, nothing anyone relies on.
const history = 5

// EntryWarning names a catalog entry a bulk read skipped because its
// stored document is unreadable. Bulk readers warn loudly and continue —
// one broken entry must not hide the catalog.
type EntryWarning struct {
	Name string
	Err  error
}

// Publish creates or updates one entry. Create-or-update rides the KV's
// own optimistic concurrency, so racing writers get an error, never a
// lost write. The first publish anywhere creates the bucket.
func Publish(ctx context.Context, c *realm.Client, e Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	kv, err := ensureBucket(ctx, c)
	if err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("toolcatalog: encode entry %s: %w", e.Name, err)
	}
	entry, err := kv.Get(ctx, e.Name)
	switch {
	case errors.Is(err, jetstream.ErrKeyNotFound):
		if _, cerr := kv.Create(ctx, e.Name, data); cerr != nil {
			return fmt.Errorf("toolcatalog: create entry %s: %w", e.Name, cerr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("toolcatalog: read entry %s: %w", e.Name, err)
	}
	if _, uerr := kv.Update(ctx, e.Name, data, entry.Revision()); uerr != nil {
		return fmt.Errorf("toolcatalog: update entry %s: %w", e.Name, uerr)
	}
	return nil
}

// Remove deletes one entry. An absent entry — or a realm with no catalog
// at all — is a Remove that already happened, never an error.
func Remove(ctx context.Context, c *realm.Client, name string) error {
	kv, err := bucket(ctx, c)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil
		}
		return err
	}
	if _, err := kv.Get(ctx, name); err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("toolcatalog: read entry %s: %w", name, err)
	}
	if err := kv.Delete(ctx, name); err != nil {
		return fmt.Errorf("toolcatalog: remove entry %s: %w", name, err)
	}
	return nil
}

// Lookup reads one entry. A realm without a catalog, or a name without an
// entry, is (Entry{}, false, nil) — absence is a normal state. The read is
// lenient on purpose: an entry of an unfamiliar kind comes back whole, and
// what to do with it is the reader's call.
func Lookup(ctx context.Context, c *realm.Client, name string) (Entry, bool, error) {
	kv, err := bucket(ctx, c)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	entry, err := kv.Get(ctx, name)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return Entry{}, false, nil
		}
		return Entry{}, false, fmt.Errorf("toolcatalog: read entry %s: %w", name, err)
	}
	var e Entry
	if err := json.Unmarshal(entry.Value(), &e); err != nil {
		return Entry{}, false, fmt.Errorf("toolcatalog: entry %s is unreadable: %w", name, err)
	}
	return e, true, nil
}

// All reads the whole catalog, plus a warning per unreadable entry. A
// realm without a catalog yields an empty slice — readers degrade, they
// do not fail.
func All(ctx context.Context, c *realm.Client) ([]Entry, []EntryWarning, error) {
	kv, err := bucket(ctx, c)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	lister, err := kv.ListKeys(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("toolcatalog: list entries: %w", err)
	}
	var entries []Entry
	var warnings []EntryWarning
	for name := range lister.Keys() {
		entry, err := kv.Get(ctx, name)
		if err != nil {
			if errors.Is(err, jetstream.ErrKeyNotFound) {
				continue // removed between list and get
			}
			return nil, nil, fmt.Errorf("toolcatalog: read entry %s: %w", name, err)
		}
		var e Entry
		if derr := json.Unmarshal(entry.Value(), &e); derr != nil {
			warnings = append(warnings, EntryWarning{Name: name, Err: derr})
			continue
		}
		entries = append(entries, e)
	}
	return entries, warnings, nil
}

// ensureBucket opens the catalog, creating it on the first write anywhere.
// A racing creator is fine: whoever loses re-opens what the winner made.
func ensureBucket(ctx context.Context, c *realm.Client) (jetstream.KeyValue, error) {
	kv, err := bucket(ctx, c)
	if err == nil {
		return kv, nil
	}
	if !errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, err
	}
	kv, cerr := c.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: BucketName, History: history,
	})
	if cerr == nil {
		return kv, nil
	}
	if errors.Is(cerr, jetstream.ErrBucketExists) {
		return bucket(ctx, c)
	}
	return nil, fmt.Errorf("toolcatalog: create catalog: %w", cerr)
}

func bucket(ctx context.Context, c *realm.Client) (jetstream.KeyValue, error) {
	kv, err := c.JetStream().KeyValue(ctx, BucketName)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("toolcatalog: open catalog: %w", err)
	}
	return kv, nil
}
