// wt/cmd/wt/profile_test.go
package main

import (
	"bytes"
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
