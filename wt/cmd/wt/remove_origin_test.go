package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// originRepo is a repository whose origin (a local bare repo that git reaches
// through url.insteadOf, so wt reads a GitHub-shaped URL and knows the own
// repo) has a merged branch "topic" at main's tip with a merged PR at that
// tip. It returns the clone and the bare origin's path.
func originRepo(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = cleanupRepo(t)
	bare = filepath.Join(filepath.Dir(dir), "origin.git")
	cgit(t, dir, "branch", "topic")
	cgit(t, dir, "push", "-u", "origin", "topic")
	cgit(t, dir, "config", "url."+bare+".insteadOf", "https://github.com/o/r.git")
	cgit(t, dir, "remote", "set-url", "origin", "https://github.com/o/r.git")
	tip := cgit(t, dir, "rev-parse", "main")
	merged := fmt.Sprintf(`[{"number":8,"state":"MERGED","headRefOid":%q,"baseRefName":"main","mergeCommit":{"oid":%q},"isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`, tip, tip)
	t.Cleanup(cleanup.StubPRListForTest(func(_, b string) ([]byte, error) {
		if b == "topic" {
			return []byte(merged), nil
		}
		return []byte("[]"), nil
	}))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	return dir, bare
}

// originTip is the bare origin's tip of branch, or "".
func originTip(t *testing.T, bare, branch string) string {
	t.Helper()
	b, err := exec.Command("git", "-C", bare, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// TestRemoveCmdOriginBranch drives `wt remove origin/topic` through its flags.
// What: a dry run changes nothing and reports the delete; --remote --yes
// deletes and prints the restore tip; without --remote the item is refused
// (exit 2) however many other approvals are given; --no-fetch refuses it.
// Why: deleting an origin branch is public, so only its own flag grants it,
// and only a list built after this run's fetch may offer it.
func TestRemoveCmdOriginBranch(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		a, _ := removeApp(t)
		dir, bare := originRepo(t)
		tip := originTip(t, bare, "topic")
		stdout, _, err := runRemove(t, a, dir, "", "origin/topic", "--remote", "--dry-run", "--json")
		env := decodeOne(t, stdout)
		if exitOf(err) != 0 || !env.DryRun || len(env.Items) != 1 || env.Items[0].Kind != "remote" || env.Items[0].Branch != "deleted" || env.Items[0].BranchTip != "" {
			t.Fatalf("exit %d, %+v", exitOf(err), env)
		}
		if originTip(t, bare, "topic") != tip {
			t.Fatal("a dry run changed origin")
		}
	})
	t.Run("--remote --yes deletes", func(t *testing.T) {
		a, _ := removeApp(t)
		dir, bare := originRepo(t)
		tip := originTip(t, bare, "topic")
		stdout, _, err := runRemove(t, a, dir, "", "origin/topic", "--remote", "--yes", "--json")
		env := decodeOne(t, stdout)
		if exitOf(err) != 0 || len(env.Items) != 1 || env.Items[0].Branch != "deleted" || env.Items[0].Worktree != "not_requested" || env.Items[0].BranchTip != tip {
			t.Fatalf("exit %d, %+v", exitOf(err), env)
		}
		if originTip(t, bare, "topic") != "" {
			t.Fatal("origin still has topic")
		}
	})
	t.Run("no --remote refuses whatever else is approved", func(t *testing.T) {
		a, _ := removeApp(t)
		dir, bare := originRepo(t)
		tip := originTip(t, bare, "topic")
		stdout, _, err := runRemove(t, a, dir, "", "origin/topic", "--yes", "--accept-loss", "--force", "-D", "--json")
		env := decodeOne(t, stdout)
		if exitOf(err) != 2 || env.Items[0].Worktree != "refused" || !strings.Contains(env.Items[0].Reason, "--remote") {
			t.Fatalf("exit %d, %+v", exitOf(err), env.Items)
		}
		if originTip(t, bare, "topic") != tip {
			t.Fatal("origin changed")
		}
	})
	t.Run("--no-fetch refuses", func(t *testing.T) {
		a, _ := removeApp(t)
		dir, bare := originRepo(t)
		tip := originTip(t, bare, "topic")
		stdout, _, err := runRemove(t, a, dir, "", "origin/topic", "--remote", "--yes", "--no-fetch", "--json")
		env := decodeOne(t, stdout)
		if exitOf(err) != 2 || env.Items[0].Branch == "deleted" {
			t.Fatalf("exit %d, %+v", exitOf(err), env.Items)
		}
		if originTip(t, bare, "topic") != tip {
			t.Fatal("origin changed")
		}
	})
	t.Run("no terminal and no --yes", func(t *testing.T) {
		a, _ := removeApp(t)
		dir, bare := originRepo(t)
		_, _, err := runRemove(t, a, dir, "", "origin/topic", "--remote")
		if exitOf(err) != 3 || originTip(t, bare, "topic") == "" {
			t.Fatalf("exit %d", exitOf(err))
		}
	})
}

