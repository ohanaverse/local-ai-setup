package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
)

// The detached-root tests of `wt restore-root` (#584): a root whose commit a
// branch holds moves with the new worktree left detached (E); one whose
// commits no branch holds needs --branch NAME (C).

// detachedHeld puts the root of a fresh repo on a detached HEAD at the tip of
// topic, which topic holds, with a staged file and an untracked file when
// dirty.
func detachedHeld(t *testing.T, dirty bool) string {
	t.Helper()
	dir := cleanupRepo(t)
	excludeWorktrees(t, dir)
	cgit(t, dir, "switch", "-q", "-c", "topic")
	cleanupCommit(t, dir, "one.txt", "one")
	cgit(t, dir, "switch", "-q", "--detach")
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// detachedOrphan puts the root on a detached HEAD one commit past main, which
// no branch holds.
func detachedOrphan(t *testing.T, dirty bool) string {
	t.Helper()
	dir := cleanupRepo(t)
	excludeWorktrees(t, dir)
	cgit(t, dir, "switch", "-q", "--detach")
	cleanupCommit(t, dir, "orphan.txt", "only here")
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRestoreRootBranchFlagUsage: an empty, whitespace-only or padded --branch
// is a usage error (exit 1, a JSON error document, nothing changed), and so is
// --branch with no value.
func TestRestoreRootBranchFlagUsage(t *testing.T) {
	a := restoreRootApp(t)
	dir := detachedOrphan(t, false)
	world := repoWorld(t, dir)
	for name, args := range map[string][]string{
		"empty":      {"--branch", "", "--json", "--no-fetch"},
		"whitespace": {"--branch", "  ", "--json", "--no-fetch"},
		"padded":     {"--branch", " x", "--json", "--no-fetch"},
		"no value":   {"--json", "--no-fetch", "--branch"},
	} {
		stdout, _, err := runRestoreRoot(t, a, dir, "", args...)
		if exitOf(err) != 1 {
			t.Errorf("%s: exit %d (%v)", name, exitOf(err), err)
			continue
		}
		if env := decodeRestoreRoot(t, stdout); env.Error == nil || env.Outcome != "" {
			t.Errorf("%s: envelope = %+v", name, env)
		}
	}
	if repoWorld(t, dir) != world {
		t.Error("a usage error changed the repository")
	}
}

// TestRestoreRootBranchFlagRefusals: the state-dependent --branch refusals are
// exit 2 with their code, creating nothing.
func TestRestoreRootBranchFlagRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) string
		flag  string
		code  string
	}{
		{"on a branch", func(t *testing.T) string { d := cleanupRepo(t); rootOnTopic(t, d); return d }, "x", rootmove.CodeBranchUnneeded},
		{"invalid", func(t *testing.T) string { return detachedOrphan(t, false) }, "bad..name", rootmove.CodeBranchInvalid},
		{"exists", func(t *testing.T) string { return detachedOrphan(t, false) }, "main", rootmove.CodeBranchExists},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := restoreRootApp(t)
			dir := c.setup(t)
			world := repoWorld(t, dir)
			stdout, _, err := runRestoreRoot(t, a, dir, "", "--branch", c.flag, "--yes", "--json", "--no-fetch")
			if exitOf(err) != 2 {
				t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
			}
			if env := decodeRestoreRoot(t, stdout); env.Outcome != "refused" || env.Code != c.code || env.Created {
				t.Errorf("envelope = %+v", env)
			}
			if repoWorld(t, dir) != world {
				t.Error("a refusal changed the repository")
			}
		})
	}
}

// TestRestoreRootDetachedUnreachableRefusalNamesFlag: without --branch such a
// root is refused and the message names the flag, in text and JSON.
func TestRestoreRootDetachedUnreachableRefusalNamesFlag(t *testing.T) {
	a := restoreRootApp(t)
	dir := detachedOrphan(t, true)
	world := repoWorld(t, dir)
	text, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--no-fetch")
	if exitOf(err) != 2 || !strings.Contains(text, "refused (detached_root)") || !strings.Contains(text, "--branch NAME") {
		t.Fatalf("exit %d, text %q", exitOf(err), text)
	}
	if repoWorld(t, dir) != world {
		t.Error("a refusal changed the repository")
	}
}

