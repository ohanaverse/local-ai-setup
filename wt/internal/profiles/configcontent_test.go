// wt/internal/profiles/configcontent_test.go
package profiles

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

// TestConfigFileTargetClaudeResolvesRelativeWorktreePathToAbsolute is the
// regression lock for the code-review finding that an outside-a-git-repo
// claude launch (worktreePath == ".", see cmd/wt/main.go's outside-repo
// passthrough) always hashed the SAME backup key regardless of which real
// directory the launch actually ran in: ConfigFileTarget joined "." onto
// ".claude/settings.local.json" literally, and backupKey (which hashes the
// returned path string) collided across launches from different
// directories that all happened to pass the relative path ".". Two
// launches from different directories must resolve to different,
// directory-specific backup keys.
func TestConfigFileTargetClaudeResolvesRelativeWorktreePathToAbsolute(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}
	pathA, _, _, err := ConfigFileTarget("claude", ".")
	if err != nil {
		t.Fatalf("ConfigFileTarget(claude, \".\") in dirA: %v", err)
	}

	if err := os.Chdir(dirB); err != nil {
		t.Fatal(err)
	}
	pathB, _, _, err := ConfigFileTarget("claude", ".")
	if err != nil {
		t.Fatalf("ConfigFileTarget(claude, \".\") in dirB: %v", err)
	}

	if !filepath.IsAbs(pathA) || !filepath.IsAbs(pathB) {
		t.Fatalf("ConfigFileTarget(claude, \".\") = (%q, %q), want both absolute", pathA, pathB)
	}
	if pathA == pathB {
		t.Fatalf("ConfigFileTarget(claude, \".\") returned the same path %q for two different directories", pathA)
	}
	if backupKey(pathA) == backupKey(pathB) {
		t.Error("backupKey(pathA) == backupKey(pathB), want distinct backup keys for distinct directories")
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
// target file's content is snapshotted before config_content is merged in,
// and that the returned cleanup restores the profile's own top-level keys
// to their pre-launch values — the "save, merge for the session, restore
// on exit" behavior the design commits to. Cleanup now does a key-level
// restore (re-marshaled JSON), not a byte-for-byte one — compared
// semantically here; see
// TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession for why.
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
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(restored, &gotDoc); err != nil {
		t.Fatalf("restored content is not valid JSON: %v (%s)", err, restored)
	}
	if err := json.Unmarshal(original, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("restored content = %v, want %v (same keys/values as the pre-launch original)", gotDoc, wantDoc)
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
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("self-healed content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("self-healed content = %v, want %v (the original hand-edited content)", gotDoc, wantDoc)
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

// TestApplyConfigContentCodexProfileFlagOnlyAppendedAfterSuccessfulWrite is
// the regression lock for Important finding #2: --profile
// agent-wt-profile must be appended to cmd.Args ONLY after
// snapshotAndWrite has actually succeeded. Before the fix, the flag was
// appended unconditionally before the write, so a failed write (which the
// caller degrades to an unprofiled launch) still left codex pointed at a
// file that might be missing or stale. This forces a deterministic write
// failure by pre-creating the target path AS A DIRECTORY (so
// os.ReadFile/WriteFileAtomic fail on it), then asserts cmd.Args is
// completely unchanged.
func TestApplyConfigContentCodexProfileFlagOnlyAppendedAfterSuccessfulWrite(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))

	target := filepath.Join(home, ".codex", "agent-wt-profile.config.toml")
	if err := os.MkdirAll(target, 0o755); err != nil { // target is a DIRECTORY, not a file
		t.Fatal(err)
	}

	cmd := exec.Command("codex", "--model", "x")
	before := append([]string{}, cmd.Args...)
	rp := ResolvedProfile{ConfigContent: map[string]any{"model_reasoning_effort": "high"}}
	if _, err := ApplyConfigContent(cmd, "codex", "/tmp/some-worktree", rp); err == nil {
		t.Fatal("ApplyConfigContent() error = nil, want an error (target path is a directory)")
	}
	if strings.Join(cmd.Args, "|") != strings.Join(before, "|") {
		t.Errorf("cmd.Args = %v, want unchanged %v (--profile must not be appended before a successful write)", cmd.Args, before)
	}
}

// TestMergeOpenCodeEnvUsesLastEntryNotFirst is the regression lock for
// Important finding #3: mergeOpenCodeEnv must merge into the LAST
// OPENCODE_CONFIG_CONTENT entry in cmd.Env, not the first. Command()
// (internal/agents) builds cmd.Env as os.Environ() (inherited parent env)
// followed by the driver's own entries; if the parent process already
// exports OPENCODE_CONFIG_CONTENT (e.g. wt launched from inside an
// opencode session), a first-match merge would silently target that
// unrelated inherited value — and since exec.Cmd.Env uses the LAST value
// for a duplicate key, the driver's own untouched (unmerged) entry would
// then win at exec time, silently dropping the profile.
func TestMergeOpenCodeEnvUsesLastEntryNotFirst(t *testing.T) {
	cmd := exec.Command("opencode")
	cmd.Env = []string{
		`OPENCODE_CONFIG_CONTENT={"model":"stale-inherited-value"}`,                       // simulates a parent shell's own inherited env
		`OPENCODE_CONFIG_CONTENT={"model":"agent-wt/real","small_model":"agent-wt/real"}`, // the opencode driver's own (real, later) entry
	}
	rp := ResolvedProfile{ConfigContent: map[string]any{"compaction": map[string]any{"auto": true}}}
	cleanup, err := ApplyConfigContent(cmd, "opencode", "/tmp/wt", rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()

	if len(cmd.Env) != 2 {
		t.Fatalf("cmd.Env = %v, want 2 entries (no dedupe/removal)", cmd.Env)
	}
	if cmd.Env[0] != `OPENCODE_CONFIG_CONTENT={"model":"stale-inherited-value"}` {
		t.Errorf("first (inherited) entry was mutated: %v, want it left untouched", cmd.Env[0])
	}
	merged := strings.TrimPrefix(cmd.Env[1], openCodeConfigEnvPrefix)
	var doc map[string]any
	if err := json.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatalf("last entry not valid JSON: %v (%s)", err, merged)
	}
	if doc["model"] != "agent-wt/real" {
		t.Errorf("merged doc = %v, lost its own model key", doc)
	}
	if _, has := doc["compaction"]; !has {
		t.Errorf("merged doc = %v, missing the merged compaction key", doc)
	}
}

// TestSelfHealRestoresOrphanedBackup verifies the exported SelfHeal
// wrapper (added for Important finding #4, so cmd/wt's
// applyProfileForLaunch can self-heal a config_content target from outside
// this package) delegates correctly to the same restore logic
// TestApplyConfigContentSelfHealsOrphanedBackup exercises directly: a
// backup left behind by a session that never called its cleanup (kill -9)
// is restored, and the restored flag reports true.
func TestSelfHealRestoresOrphanedBackup(t *testing.T) {
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
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("first ApplyConfigContent() error = %v", err)
	}
	// No cleanup called — simulates kill -9 before the restore could run.

	restored, err := SelfHeal(target)
	if err != nil {
		t.Fatalf("SelfHeal() error = %v", err)
	}
	if !restored {
		t.Fatal("SelfHeal() restored = false, want true (orphaned backup from the prior session)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("self-healed content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("self-healed content = %v, want %v (the original hand-edited content)", gotDoc, wantDoc)
	}
}

// TestSelfHealNoopWithoutBackup verifies SelfHeal is safe to call
// unconditionally on every launch (as applyProfileForLaunch now does) even
// when there is nothing to restore — no backup file, no error, and the
// target (which may not even exist) is left alone.
func TestSelfHealNoopWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	target := filepath.Join(dir, "worktree", ".claude", "settings.local.json")

	restored, err := SelfHeal(target)
	if err != nil {
		t.Fatalf("SelfHeal() error = %v, want nil", err)
	}
	if restored {
		t.Error("SelfHeal() restored = true, want false (no backup exists)")
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("SelfHeal() created %q out of nothing", target)
	}
}

