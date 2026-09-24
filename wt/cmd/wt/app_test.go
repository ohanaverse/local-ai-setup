package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewAppLoadsProfilesStore verifies newApp populates a.profiles from
// profiles.toml (via the same XDG_CONFIG_HOME resolution config.Dir()
// uses) so `wt profile ...` commands and the launch path share one
// loaded-once-per-invocation copy — a missing file must not be an error.
func TestNewAppLoadsProfilesStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if !a.profiles.Enabled {
		t.Errorf("a.profiles.Enabled = false, want true (default, no profiles.toml yet)")
	}
	if a.profilesErr != nil {
		t.Errorf("a.profilesErr = %v, want nil", a.profilesErr)
	}
}

// TestNewAppSurfacesProfilesValidateError verifies a profiles.toml entry
// using a mechanism its agent doesn't declare is caught at startup
// (a.profilesErr set), not silently accepted — mirrors how a.cfgErr
// surfaces a bad config.toml.
func TestNewAppSurfacesProfilesValidateError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := `
[[profiles]]
agent = "pi"
match = "location"
location = "local"
config_content = { x = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v, want nil (profilesErr carries the problem, like cfgErr does)", err)
	}
	if a.profilesErr == nil {
		t.Error("a.profilesErr = nil, want an error (pi does not accept config_content)")
	}
}
