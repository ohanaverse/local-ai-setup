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

// claudeTarget sets up a claude config_content target for one test: a
// throwaway XDG_CONFIG_HOME (so backups land in the test's own directory) and
// a worktree whose .claude/settings.local.json is the target. A non-nil
// content is written there as the pre-existing file; nil leaves the target,
// and the worktree, absent.
func claudeTarget(t *testing.T, content []byte) (worktree, target string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree = filepath.Join(dir, "worktree")
	target = filepath.Join(worktree, ".claude", "settings.local.json")
	if content != nil {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return worktree, target
}

// assertSameJSON fails the test unless got and want hold the same JSON
// document. The comparison is semantic, not byte for byte, because a restore
// re-marshals the file. what names got in the failure and wantNote says what
// want stands for.
func assertSameJSON(t *testing.T, what, wantNote string, got, want []byte) {
	t.Helper()
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("%s is not valid JSON: %v (%s)", what, err, got)
	}
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("%s = %v, want %v (%s)", what, gotDoc, wantDoc, wantNote)
	}
}

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

	t.Chdir(dirA)
	pathA, _, _, err := ConfigFileTarget("claude", ".")
	if err != nil {
		t.Fatalf("ConfigFileTarget(claude, \".\") in dirA: %v", err)
	}

	t.Chdir(dirB)
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
	original := []byte(`{"env":{"MY_OWN_SETTING":"keep-me"}}`)
	worktree, target := claudeTarget(t, original)

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
	assertSameJSON(t, "restored content", "same keys/values as the pre-launch original", restored, original)
}

// TestApplyConfigContentDeletesFileThatDidNotExistBefore verifies that
// when the target had no prior file, cleanup deletes it rather than
// leaving the wt-written file behind — the "restore, or delete if none
// existed" half of the design.
func TestApplyConfigContentDeletesFileThatDidNotExistBefore(t *testing.T) {
	worktree, target := claudeTarget(t, nil)

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
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	worktree, target := claudeTarget(t, handEdited)

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
	assertSameJSON(t, "self-healed content", "the original hand-edited content", got, handEdited)
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
// OPENCODE_CONFIG_CONTENT entry in cmd.Env, not the first. BuildLaunchCmd
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
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	worktree, target := claudeTarget(t, handEdited)

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
	assertSameJSON(t, "self-healed content", "the original hand-edited content", got, handEdited)
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
	assertSameJSON(t, "target content", "the restored hand-edited content", got, handEdited)
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
	original := []byte(`{"env":{"MY_OWN_SETTING":"keep-me"}}`)
	worktree, target := claudeTarget(t, original)

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
	original := []byte(`{"hooks":{"my-own":"thing"}}`)
	worktree, target := claudeTarget(t, original)

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
	original := []byte(`{"hooks":{"my-own":"thing"}}`)
	worktree, target := claudeTarget(t, original)
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
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	worktree, target := claudeTarget(t, handEdited)
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
	gotBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "self-healed content", "the original hand-edited content", gotBytes, handEdited)
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
	invalidJSON := []byte(`{ not valid json`)
	worktree, target := claudeTarget(t, invalidJSON)

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
	worktree, target := claudeTarget(t, nil)

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

