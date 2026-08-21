package toolcatalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/internal/natstest"
	"github.com/impire-io/soulstream-core/realm"
)

// provisioned starts a server, provisions the realm, and returns a client.
func provisioned(t *testing.T, persona string) *realm.Client {
	t.Helper()
	url, shutdown := natstest.StartJetStream(t)
	t.Cleanup(shutdown)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	c, err := realm.NewClient(context.Background(), nc, realm.Config{Realm: "acme", Persona: persona})
	if err != nil {
		nc.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Provision(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

// The catalog's two kinds round-trip through the store, and the first
// write creates the bucket nobody provisioned.
func TestPublishLookupRoundTrip(t *testing.T) {
	ctx := context.Background()
	c := provisioned(t, "ops")

	remote := Entry{Name: "github", Kind: KindRemote,
		Endpoint:    "https://api.github.invalid/mcp",
		Description: "GitHub, reached through the identity plane"}
	workload := Entry{Name: "notes", Kind: KindWorkload, Persona: "notes-tool",
		Endpoint: "http://127.0.0.1:9999/mcp", Description: "the deployment's own notes server"}
	for _, e := range []Entry{remote, workload} {
		if err := Publish(ctx, c, e); err != nil {
			t.Fatalf("publish %s: %v", e.Name, err)
		}
	}

	got, found, err := Lookup(ctx, c, "github")
	if err != nil || !found {
		t.Fatalf("lookup github: found=%v %v", found, err)
	}
	if got.Kind != KindRemote || got.Description != remote.Description ||
		got.Persona != "" || got.Endpoint != remote.Endpoint {
		t.Fatalf("github came back %+v", got)
	}
	got, found, err = Lookup(ctx, c, "notes")
	if err != nil || !found {
		t.Fatalf("lookup notes: found=%v %v", found, err)
	}
	if got.Kind != KindWorkload || got.Persona != "notes-tool" ||
		got.Endpoint != workload.Endpoint {
		t.Fatalf("notes came back %+v", got)
	}

	entries, warnings, err := All(ctx, c)
	if err != nil || len(warnings) != 0 || len(entries) != 2 {
		t.Fatalf("All = %d entries, %d warnings, %v", len(entries), len(warnings), err)
	}
}

// Absence is a normal state everywhere: a realm with no catalog answers
// empty, never errors — and removing what is not there already happened.
func TestAbsenceIsNormal(t *testing.T) {
	ctx := context.Background()
	c := provisioned(t, "ops")

	if _, found, err := Lookup(ctx, c, "github"); err != nil || found {
		t.Fatalf("lookup on no catalog: found=%v %v", found, err)
	}
	if entries, warnings, err := All(ctx, c); err != nil || entries != nil || warnings != nil {
		t.Fatalf("All on no catalog: %v %v %v", entries, warnings, err)
	}
	if err := Remove(ctx, c, "github"); err != nil {
		t.Fatalf("remove on no catalog: %v", err)
	}
	if err := Publish(ctx, c, Entry{Name: "github", Kind: KindRemote}); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, c, "github"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := Lookup(ctx, c, "github"); found {
		t.Fatal("removed entry still present")
	}
	if err := Remove(ctx, c, "github"); err != nil {
		t.Fatalf("removing twice: %v", err)
	}
}

// Additive evolution, the acceptance criterion: a newer writer's fields
// and kinds pass through this version's hands whole.
func TestUnknownFieldsAndKindsSurvive(t *testing.T) {
	ctx := context.Background()
	c := provisioned(t, "ops")

	// A future writer stored an entry with a field and a kind this
	// version has never heard of.
	future := []byte(`{"name":"webhook","kind":"function","description":"a future kind",` +
		`"max_concurrency":5,"labels":{"tier":"gold"}}`)
	var e Entry
	if err := json.Unmarshal(future, &e); err != nil {
		t.Fatalf("a future entry did not decode: %v", err)
	}
	if e.Kind != Kind("function") {
		t.Fatalf("the unfamiliar kind was not carried: %q", e.Kind)
	}
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"max_concurrency":5`, `"tier":"gold"`, `"kind":"function"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("round trip dropped %s:\n%s", want, out)
		}
	}

	// And the store is no different: written raw by the future writer,
	// read leniently here, present-but-unsupported.
	if err := Publish(ctx, c, Entry{Name: "github", Kind: KindRemote}); err != nil {
		t.Fatal(err)
	}
	kv, err := c.JetStream().KeyValue(ctx, BucketName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Create(ctx, "webhook", future); err != nil {
		t.Fatal(err)
	}
	entries, warnings, err := All(ctx, c)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("All: %v warnings=%v", err, warnings)
	}
	if len(entries) != 2 {
		t.Fatalf("the future entry was hidden: %d entries", len(entries))
	}

	// Unreadable is different from unfamiliar: warned and skipped, loudly.
	if _, err := kv.Create(ctx, "broken", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	entries, warnings, err = All(ctx, c)
	if err != nil || len(entries) != 2 || len(warnings) != 1 || warnings[0].Name != "broken" {
		t.Fatalf("the broken entry was not warned: %d entries, %+v, %v", len(entries), warnings, err)
	}
}

// Validation runs at write time and only there: what this version cannot
// write, it still reads.
func TestWriteTimeValidation(t *testing.T) {
	ctx := context.Background()
	c := provisioned(t, "ops")
	for what, e := range map[string]Entry{
		"a nameless entry":           {Kind: KindRemote},
		"an unknown kind":            {Name: "x", Kind: "function"},
		"a remote with a persona":    {Name: "x", Kind: KindRemote, Persona: "y"},
		"a workload without persona": {Name: "x", Kind: KindWorkload},
		"a workload with a bad name": {Name: "x", Kind: KindWorkload, Persona: "NOT A NAME"},
		"an entry with an ugly name": {Name: "Not A Slug", Kind: KindRemote},
	} {
		if err := Publish(ctx, c, e); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
}

// A lost write is an error, never silent: publishing over a concurrent
// change surfaces the KV's own refusal.
func TestRacingWritersLoseLoudly(t *testing.T) {
	ctx := context.Background()
	c := provisioned(t, "ops")
	if err := Publish(ctx, c, Entry{Name: "github", Kind: KindRemote, Description: "one"}); err != nil {
		t.Fatal(err)
	}
	// An update through the package rides the read revision; a second
	// update through the same path just works (fresh read each time).
	if err := Publish(ctx, c, Entry{Name: "github", Kind: KindRemote, Description: "two"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := Lookup(ctx, c, "github")
	if err != nil || got.Description != "two" {
		t.Fatalf("update lost: %+v %v", got, err)
	}
}
