package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	bad := `
[[profiles]]
agent = "pi"
match = "location"
location = "local"
config_content = { x = "1" }
`
	writeProfilesTOML(t, bad)
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
	writeProfilesTOML(t, "not [ valid toml")
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

// writeSlotProfilesTOML points XDG_CONFIG_HOME at a fresh temp directory,
// writes body there as agent-wt/slot-profiles.toml, and returns the file's
// path.
func writeSlotProfilesTOML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agent-wt", "slot-profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestNewAppLoadsSlotProfiles verifies newApp reads slot-profiles.toml and
// keeps one static verdict per profile, in file order, against the real agent
// drivers. A profile with a malformed name stays in the list as not usable, so
// `wt profile list` can report it by position and the launch can warn once. If
// newApp dropped it, a typo in one profile would vanish without a word.
func TestNewAppLoadsSlotProfiles(t *testing.T) {
	writeSlotProfilesTOML(t, `
[[profile]]
name    = "mixed"
agent   = "claude"
default = "opus"

[profile.slots]
opus = "openrouter/a"

[[profile]]
name    = "Bad_Name"
agent   = "claude"
default = "opus"

[profile.slots]
opus = "openrouter/a"
`)
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if a.slotLoadErr != nil {
		t.Fatalf("a.slotLoadErr = %v, want nil", a.slotLoadErr)
	}
	if len(a.slotChecks) != 2 {
		t.Fatalf("a.slotChecks = %+v, want 2 checks", a.slotChecks)
	}
	if c := a.slotChecks[0]; !c.Usable || c.Err != nil || c.Label() != "profile/mixed" {
		t.Errorf("slotChecks[0] = %+v, want the usable profile/mixed with no error", c)
	}
	if c := a.slotChecks[1]; c.Usable || c.Err == nil || c.Label() != "slot-profiles.toml[1]" {
		t.Errorf("slotChecks[1] = %+v, want a not-usable check labelled by its position", c)
	}
}

// TestNewAppWithoutSlotProfilesFile verifies a machine with no
// slot-profiles.toml gets no checks and no error: the file is optional, and
// its absence must not print a warning on every launch.
func TestNewAppWithoutSlotProfilesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if a.slotChecks != nil || a.slotLoadErr != nil {
		t.Errorf("slotChecks = %+v, slotLoadErr = %v, want nil and nil", a.slotChecks, a.slotLoadErr)
	}
}

// TestNewAppKeepsUnknownSlotKeys verifies newApp carries the keys
// slot-profiles.toml has and the schema does not, also when the file gives no
// profile at all: a file written with [[profiles]] for [[profile]] loads with
// no error and no profile, and the key is all there is to warn about. If
// newApp dropped it, that file would look exactly like no file.
func TestNewAppKeepsUnknownSlotKeys(t *testing.T) {
	writeSlotProfilesTOML(t, "[[profiles]]\nname = \"mixed\"\nagent = \"claude\"\n")
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if a.slotLoadErr != nil || a.slotChecks != nil {
		t.Fatalf("slotLoadErr = %v slotChecks = %+v, want a file that loads with no profile", a.slotLoadErr, a.slotChecks)
	}
	if want := []string{"profiles"}; !slices.Equal(a.slotUnknown, want) {
		t.Fatalf("a.slotUnknown = %q, want %q", a.slotUnknown, want)
	}
	if got, want := slotWarnings(a), []string{`wt: slot-profiles.toml: unknown key "profiles" — ignored`}; !slices.Equal(got, want) {
		t.Errorf("slotWarnings() = %q, want %q", got, want)
	}
}

// TestWrongTypeInOneSlotProfileFailsTheWholeFile pins the rule for a value of
// the wrong TOML type: the file does not parse, so no profile of it loads,
// the valid one beside the bad one included, and the launch gets one warning
// line that names the file and carries the decoder's error with its line
// number. If the line lost either, a user whose profiles all vanished would
// not know which file to open or where.
func TestWrongTypeInOneSlotProfileFailsTheWholeFile(t *testing.T) {
	writeSlotProfilesTOML(t, `[[profile]]
name = "cloud"
agent = "claude"
default = "opus"

[profile.slots]
opus = "openrouter/a"

[[profile]]
name = "second"
agent = "claude"
default = 3
`)
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	if a.slotChecks != nil {
		t.Errorf("a.slotChecks = %+v, want none: a file that does not parse gives no profile", a.slotChecks)
	}
	got := slotWarnings(a)
	if len(got) != 1 {
		t.Fatalf("slotWarnings() = %q, want exactly one line", got)
	}
	for _, want := range []string{"wt: parse slot-profiles.toml: ", "line 12", "default", " — slot profiles are unavailable"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("warning = %q, want it to contain %q", got[0], want)
		}
	}
}

// TestNewAppSlotParseErrorDoesNotStopWt verifies a TOML typo in
// slot-profiles.toml is stored, not returned: wt still starts, automatic
// profiles still load, and the launch path can say what is wrong. Returning
// the error would make one bad hand-written file break every wt command.
func TestNewAppSlotParseErrorDoesNotStopWt(t *testing.T) {
	writeSlotProfilesTOML(t, "[[profile]]\nname = \"mixed\n")
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v, want nil (slotLoadErr carries the problem)", err)
	}
	if a.slotLoadErr == nil || !strings.Contains(a.slotLoadErr.Error(), "parse slot-profiles.toml") {
		t.Fatalf("a.slotLoadErr = %v, want the parse error", a.slotLoadErr)
	}
	if a.slotChecks != nil {
		t.Errorf("a.slotChecks = %+v, want nil when the file did not load", a.slotChecks)
	}
	if a.profilesLoadErr != nil || !a.profiles.Enabled {
		t.Errorf("profiles.toml state = (%+v, %v), want it untouched by the slot file's error", a.profiles, a.profilesLoadErr)
	}
}
