package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// envDumpingClaude puts a claude first on PATH that writes the environment it
// was started with to the returned file, so a test reads what the agent
// process really received instead of interpreting cmd.Env's duplicates.
func envDumpingClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return out
}

// readEnvDump parses the file envDumpingClaude's claude wrote.
func readEnvDump(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake claude wrote no environment: %v", err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

// TestAutomaticProfileSlotVarWinsOverSingleModelLaunch pins the precedence of
// the slot variables on the non-TUI path, from profiles.toml to the process:
// the one an applied automatic profile sets reaches claude with the profile's
// value, the other seven with wt's, and a value exported in the shell reaches
// it for none of them.
func TestAutomaticProfileSlotVarWinsOverSingleModelLaunch(t *testing.T) {
	writeProfilesTOML(t, `
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { ANTHROPIC_DEFAULT_HAIKU_MODEL = "ollama/small-from-the-profile" }
`)
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "claude-haiku-from-the-shell")
	t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "claude-opus-from-the-shell")
	dump := envDumpingClaude(t)

	cfg := &config.Config{Providers: []config.Provider{{
		ID:        "ollama",
		Location:  config.LocationLocal,
		Protocols: []config.Protocol{config.ProtocolAnthropic},
		Auth:      config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"},
	}}}
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	m := config.Model{ID: "ollama/big", ModelName: "big", ProviderID: "ollama"}

	cmd, err := agents.BuildLaunchCmd("claude", m, t.TempDir(), false, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, nil)
	if err != nil {
		t.Fatalf("applyProfileForLaunch: %v", err)
	}
	t.Cleanup(func() { cleanup() })
	if err := cmd.Run(); err != nil {
		t.Fatalf("run the fake claude: %v", err)
	}

	got := readEnvDump(t, dump)
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
