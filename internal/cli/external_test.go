package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// An unknown verb with a soulstream-<verb> binary on PATH runs it: args
// forwarded, the resolved identity projected into its environment, the exit
// code propagated. The seam is the git convention — built-ins always win,
// and it runs only from the unknown-command arm.
func TestExternalSubcommandRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a shell script")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "soulstream-testverb")
	script := "#!/bin/sh\necho \"verb ran: $1 persona=$SOULSTREAM_PERSONA realm=$SOULSTREAM_REALM\"\nexit 7\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out, errb bytes.Buffer
	code := Run(context.Background(),
		[]string{"--persona", "clerk", "--realm", "home", "testverb", "hello"},
		&out, &errb, nil)
	if code != 7 {
		t.Fatalf("exit = %d (stderr %q), want the child's 7", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "verb ran: hello persona=clerk realm=home") {
		t.Fatalf("child saw %q, want args and resolved identity", got)
	}
}

// A verb with no binary keeps the CLI's own usage failure — the seam never
// turns a typo into a silent search.
func TestUnknownVerbWithoutBinaryStillFails(t *testing.T) {
	var out, errb bytes.Buffer
	code := Run(context.Background(), []string{"no-such-verb"}, &out, &errb, nil)
	if code != 2 {
		t.Fatalf("exit = %d, want the usage failure 2", code)
	}
	if !strings.Contains(errb.String(), `unknown command "no-such-verb"`) {
		t.Fatalf("stderr = %q, want the unknown-command message", errb.String())
	}
}
