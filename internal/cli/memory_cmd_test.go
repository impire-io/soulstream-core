package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/impire-io/soulstream-core/internal/keystore"
	"github.com/impire-io/soulstream-core/realm"
	"github.com/impire-io/soulstream-core/record"
	"github.com/impire-io/soulstream-core/topic"
)

// turnOpID extracts a topic's first contribution op-id via show --json.
func turnOpID(t *testing.T, connect Connector, base []string, path string) string {
	t.Helper()
	code, out, errs := run(connect, append(base, "show", path, "--json")...)
	if code != 0 {
		t.Fatalf("show exit %d: %s", code, errs)
	}
	var v struct {
		Contributions []struct {
			OpID string `json:"op_id"`
		} `json:"contributions"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Contributions) == 0 {
		t.Fatalf("show --json: %v (%q)", err, out)
	}
	return v.Contributions[0].OpID
}

func TestMemoryUsageAndSilence(t *testing.T) {
	connect := testConnector(t)
	base := []string{"--realm", "acme", "--persona", "daan"}
	run(connect, append(base, "provision")...)

	// Usage errors.
	if code, _, _ := run(connect, append(base, "memory")...); code != 2 {
		t.Error("bare memory should be usage error")
	}
	if code, _, _ := run(connect, append(base, "memory", "nonsense")...); code != 2 {
		t.Error("unknown subcommand should be usage error")
	}
	if code, _, _ := run(connect, append(base, "memory", "query")...); code != 2 {
		t.Error("query without a question should be usage error")
	}
	if code, _, _ := run(connect, append(base, "memory", "query", "q", "--after", "yesterday")...); code != 2 {
		t.Error("malformed --after should be usage error")
	}
	if code, _, _ := run(connect, "--realm", "acme", "memory", "query", "q"); code == 0 {
		t.Error("query without persona should fail")
	}

	// A witnessless realm answers with honest silence, exit 0.
	code, out, errs := run(connect, append(base, "memory", "query", "anyone?", "--timeout", "300ms")...)
	if code != 0 {
		t.Fatalf("silent query exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "no answers before the deadline") {
		t.Errorf("silent query output: %q", out)
	}
}

func TestMemoryQueryGradedOutput(t *testing.T) {
	connect, url := testConnectorWithURL(t)
	base := []string{"--realm", "acme", "--persona", "daan"}
	run(connect, append(base, "provision")...)
	_, out, _ := run(connect, append(base, "start", "Cadence", "--subject", "the cadence decision")...)
	path := strings.TrimSpace(out)
	if code, _, errs := run(connect, append(base, "post", path, "weekly it is")...); code != 0 {
		t.Fatalf("post: %s", errs)
	}
	opID := turnOpID(t, connect, base, path)

	// A witness under another persona, served through the public library surface.
	wc := witnessClient(t, url, "historian")
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = topic.RespondMemory(wctx, wc, topic.MemoryWitness{
			Answer: func(topic.MemoryQueryRequest) []topic.MemoryAnswerDraft {
				return []topic.MemoryAnswerDraft{
					{Answer: "weekly, see the topic", Citations: []topic.MemoryCitation{
						{Topic: path, OpID: opID},
						{Topic: path, OpID: record.NewID()},
					}},
					{Answer: "and that is all I know"},
				}
			},
		})
	}()

	var code int
	var errs string
	for range 20 { // witness liveness: retry until the subscription serves
		code, out, errs = run(connect, append(base, "memory", "query", "cadence?", "--timeout", "300ms")...)
		if code != 0 {
			t.Fatalf("query exit %d: %s", code, errs)
		}
		if strings.Contains(out, "WITNESS") {
			break
		}
	}
	if !strings.Contains(out, "WITNESS historian") {
		t.Fatalf("attribution missing:\n%s", out)
	}
	if !strings.Contains(out, "["+string(topic.GradeFact)+"]") {
		t.Errorf("fact grade missing:\n%s", out)
	}
	if !strings.Contains(out, "["+string(topic.GradeUnverifiable)+"]") || !strings.Contains(out, "memory fetch") {
		t.Errorf("unverifiable caution + fetch hint missing:\n%s", out)
	}
	if !strings.Contains(out, "["+string(topic.GradeGossip)+"]") {
		t.Errorf("gossip tag missing:\n%s", out)
	}

	// JSON mode carries the same graded shape.
	code, out, errs = run(connect, append(base, "memory", "query", "cadence?", "--timeout", "300ms", "--json")...)
	if code != 0 {
		t.Fatalf("json query exit %d: %s", code, errs)
	}
	var res topic.MemoryResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json output: %v (%q)", err, out)
	}
	if len(res.Answers) == 0 {
		t.Error("json query lost the answers")
	}
}

// witnessClient connects a bare library client for playing the witness role.
func witnessClient(t *testing.T, url, persona string) *realm.Client {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("witness connect: %v", err)
	}
	c, err := realm.NewClient(context.Background(), nc, realm.Config{Realm: "acme", Persona: persona})
	if err != nil {
		nc.Close()
		t.Fatalf("witness client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestMemoryExhibitAndOfflineVerify(t *testing.T) {
	connect := testConnector(t)
	keyFile := filepath.Join(t.TempDir(), "daan.ed25519")
	pinsFile := filepath.Join(t.TempDir(), "pins.json")
	base := []string{"--realm", "acme", "--persona", "daan", "--key-file", keyFile}

	if code, _, errs := run(connect, append(base, "key", "init")...); code != 0 {
		t.Fatalf("key init: %s", errs)
	}
	run(connect, append(base, "provision")...)
	_, out, _ := run(connect, append(base, "start", "Evidence", "--subject", "s")...)
	path := strings.TrimSpace(out)
	if code, _, errs := run(connect, append(base, "post", path, "the decision")...); code != 0 {
		t.Fatalf("post: %s", errs)
	}
	opID := turnOpID(t, connect, base, path)

	// Pin the author's key so offline verification can say "verified".
	key, err := keystore.LoadKey(keyFile)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	if err := keystore.SavePins(pinsFile, keystore.Pins{Realm: "acme", Personas: map[string][]string{"daan": {key.PublicKey()}}}); err != nil {
		t.Fatalf("save pins: %v", err)
	}

	// Export a live op to a file.
	outfile := filepath.Join(t.TempDir(), "decision.exhibit.json")
	code, out, errs := run(connect, append(base, "memory", "exhibit", path, opID, "-o", outfile)...)
	if code != 0 {
		t.Fatalf("exhibit exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "source:  live") || !strings.Contains(out, "wrote "+outfile) {
		t.Errorf("exhibit output:\n%s", out)
	}
	// Refuses to overwrite without --force.
	if code, _, errs = run(connect, append(base, "memory", "exhibit", path, opID, "-o", outfile)...); code == 0 {
		t.Error("second export must refuse to overwrite")
	} else if !strings.Contains(errs, "--force") {
		t.Errorf("overwrite refusal should name --force: %q", errs)
	}

	// Offline verify: NEVER connects — a connector that always fails proves it.
	brokenConnect := func(context.Context, Config) (*realm.Client, error) {
		return nil, fmt.Errorf("verify must not connect")
	}
	code, out, errs = run(brokenConnect, "--realm", "acme", "--pins-file", pinsFile, "memory", "verify", outfile)
	if code != 0 {
		t.Fatalf("verify exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "verdict: verified") || !strings.Contains(out, "author:  daan") {
		t.Errorf("verify output:\n%s", out)
	}

	// Without pins the same document is honestly unknown-key (still exit 0).
	code, out, errs = run(brokenConnect, "--realm", "acme", "memory", "verify", outfile)
	if code != 0 {
		t.Fatalf("pinless verify exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "verdict: unknown-key") || !strings.Contains(errs, "not among your pins") {
		t.Errorf("pinless verify: out=%q errs=%q", out, errs)
	}

	// Tampering flips the verdict to failed, exit 1.
	doc, err := os.ReadFile(outfile)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := record.ParseExhibit(doc)
	if err != nil {
		t.Fatalf("parse exported exhibit: %v", err)
	}
	ex.Payload = []byte(`{"body":"a lie"}`)
	tampered, err := ex.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	tamperedFile := filepath.Join(t.TempDir(), "tampered.exhibit.json")
	if err := os.WriteFile(tamperedFile, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run(brokenConnect, "--realm", "acme", "--pins-file", pinsFile, "memory", "verify", tamperedFile)
	if code != 1 {
		t.Fatalf("tampered verify exit %d (want 1): %s", code, errs)
	}
	if !strings.Contains(out, "verdict: failed") || !strings.Contains(errs, "does NOT verify") {
		t.Errorf("tampered verify: out=%q errs=%q", out, errs)
	}

	// A document that is not an exhibit fails loudly.
	garbage := filepath.Join(t.TempDir(), "garbage.json")
	_ = os.WriteFile(garbage, []byte(`{"nope":true}`), 0o644)
	if code, _, errs = run(brokenConnect, "--realm", "acme", "memory", "verify", garbage); code != 2 {
		t.Errorf("garbage verify exit %d (want 2): %s", code, errs)
	}
}

func TestMemoryFetchAndExhibitNotLive(t *testing.T) {
	connect := testConnector(t)
	base := []string{"--realm", "acme", "--persona", "daan"}
	run(connect, append(base, "provision")...)
	_, out, _ := run(connect, append(base, "start", "Fetching", "--subject", "s")...)
	path := strings.TrimSpace(out)
	if code, _, errs := run(connect, append(base, "post", path, "still live")...); code != 0 {
		t.Fatalf("post: %s", errs)
	}
	opID := turnOpID(t, connect, base, path)

	// Live-first: no witness needed.
	code, out, errs := run(connect, append(base, "memory", "fetch", path, opID)...)
	if code != 0 {
		t.Fatalf("fetch exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "source:  live") || !strings.Contains(out, "verdict: unsigned") {
		t.Errorf("live fetch output:\n%s", out)
	}

	// A vanished op with no witnesses: silence, exit 1.
	code, out, _ = run(connect, append(base, "memory", "fetch", path, record.NewID(), "--timeout", "300ms")...)
	if code != 1 {
		t.Fatalf("silent fetch exit %d (want 1)", code)
	}
	if !strings.Contains(out, "no exhibit before the deadline") {
		t.Errorf("silent fetch output: %q", out)
	}

	// exhibit is deliberately live-only: a vanished op points at fetch.
	code, _, errs = run(connect, append(base, "memory", "exhibit", path, record.NewID())...)
	if code != 1 {
		t.Fatalf("not-live exhibit exit %d (want 1)", code)
	}
	if !strings.Contains(errs, "memory fetch") {
		t.Errorf("not-live exhibit should point at memory fetch: %q", errs)
	}
}
