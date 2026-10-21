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
// of its listing shows no tag. The picker runs two listings (PRListStates):
// open PRs alone, so merged and closed ones can never crowd an old open PR
// out, and every state.
const prLimit = 200

// The `gh pr list --state` values the picker lists. The open listing is
// shorter, so its tags usually arrive first.
const (
	PRListOpen = "open"
	PRListAll  = "all"
)

// PRListStates are the listings the picker runs, each its own PRs call.
var PRListStates = []string{PRListOpen, PRListAll}

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
		"--json", "number,state,isDraft,headRefName,isCrossRepository,title,headRepository")
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

// PRKey is the head a pull request was opened from: Repo, the lower-cased
// owner/repo of its head repository ("" when gh did not say, for a
// same-repository PR), and Branch, the head branch name.
type PRKey struct {
	Repo   string
	Branch string
}

// PRSet is one listing's (or the join of several) pull requests: ByHead is
// the PR to show for each head (repository, branch), and BaseRepo the
// lower-cased owner/repo gh listed, read off a same-repository PR's head
// ("" when the listing had none). BaseRepo decides no remote row's tag: it
// marks the listed repository's PRs as yours for a local row (ForBranch),
// and the picker names it in its "no remote is …" notice.
type PRSet struct {
	BaseRepo string
	ByHead   map[PRKey]PR
}

// Loaded reports whether a listing has answered (even with no PRs).
func (s PRSet) Loaded() bool { return s.ByHead != nil }

// ForRemote is the PR for a remote-tracking row: the one whose head is repo
// (the row's remote's owner/repo, compared without case) and branch. A row
// whose repository is unknown ("") or with no branch has none.
func (s PRSet) ForRemote(repo, branch string) (PR, bool) {
	if repo == "" || branch == "" {
		return PR{}, false
	}
	pr, ok := s.ByHead[PRKey{Repo: strings.ToLower(repo), Branch: branch}]
	return pr, ok
}

// ForBranch is the PR for a local branch or worktree row, which has no
// repository: branch is looked up under the head repositories known to be
// yours — the listed repository (BaseRepo, and "" for a same-repository PR
// gh gave no head repository for) and own, the repositories the
// repository's remotes point at (RemoteRepoSet) — and never under a
// stranger's fork, whose main or patch-1 would otherwise tag your branch of
// the same name. Among those, one PR is chosen by preferPR (a live PR beats
// a closed or merged one, then the higher number), so two repositories' PRs
// from one branch name resolve the same way every time.
func (s PRSet) ForBranch(branch string, own RepoSet) (PR, bool) {
	var best PR
	found := false
	if branch == "" {
		return best, false
	}
	for k, pr := range s.ByHead {
		if k.Branch != branch || !s.yours(k.Repo, own) {
			continue
		}
		if !found || preferPR(pr, best) {
			best, found = pr, true
		}
	}
	return best, found
}

// yours reports whether a PR from head repository repo (lower-cased) may tag
// a local row: one from the listed repository or from a repository in own.
func (s PRSet) yours(repo string, own RepoSet) bool {
	return repo == "" || repo == s.BaseRepo || own.Has(repo)
}

// HasHeadRepo reports whether any PR in the set was opened from repo
// (compared without case).
func (s PRSet) HasHeadRepo(repo string) bool {
	if repo == "" {
		return false
	}
	repo = strings.ToLower(repo)
	for k := range s.ByHead {
		if k.Repo == repo {
			return true
		}
	}
	return false
}

// PRs returns the pull requests to show for each head of the repository at
// dir from one listing, `gh pr list --state state` (PRListOpen or
// PRListAll), keyed by head repository and branch. Any failure — gh not
// installed, not a GitHub remote, not logged in, the timeout, output that is
// not the expected JSON — returns a loaded, empty set and nothing else: that
// listing then adds no tags, and nothing is printed.
func PRs(dir, state string) PRSet {
	out, err := listPRs(dir, state)
	if err != nil {
		return PRSet{ByHead: map[PRKey]PR{}}
	}
	prs, err := parsePRs(out)
	if err != nil {
		return PRSet{ByHead: map[PRKey]PR{}}
	}
	return prs
}

