// The realm's cryptographic identity (A10 — hq
// 02-DESIGN/soulstream-core/extensions/tenancy.md, episode 0112): since
// canonical v2, every signature is scoped to the realm's KEY, not its
// reusable name. Where the deployment runs real accounts the identity
// IS the account's public key (read off the connection at first
// provision); on plain servers a keypair is minted for the role. Either
// way it is born once, first-provision-wins, and the name demotes to
// display.

package realm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
)

// IdentityBucket is the KV bucket holding the realm's identity record.
const IdentityBucket = "soulstream-realm"

// identityKey is the record's key inside the bucket.
const identityKey = "identity"

// Identity is the stored record: the key that scopes every
// signature, and the display name it replaced on the wire.
type Identity struct {
	RealmKey  string `json:"realm_key"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"created_at"`
}

// accountFromConnection reads the connection's server-proven account
// public key, when the deployment has one ($SYS.REQ.USER.INFO answers on
// operator-mode servers; plain servers refuse or answer accountless).
func accountFromConnection(nc *nats.Conn) string {
	msg, err := nc.Request("$SYS.REQ.USER.INFO", nil, 2*time.Second)
	if err != nil {
		return ""
	}
	var info struct {
		Data struct {
			Account string `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msg.Data, &info); err != nil {
		return ""
	}
	if nkeys.IsValidPublicAccountKey(info.Data.Account) {
		return info.Data.Account
	}
	return ""
}

// provisionIdentity births the realm identity: the connection's account
// key where real, a minted keypair's public half otherwise. First
// provision wins forever; re-provision reads what stands.
func provisionIdentity(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, name string) (created bool, err error) {
	kv, err := js.KeyValue(ctx, IdentityBucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		kv, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket: IdentityBucket,
			// One tiny record lives here; the fixed roof lets
			// limit-enforced accounts provision out of the box (016's
			// budget rule).
			MaxBytes: 4096,
		})
	}
	if err != nil {
		return false, fmt.Errorf("realm: identity bucket %q: %w", IdentityBucket, err)
	}
	if _, gerr := kv.Get(ctx, identityKey); gerr == nil {
		return false, nil
	}

	realmKey := ""
	if nc != nil {
		realmKey = accountFromConnection(nc)
	}
	if realmKey == "" {
		kp, kerr := nkeys.CreateAccount()
		if kerr != nil {
			return false, fmt.Errorf("realm: mint identity key: %w", kerr)
		}
		realmKey, _ = kp.PublicKey()
		// The seed is deliberately dropped: the identity is a NAME in key
		// form — nothing ever signs with it, so nothing custodies it.
	}
	rec := Identity{RealmKey: realmKey, Name: name, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	data, merr := json.Marshal(rec)
	if merr != nil {
		return false, merr
	}
	if _, cerr := kv.Create(ctx, identityKey, data); cerr != nil {
		// A racing provision won; what stands, stands.
		if _, gerr := kv.Get(ctx, identityKey); gerr == nil {
			return false, nil
		}
		return false, fmt.Errorf("realm: write identity: %w", cerr)
	}
	return true, nil
}

// LoadIdentity reads the realm's identity record; ErrNoIdentity when the
// realm predates the v2 break or was never provisioned.
func LoadIdentity(ctx context.Context, js jetstream.JetStream) (Identity, error) {
	kv, err := js.KeyValue(ctx, IdentityBucket)
	if err != nil {
		return Identity{}, ErrNoIdentity
	}
	entry, err := kv.Get(ctx, identityKey)
	if err != nil {
		return Identity{}, ErrNoIdentity
	}
	var rec Identity
	if err := json.Unmarshal(entry.Value(), &rec); err != nil || rec.RealmKey == "" {
		return Identity{}, ErrNoIdentity
	}
	return rec, nil
}

// ErrNoIdentity names the pre-v2 realm: signing needs the identity, and
// the migration is one act.
var ErrNoIdentity = errors.New("realm: no realm identity — this realm predates the v2 canonical form; run Provision once (existing signed history stays legacy-shape)")

// EnsureIdentity births the realm identity on its own — the narrow act
// consumers (and legacy-realm migrations) use when full provisioning is
// not theirs to run. Idempotent; first wins.
func EnsureIdentity(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, name string) error {
	_, err := provisionIdentity(ctx, js, nc, name)
	return err
}
