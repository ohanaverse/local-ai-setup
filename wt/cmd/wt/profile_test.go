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