// MergePRs returns a new set with every head of a and b, a head in both
// keeping the PR preferPR picks — the rule parsePRs applies within one
// listing, so the merge of two listings does not depend on which arrived
// first — and the first non-empty BaseRepo. Either set may be the zero value.
func MergePRs(a, b PRSet) PRSet {
	out := PRSet{BaseRepo: a.BaseRepo, ByHead: make(map[PRKey]PR, len(a.ByHead)+len(b.ByHead))}
	if out.BaseRepo == "" {
		out.BaseRepo = b.BaseRepo
	}
	for _, m := range []map[PRKey]PR{a.ByHead, b.ByHead} {
		for k, pr := range m {
			if cur, ok := out.ByHead[k]; ok && !preferPR(pr, cur) {
				continue
			}
			out.ByHead[k] = pr
		}
	}
	return out
}

// ghPR is one element of `gh pr list --json ...`.
type ghPR struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	HeadRefName       string `json:"headRefName"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Title             string `json:"title"`
	// HeadRepository is null for a fork that was deleted; it then decodes
	// as the zero value.
	HeadRepository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
}

// preferPR reports whether p should replace cur as a branch's PR. A live PR
// (open or draft) beats a closed or merged one, so a closed duplicate opened
// later does not hide the branch's open PR; otherwise the higher number wins
// — the one opened last, which is the branch's current PR whatever later
// activity (a comment, a label) did to an older one's updatedAt.
func preferPR(p, cur PR) bool {
	if pl, cl := p.State.live(), cur.State.live(); pl != cl {
		return pl
	}
	return p.Number > cur.Number
}

// live reports whether a PR in state s is still open, draft or not.
func (s PRState) live() bool { return s == PROpen || s == PRDraft }

// parsePRs maps gh's JSON to one PR per head (repository, branch), a head
// with several PRs keeping the one preferPR picks. A PR from a fork
// (isCrossRepository) is kept under its own head repository (#387), so it
// tags only that fork's remote rows and, by branch name, local rows while
// the fork is one of the repository's remotes (ForBranch); a fork PR whose
// head repository is gone (null: the fork was deleted) is dropped.
// A same-repository PR with no head repository is kept under "", which only
// a local row's lookup reaches. An element with no head branch or no number
// is skipped. BaseRepo is the first same-repository PR's head repository.
func parsePRs(data []byte) (PRSet, error) {
	var raw []ghPR
	if err := json.Unmarshal(data, &raw); err != nil {
		return PRSet{}, err
	}
	set := PRSet{ByHead: make(map[PRKey]PR, len(raw))}
	for _, p := range raw {
		if p.HeadRefName == "" || p.Number <= 0 {
			continue
		}
		repo := strings.ToLower(p.HeadRepository.NameWithOwner)
		if p.IsCrossRepository && repo == "" {
			continue
		}
		if !p.IsCrossRepository && set.BaseRepo == "" {
			set.BaseRepo = repo
		}
		k := PRKey{Repo: repo, Branch: p.HeadRefName}
		pr := PR{Number: p.Number, State: prState(p.State, p.IsDraft), Title: p.Title}
		if cur, ok := set.ByHead[k]; ok && !preferPR(pr, cur) {
			continue
		}
		set.ByHead[k] = pr
	}
	return set, nil
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

// PRBranch is the head branch name a worktree or local picker entry's PR is
// looked up under (PRSet.ForBranch), and a remote-tracking entry's display
// name: the branch itself for a worktree or local branch, and the branch
// without its first path segment (origin/foo -> foo) for a remote-tracking
// branch. A detached worktree has none. A remote row's PR is looked up under
// Entry.RemoteRepo and Entry.RemoteBranch (PRSet.ForRemote), the branch split
// off by the remote's real name, never under this name (#388, #387).
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