// TestRestoreRootDetachedHeldDirtyJSON (E): the new worktree is detached, no
// branch exists in the envelope or the repository, the record is there.
func TestRestoreRootDetachedHeldDirtyJSON(t *testing.T) {
	a := restoreRootApp(t)
	dir := detachedHeld(t, true)
	head := cgit(t, dir, "rev-parse", "HEAD")
	branches := cgit(t, dir, "for-each-ref", "refs/heads")

	stdout, stderr, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")

	if exitOf(err) != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Outcome != "moved" || !env.Created || !env.Detached || env.Head != head || env.Branch != "" || env.BranchCreated ||
		env.Record == nil || !strings.HasSuffix(env.DestPath, "detached-"+head[:8]) {
		t.Fatalf("envelope = %+v", env)
	}
	if rootBranch(t, dir) != "main" || cgit(t, dir, "status", "--porcelain") != "" {
		t.Errorf("root on %s, status %q", rootBranch(t, dir), cgit(t, dir, "status", "--porcelain"))
	}
	if cgit(t, dir, "rev-parse", "HEAD") != cgit(t, dir, "rev-parse", "main") {
		t.Error("the root is not at main")
	}
	if got := cgit(t, env.DestPath, "rev-parse", "HEAD"); got != head {
		t.Errorf("dest at %s, want %s", got, head)
	}
	if got := cgit(t, dir, "for-each-ref", "refs/heads"); got != branches {
		t.Errorf("branches changed:\n%s\n--\n%s", branches, got)
	}
	if b, _ := os.ReadFile(filepath.Join(env.DestPath, "new.txt")); string(b) != "n" {
		t.Errorf("untracked file not carried: %q", b)
	}
}

// TestRestoreRootDetachedHeldCleanOnlySwitches (E, clean): moved, created
// false, no dest_path or record, no worktree directory, root on main; the
// text says no worktree was made.
func TestRestoreRootDetachedHeldCleanOnlySwitches(t *testing.T) {
	a := restoreRootApp(t)
	dir := detachedHeld(t, false)
	stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")
	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
	}
	env := decodeRestoreRoot(t, stdout)
	if env.Outcome != "moved" || env.Created || env.DestPath != "" || env.Record != nil || !env.Detached || env.Branch != "" {
		t.Fatalf("envelope = %+v", env)
	}
	if rootBranch(t, dir) != "main" {
		t.Errorf("root on %s", rootBranch(t, dir))
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, ".worktrees")); len(ents) != 0 {
		t.Errorf(".worktrees holds %d entries", len(ents))
	}

	dir2 := detachedHeld(t, false)
	text, _, err := runRestoreRoot(t, a, dir2, "", "--yes", "--no-fetch")
	if err != nil || !strings.Contains(text, "no worktree was made") || strings.Contains(text, "left behind") || strings.Contains(text, "topic,") {
		t.Errorf("text %q, err %v", text, err)
	}
}

// TestRestoreRootDetachedBranchCreatesName (C): --branch spike creates the
// branch at the root's commit in the new worktree, which ends on it; the root
// is on main; the envelope says so.
func TestRestoreRootDetachedBranchCreatesName(t *testing.T) {
	for _, dirty := range []bool{true, false} {
		a := restoreRootApp(t)
		dir := detachedOrphan(t, dirty)
		head := cgit(t, dir, "rev-parse", "HEAD")
		stdout, _, err := runRestoreRoot(t, a, dir, "", "--branch", "spike/one", "--yes", "--json", "--no-fetch")
		if exitOf(err) != 0 {
			t.Fatalf("dirty=%v exit %d (%v)\n%s", dirty, exitOf(err), err, stdout)
		}
		env := decodeRestoreRoot(t, stdout)
		if env.Outcome != "moved" || !env.Created || !env.Detached || env.Branch != "spike/one" || !env.BranchCreated || env.Head != head || (env.Record != nil) != dirty {
			t.Fatalf("dirty=%v envelope = %+v", dirty, env)
		}
		if rootBranch(t, dir) != "main" || rootBranch(t, env.DestPath) != "spike/one" || cgit(t, dir, "rev-parse", "spike/one") != head {
			t.Errorf("root on %s, dest on %s, spike/one %s", rootBranch(t, dir), rootBranch(t, env.DestPath), cgit(t, dir, "rev-parse", "spike/one"))
		}
		if env.Carried == nil || env.Carried.Commits != 1 {
			t.Errorf("carried = %+v, want 1 commit", env.Carried)
		}
	}
}