// TestRemoveCmdOriginArgumentMatching resolves `origin/topic` when a local
// branch has that very name. What: it is ambiguous (exit 1) and nothing is
// touched; a plain `topic` still means the local branch only. Why: the
// argument decides what is deleted on a server; guessing is not allowed.
func TestRemoveCmdOriginArgumentMatching(t *testing.T) {
	a, _ := removeApp(t)
	dir, bare := originRepo(t)
	cgit(t, dir, "branch", "origin/topic")
	stdout, _, err := runRemove(t, a, dir, "", "origin/topic", "--remote", "--yes", "--json")
	env := decodeOne(t, stdout)
	if exitOf(err) != 1 || env.Error == nil || !strings.Contains(env.Error.Message, "more than one") || originTip(t, bare, "topic") == "" {
		t.Fatalf("exit %d, %+v", exitOf(err), env)
	}
}

// TestRemoveCmdOriginWithAWorktree removes a merged worktree and its origin
// branch in one call. What: both go, each by its own approvals. Why: a mixed
// batch is the normal use, and --remote must not leak to or from the others.
func TestRemoveCmdOriginWithAWorktree(t *testing.T) {
	a, _ := removeApp(t)
	dir, bare := originRepo(t)
	p := mergedWorktree(t, dir, "other")
	stdout, _, err := runRemove(t, a, dir, "", p, "origin/topic", "--remote", "--yes", "--json")
	env := decodeOne(t, stdout)
	if exitOf(err) != 0 || len(env.Items) != 2 || originTip(t, bare, "topic") != "" || exists(p) {
		t.Fatalf("exit %d, %+v", exitOf(err), env.Items)
	}
}

// TestRemoveCmdOriginPromptNamesTheAudience answers the y/N prompt for an
// origin deletion. What: the prompt says how many branches are on origin and
// who can see them, and shows the restore command; "n" deletes nothing; "y"
// deletes only because --remote was also given. Why: the prompt replaces
// --yes, never --remote, and is the user's last look at a public deletion.
func TestRemoveCmdOriginPromptNamesTheAudience(t *testing.T) {
	a, _ := removeApp(t)
	onTTY(t)
	dir, bare := originRepo(t)
	_, errOut, err := runRemove(t, a, dir, "n\n", "origin/topic", "--remote")
	if exitOf(err) != 3 || originTip(t, bare, "topic") == "" {
		t.Fatalf("exit %d after n", exitOf(err))
	}
	for _, want := range []string{"1 branch on origin (visible to everyone with access to the repository)", "git push origin ", ":refs/heads/topic"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("prompt lacks %q:\n%s", want, errOut)
		}
	}
	if _, _, err := runRemove(t, a, dir, "y\n", "origin/topic", "--remote"); exitOf(err) != 0 || originTip(t, bare, "topic") != "" {
		t.Fatalf("exit %d after y", exitOf(err))
	}
	// y alone, without --remote, deletes nothing.
	dir2, bare2 := originRepo(t)
	_, _, err = runRemove(t, a, dir2, "y\n", "origin/topic")
	if exitOf(err) != 2 || originTip(t, bare2, "topic") == "" {
		t.Fatalf("exit %d without --remote", exitOf(err))
	}
}
