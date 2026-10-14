package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// prTimeout bounds each `gh pr list` the picker runs, like fetchTimeout
// bounds `git fetch`: a slow or unreachable GitHub must never hold up the
// picker, and the picker never waits for it anyway (the tags arrive later).
const prTimeout = 5 * time.Second

// prLimit is how many pull requests one listing asks for. gh lists the
// newest first, so a branch whose only PR is older than the newest prLimit
// of its listing shows no tag. There are two listings (prStates): open PRs
// alone, so merged and closed ones can never crowd an old open PR out, then
// every state.
const prLimit = 200

// prStates are the `gh pr list --state` values PRs lists, in order.
var prStates = []string{"open", "all"}

// PRState is the state the picker shows for a pull request.
type PRState string

const (
	PROpen   PRState = "open"
	PRDraft  PRState = "draft"
	PRMerged PRState = "merged"
	PRClosed PRState = "closed"
)

// PR is the pull request the picker shows for one branch.
type PR struct {
	Number int
	State  PRState
	Title  string
}

// listPRs runs `gh pr list --state state` in dir and returns its JSON. It is the test seam:
// every TestMain that can reach PRs stubs it (StubPRListForTest), and the
// real one refuses inside a test binary besides, so no test ever asks the
// developer's GitHub account anything.
var listPRs = realListPRs

var errPRListUnderTest = errors.New("gh pr list is not run from a test binary")

func realListPRs(dir, state string) ([]byte, error) {
	if testing.Testing() {
		return nil, errPRListUnderTest
	}
	ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--state", state,
		"--limit", strconv.Itoa(prLimit),
		"--json", "number,state,isDraft,headRefName,isCrossRepository,title")
	if dir != "" {
		cmd.Dir = dir
	}
	// gh must never prompt, page, colour or check for its own update: the
	// picker owns the terminal (alt screen), and neither stdin (the null
	// device, as exec leaves it) nor stderr (captured by Output, then
	// dropped) is connected to it, so a prompt could only hang until the
	// timeout.
	cmd.Env = append(os.Environ(),
		"GH_PROMPT_DISABLED=1",
		"GH_PAGER=cat",
		"PAGER=cat",
		"GH_NO_UPDATE_NOTIFIER=1",
		"GH_SPINNER_DISABLED=1",
		"NO_COLOR=1",
	)
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// StubPRListForTest replaces the `gh pr list` call with fn and returns the
// function that puts the previous one back
// (`t.Cleanup(worktree.StubPRListForTest(fn))`). Production code never calls
// it; it is exported because internal/tui's tests reach PRs through the
// picker.
func StubPRListForTest(fn func(dir, state string) ([]byte, error)) (restore func()) {
	old := listPRs
	listPRs = fn
	return func() { listPRs = old }
}

// PRs returns the pull request to show for each branch of the repository at
// dir, keyed by head branch name (no remote prefix). It lists open PRs, then
// PRs of every state (prStates), and parses the two listings as one, so a
// PR in both counts once. A listing that fails — gh not installed, not a
// GitHub remote, not logged in, the timeout, output that is not the
// expected JSON — adds nothing and nothing is printed: with both failed the
// map is empty and the picker shows no tags.
func PRs(dir string) map[string]PR {
	var all []ghPR
	for _, state := range prStates {
		out, err := listPRs(dir, state)
		if err != nil {
			continue
		}
		var raw []ghPR
		if err := json.Unmarshal(out, &raw); err != nil {
			continue
		}
		all = append(all, raw...)
	}
	return pickPRs(all)
}

// ghPR is one element of `gh pr list --json ...`.
type ghPR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	HeadRefName       string `json:"headRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Title             string `json:"title"`
}

// parsePRs maps one listing's JSON to one PR per head branch (pickPRs).
func parsePRs(data []byte) (map[string]PR, error) {
	var raw []ghPR
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return pickPRs(raw), nil
}

// pickPRs keeps one PR per head branch. A branch with several PRs keeps the
// one with the highest number — the one opened last, which is the branch's
// current PR whatever later activity (a comment, a label) did to an older
// one's updatedAt. A PR from a fork (isCrossRepository) is skipped: its head
// branch names a branch in someone else's repository, not one of this
// repository's. An element with no head branch or no number is skipped too.
func pickPRs(raw []ghPR) map[string]PR {
	prs := make(map[string]PR, len(raw))
	for _, p := range raw {
		if p.IsCrossRepository || p.HeadRefName == "" || p.Number <= 0 {
			continue
		}
		if cur, ok := prs[p.HeadRefName]; ok && cur.Number > p.Number {
			continue
		}
		prs[p.HeadRefName] = PR{Number: p.Number, State: prState(p.State, p.IsDraft), Title: p.Title}
	}
	return prs
}

// prState names gh's state the way the picker shows it: an open draft is
// "draft"; anything gh adds later reads as its own lower-cased name.
func prState(state string, draft bool) PRState {
	switch strings.ToUpper(state) {
	case "OPEN":
		if draft {
			return PRDraft
		}
		return PROpen
	case "MERGED":
		return PRMerged
	case "CLOSED":
		return PRClosed
	}
	return PRState(strings.ToLower(state))
}

// PRBranch is the head branch name a picker entry's PR is looked up under:
// the branch itself for a worktree or local branch, and the branch without
// its remote (origin/foo -> foo) for a remote-tracking branch. A detached
// worktree has none.
func PRBranch(kind GroupKind, e Entry) string {
	if e.Branch == "" || e.Branch == "(detached)" {
		return ""
	}
	if kind == GroupRemoteBranches {
		if i := strings.IndexByte(e.Branch, '/'); i >= 0 {
			return e.Branch[i+1:]
		}
	}
	return e.Branch
}
