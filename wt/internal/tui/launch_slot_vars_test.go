package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// TestRunAndWaitCmdProfileSlotVarWinsOverSingleModelLaunch pins the precedence
// of the slot variables on the picker's launch path, from the built command to
// the process: the one an applied automatic profile sets reaches claude with
// the profile's value, the other seven with wt's, and a value exported in the
// shell reaches it for none of them. The applier here layers the profile's env
// the way cmd/wt's real one does (profiles.ApplyEnvAndArgs); this package
// cannot import that one.
func TestRunAndWaitCmdProfileSlotVarWinsOverSingleModelLaunch(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nenv > '"+dump+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "claude-haiku-from-the-shell")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "claude-opus-from-the-shell")

	oldApplier, oldSummary, oldSurvey := profileApplier, pendingSummary, pendingSurveyState
	t.Cleanup(func() { profileApplier, pendingSummary, pendingSurveyState = oldApplier, oldSummary, oldSurvey })
	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		profiles.ApplyEnvAndArgs(cmd, profiles.ResolvedProfile{
			Env: map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "ollama/small-from-the-profile"},
		})
		return func() error { return nil }, nil
	}

	cfg := &config.Config{Providers: []config.Provider{{
		ID:        "ollama",
		Location:  config.LocationLocal,
		Protocols: []config.Protocol{config.ProtocolAnthropic},
		Auth:      config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"},
	}}}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	m := config.Model{ID: "ollama/big", ModelName: "big", ProviderID: "ollama"}

	cmd, err := launchAgent("claude", m, t.TempDir(), false, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done, ok := runAndWaitCmd(cmd, "claude", m)().(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", done)
	}

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("the fake claude wrote no environment: %v", err)
	}
	got := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	want := map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":       "ollama/small-from-the-profile",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME":  "big",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":       "ollama/big",
		"ANTHROPIC_DEFAULT_FABLE_MODEL_NAME":  "big",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":        "ollama/big",
		"ANTHROPIC_DEFAULT_OPUS_MODEL_NAME":   "big",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":      "ollama/big",
		"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "big",
	}
	for key, w := range want {
		if got[key] != w {
			t.Errorf("claude received %s = %q, want %q", key, got[key], w)
		}
	}
}