// leafSession runs one claude config_content session against a fresh
// target: original is the pre-existing file (nil: none), content the
// profile's config_content, and session, when non-nil, edits the merged
// document on disk the way the launched agent would mid-session. It returns
// the target after cleanup, or nil when cleanup deleted it.
func leafSession(t *testing.T, original []byte, content map[string]any, session func(doc map[string]any)) []byte {
	t.Helper()
	worktree, target := claudeTarget(t, original)
	cleanup, err := ApplyConfigContent(exec.Command("claude"), "claude", worktree, ResolvedProfile{ConfigContent: content})
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	if session != nil {
		written, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(written, &doc); err != nil {
			t.Fatalf("written file is not valid JSON: %v (%s)", err, written)
		}
		session(doc)
		edited, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, edited, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// addAllow is the session's "always allow" grant: Claude Code saving
// permissions.allow beside whatever permissions already holds.
func addAllow(doc map[string]any) {
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		doc["permissions"] = perms
	}
	perms["allow"] = []any{"Bash(ls)"}
}

func pinDefaultMode(mode string) map[string]any {
	return map[string]any{"permissions": map[string]any{"defaultMode": mode}}
}

// TestCleanupKeepsSessionPermissionsAllowBesideThePinnedDefaultMode is the
// repro of #401: a profile that pins permissions.defaultMode used to make
// cleanup put the whole permissions object back, dropping the
// permissions.allow grant Claude Code saved mid-session. Restore is per
// leaf now, so only defaultMode goes back.
func TestCleanupKeepsSessionPermissionsAllowBesideThePinnedDefaultMode(t *testing.T) {
	got := leafSession(t, []byte(`{"permissions":{"defaultMode":"plan"}}`), pinDefaultMode("default"), addAllow)
	assertSameJSON(t, "restored content", "the session's allow kept, defaultMode restored",
		got, []byte(`{"permissions":{"allow":["Bash(ls)"],"defaultMode":"plan"}}`))
}

// TestCleanupKeepsSessionPermissionsAllowWhenTargetDidNotExistBefore is the
// new-file half of #401: with no settings.local.json before the launch, the
// pinned defaultMode is removed and the session's grant stays, rather than
// the whole permissions key (or file) going.
func TestCleanupKeepsSessionPermissionsAllowWhenTargetDidNotExistBefore(t *testing.T) {
	got := leafSession(t, nil, pinDefaultMode("default"), addAllow)
	if got == nil {
		t.Fatal("target was deleted, losing the session's permissions.allow")
	}
	assertSameJSON(t, "restored content", "the session's allow kept, defaultMode gone",
		got, []byte(`{"permissions":{"allow":["Bash(ls)"]}}`))
}

// TestCleanupKeepsAValueTheSessionChangedAfterTheMerge pins the owner's
// rule for #401: a leaf the profile wrote and the session then changed
// keeps the session's value, since the user chose it mid-session.
func TestCleanupKeepsAValueTheSessionChangedAfterTheMerge(t *testing.T) {
	change := func(doc map[string]any) {
		doc["permissions"].(map[string]any)["defaultMode"] = "acceptEdits"
	}
	for name, original := range map[string][]byte{
		"existing file": []byte(`{"permissions":{"defaultMode":"plan"}}`),
		"new file":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := leafSession(t, original, pinDefaultMode("default"), change)
			assertSameJSON(t, "restored content", "the session's acceptEdits kept",
				got, []byte(`{"permissions":{"defaultMode":"acceptEdits"}}`))
		})
	}
}

// TestCleanupDoesNotResurrectALeafTheSessionDeleted verifies a profile leaf
// the session removed stays removed: putting the original back would undo
// the user's own mid-session edit.
func TestCleanupDoesNotResurrectALeafTheSessionDeleted(t *testing.T) {
	got := leafSession(t, []byte(`{"permissions":{"defaultMode":"plan","allow":["X"]}}`), pinDefaultMode("default"),
		func(doc map[string]any) { delete(doc["permissions"].(map[string]any), "defaultMode") })
	assertSameJSON(t, "restored content", "defaultMode stays deleted",
		got, []byte(`{"permissions":{"allow":["X"]}}`))
}

// TestCleanupPrunesAContainerTheProfileCreatedAndRemovesTheFileWhenNothingIsLeft
// verifies a profile-only write into a file that did not exist leaves
// nothing behind: the permissions object the merge created is pruned once
// its leaf is gone, and the empty file is deleted.
func TestCleanupPrunesAContainerTheProfileCreatedAndRemovesTheFileWhenNothingIsLeft(t *testing.T) {
	if got := leafSession(t, nil, pinDefaultMode("default"), nil); got != nil {
		t.Errorf("target = %s after cleanup, want it deleted", got)
	}
}

// TestCleanupKeepsAnEmptyContainerTheOriginalHad verifies pruning stops at
// what the original held: an empty permissions object that was there
// before the launch is still there after it.
func TestCleanupKeepsAnEmptyContainerTheOriginalHad(t *testing.T) {
	got := leafSession(t, []byte(`{"permissions":{}}`), pinDefaultMode("default"), nil)
	assertSameJSON(t, "restored content", "the original empty permissions", got, []byte(`{"permissions":{}}`))
}

