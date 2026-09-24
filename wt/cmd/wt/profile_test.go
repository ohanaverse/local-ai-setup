// wt/cmd/wt/profile_test.go
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestProfileListPrintsEveryProfile verifies `wt profile list` prints one
// line per profiles.toml entry, naming its agent and match tier — the
// simplest possible smoke test that the command reads a.profiles rather
// than reloading the file itself.
func TestProfileListPrintsEveryProfile(t *testing.T) {
	a := &app{profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "claude", Match: "location", Location: "local"},
		{Agent: "pi", Match: "location", Location: "local"},
	}}}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "claude") || !strings.Contains(got, "pi") {
		t.Errorf("output = %q, want both claude and pi listed", got)
	}
}

// TestProfileShowResolvesForAgentAndModel verifies `wt profile show -A
// claude -M <id>` dry-runs Resolve() and reports what would apply,
// without launching anything.
func TestProfileShowResolvesForAgentAndModel(t *testing.T) {
	a := &app{
		cfg: &config.Config{
			Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
			Models:    []config.Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		},
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
		}},
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "-A", "claude", "-M", "ollama/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	if !strings.Contains(out.String(), "X=1") {
		t.Errorf("output = %q, want the resolved env var shown", out.String())
	}
}

// TestProfileShowWithoutModelDoesNotError verifies `wt profile show -A
// <agent>` (no `-M`) succeeds instead of erroring "model \"\" not found in
// registry" — the design spec documents `-M` as optional
// (`wt profile show -A <agent> [-M <model>]`), but findModelByID used to be
// called unconditionally with an empty id, which can never match a
// registry entry.
func TestProfileShowWithoutModelDoesNotError(t *testing.T) {
	a := &app{
		cfg: &config.Config{
			Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
			Models:    []config.Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		},
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
		}},
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "-A", "claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v, want nil (-M is optional)", err)
	}
	if !strings.Contains(out.String(), "no matching profile") {
		t.Errorf("output = %q, want \"no matching profile\" (a location-tier profile can't match without a model)", out.String())
	}
}

// TestProfileListFlagsInvalidMatchTier is the regression lock for the
// code-review finding that `wt profile list` silently misdescribed a
// profile with an invalid/typo'd match tier as "model=" (showing whatever
// stale/empty Model field happened to be set) instead of flagging the
// problem the same way `wt profile show`/a real launch would.
func TestProfileListFlagsInvalidMatchTier(t *testing.T) {
	a := &app{profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "claude", Match: "locaton", Location: "local"},
	}}}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "invalid") {
		t.Errorf("output = %q, want the invalid match tier flagged", got)
	}
	if strings.Contains(got, "model=") {
		t.Errorf("output = %q, misdescribed the invalid entry as a model match", got)
	}
}

// TestProfileListWarnsOnValidateError verifies `wt profile list` also
// surfaces a.profilesValidateErr as a warning after the listing — the file
// parsed fine (list still shows every entry) but no profile in it can ever
// actually apply at launch time, and the user should see that.
func TestProfileListWarnsOnValidateError(t *testing.T) {
	a := &app{
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local"},
		}},
		profilesValidateErr: errors.New(`profiles.toml[0] (agent=claude): agent does not support mechanism "wrapper"`),
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	if !strings.Contains(out.String(), "disabled for every launch") {
		t.Errorf("output = %q, want a validate-error warning", out.String())
	}
}

// TestProfileShowReportsValidateErrorInsteadOfResolving is the regression
// lock for the code-review finding that `wt profile show` ignored
// a.profilesValidateErr and called profiles.Resolve directly — so its
// dry-run output could claim a profile applies when a real launch would
// disable ALL profile application file-wide (applyProfileForLaunch checks
// both loadErr and validateErr and disables everything on either).
func TestProfileShowReportsValidateErrorInsteadOfResolving(t *testing.T) {
	a := &app{
		cfg: &config.Config{
			Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
			Models:    []config.Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		},
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
		}},
		profilesValidateErr: errors.New(`profiles.toml[1] (agent=pi): agent does not support mechanism "config_file"`),
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "-A", "claude", "-M", "ollama/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, "X=1") {
		t.Errorf("output = %q, showed a profile as applying despite a.profilesValidateErr set (a real launch would disable it)", got)
	}
	if !strings.Contains(got, "disabled") {
		t.Errorf("output = %q, want it to report profiles are disabled due to the validation error", got)
	}
}