// TestRestoreIfBackedUpReturnsTrueWhenCleanupFailsAfterSuccessfulRestore is
// the regression lock for the code-review finding that
// restoreIfBackedUpLocked's two restore branches disagreed on what
// `restored` bool to return when the restore itself succeeds but the
// following backup-marker cleanup then fails: the "target was absent"
// branch already returned (true, err) for this case, but the "target
// existed" branch returned (false, err) — falsely reporting "not restored"
// despite the target's content already being back in place.
func TestRestoreIfBackedUpReturnsTrueWhenCleanupFailsAfterSuccessfulRestore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	cmd := exec.Command("claude")
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("seed backup: %v", err)
	}

	// Make backupDir() read-only so the restore's own WriteFileAtomic(target,
	// ...) still succeeds (target lives under worktree, not backupDir()) but
	// the subsequent os.Remove(backupPresentPath(target)) inside backupDir()
	// fails with permission denied — the exact split this test pins.
	if err := os.Chmod(backupDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(backupDir(), 0o700) })

	restored, err := restoreIfBackedUp(target)
	if err == nil {
		t.Fatal("restoreIfBackedUp() error = nil, want the cleanup permission error")
	}
	if !restored {
		t.Error("restoreIfBackedUp() restored = false, want true — the target's content was already restored before cleanup failed")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("restored content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("target content = %v, want %v (the restored hand-edited content)", gotDoc, wantDoc)
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

// TestMergeOpenCodeEnvDeepMergesCollidingTopLevelKey is the regression lock
// for the code-review finding that mergeOpenCodeEnv did a shallow merge: a
// profile's config_content.provider colliding with the opencode driver's
// own top-level "provider" key (baseURL/apiKey/models — set by Build())
// must merge the profile's new nested field in, not wholesale-replace the
// whole object and silently drop the connectivity fields.
func TestMergeOpenCodeEnvDeepMergesCollidingTopLevelKey(t *testing.T) {
	cmd := exec.Command("opencode")
	cmd.Env = []string{`OPENCODE_CONFIG_CONTENT={"provider":{"agent-wt":{"baseURL":"http://x","apiKey":"k"}}}`}
	rp := ResolvedProfile{ConfigContent: map[string]any{
		"provider": map[string]any{"agent-wt": map[string]any{"extra": "field"}},
	}}
	cleanup, err := ApplyConfigContent(cmd, "opencode", "/tmp/wt", rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()

	merged := strings.TrimPrefix(cmd.Env[0], openCodeConfigEnvPrefix)
	var doc map[string]any
	if err := json.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatalf("merged env is not valid JSON: %v (%s)", err, merged)
	}
	provider, ok := doc["provider"].(map[string]any)
	if !ok {
		t.Fatalf("doc[provider] = %#v, want a map", doc["provider"])
	}
	agentWT, ok := provider["agent-wt"].(map[string]any)
	if !ok {
		t.Fatalf("doc[provider][agent-wt] = %#v, want a map", provider["agent-wt"])
	}
	if agentWT["baseURL"] != "http://x" || agentWT["apiKey"] != "k" {
		t.Errorf("doc[provider][agent-wt] = %v, lost the driver's own baseURL/apiKey", agentWT)
	}
	if agentWT["extra"] != "field" {
		t.Errorf("doc[provider][agent-wt] = %v, missing the profile's merged field", agentWT)
	}
}

// TestApplyConfigContentPreservesOriginalFilePermissions verifies that
// cleanup restores a pre-existing target with its ORIGINAL permission bits
// (e.g. a hand-set 0600), not a hardcoded 0644 — restoreIfBackedUpLocked
// used to always write the restored file back at 0644 regardless of what
// mode it found, silently loosening a tighter-permissioned config file.
func TestApplyConfigContentPreservesOriginalFilePermissions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"env":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	cleanup, err := ApplyConfigContent(cmd, "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("restored mode = %o, want 0600 (the original, not a hardcoded 0644)", got)
	}
}

// TestWithTargetLockSerializesConcurrentAccess verifies restoreIfBackedUp
// (and therefore snapshotAndWrite, which shares the same per-target lock)
// blocks while another process holds the lock instead of racing it — the
// gap that let two concurrent profiled launches of the same agent (e.g.
// codex, whose config_content target is a fixed global path, not
// worktree-scoped) self-heal over or restore out from under each other's
// still-in-use write.
func TestWithTargetLockSerializesConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	target := filepath.Join(dir, "target.toml")

	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(backupKey(target)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := restoreIfBackedUp(target)
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("restoreIfBackedUp() returned (err = %v) while another holder still held the lock", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("restoreIfBackedUp() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restoreIfBackedUp() never completed after the lock was released")
	}
}

// TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession is the
// regression lock for the code-review finding that cleanup blindly
// restored the pre-launch snapshot, discarding any legitimate content the
// launched agent itself wrote to the same file during the session (e.g.
// Claude Code persisting a user-approved permission grant into
// settings.local.json). It simulates the agent writing a new top-level key
// while the profile-merged file is in place, then asserts cleanup keeps
// that key while still reverting the profile's own "env" key to its
// pre-launch value.
func TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession(t *testing.T) {
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

	// Simulate the launched agent (e.g. Claude Code) persisting a new
	// top-level key into the same file mid-session — a real "always
	// allow" permission grant, in production.
	withGrant := []byte(`{"env":{"MY_OWN_SETTING":"keep-me","MAX_THINKING_TOKENS":"4096"},"permissions":{"allow":["Bash(npm test)"]}}`)
	if err := os.WriteFile(target, withGrant, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("restored file is not valid JSON: %v (%s)", err, got)
	}
	if _, has := doc["permissions"]; !has {
		t.Errorf("restored content = %s, lost the permission grant the agent wrote during the session", got)
	}
	env, ok := doc["env"].(map[string]any)
	if !ok {
		t.Fatalf("restored content = %s, want an env object", got)
	}
	if env["MY_OWN_SETTING"] != "keep-me" {
		t.Errorf("env = %v, want the pre-launch MY_OWN_SETTING preserved", env)
	}
	if _, has := env["MAX_THINKING_TOKENS"]; has {
		t.Errorf("env = %v, want the profile's own MAX_THINKING_TOKENS key removed on restore", env)
	}
}

// TestApplyConfigContentRefusesWhenTargetOwnedByLiveSession is the
// regression lock for the code-review finding that the per-target flock
// only serializes the brief snapshot/write/restore call, not a launched
// agent's whole lifetime: a second launch touching the SAME config_content
// target (e.g. codex's fixed global path, shared across every worktree)
// used to self-heal over — or write on top of — a backup that in fact
// still belongs to a live, still-running sibling wt session. This
// simulates that sibling by writing the backup's .owner marker to a
// different (fake) pid and forcing pidAlive to report it alive; the
// second session's write must refuse instead of clobbering the first
// session's still-in-use file.
func TestApplyConfigContentRefusesWhenTargetOwnedByLiveSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd1 := exec.Command("claude")
	rp1 := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd1, "claude", worktree, rp1); err != nil {
		t.Fatalf("session A ApplyConfigContent() error = %v", err)
	}

	fakeOwnerPid := os.Getpid() + 999983 // arbitrary, distinct from this test process's own pid
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == fakeOwnerPid }
	t.Cleanup(func() { pidAlive = origPidAlive })

	sessionAContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	cmd2 := exec.Command("claude")
	rp2 := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"Y": "2"}}}
	if _, err := ApplyConfigContent(cmd2, "claude", worktree, rp2); err == nil {
		t.Fatal("session B ApplyConfigContent() error = nil, want an error (target owned by a live sibling session)")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sessionAContent) {
		t.Errorf("target was modified by session B: got %s, want session A's untouched content %s", got, sessionAContent)
	}
	if _, err := os.Stat(backupPresentPath(target)); err != nil {
		t.Errorf("session A's backup was removed by session B's failed attempt: %v", err)
	}
}