// TestRestoreTypeConflictKeepsSessionSiblingAndRestoresScalar covers an
// original that held a non-object where the profile writes an object: the
// merge replaces it. The profile's leaf is removed, a session sibling under
// the replaced key is kept, and the original scalar comes back only once
// nothing is left there.
func TestRestoreTypeConflictKeepsSessionSiblingAndRestoresScalar(t *testing.T) {
	original := []byte(`{"permissions":"legacy"}`)
	t.Run("session sibling", func(t *testing.T) {
		got := leafSession(t, original, pinDefaultMode("default"), addAllow)
		assertSameJSON(t, "restored content", "the session's allow, no defaultMode",
			got, []byte(`{"permissions":{"allow":["Bash(ls)"]}}`))
	})
	t.Run("no session sibling", func(t *testing.T) {
		got := leafSession(t, original, pinDefaultMode("default"), nil)
		assertSameJSON(t, "restored content", "the original scalar", got, original)
	})
}

// TestCleanupArraysAreWholeValues verifies an array the profile pinned is
// one leaf: a session that appended to it changed it, so the session's
// array stays rather than being reverted or merged element by element.
func TestCleanupArraysAreWholeValues(t *testing.T) {
	content := map[string]any{"permissions": map[string]any{"allow": []any{"A"}}}
	got := leafSession(t, []byte(`{"permissions":{"allow":["X"]}}`), content, func(doc map[string]any) {
		perms := doc["permissions"].(map[string]any)
		perms["allow"] = append(perms["allow"].([]any), "B")
	})
	assertSameJSON(t, "restored content", "the session's appended array",
		got, []byte(`{"permissions":{"allow":["A","B"]}}`))
}

// TestCleanupHandlesNullLeaf verifies a null the profile wrote is a leaf
// distinct from an absent one: with no original value it is deleted, and an
// original null stays null.
func TestCleanupHandlesNullLeaf(t *testing.T) {
	content := map[string]any{"statusLine": nil}
	t.Run("absent original", func(t *testing.T) {
		got := leafSession(t, []byte(`{"model":"x"}`), content, nil)
		assertSameJSON(t, "restored content", "statusLine deleted", got, []byte(`{"model":"x"}`))
	})
	t.Run("null original", func(t *testing.T) {
		got := leafSession(t, []byte(`{"model":"x","statusLine":null}`), content, nil)
		assertSameJSON(t, "restored content", "statusLine still null", got, []byte(`{"model":"x","statusLine":null}`))
	})
}

// TestCleanupKeepsSessionContentWhenTheProfileWroteNothingNew verifies a
// profile whose config_content changes nothing (an empty table merged into
// an existing object, which records no leaf) still restores surgically:
// falling back to the whole-file snapshot would drop the session's grant.
func TestCleanupKeepsSessionContentWhenTheProfileWroteNothingNew(t *testing.T) {
	content := map[string]any{"permissions": map[string]any{}}
	got := leafSession(t, []byte(`{"permissions":{"defaultMode":"plan"}}`), content, addAllow)
	assertSameJSON(t, "restored content", "the session's allow kept",
		got, []byte(`{"permissions":{"allow":["Bash(ls)"],"defaultMode":"plan"}}`))
}

