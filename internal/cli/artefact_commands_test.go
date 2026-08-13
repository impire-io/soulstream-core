package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/topic"
)

// TestArtefactCommands walks US1 through the CLI: attach, revise --of, list,
// history, and fetching tip and an old revision — both digest-verified.
func TestArtefactCommands(t *testing.T) {
	connect := testConnector(t)
	run(connect, "--realm", "acme", "--persona", "daan", "provision")

	_, out, _ := run(connect, "--realm", "acme", "--persona", "daan", "start", "design notes")
	path := strings.TrimSpace(out)

	dir := t.TempDir()
	v1 := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(v1, []byte("draft one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run(connect, "--realm", "acme", "--persona", "daan",
		"attach", path, v1, "--type", "text/markdown"); code != 0 {
		t.Fatalf("attach exit %d: %s", code, errs)
	}

	if err := os.WriteFile(v1, []byte("draft two"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run(connect, "--realm", "acme", "--persona", "scribe",
		"revise", path, v1, "--of", "notes.md")
	if code != 0 || !strings.Contains(out, "revised notes.md") {
		t.Fatalf("revise: exit %d, out %q, err %q", code, out, errs)
	}

	// List: one artefact, two revisions, tip by scribe.
	_, out, _ = run(connect, "--realm", "acme", "artefacts", path)
	if !strings.Contains(out, "notes.md") || !strings.Contains(out, " 2 revisions") || !strings.Contains(out, "tip by scribe") {
		t.Errorf("artefacts list = %q", out)
	}

	// History (by name) marks the tip.
	_, out, _ = run(connect, "--realm", "acme", "artefacts", path, "notes.md")
	if !strings.Contains(out, "<- tip") || !strings.Contains(out, "daan") || !strings.Contains(out, "scribe") {
		t.Errorf("artefact history = %q", out)
	}

	// JSON exposes the op-ids for revision-precise fetching.
	_, out, _ = run(connect, "--realm", "acme", "artefacts", path, "--json")
	var arts []topic.Artefact
	if err := json.Unmarshal([]byte(out), &arts); err != nil || len(arts) != 1 {
		t.Fatalf("artefacts --json = %q (%v)", out, err)
	}

	// Fetch the tip by name; fetch the first revision by op-id.
	tipOut := filepath.Join(dir, "tip.md")
	code, out, errs = run(connect, "--realm", "acme", "get", path, "--artefact", "notes.md", "-o", tipOut)
	if code != 0 || !strings.Contains(out, "integrity verified") {
		t.Fatalf("get tip: exit %d, out %q, err %q", code, out, errs)
	}
	if got, _ := os.ReadFile(tipOut); string(got) != "draft two" {
		t.Errorf("tip bytes = %q", got)
	}
	oldOut := filepath.Join(dir, "old.md")
	code, _, errs = run(connect, "--realm", "acme", "get", path,
		"--artefact", arts[0].Root, "--revision", arts[0].Revisions[0].OpID, "-o", oldOut)
	if code != 0 {
		t.Fatalf("get old revision exit %d: %s", code, errs)
	}
	if got, _ := os.ReadFile(oldOut); string(got) != "draft one" {
		t.Errorf("old revision bytes = %q", got)
	}

	// A second lineage under the same name makes fetch-by-name ambiguous — the
	// error names the roots instead of guessing.
	second := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(second, []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(connect, "--realm", "acme", "--persona", "daan", "attach", path, second)
	code, _, errs = run(connect, "--realm", "acme", "get", path, "--artefact", "notes.md", "-o", filepath.Join(dir, "x.md"))
	if code == 0 || !strings.Contains(errs, "ambiguous") {
		t.Errorf("ambiguous fetch: exit %d, err %q", code, errs)
	}
}