// TestSelfHealSkipsBackupOwnedByLiveProcess verifies the exported SelfHeal
// entry point (used by cmd/wt's applyProfileForLaunch on every launch of an
// agent) also respects the live-owner guard: it must never restore a
// backup a different, still-running wt process still owns.
func TestSelfHealSkipsBackupOwnedByLiveProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}

	fakeOwnerPid := os.Getpid() + 999983
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == fakeOwnerPid }
	t.Cleanup(func() { pidAlive = origPidAlive })

	liveContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := SelfHeal(target)
	if err != nil {
		t.Fatalf("SelfHeal() error = %v, want nil (a live owner must never fail the launch)", err)
	}
	if restored {
		t.Error("SelfHeal() restored = true, want false (backup is owned by a live sibling session)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(liveContent) {
		t.Errorf("SelfHeal() modified a live-owned target: got %s, want %s", got, liveContent)
	}
}

// TestRestoreIfBackedUpStillHealsWhenOwnerIsDead verifies the pre-existing
// crash-recovery self-heal behavior survives the new owner-liveness guard:
// when the recorded owner pid is a DIFFERENT process that is no longer
// alive (the owning wt process was killed, e.g. `kill -9`), the backup is
// still treated as orphaned and restored, exactly as before this fix.
func TestRestoreIfBackedUpStillHealsWhenOwnerIsDead(t *testing.T) {
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
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}

	fakeOwnerPid := os.Getpid() + 999983
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(int) bool { return false } // the recorded owner is no longer running
	t.Cleanup(func() { pidAlive = origPidAlive })

	restored, err := restoreIfBackedUp(target)
	if err != nil {
		t.Fatalf("restoreIfBackedUp() error = %v", err)
	}
	if !restored {
		t.Fatal("restoreIfBackedUp() restored = false, want true (owner pid is dead — orphaned backup)")
	}
	var gotDoc, wantDoc map[string]any
	gotBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &gotDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("self-healed content = %v, want %v", gotDoc, wantDoc)
	}
}