// TestSelfHealRestoresLegacyKeysMarker verifies a backup an older wt left
// behind (a .keys marker listing top-level key names) still self-heals
// after an upgrade: the named key is restored from the snapshot whole, as
// that wt would have, and a key the session wrote elsewhere is kept.
func TestSelfHealRestoresLegacyKeysMarker(t *testing.T) {
	_, target := claudeTarget(t, []byte(`{"permissions":{"defaultMode":"default"},"model":"session"}`))
	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPresentPath(target), []byte(`{"permissions":{"defaultMode":"plan","allow":["X"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupKeysPath(target), []byte(`["permissions"]`), 0o600); err != nil {
		t.Fatal(err)
	}

	restored, err := SelfHeal(target)
	if err != nil {
		t.Fatalf("SelfHeal() error = %v", err)
	}
	if !restored {
		t.Fatal("SelfHeal() restored = false, want true (orphaned legacy backup)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, "self-healed content", "permissions from the snapshot, the session's model kept",
		got, []byte(`{"permissions":{"defaultMode":"plan","allow":["X"]},"model":"session"}`))
	if _, err := os.Stat(backupKeysPath(target)); !os.IsNotExist(err) {
		t.Errorf("legacy .keys marker left behind after self-heal (stat err = %v)", err)
	}
}

// TestSecondLaunchRefusedWhileFirstIsLiveWithLeafRecords verifies the
// live-owner guard still holds with the leaf-record marker: a second
// launch on a target a live session owns is refused, and neither the
// target nor the first session's backup and records are touched.
func TestSecondLaunchRefusedWhileFirstIsLiveWithLeafRecords(t *testing.T) {
	worktree, target := claudeTarget(t, []byte(`{"permissions":{"defaultMode":"plan"}}`))
	if _, err := ApplyConfigContent(exec.Command("claude"), "claude", worktree, ResolvedProfile{ConfigContent: pinDefaultMode("default")}); err != nil {
		t.Fatalf("session A ApplyConfigContent() error = %v", err)
	}
	keysA, err := os.ReadFile(backupKeysPath(target))
	if err != nil {
		t.Fatalf("session A wrote no .keys marker: %v", err)
	}
	if recs, ok := decodeLeafRecs(keysA); !ok || len(recs) != 1 || recs[0].Hash == "" {
		t.Fatalf(".keys = %s, want one leaf record with a hash", keysA)
	}

	fakeOwnerPid := os.Getpid() + 999983
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == fakeOwnerPid }
	t.Cleanup(func() { pidAlive = origPidAlive })
	contentA, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyConfigContent(exec.Command("claude"), "claude", worktree, ResolvedProfile{ConfigContent: pinDefaultMode("acceptEdits")}); err == nil {
		t.Fatal("session B ApplyConfigContent() error = nil, want an error (target owned by a live session)")
	}
	if got, _ := os.ReadFile(target); string(got) != string(contentA) {
		t.Errorf("target = %s, want session A's untouched %s", got, contentA)
	}
	if got, _ := os.ReadFile(backupKeysPath(target)); string(got) != string(keysA) {
		t.Errorf(".keys = %s, want session A's untouched %s", got, keysA)
	}
	if _, err := os.Stat(backupPresentPath(target)); err != nil {
		t.Errorf("session A's backup was removed by session B's refused attempt: %v", err)
	}
}

// TestKeysMarkerHoldsNoWrittenValue verifies the .keys marker records a
// hash of each value written, never the value: config_content may carry a
// secret in env, and the marker must not copy it into a second file.
func TestKeysMarkerHoldsNoWrittenValue(t *testing.T) {
	worktree, target := claudeTarget(t, nil)
	content := map[string]any{"env": map[string]any{"TOKEN": "s3cret"}}
	cleanup, err := ApplyConfigContent(exec.Command("claude"), "claude", worktree, ResolvedProfile{ConfigContent: content})
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()
	raw, err := os.ReadFile(backupKeysPath(target))
	if err != nil {
		t.Fatalf("no .keys marker written: %v", err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Errorf(".keys = %s, holds the written value", raw)
	}
}

// defaultsSession runs one claude session with rp against a fresh target:
// original is the pre-existing file (nil: none), and session, when non-nil,
// edits the document on disk the way the launched agent would mid-session.
// It returns the target as the session saw it and after cleanup (nil when
// cleanup deleted it).
func defaultsSession(t *testing.T, original []byte, rp ResolvedProfile, session func(doc map[string]any)) (during, after []byte) {
	t.Helper()
	worktree, target := claudeTarget(t, original)
	cleanup, err := ApplyConfigContent(exec.Command("claude"), "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	during, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("target unreadable during the session: %v", err)
	}
	if session != nil {
		var doc map[string]any
		if err := json.Unmarshal(during, &doc); err != nil {
			t.Fatalf("written file is not valid JSON: %v (%s)", err, during)
		}
		session(doc)
		edited, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, edited, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	after, err = os.ReadFile(target)
	if os.IsNotExist(err) {
		return during, nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return during, after
}

// TestConfigDefaultsWritesAnAbsentLeafAndRestoresIt verifies a
// config_defaults leaf the user's settings.local.json does not have is
// written for the session and removed at cleanup, while a grant the session
// saved beside it survives. Without it the claude local profile could not
// keep auto mode off for a worktree that never chose a mode (#402).
func TestConfigDefaultsWritesAnAbsentLeafAndRestoresIt(t *testing.T) {
	rp := ResolvedProfile{ConfigDefaults: pinDefaultMode("default")}
	for name, tc := range map[string]struct{ original, want []byte }{
		"existing file": {[]byte(`{"model":"x"}`), []byte(`{"model":"x","permissions":{"allow":["Bash(ls)"]}}`)},
		"new file":      {nil, []byte(`{"permissions":{"allow":["Bash(ls)"]}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			during, after := defaultsSession(t, tc.original, rp, addAllow)
			var doc map[string]any
			if err := json.Unmarshal(during, &doc); err != nil {
				t.Fatal(err)
			}
			if mode, _ := getPath(doc, []string{"permissions", "defaultMode"}); mode != "default" {
				t.Errorf("during the session = %s, want permissions.defaultMode = \"default\"", during)
			}
			if after == nil {
				t.Fatal("target deleted at cleanup, losing the session's permissions.allow")
			}
			assertSameJSON(t, "restored content", "defaultMode gone, the session's allow kept", after, tc.want)
		})
	}
}

