package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewAppLoadsProfilesStore verifies newApp populates a.profiles from
// profiles.toml (via the same XDG_CONFIG_HOME resolution config.Dir()
// uses), the same load path `wt profile ...` commands use — a.profiles
// and the launch path's independent loadProfileStore call each load their
// own copy (runAgentCmd does not take *app), not a shared one — a missing
// file must not be an error.
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
	if a.profilesLoadErr != nil {
		t.Errorf("a.profilesLoadErr = %v, want nil", a.profilesLoadErr)
	}
	if a.profilesValidateErr != nil {
		t.Errorf("a.profilesValidateErr = %v, want nil", a.profilesValidateErr)
	}
}

// TestNewAppSurfacesProfilesValidateError verifies a profiles.toml entry
// using a mechanism its agent doesn't declare is caught at startup
// (a.profilesValidateErr set, NOT a.profilesLoadErr — the file parsed
// fine), mirroring how a.cfgErr surfaces a bad config.toml. Critically,
// a.profiles must still carry the real, correctly-parsed data in this
// case (the whole point of splitting the two error fields — see
// TestNewAppLoadFailureLeavesProfilesEmpty for the contrasting case).
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
		t.Fatalf("newApp() error = %v, want nil (profilesValidateErr carries the problem, like cfgErr does)", err)
	}
	if a.profilesLoadErr != nil {
		t.Errorf("a.profilesLoadErr = %v, want nil (the file parsed fine — this is a Validate failure, not a Load failure)", a.profilesLoadErr)
	}
	if a.profilesValidateErr == nil {
		t.Error("a.profilesValidateErr = nil, want an error (pi does not accept config_content)")
	}
	if len(a.profiles.Profiles) != 1 {
		t.Errorf("a.profiles.Profiles = %v, want the one parsed entry preserved (a validation error must not discard real data)", a.profiles.Profiles)
	}
}

// TestNewAppLoadFailureLeavesProfilesEmpty verifies the OTHER failure mode
// — a genuine parse/IO error — sets a.profilesLoadErr (not just
// a.profilesValidateErr) and leaves a.profiles as the zero-value Store{}.
// This is the critical distinction Critical finding #1 depends on:
// `wt profile on|off` must be able to tell "the file is malformed, don't
// touch it" apart from "the file parsed fine but one profile is invalid,
// the data is safe to write back".
func TestNewAppLoadFailureLeavesProfilesEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte("not [ valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v, want nil (profilesLoadErr carries the problem)", err)
	}
	if a.profilesLoadErr == nil {
		t.Fatal("a.profilesLoadErr = nil, want a parse error")
	}
	if a.profilesValidateErr != nil {
		t.Errorf("a.profilesValidateErr = %v, want nil (Validate never runs when Load itself failed)", a.profilesValidateErr)
	}
	if len(a.profiles.Profiles) != 0 || a.profiles.Enabled {
		t.Errorf("a.profiles = %+v, want the zero-value Store{} (Load failed, nothing to populate it with)", a.profiles)
	}
}
