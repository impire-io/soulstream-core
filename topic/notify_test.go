package topic

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/impire-io/soulstream-core/identity"
	"github.com/impire-io/soulstream-core/realm"
)

func TestFetchInbox(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")

	// Empty inbox.
	got, err := FetchInbox(ctx, c, "bookkeeper-agent", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty inbox returned %d, want 0", len(got))
	}

	// Post three notifications.
	for i := range 3 {
		if err := publishNotify(ctx, c, "bookkeeper-agent", NotifyPayload{
			Topic: "vat", OpID: fmt.Sprintf("op-%d", i), Author: "daan",
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err = FetchInbox(ctx, c, "bookkeeper-agent", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("inbox returned %d, want 3", len(got))
	}
	if got[0].OpID != "op-2" {
		t.Errorf("inbox not newest-first: %+v", got)
	}

	// Honour the limit.
	got, err = FetchInbox(ctx, c, "bookkeeper-agent", 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].OpID != "op-2" {
		t.Errorf("limited inbox = %+v, want 2 newest-first", got)
	}
}

// 014/US4: an inbox holds only the newest realm.InboxWindow notifications — the
// stream sheds the oldest per persona — and a fetch reads the bounded window, never
// lifetime history. The mentioning history itself is untouched by the bound.
func TestInboxIsBounded(t *testing.T) {
	ctx := context.Background()
	c := provisionedClient(t, "daan")

	total := realm.InboxWindow + 20
	for i := 0; i < total; i++ {
		if err := publishNotify(ctx, c, "busy", NotifyPayload{
			Topic: "vat", OpID: fmt.Sprintf("op-%d", i), Author: "daan",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Storage: exactly the window remains for that persona.
	stream, err := c.JetStream().Stream(ctx, realm.NotifyStreamName)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(NotifySubject("busy")))
	if err != nil {
		t.Fatal(err)
	}
	if n := info.State.Subjects[NotifySubject("busy")]; n != realm.InboxWindow {
		t.Fatalf("stored notifications = %d, want the window of %d", n, realm.InboxWindow)
	}

	// Fetch: the default cap of 50, newest first, all from inside the window.
	got, err := FetchInbox(ctx, c, "busy", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("fetched %d, want the default cap of 50", len(got))
	}
	if got[0].OpID != fmt.Sprintf("op-%d", total-1) {
		t.Errorf("newest fetched = %s, want op-%d", got[0].OpID, total-1)
	}
	if got[49].OpID != fmt.Sprintf("op-%d", total-50) {
		t.Errorf("oldest fetched = %s, want op-%d", got[49].OpID, total-50)
	}

	// Another persona's inbox is untouched by busy's overflow.
	if err := publishNotify(ctx, c, "quiet", NotifyPayload{Topic: "vat", OpID: "only", Author: "daan"}); err != nil {
		t.Fatal(err)
	}
	q, err := FetchInbox(ctx, c, "quiet", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 1 || q[0].OpID != "only" {
		t.Errorf("quiet inbox = %+v, want the single notification", q)
	}
}

// 014/US3: a realm provisioned before the inbox stream existed gets a pointer at
// the fix, not a bare stream-not-found.
func TestInboxWithoutNotifyStreamSaysReprovision(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c := connectClient(t, url, "daan")
	// Deliberately NOT provisioned: no notify stream on this server.
	_, err := FetchInbox(ctx, c, "daan", 0, nil)
	if err == nil {
		t.Fatal("fetch on an unprovisioned realm succeeded")
	}
	if !strings.Contains(err.Error(), "provision") {
		t.Errorf("error does not point at provisioning: %v", err)
	}
}

// 014/US3: converging a legacy realm keeps migrated notifications verifiable — the
// bytes and subjects are unchanged, so the signature still binds.
func TestMigratedNotificationsStillVerify(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)

	// Build the pre-014 world by hand: one stream capturing everything.
	key, err := identity.GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	c := connectClientSigned(t, url, "signer", key)
	legacy := jetstream.StreamConfig{
		Name:        realm.StreamName,
		Subjects:    []string{realm.LegacyStreamSubject},
		Retention:   jetstream.LimitsPolicy,
		AllowRollup: true,
		Duplicates:  2 * time.Minute,
		Storage:     jetstream.FileStorage,
	}
	if _, err := c.JetStream().CreateStream(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	// The identity is a v2 artifact orthogonal to the legacy stream
	// shape: signing needs it in any world.
	if err := realm.EnsureIdentity(ctx, c.JetStream(), nil, "test-realm"); err != nil {
		t.Fatal(err)
	}

	// A signed mention lands its notification in the legacy stream.
	h, err := StartTopic(ctx, c, StartTopicInput{Name: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostTurn(ctx, "hello @reader"); err != nil {
		t.Fatal(err)
	}

	// Converge, then read the inbox through the new stream with the signer's key.
	if _, err := realm.ProvisionOn(ctx, c.JetStream()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	kr := &identity.Keyring{Keys: map[string][]string{"signer": {key.PublicKey()}}}
	notes, err := FetchInbox(ctx, c, "reader", 0, kr)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("migrated inbox = %+v, want 1 notification", notes)
	}
	if notes[0].Sig != SigVerified {
		t.Errorf("migrated notification sig = %q, want %q", notes[0].Sig, SigVerified)
	}
}

func TestMentionNotifiesInbox(t *testing.T) {
	ctx := context.Background()
	url := testServer(t)
	c1 := connectClient(t, url, "daan")
	if _, err := c1.Provision(ctx); err != nil {
		t.Fatal(err)
	}
	c2 := connectClient(t, url, "bookkeeper-agent")

	h, err := StartTopic(ctx, c1, StartTopicInput{Name: "VAT"})
	if err != nil {
		t.Fatal(err)
	}

	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	notes := make(chan Notification, 8)
	go func() { _ = FollowInbox(fctx, c2, "bookkeeper-agent", nil, func(n Notification) { notes <- n }) }()

	opID, err := h.PostTurn(ctx, "please check box 5 @bookkeeper-agent")
	if err != nil {
		t.Fatal(err)
	}

	select {
	case n := <-notes:
		if n.Topic != h.Path() {
			t.Errorf("notification topic = %q, want %q", n.Topic, h.Path())
		}
		if n.OpID != opID {
			t.Errorf("notification op-id = %q, want %q", n.OpID, opID)
		}
		if n.Author != "daan" {
			t.Errorf("notification author = %q, want daan", n.Author)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification received on the inbox")
	}

	// The op payload records the mention (surfaced on the materialised contribution).
	view, err := h.Materialise(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Contributions) != 1 {
		t.Fatalf("contributions = %d, want 1", len(view.Contributions))
	}
	if got := view.Contributions[0].Mentions; len(got) != 1 || got[0] != "bookkeeper-agent" {
		t.Errorf("mentions = %v, want [bookkeeper-agent]", got)
	}
}
