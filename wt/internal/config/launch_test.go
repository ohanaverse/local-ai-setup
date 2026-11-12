package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLaunchExitPromptAnyValueIsTolerated pins "tolerate and ignore"
// (R305a): every value of the retired `[launch] exit_prompt` key parses and
// round-trips a save unchanged — the old "ask"/"keep", a typo like "kepp",
// even "remove", which no value may mean. Nothing validates or consults it
// any more (the ask/keep rejection and ExitPromptKeep died with the exit
// prompt), so a stale key can never again fail the config load.
func TestLaunchExitPromptAnyValueIsTolerated(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"empty", ""},
		{"ask", "ask"},
		{"keep", "keep"},
		{"remove", "remove"},
		{"kepp", "kepp"},
		{"yes", "yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			litellmStateEnv(t, "default_tag = \"code\"\n[launch]\nexit_prompt = \""+tc.value+"\"\n")
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want nil for exit_prompt = %q (every value is tolerated and ignored)", err, tc.value)
			}
			if cfg.LaunchTable == nil || cfg.LaunchTable.ExitPrompt != tc.value {
				t.Fatalf("Load gave %+v, want exit_prompt = %q kept as written", cfg.LaunchTable, tc.value)
			}
			if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "other" }); err != nil {
				t.Fatal(err)
			}
			again, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if again.LaunchTable == nil || again.LaunchTable.ExitPrompt != tc.value {
				t.Fatalf("after an unrelated save: launch table %+v, want exit_prompt = %q round-tripped unchanged", again.LaunchTable, tc.value)
			}
		})
	}
}

// TestIgnoredLaunchKeys pins the tolerated-and-ignored surface: a
// `[launch] exit_prompt` carrying a non-empty value is named for the
// once-per-process note whatever the value is (no spelling is special —
// nothing reads it), and an absent key, a bare [launch] table or an empty
// value names nothing, so a config with no legacy key gets no note.
func TestIgnoredLaunchKeys(t *testing.T) {
	legacy := []string{"[launch] exit_prompt"}
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"no table", "default_tag = \"code\"\n", nil},
		{"bare table", "default_tag = \"code\"\n[launch]\n", nil},
		{"empty value", "default_tag = \"code\"\n[launch]\nexit_prompt = \"\"\n", nil},
		{"ask", "default_tag = \"code\"\n[launch]\nexit_prompt = \"ask\"\n", legacy},
		{"keep", "default_tag = \"code\"\n[launch]\nexit_prompt = \"keep\"\n", legacy},
		{"a value no code reads", "default_tag = \"code\"\n[launch]\nexit_prompt = \"remove\"\n", legacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			litellmStateEnv(t, tc.body)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.IgnoredLaunchKeys(); !slices.Equal(got, tc.want) {
				t.Fatalf("IgnoredLaunchKeys() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLaunchTableSurvivesASave pins that wt's own config writes (PatchSave,
// used by every other setter) keep a hand-edited [launch] table and its
// exit_prompt value verbatim. The key is legacy and ignored now (R305a — no
// value is validated or consulted), but tolerated means round-tripped: an
// unrelated save may neither drop the table nor rewrite what it holds.
func TestLaunchTableSurvivesASave(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n[launch]\nexit_prompt = \"keep\"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "other" }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "exit_prompt") {
		t.Fatalf("[launch] dropped by the save:\n%s", data)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.LaunchTable == nil || again.LaunchTable.ExitPrompt != "keep" || again.DefaultTag != "other" {
		t.Fatalf("after save: launch=%+v tag=%q", again.LaunchTable, again.DefaultTag)
	}
}
