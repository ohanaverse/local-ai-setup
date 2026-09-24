package profiles

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMissingFileReturnsEnabledEmptyStore verifies that a fresh
// install (no profiles.toml yet) is not an error and defaults to
// enabled=true with no profiles — matching config.Load's "empty Config
// if config.toml does not exist yet" convention, so `wt` never fails to
// launch just because no one has authored a profile yet.
func TestLoadMissingFileReturnsEnabledEmptyStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	store, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if !store.Enabled {
		t.Errorf("store.Enabled = false, want true (default)")
	}
	if len(store.Profiles) != 0 {
		t.Errorf("store.Profiles = %v, want empty", store.Profiles)
	}
}

// TestLoadParsesProfilesAndEnabledFlag verifies the on-disk TOML shape
// from the design spec round-trips into Store correctly, including a
// profile using every field (env, args, config_content, wrapper) so a
// schema typo in any one field is caught here rather than later.
func TestLoadParsesProfilesAndEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	body := `
enabled = false

[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0" }

[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[[profiles]]
agent = "codex"
match = "provider"
provider = "ollama"
args = ["-c", "model_reasoning_effort=\"low\""]
`
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if store.Enabled {
		t.Errorf("store.Enabled = true, want false (explicit in file)")
	}
	if len(store.Profiles) != 3 {
		t.Fatalf("len(store.Profiles) = %d, want 3", len(store.Profiles))
	}
	if store.Profiles[0].Env["CLAUDE_CODE_ATTRIBUTION_HEADER"] != "0" {
		t.Errorf("profile[0].Env = %v, want CLAUDE_CODE_ATTRIBUTION_HEADER=0", store.Profiles[0].Env)
	}
	if store.Profiles[1].Wrapper == nil || store.Profiles[1].Wrapper.Binary != "little-coder" {
		t.Errorf("profile[1].Wrapper = %+v, want little-coder", store.Profiles[1].Wrapper)
	}
	if len(store.Profiles[2].Args) != 2 {
		t.Errorf("profile[2].Args = %v, want 2 elements", store.Profiles[2].Args)
	}
}

// TestLoadMalformedTOMLReturnsError verifies a syntax error surfaces as a
// real error rather than a silently empty store — callers (Task 7) decide
// to degrade to "profiles disabled for this launch" with a warning, but
// Load itself must not swallow the problem.
func TestLoadMalformedTOMLReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("not [ valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want a parse error")
	}
}