// TestRestoreRootDetachedDryRunAndPrompt: --dry-run changes nothing and says
// which shape; the prompt names the branch or says none is created.
func TestRestoreRootDetachedDryRunAndPrompt(t *testing.T) {
	a := restoreRootApp(t)
	cases := []struct {
		name  string
		setup func(t *testing.T) string
		args  []string
		dry   []string // in the dry-run text
		ask   []string // in the prompt
	}{
		{"held, dirty", func(t *testing.T) string { return detachedHeld(t, true) }, nil,
			[]string{"would move the root", "detached at", "no branch is created", "would carry"},
			[]string{"will move the root", "held by", "so no branch is made", "no branch is created"}},
		{"held, clean", func(t *testing.T) string { return detachedHeld(t, false) }, nil,
			[]string{"no worktree would be made"},
			[]string{"nothing is uncommitted", "no worktree is made", "held by topic"}},
		{"unreachable with --branch", func(t *testing.T) string { return detachedOrphan(t, true) }, []string{"--branch", "spike"},
			[]string{"on the new branch spike", "would carry 1 commit"},
			[]string{"new branch    spike at", "holding 1 commit no branch holds yet"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := c.setup(t)
			world, before := repoWorld(t, dir), rootState(t, dir)
			text, _, err := runRestoreRoot(t, a, dir, "", append([]string{"--dry-run"}, c.args...)...)
			if err != nil {
				t.Fatalf("dry run: %v\n%s", err, text)
			}
			for _, w := range c.dry {
				if !strings.Contains(text, w) {
					t.Errorf("dry-run text lacks %q:\n%s", w, text)
				}
			}
			j, _, err := runRestoreRoot(t, a, dir, "", append([]string{"--dry-run", "--json"}, c.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if env := decodeRestoreRoot(t, j); env.Outcome != "would_move" || !env.Detached || env.Created || env.BranchCreated {
				t.Errorf("dry-run envelope = %+v", env)
			}
			if repoWorld(t, dir) != world || rootState(t, dir) != before {
				t.Error("a dry run changed the repository")
			}

			restoreRootTTY = func(io.Reader, io.Writer) bool { return true }
			_, errOut, err := runRestoreRoot(t, a, dir, "n\n", append([]string{"--no-fetch"}, c.args...)...)
			if exitOf(err) != 3 {
				t.Fatalf("declined: exit %d (%v)", exitOf(err), err)
			}
			for _, w := range c.ask {
				if !strings.Contains(errOut, w) {
					t.Errorf("prompt lacks %q:\n%s", w, errOut)
				}
			}
			if repoWorld(t, dir) != world || rootState(t, dir) != before {
				t.Error("a declined run changed the repository")
			}
		})
	}
}

// TestRestoreRootDetachedHeldRootNeedsAcceptLoss: the held-root rule holds for
// a detached root too: --yes alone is exit 3 and changes nothing.
func TestRestoreRootDetachedHeldRootNeedsAcceptLoss(t *testing.T) {
	a := restoreRootApp(t)
	dir := detachedHeld(t, true)
	holderClaim(t, dir, "", "codex")
	world, before := repoWorld(t, dir), rootState(t, dir)
	stdout, _, err := runRestoreRoot(t, a, dir, "", "--yes", "--json", "--no-fetch")
	if exitOf(err) != 3 || !strings.Contains(err.Error(), "--accept-loss") {
		t.Fatalf("err = %v (exit %d)\n%s", err, exitOf(err), stdout)
	}
	if repoWorld(t, dir) != world || rootState(t, dir) != before {
		t.Error("a --yes run without --accept-loss changed the repository")
	}
}
