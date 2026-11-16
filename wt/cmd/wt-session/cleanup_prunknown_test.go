package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// mergedBranchRepo is a repository with branch "topic" merged into main and
// no worktree holding it. githubOrigin names a GitHub origin (insteadOf the
// bare), so the own repository is known; otherwise origin is a bare path.
func mergedBranchRepo(t *testing.T, githubOrigin bool) string {
	t.Helper()
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	cgit(t, dir, "worktree", "remove", p)
	if githubOrigin {
		bare := filepath.Join(filepath.Dir(dir), "origin.git")
		cgit(t, dir, "config", "url."+bare+".insteadOf", "https://github.com/o/r.git")
		cgit(t, dir, "remote", "set-url", "origin", "https://github.com/o/r.git")
	}
	return dir
}

// hourLater classifies an hour from now, so a branch made seconds ago is
// old enough to be proven (--min-age has a 1m floor).
func hourLater(t *testing.T) {
	t.Helper()
	old := cleanupClassify
	cleanupClassify = func(s cleanup.Snapshot, o cleanup.Options) cleanup.Report {
		o.Now = o.Now.Add(time.Hour)
		return old(s, o)
	}
	t.Cleanup(func() { cleanupClassify = old })
}

func ghDown(t *testing.T) {
	t.Helper()
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh: offline") }))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh: offline") }))
}

func topicRow(t *testing.T, stdout string) (status string, reasons string) {
	t.Helper()
	var env struct {
		Rows []struct {
			Kind    string          `json:"kind"`
			Name    string          `json:"name"`
			Status  string          `json:"status"`
			Reasons json.RawMessage `json:"reasons"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	for _, r := range env.Rows {
		if r.Kind == "branch" && r.Name == "topic" {
			return r.Status, string(r.Reasons)
		}
	}
	t.Fatalf("no topic branch row:\n%s", stdout)
	return
}

func TestCleanupCmdGHDownOnGitHubOriginFloorsMergedBranch(t *testing.T) {
	a, _ := removeApp(t)
	dir := mergedBranchRepo(t, true)
	ghDown(t)
	hourLater(t)
	stdout, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	status, reasons := topicRow(t, stdout)
	if status != "less_sure" || !strings.Contains(reasons, "pr_unknown") {
		t.Fatalf("status %q reasons %s", status, reasons)
	}
}

func TestRemoveCmdGHDownNeedsAcceptLoss(t *testing.T) {
	a, _ := removeApp(t)
	dir := mergedBranchRepo(t, true)
	ghDown(t)
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "topic")
	if exitOf(err) != 2 || !hasBranch(t, dir, "topic") || !strings.Contains(stdout+stderr+errText(err), "--accept-loss") {
		t.Fatalf("exit %d branch kept %v\n%s%s%v", exitOf(err), hasBranch(t, dir, "topic"), stdout, stderr, err)
	}
	stdout, stderr, err = runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "-D", "topic")
	if exitOf(err) != 0 || hasBranch(t, dir, "topic") {
		t.Fatalf("exit %d branch kept %v\n%s%s%v", exitOf(err), hasBranch(t, dir, "topic"), stdout, stderr, err)
	}
}

func TestNonGitHubOriginStaysProvenAndRemovable(t *testing.T) {
	a, _ := removeApp(t)
	dir := mergedBranchRepo(t, false)
	ghDown(t)
	hourLater(t)
	stdout, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	if status, reasons := topicRow(t, stdout); status != "proven" {
		t.Fatalf("status %q reasons %s", status, reasons)
	}
	if stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "topic"); exitOf(err) != 0 || hasBranch(t, dir, "topic") {
		t.Fatalf("exit %d\n%s%s%v", exitOf(err), stdout, stderr, err)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