// TestProfileStatusOnOffTogglesEnabledFlag verifies `wt profile off` then
// `wt profile status` reflects the change by writing/reading the same
// profiles.toml, and `wt profile on` reverts it — the global kill switch
// the design commits to.
func TestProfileStatusOnOffTogglesEnabledFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	a := &app{profiles: profiles.Store{Enabled: true}}

	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}
	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Error("after `wt profile off`, profiles.toml still has enabled=true")
	}

	a2 := &app{profiles: reloaded}
	var statusOut bytes.Buffer
	status := profileCmd(a2)
	status.SetOut(&statusOut)
	status.SetArgs([]string{"status"})
	if err := status.Execute(); err != nil {
		t.Fatalf("status: execute error = %v", err)
	}
	if !strings.Contains(statusOut.String(), "off") {
		t.Errorf("status output = %q, want it to report off", statusOut.String())
	}
}

// TestProfileOnOffRefusesOnLoadError is the regression lock for Critical
// finding #1: `wt profile on|off` must refuse to write when
// a.profilesLoadErr is set (profiles.toml failed to parse), rather than
// re-encoding a.profiles — which, on a Load failure, is the zero-value
// Store{} — and silently overwriting every real [[profiles]] entry and
// comment in the malformed file with just "enabled = false". The command
// must return an error and leave the on-disk file byte-for-byte untouched.
func TestProfileOnOffRefusesOnLoadError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "not [ valid toml, and a hand-written [[profiles]] entry the user cares about\n"
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &app{profilesLoadErr: errors.New("parse profiles.toml: simulated parse failure")}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err == nil {
		t.Fatal("`wt profile off` with a load error: err = nil, want an error refusing to write")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("profiles.toml was modified despite a load error — got %q, want untouched %q", got, original)
	}
}

// TestProfileListShowStatusReportLoadError verifies list/show/status all
// surface a.profilesLoadErr as a real command error instead of silently
// rendering an empty/default profiles view — the other half of Critical
// finding #1's "blind spot" (a broken profiles.toml must never be
// misreported as "no profiles defined" / "profiles: off").
func TestProfileListShowStatusReportLoadError(t *testing.T) {
	loadErr := errors.New("parse profiles.toml: simulated parse failure")
	a := &app{
		cfg:             &config.Config{},
		profilesLoadErr: loadErr,
	}

	for _, args := range [][]string{{"list"}, {"show", "-A", "claude"}, {"status"}} {
		cmd := profileCmd(a)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		if err == nil {
			t.Errorf("wt profile %v: err = nil, want the load error surfaced", args)
			continue
		}
		if !strings.Contains(err.Error(), "simulated parse failure") {
			t.Errorf("wt profile %v: err = %v, want it to wrap the load error", args, err)
		}
	}
}

// TestProfileTogglePreservesCommentsAndFormatting is the regression lock
// for Important finding #6: `wt profile off` must edit profiles.toml
// surgically (just the top-level `enabled` line), not re-encode the whole
// Store via profiles.Save — a full re-encode silently drops comments and
// reformats the file, since Profile's Go struct doesn't round-trip TOML
// comments and the encoder's own formatting differs from hand-written
// TOML. This asserts against the RAW file bytes (not just Load() parsing
// the same values back), since a lossy round-trip that happens to parse
// identically would not be caught by re-parsing alone.
func TestProfileTogglePreservesCommentsAndFormatting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `enabled = true

# my own hand-written comment explaining this profile
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { MY_OWN_KEY = "my-own-value" }
`
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{profiles: store}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "# my own hand-written comment explaining this profile") {
		t.Errorf("profiles.toml after toggle = %q, want the hand-written comment preserved", got)
	}
	if !strings.Contains(got, `env = { MY_OWN_KEY = "my-own-value" }`) {
		t.Errorf("profiles.toml after toggle = %q, want the [[profiles]] entry preserved verbatim", got)
	}
	if !strings.Contains(got, "enabled = false") {
		t.Errorf("profiles.toml after toggle = %q, want enabled = false", got)
	}
	if strings.Contains(got, "enabled = true") {
		t.Errorf("profiles.toml after toggle = %q, want the old enabled = true line gone, not duplicated", got)
	}

	// The data must still round-trip correctly through Load, too.
	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Error("reloaded.Enabled = true, want false")
	}
	if len(reloaded.Profiles) != 1 || reloaded.Profiles[0].Env["MY_OWN_KEY"] != "my-own-value" {
		t.Errorf("reloaded.Profiles = %+v, want the one profile with MY_OWN_KEY preserved", reloaded.Profiles)
	}
}

