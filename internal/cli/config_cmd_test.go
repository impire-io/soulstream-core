package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/impire-io/soulstream-core/internal/config"
)

// isolateIdentity keeps tests blind to the developer's real identity sources: the
// user config dir moves into a temp home and the identity variables are blanked
// (SOULSTREAM_KEY_FILE is handled separately by testConnectorWithURL).
func isolateIdentity(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".xdgcfg"))
	for _, name := range []string{"SOULSTREAM_CONTEXT", "SOULSTREAM_REALM", "SOULSTREAM_PERSONA", "SOULSTREAM_PINS_FILE"} {
		t.Setenv(name, "")
	}
}

func writeProjectFile(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, config.ProjectFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestProjectFileIdentity (US1): a .soulstream.json supplies realm+persona to a real
// write command — no flags, no environment.
func TestProjectFileIdentity(t *testing.T) {
	connect := testConnector(t)
	dir := t.TempDir()
	writeProjectFile(t, dir, `{"realm":"acme","persona":"ada"}`)
	t.Chdir(dir)

	if code, _, errs := run(connect, "provision"); code != 0 {
		t.Fatalf("provision with config-file identity: exit %d, stderr %q", code, errs)
	}
	code, out, errs := run(connect, "start", "from-config")
	if code != 0 {
		t.Fatalf("start with config-file identity: exit %d, stderr %q", code, errs)
	}
	path := strings.TrimSpace(out)

	// A posted turn carries the file-resolved persona as its author.
	if code, _, errs := run(connect, "post", path, "hello from the config file"); code != 0 {
		t.Fatalf("post: exit %d, stderr %q", code, errs)
	}
	code, out, errs = run(connect, "show", path)
	if code != 0 || !strings.Contains(out, "ada") {
		t.Errorf("show: exit %d, out %q, stderr %q — want persona ada from project file", code, out, errs)
	}
}

// TestConfigCommandSources (US2): the config command names each field's true source.
func TestConfigCommandSources(t *testing.T) {
	connect := testConnector(t)
	dir := t.TempDir()
	projectPath := writeProjectFile(t, dir, `{"context":"file-ctx","realm":"file-realm"}`)
	t.Chdir(dir)
	t.Setenv("SOULSTREAM_REALM", "env-realm")

	code, out, errs := run(connect, "--persona", "cli-persona", "config")
	if code != 0 {
		t.Fatalf("config: exit %d, stderr %q", code, errs)
	}
	for _, want := range []string{
		"file-ctx", "project " + projectPath, // context ← project file, path named
		"env-realm", "env SOULSTREAM_REALM", // realm ← env beats file
		"cli-persona", "flag", // persona ← flag beats all
		"(unset)", "keystore default", // key/pins unset with the hint
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config output missing %q:\n%s", want, out)
		}
	}
}

// TestConfigCommandOffline (US2): nothing configured — still exit 0, all unset.
func TestConfigCommandOffline(t *testing.T) {
	isolateIdentity(t)
	t.Chdir(t.TempDir())
	code, out, _ := run(nil, "config")
	if code != 0 {
		t.Fatalf("config with nothing configured: exit %d", code)
	}
	if strings.Count(out, "(unset)") != 5 {
		t.Errorf("want all five fields unset, got:\n%s", out)
	}
}

// TestBrokenConfigFileFailsAnyCommand (US1): unknown field = loud failure naming the
// file, for ordinary commands and for `config` itself.
func TestBrokenConfigFileFailsAnyCommand(t *testing.T) {
	connect := testConnector(t)
	dir := t.TempDir()
	path := writeProjectFile(t, dir, `{"presona":"typo"}`)
	t.Chdir(dir)

	for _, cmd := range []string{"board", "config"} {
		code, _, errs := run(connect, cmd)
		if code == 0 || !strings.Contains(errs, path) {
			t.Errorf("%s with broken config: exit %d, stderr %q — want failure naming %s", cmd, code, errs, path)
		}
	}
}

// TestVersionSurvivesBrokenConfig: the diagnostics must answer even when config is broken.
func TestVersionSurvivesBrokenConfig(t *testing.T) {
	isolateIdentity(t)
	dir := t.TempDir()
	writeProjectFile(t, dir, `{not json`)
	t.Chdir(dir)
	if code, _, _ := run(nil, "version"); code != 0 {
		t.Errorf("version with broken config: exit %d, want 0", code)
	}
}
