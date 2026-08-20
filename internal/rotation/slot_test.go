package rotation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestSlotFromFlags covers the constructor used by the TUI and non-TUI
// launch paths to build a rotation slot from the (agent, tag, family)
// triple. The empty-value normalization is the contract that keeps
// state-file names predictable.
func TestSlotFromFlags(t *testing.T) {
	tests := []struct {
		name            string
		agent, tag, fam string
		want            Slot
	}{
		{"all set", "claude", "code", "gemma4", Slot{"claude", "code", "gemma4"}},
		{"empty tag normalized", "claude", "", "gemma4", Slot{"claude", "-", "gemma4"}},
		{"empty family normalized", "claude", "code", "", Slot{"claude", "code", "-"}},
		{"both empty", "claude", "", "", Slot{"claude", "-", "-"}},
		{"comma in tag escaped", "claude", "code,design", "", Slot{"claude", "code_design", "-"}},
		{"dot in tag escaped", "claude", "v1.0", "", Slot{"claude", "v1_0", "-"}},
		{"slash in family passed through", "claude", "code", "team/ai", Slot{"claude", "code", "team/ai"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SlotFromFlags(tc.agent, tc.tag, tc.fam)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestStateFileForSlot locks down the on-disk naming convention. Any
// change here is a state-file migration; reviewers must be loud.
func TestStateFileForSlot(t *testing.T) {
	tests := []struct {
		slot Slot
		want string
	}{
		{Slot{"claude", "code", "-"}, "/tmp/agent-wt/rotation-claude-code-_.state"},
		{Slot{"claude", "code", "gemma4"}, "/tmp/agent-wt/rotation-claude-code-gemma4.state"},
		{Slot{"pi", "design", "-"}, "/tmp/agent-wt/rotation-pi-design-_.state"},
		{Slot{"", "code", "qwen"}, "/tmp/agent-wt/rotation-_-code-qwen.state"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := StateFileForSlot("/tmp/agent-wt", tc.slot)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLastLaunchedBackCompat verifies that a Rotation reads the legacy
// rotation-<tag>.state file when the new per-slot file is missing.
// This protects existing users from losing their rotation state when
// they upgrade.
func TestLastLaunchedBackCompat(t *testing.T) {
	dir := t.TempDir()
	// Write the legacy tag-only file.
	legacyPath := filepath.Join(dir, "rotation-code.state")
	if err := os.WriteFile(legacyPath, []byte("claude/opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	models := []config.Model{
		{ID: "claude/opus"},
		{ID: "claude/sonnet"},
	}
	r := NewForSlot(Slot{"claude", "code", "-"}, models, dir)
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("expected ok=true from legacy file")
	}
	if got.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", got.ID)
	}
}

// TestLastLaunchedPrefersNewFile verifies that the per-slot file wins
// when both exist. Without this precedence, the first RecordLaunch
// after the upgrade would silently re-use the legacy file and
// per-slot writes would look like they took no effect.
func TestLastLaunchedPrefersNewFile(t *testing.T) {
	dir := t.TempDir()
	// Legacy file with old model.
	if err := os.WriteFile(filepath.Join(dir, "rotation-code.state"), []byte("claude/opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// New per-slot file with a different model.
	newPath := StateFileForSlot(dir, Slot{"claude", "code", "-"})
	if err := os.WriteFile(newPath, []byte("claude/sonnet\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	models := []config.Model{
		{ID: "claude/opus"},
		{ID: "claude/sonnet"},
	}
	r := NewForSlot(Slot{"claude", "code", "-"}, models, dir)
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.ID != "claude/sonnet" {
		t.Errorf("got %q, want claude/sonnet (from new file)", got.ID)
	}
}

// TestRecordLaunchWritesNewFile verifies that RecordLaunch writes to
// the new per-slot file and leaves any legacy file untouched. The
// legacy file stays on disk so a rollback or shared config dir can
// still find the old state; only fresh writes target the per-slot
// file introduced in PR 3a.
func TestRecordLaunchWritesNewFile(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "rotation-code.state")
	if err := os.WriteFile(legacyPath, []byte("claude/opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewForSlot(Slot{"claude", "code", "-"}, []config.Model{{ID: "claude/sonnet"}}, dir)
	if err := r.RecordLaunch(config.Model{ID: "claude/sonnet"}); err != nil {
		t.Fatal(err)
	}

	// New per-slot file has the new model.
	newPath := StateFileForSlot(dir, Slot{"claude", "code", "-"})
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "claude/sonnet" {
		t.Errorf("new file = %q, want claude/sonnet", strings.TrimSpace(string(data)))
	}

	// Legacy file is unchanged.
	data, err = os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "claude/opus" {
		t.Errorf("legacy file = %q, want claude/opus (untouched)", strings.TrimSpace(string(data)))
	}
}
