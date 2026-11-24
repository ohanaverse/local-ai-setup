package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// mergedPRList is a gh hook answering one MERGED PR #n whose head is head.
func mergedPRList(n int, head string) func(string, string) ([]byte, error) {
	return func(string, string) ([]byte, error) {
		return []byte(fmt.Sprintf(`[{"number":%d,"state":"MERGED","headRefOid":%q,"baseRefName":"main","mergeCommit":null,`+
			`"headRepository":{"name":"r","nameWithOwner":"o/r"},"headRepositoryOwner":{"login":"o"},"isCrossRepository":false}]`, n, head)), nil
	}
}

// prLaunchRepo is a github-origin repository whose linked worktree "topic"
// has a commit; it returns the root and topic's tip.
func prLaunchRepo(t *testing.T) (root, linked, tip string) {
	t.Helper()
	root = sharedRootRepo(t)
	cgit(t, root, "remote", "add", "origin", "https://github.com/o/r.git")
	setOriginDefault(t, root)
	linked = addLinkedWorktree(t, root, "topic")
	cleanupCommit(t, linked, "f.txt", "work")
	return root, linked, strings.TrimSpace(cgit(t, linked, "rev-parse", "HEAD"))
}

// TestFlagLaunchWarnsOfAMergedPR checks that --cwd, -W and --root launches on a
// branch whose PR gh reports merged print "PR #N is merged" once, in the one
// confirm line, and still launch with no terminal (#585). Why: before this a
// launch by flag never looked the PR up, so only the picker warned.
func TestFlagLaunchWarnsOfAMergedPR(t *testing.T) {
	for name, where := range map[string]func(root, linked string) (dir string, args []string){
		"cwd": func(_, linked string) (string, []string) { return linked, []string{"--cwd"} },
		"-W":  func(root, _ string) (string, []string) { return root, []string{"-W", "topic"} },
	} {
		t.Run(name, func(t *testing.T) {
			root, linked, tip := prLaunchRepo(t)
			stubStdinTTY(t, false)
			notes := captureNotes(t)
			h := testHooks()
			h.prList = mergedPRList(42, tip)
			dir, args := where(root, linked)
			res := runPinnedLaunchWith(t, h, dir, args)
			if res.err != nil || res.agentDir != linked {
				t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
			}
			if n := strings.Count(notes.String(), "PR #42 is merged"); n != 1 {
				t.Errorf("output %q names the merged PR %d times, want once", notes.String(), n)
			}
		})
	}
}

// TestFlagLaunchIgnoresAFailingGh checks that a gh hook that fails adds no
// warning and the launch still goes ahead. Why: gh is optional; a lookup must
// never block or error a launch.
func TestFlagLaunchIgnoresAFailingGh(t *testing.T) {
	_, linked, _ := prLaunchRepo(t)
	stubStdinTTY(t, false)
	notes := captureNotes(t)
	h := testHooks()
	h.prList = func(string, string) ([]byte, error) { return nil, errors.New("gh: not installed") }
	res := runPinnedLaunchWith(t, h, linked, []string{"--cwd"})
	if res.err != nil || res.agentDir != linked {
		t.Fatalf("err=%v agentDir=%q; want a launch in %s", res.err, res.agentDir, linked)
	}
	if strings.Contains(notes.String(), "PR #") || strings.Contains(notes.String(), "gh") {
		t.Errorf("output %q carries a PR line for a failed lookup", notes.String())
	}
}