// TestProfileToggleInsertsEnabledLineWhenAbsent verifies a profiles.toml
// with [[profiles]] entries but no existing top-level `enabled` line (the
// key is optional — Load defaults it to true) gets one inserted at the top
// on toggle, rather than erroring or silently no-op'ing.
func TestProfileToggleInsertsEnabledLineWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `[[profiles]]
agent = "claude"
match = "location"
location = "local"
`
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{profiles: store}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}

	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Error("reloaded.Enabled = true, want false")
	}
	if len(reloaded.Profiles) != 1 {
		t.Errorf("reloaded.Profiles = %v, want the one entry preserved", reloaded.Profiles)
	}
}

// TestProfileToggleHandlesTrailingCommentOnEnabledLine is the regression
// lock for the code-review finding that setEnabledLine produced invalid
// TOML (two conflicting top-level `enabled` keys) when the existing
// `enabled = ...` line carried a trailing comment: the old regex required
// the line to end immediately after true/false, so it fell through to
// PREPENDING a brand-new enabled line ahead of the untouched original,
// leaving both in the file — which BurntSushi/toml then rejects as a
// duplicate key on the next Load.
func TestProfileToggleHandlesTrailingCommentOnEnabledLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `enabled = true  # keep local profiles on

[[profiles]]
agent = "claude"
match = "location"
location = "local"
`
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{profiles: store}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "enabled = ") != 1 {
		t.Fatalf("profiles.toml after toggle has %d 'enabled = ' occurrences, want exactly 1 (no duplicate top-level key): %q", strings.Count(got, "enabled = "), got)
	}
	if !strings.Contains(got, "enabled = false") {
		t.Errorf("profiles.toml after toggle = %q, want enabled = false", got)
	}
	if !strings.Contains(got, "# keep local profiles on") {
		t.Errorf("profiles.toml after toggle = %q, want the trailing comment preserved", got)
	}

	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.toml is not valid TOML after toggle: %v", err)
	}
	if reloaded.Enabled {
		t.Error("reloaded.Enabled = true, want false")
	}
	if len(reloaded.Profiles) != 1 {
		t.Errorf("reloaded.Profiles = %v, want the one entry preserved", reloaded.Profiles)
	}
}

// TestProfileToggleHandlesTrailingWhitespaceOnEnabledLine is the
// regression lock for the final-review finding that the trailing-comment
// fix (topLevelEnabledLineRe) reintroduced the exact invalid-TOML bug it
// was meant to close, just triggered by a different pre-existing
// condition: a hand-edited `enabled = true` line with trailing whitespace
// but NO comment no longer matched at all (the old regex's unconditional
// `\s*$` allowed bare trailing whitespace; the fix's
// `([ \t]*#[^\n]*)?$` only allows whitespace when a comment follows), so
// setEnabledLine fell through to prepending a duplicate `enabled` line.
func TestProfileToggleHandlesTrailingWhitespaceOnEnabledLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "enabled = true  \n\n[[profiles]]\nagent = \"claude\"\nmatch = \"location\"\nlocation = \"local\"\n"
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{profiles: store}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "enabled = ") != 1 {
		t.Fatalf("profiles.toml after toggle has %d 'enabled = ' occurrences, want exactly 1 (no duplicate top-level key): %q", strings.Count(got, "enabled = "), got)
	}

	if _, err := profiles.Load(); err != nil {
		t.Fatalf("profiles.toml is not valid TOML after toggle: %v", err)
	}
}

// TestProfileToggleHandlesCRLFEnabledLine is the regression lock for the
// same final-review finding, for a CRLF-terminated enabled line (a file
// edited on Windows, or by a tool that preserves CRLF line endings): the
// trailing-comment fix's `$` never matches when a bare `\r` sits between
// `true`/`false` and the `\n`, so this must also produce valid TOML.
func TestProfileToggleHandlesCRLFEnabledLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "enabled = true\r\n\r\n[[profiles]]\r\nagent = \"claude\"\r\nmatch = \"location\"\r\nlocation = \"local\"\r\n"
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{profiles: store}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "enabled = ") != 1 {
		t.Fatalf("profiles.toml after toggle has %d 'enabled = ' occurrences, want exactly 1 (no duplicate top-level key): %q", strings.Count(got, "enabled = "), got)
	}

	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.toml is not valid TOML after toggle: %v", err)
	}
	if reloaded.Enabled {
		t.Error("reloaded.Enabled = true, want false")
	}
}