// TestConfigDefaultsLeavesAnExistingValueAlone is the repro of #402: the
// claude local profile pinned permissions.defaultMode over the user's own
// value. As a default it now leaves any value the file already has alone:
// the file is not rewritten during the session, and cleanup changes nothing.
func TestConfigDefaultsLeavesAnExistingValueAlone(t *testing.T) {
	rp := ResolvedProfile{ConfigDefaults: pinDefaultMode("default")}
	for _, mode := range []string{"plan", "default", "auto"} {
		t.Run(mode, func(t *testing.T) {
			original := []byte(`{"permissions":{"defaultMode":"` + mode + `"}}`)
			during, after := defaultsSession(t, original, rp, nil)
			if string(during) != string(original) {
				t.Errorf("during the session = %s, want the untouched %s", during, original)
			}
			assertSameJSON(t, "restored content", "the user's own defaultMode", after, original)
		})
	}
}

// TestConfigDefaultsLeavesANonObjectPermissionsAlone verifies a default
// whose path crosses a non-object counts as present: a "permissions" the
// user set to a string is never replaced by the default's object.
func TestConfigDefaultsLeavesANonObjectPermissionsAlone(t *testing.T) {
	original := []byte(`{"permissions":"x"}`)
	during, after := defaultsSession(t, original, ResolvedProfile{ConfigDefaults: pinDefaultMode("default")}, nil)
	if string(during) != string(original) {
		t.Errorf("during the session = %s, want the untouched %s", during, original)
	}
	assertSameJSON(t, "restored content", "the original non-object permissions", after, original)
}

// TestConfigContentStillOverridesWhileConfigDefaultsDoNot verifies the two
// tables of one profile keep their own rules: config_content overwrites an
// existing value for the session, config_defaults does not, and cleanup puts
// the original back either way.
func TestConfigContentStillOverridesWhileConfigDefaultsDoNot(t *testing.T) {
	original := []byte(`{"env":{"CLAUDE_CODE_ATTRIBUTION_HEADER":"1"},"permissions":{"defaultMode":"plan"}}`)
	rp := ResolvedProfile{
		ConfigContent:  map[string]any{"env": map[string]any{"CLAUDE_CODE_ATTRIBUTION_HEADER": "0"}},
		ConfigDefaults: pinDefaultMode("default"),
	}
	during, after := defaultsSession(t, original, rp, nil)
	assertSameJSON(t, "during the session", "config_content's header, the user's defaultMode",
		during, []byte(`{"env":{"CLAUDE_CODE_ATTRIBUTION_HEADER":"0"},"permissions":{"defaultMode":"plan"}}`))
	assertSameJSON(t, "restored content", "the original", after, original)
}

