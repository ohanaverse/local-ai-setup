// wt/internal/profiles/configcontent_test.go
package profiles

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestConfigFileTargetClaudeIsWorktreeScoped verifies claude's target is
// inside the launch's own worktree (.claude/settings.local.json), never
// the global ~/.claude/settings.json — the whole point of the
// worktree-scoped design (spec §4).
func TestConfigFileTargetClaudeIsWorktreeScoped(t *testing.T) {
	path, isTOML, ok, err := ConfigFileTarget("claude", "/tmp/some-worktree")
	if err != nil {
		t.Fatalf("ConfigFileTarget(claude) error = %v, want nil", err)
	}
	if !ok || isTOML {
		t.Fatalf("ConfigFileTarget(claude) = (%q, %v, %v), want (path, false, true)", path, isTOML, ok)
	}
	want := filepath.Join("/tmp/some-worktree", ".claude", "settings.local.json")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// TestConfigFileTargetOpenCodeHasNoFile verifies opencode reports ok=false
// — its config_content goes through OPENCODE_CONFIG_CONTENT env merging
// instead (ApplyConfigContent's opencode branch), never a file.
func TestConfigFileTargetOpenCodeHasNoFile(t *testing.T) {
	_, _, ok, err := ConfigFileTarget("opencode", "/tmp/wt")
	if err != nil {
		t.Fatalf("ConfigFileTarget(opencode) error = %v, want nil", err)
	}
	if ok {
		t.Error("ConfigFileTarget(opencode) ok = true, want false (env-merged, no file)")
	}
}

// TestConfigFileTargetCodexUsesGlobalHomeScopedPath verifies codex's target
// resolves to ~/.codex/agent-wt-profile.config.toml (TOML, ok=true, no
// error) using the real os.UserHomeDir() resolution ($HOME on
// darwin/linux) — codex has no project-scoped profile mechanism, so its
// target is a dedicated wt-owned GLOBAL file, never inside a
// repo/worktree.
func TestConfigFileTargetCodexUsesGlobalHomeScopedPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, isTOML, ok, err := ConfigFileTarget("codex", "/tmp/some-worktree")
	if err != nil {
		t.Fatalf("ConfigFileTarget(codex) error = %v, want nil", err)
	}
	if !ok || !isTOML {
		t.Fatalf("ConfigFileTarget(codex) = (%q, %v, %v), want (path, true, true)", path, isTOML, ok)
	}
	want := filepath.Join(home, ".codex", "agent-wt-profile.config.toml")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// TestConfigFileTargetCodexHomeDirErrorPropagates verifies that when
// os.UserHomeDir() cannot resolve $HOME, ConfigFileTarget reports a real
// error instead of silently falling back to a relative path under the
// process's cwd — a relative path would defeat codex's target being a
// fixed global file, never inside a repo/worktree (this was a real bug in
// the original brief: `home, _ := os.UserHomeDir()` swallowed the error).
func TestConfigFileTargetCodexHomeDirErrorPropagates(t *testing.T) {
	t.Setenv("HOME", "")
	// os.UserHomeDir() on darwin/linux consults $HOME only; clearing it
	// with no fallback makes resolution fail.
	_, _, ok, err := ConfigFileTarget("codex", "/tmp/some-worktree")
	if err == nil {
		t.Fatal("ConfigFileTarget(codex) error = nil, want non-nil when $HOME is unresolvable")
	}
	if !ok {
		t.Error("ConfigFileTarget(codex) ok = false, want true (codex does have a target mechanism; err signals resolution failure)")
	}
}

// TestApplyConfigContentWritesAndBacksUpExistingFile verifies an existing
// target file's content is snapshotted before being overwritten, and that
// the returned cleanup restores it exactly — the "save, overwrite for the
// session, restore on exit" behavior the design commits to.
func TestApplyConfigContentWritesAndBacksUpExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"env":{"MY_OWN_SETTING":"keep-me"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"MAX_THINKING_TOKENS": "4096"}}}
	cleanup, err := ApplyConfigContent(cmd, "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if _, has := doc["env"]; !has {
		t.Errorf("written content = %s, want an env key", written)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Errorf("restored content = %s, want original %s", restored, original)
	}
}

// TestApplyConfigContentDeletesFileThatDidNotExistBefore verifies that
// when the target had no prior file, cleanup deletes it rather than
// leaving the wt-written file behind — the "restore, or delete if none
// existed" half of the design.
func TestApplyConfigContentDeletesFileThatDidNotExistBefore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	cleanup, err := ApplyConfigContent(cmd, "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target still exists after cleanup, want deleted (stat err = %v)", err)
	}
}