// TestSnapshotAndWriteLeavesNoOrphanedBackupWhenBuildDataFails is the
// regression lock for the final-review finding that a buildData failure
// (e.g. existing settings.local.json is not valid JSON) used to still
// leave a fresh backup + owner marker behind, because they were written
// BEFORE buildData ran. A later launch's self-heal would then treat that
// marker as an ordinary orphaned-crash backup and silently overwrite
// whatever the user had since hand-fixed the file to, with the stale
// pre-failure snapshot. buildData must run — and succeed — before any
// backup marker is written, so a failed write leaves nothing behind to
// self-heal over.
func TestSnapshotAndWriteLeavesNoOrphanedBackupWhenBuildDataFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	invalidJSON := []byte(`{ not valid json`)
	if err := os.WriteFile(target, invalidJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err == nil {
		t.Fatal("ApplyConfigContent() error = nil, want an error (existing content is not valid JSON)")
	}

	if _, statErr := os.Stat(backupPresentPath(target)); !os.IsNotExist(statErr) {
		t.Errorf("backupPresentPath exists after a buildData failure, want no orphaned backup (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(backupOwnerPath(target)); !os.IsNotExist(statErr) {
		t.Errorf("backupOwnerPath exists after a buildData failure, want no orphaned owner marker (stat err = %v)", statErr)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(invalidJSON) {
		t.Errorf("target = %s, want it left untouched %s", got, invalidJSON)
	}
}

// TestApplyConfigContentCleanupPreservesKeysWrittenDuringSessionWhenTargetDidNotExistBefore
// is the regression lock for the final-review finding that the "target did
// not exist before" restore branch always deleted the whole file, even
// when the launched agent wrote legitimate content into it during the
// session (e.g. Claude Code creating settings.local.json for the first
// time — the common case for a fresh worktree — and then persisting a
// permission grant into it): TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession
// already fixed this for a target that DID exist beforehand, but the
// "did not exist" branch still discarded everything unconditionally.
func TestApplyConfigContentCleanupPreservesKeysWrittenDuringSessionWhenTargetDidNotExistBefore(t *testing.T) {
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

	// Simulate the launched agent creating settings.local.json for the
	// first time and persisting a permission grant into it mid-session.
	withGrant := []byte(`{"env":{"X":"1"},"permissions":{"allow":["Bash(npm test)"]}}`)
	if err := os.WriteFile(target, withGrant, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target was deleted, losing the agent's permission grant: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("restored file is not valid JSON: %v (%s)", err, got)
	}
	if _, has := doc["permissions"]; !has {
		t.Errorf("restored content = %s, lost the permission grant the agent wrote during the session", got)
	}
	if _, has := doc["env"]; has {
		t.Errorf("restored content = %s, want the profile's own env key removed (target did not exist before)", got)
	}
}