// TestApplyConfigContentRejectsConfigDefaultsForCodexAndOpenCode verifies
// config_defaults is refused, with nothing written, for the agents it is not
// implemented for: Validate already rejects it, and applying it as
// config_content instead would override the user's values, the bug it
// exists to avoid.
func TestApplyConfigContentRejectsConfigDefaultsForCodexAndOpenCode(t *testing.T) {
	rp := ResolvedProfile{
		ConfigContent:  map[string]any{"model_reasoning_effort": "high"},
		ConfigDefaults: map[string]any{"approval_policy": "never"},
	}
	t.Run("codex", func(t *testing.T) {
		dir := t.TempDir()
		home := filepath.Join(dir, "home")
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
		cmd := exec.Command("codex")
		_, err := ApplyConfigContent(cmd, "codex", "/tmp/some-worktree", rp)
		if err == nil || err.Error() != `profile: agent "codex" does not take config_defaults` {
			t.Fatalf("ApplyConfigContent() error = %v, want the config_defaults refusal", err)
		}
		if _, statErr := os.Stat(filepath.Join(home, ".codex", "agent-wt-profile.config.toml")); !os.IsNotExist(statErr) {
			t.Errorf("codex profile file written despite the refusal (stat err = %v)", statErr)
		}
		if len(cmd.Args) != 1 {
			t.Errorf("cmd.Args = %v, want no --profile appended", cmd.Args)
		}
	})
	t.Run("opencode", func(t *testing.T) {
		cmd := exec.Command("opencode")
		cmd.Env = []string{openCodeConfigEnvPrefix + `{}`}
		_, err := ApplyConfigContent(cmd, "opencode", "/tmp/some-worktree", rp)
		if err == nil || err.Error() != `profile: agent "opencode" does not take config_defaults` {
			t.Fatalf("ApplyConfigContent() error = %v, want the config_defaults refusal", err)
		}
		if cmd.Env[0] != openCodeConfigEnvPrefix+`{}` {
			t.Errorf("cmd.Env = %v, want OPENCODE_CONFIG_CONTENT untouched", cmd.Env)
		}
	})
}

// TestConfigDefaultsRefusesATargetHoldingNull verifies a config_defaults
// launch goes through the same JSON-null refusal as config_content, before
// any default is applied: the launch is refused with the not-a-JSON-object
// error, the file still holds null and no backup is left behind.
func TestConfigDefaultsRefusesATargetHoldingNull(t *testing.T) {
	worktree, target := claudeTarget(t, []byte(`null`))
	rp := ResolvedProfile{ConfigDefaults: pinDefaultMode("default")}
	_, err := ApplyConfigContent(exec.Command("claude"), "claude", worktree, rp)
	if err == nil || !strings.Contains(err.Error(), "is not a JSON object, refusing to merge config_content") {
		t.Fatalf("ApplyConfigContent() error = %v, want the not-a-JSON-object refusal", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("target = %s, want it left untouched", got)
	}
	if _, statErr := os.Stat(backupPresentPath(target)); !os.IsNotExist(statErr) {
		t.Errorf("backupPresentPath exists after the refusal (stat err = %v)", statErr)
	}
}

// TestApplyConfigContentRefusesATargetHoldingNull pins the one JSON payload
// a merge must refuse beside invalid JSON: a whole-file null decodes with
// no error into a nil map, which mergeInto used to panic on ("assignment to
// entry in nil map") — crashing wt at launch instead of degrading to an
// unprofiled launch the way every other refusal does. Like any buildData
// failure, the refusal must also leave the target untouched and no orphaned
// backup behind for a later self-heal to restore over.
func TestApplyConfigContentRefusesATargetHoldingNull(t *testing.T) {
	worktree, target := claudeTarget(t, []byte(`null`))

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err == nil {
		t.Fatal("ApplyConfigContent() error = nil, want an error (existing content is JSON null)")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "null" {
		t.Errorf("target = %s, want it left untouched", got)
	}
	if _, statErr := os.Stat(backupPresentPath(target)); !os.IsNotExist(statErr) {
		t.Errorf("backupPresentPath exists after a buildData failure, want no orphaned backup (stat err = %v)", statErr)
	}
	if _, statErr := os.Stat(backupOwnerPath(target)); !os.IsNotExist(statErr) {
		t.Errorf("backupOwnerPath exists after a buildData failure, want no orphaned owner marker (stat err = %v)", statErr)
	}
}

// TestCleanupRestoresALeafPastFloat64sExactRange verifies cleanup reverts a
// pinned integer past float64's exact range (2^53 and up) that the session
// never touched: the marker's hash comes from the profile's int64 and the
// restored file's from the float64 parsing that same JSON number gives, and
// hashing the write-side form alone used to make those two disagree — the
// leaf then read as changed by the session and the profile's own value
// stayed after cleanup instead of the original coming back.
func TestCleanupRestoresALeafPastFloat64sExactRange(t *testing.T) {
	content := map[string]any{"reserved": int64(9007199254740993)} // 2^53 + 1
	got := leafSession(t, []byte(`{"reserved":1}`), content, nil)
	assertSameJSON(t, "restored content", "the original reserved of 1 back", got, []byte(`{"reserved":1}`))
}