// TestApplyConfigContentSelfHealsOrphanedBackup verifies that if a
// previous session's backup for this exact target was never restored
// (simulating wt being killed mid-session), the NEXT write for that same
// target restores the orphaned original content first, before writing
// new content — so a hand-edited file is never permanently lost to an
// unclean exit.
func TestApplyConfigContentSelfHealsOrphanedBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	// First "session": write profile content, but DO NOT call cleanup —
	// simulates `kill -9` before the restore could run.
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("first ApplyConfigContent() error = %v", err)
	}

	// Second "session" for the same target: the leftover backup from the
	// first session must be restored (self-heal) before the new write.
	restored, err := restoreIfBackedUp(target)
	if err != nil {
		t.Fatalf("restoreIfBackedUp() error = %v", err)
	}
	if !restored {
		t.Fatal("restoreIfBackedUp() restored = false, want true (orphaned backup from first session)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(handEdited) {
		t.Errorf("self-healed content = %s, want original hand-edited content %s", got, handEdited)
	}
}

// TestApplyConfigContentCodexWritesValidTOMLAndAppendsProfileFlag verifies
// codex's config_content is written as valid TOML that round-trips
// rp.ConfigContent's shape, and that ApplyConfigContent appends
// `--profile agent-wt-profile` to cmd.Args so codex actually picks the
// written file up — without that flag codex would silently ignore the
// wt-written profile file.
func TestApplyConfigContentCodexWritesValidTOMLAndAppendsProfileFlag(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))

	cmd := exec.Command("codex")
	rp := ResolvedProfile{ConfigContent: map[string]any{"model_reasoning_effort": "high"}}
	cleanup, err := ApplyConfigContent(cmd, "codex", "/tmp/some-worktree", rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()

	target := filepath.Join(home, ".codex", "agent-wt-profile.config.toml")
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("codex config_content file not written: %v", err)
	}
	var doc map[string]any
	if _, err := toml.Decode(string(written), &doc); err != nil {
		t.Fatalf("written file is not valid TOML: %v (%s)", err, written)
	}
	if doc["model_reasoning_effort"] != "high" {
		t.Errorf("round-tripped TOML = %v, want model_reasoning_effort=high", doc)
	}

	wantArgs := []string{"--profile", "agent-wt-profile"}
	gotArgs := cmd.Args[len(cmd.Args)-2:]
	if gotArgs[0] != wantArgs[0] || gotArgs[1] != wantArgs[1] {
		t.Errorf("cmd.Args tail = %v, want %v appended", gotArgs, wantArgs)
	}
}

// TestApplyConfigContentCodexHomeDirErrorFailsLoudly verifies that when
// codex's global target can't be resolved (unresolvable $HOME),
// ApplyConfigContent returns a real error and writes nothing, rather than
// silently writing to a relative path under the process's cwd (see fix
// for the swallowed os.UserHomeDir() error in ConfigFileTarget).
func TestApplyConfigContentCodexHomeDirErrorFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relTarget := filepath.Join(cwd, ".codex", "agent-wt-profile.config.toml")

	cmd := exec.Command("codex")
	rp := ResolvedProfile{ConfigContent: map[string]any{"model_reasoning_effort": "high"}}
	if _, err := ApplyConfigContent(cmd, "codex", "/tmp/some-worktree", rp); err == nil {
		t.Fatal("ApplyConfigContent() error = nil, want non-nil when $HOME is unresolvable")
	}
	if _, statErr := os.Stat(relTarget); !os.IsNotExist(statErr) {
		t.Errorf("ApplyConfigContent wrote to relative-path fallback %q, want no write anywhere", relTarget)
	}
}

// TestApplyConfigContentOpenCodeMergesIntoEnv verifies opencode's
// config_content merges into the existing OPENCODE_CONFIG_CONTENT env
// entry (already set by the opencode driver's Build()) rather than
// writing any file, and that the merge is additive — existing top-level
// keys (model, small_model, provider) survive alongside the new one.
func TestApplyConfigContentOpenCodeMergesIntoEnv(t *testing.T) {
	cmd := exec.Command("opencode")
	cmd.Env = []string{`OPENCODE_CONFIG_CONTENT={"model":"agent-wt/x","small_model":"agent-wt/x"}`}
	rp := ResolvedProfile{ConfigContent: map[string]any{"compaction": map[string]any{"auto": true}}}
	cleanup, err := ApplyConfigContent(cmd, "opencode", "/tmp/wt", rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()
	var merged string
	for _, e := range cmd.Env {
		if len(e) > len("OPENCODE_CONFIG_CONTENT=") && e[:len("OPENCODE_CONFIG_CONTENT=")] == "OPENCODE_CONFIG_CONTENT=" {
			merged = e[len("OPENCODE_CONFIG_CONTENT="):]
		}
	}
	if merged == "" {
		t.Fatal("OPENCODE_CONFIG_CONTENT not found in cmd.Env")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatalf("merged env is not valid JSON: %v (%s)", err, merged)
	}
	if doc["model"] != "agent-wt/x" {
		t.Errorf("merged doc lost existing key: %v", doc)
	}
	if _, has := doc["compaction"]; !has {
		t.Errorf("merged doc missing new key: %v", doc)
	}
}
