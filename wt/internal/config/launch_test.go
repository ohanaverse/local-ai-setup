package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLaunchExitPromptDefaultsToAsk pins that only an explicit "keep" turns
// the exit prompt off: no [launch] table, an empty value and "ask" all leave
// it on, because the safe default is to offer (never to remove silently).
func TestLaunchExitPromptDefaultsToAsk(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantKeep   bool
	}{
		{"no table", "default_tag = \"code\"\n", false},
		{"empty value", "default_tag = \"code\"\n[launch]\nexit_prompt = \"\"\n", false},
		{"bare table", "default_tag = \"code\"\n[launch]\n", false},
		{"ask", "default_tag = \"code\"\n[launch]\nexit_prompt = \"ask\"\n", false},
		{"keep", "default_tag = \"code\"\n[launch]\nexit_prompt = \"keep\"\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			litellmStateEnv(t, tc.body)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.ExitPromptKeep(); got != tc.wantKeep {
				t.Fatalf("ExitPromptKeep() = %v, want %v", got, tc.wantKeep)
			}
		})
	}
}

// TestLaunchExitPromptRejectsOtherValues pins that a value outside ask/keep
// (a typo, or an attempt at "remove") fails the load with an error naming the
// key and the allowed values, so no spelling silently deletes a worktree.
func TestLaunchExitPromptRejectsOtherValues(t *testing.T) {
	for _, v := range []string{"remove", "kepp", "yes"} {
		t.Run(v, func(t *testing.T) {
			litellmStateEnv(t, "default_tag = \"code\"\n[launch]\nexit_prompt = \""+v+"\"\n")
			_, err := Load()
			if err == nil {
				t.Fatalf("Load accepted exit_prompt = %q", v)
			}
			for _, want := range []string{"[launch] exit_prompt", "ask", "keep"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestLaunchTableSurvivesASave pins that wt's own config writes (PatchSave,
// used by every other setter) keep a hand-edited [launch] table, so a
// "keep" the user chose is not lost the next time wt saves something else.
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
	if !again.ExitPromptKeep() || again.DefaultTag != "other" {
		t.Fatalf("after save: keep=%v tag=%q", again.ExitPromptKeep(), again.DefaultTag)
	}
}
