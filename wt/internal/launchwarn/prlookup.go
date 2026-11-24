package launchwarn

import (
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// prBudget is how long a flag launch waits for the PR lookup at the launch
// question (#585). The lookup starts at the top of the launch and overlaps
// the other git reads, so the wait is usually zero; past the budget the
// launch goes on with no PR warning.
const prBudget = time.Second

// PRBudget is the budget a production launch passes to Resolve. Tests pass
// their own per call (there is no package variable to swap).
const PRBudget = prBudget

// PRLookup is one background `gh pr list` for the branch a flag launch is
// about to start in (--root, -W, --cwd). The picker never uses it: its rows
// carry PR facts already (FromRow). Design (#585, recorded here): read-only,
// started early, waited for at most the budget at confirmLaunch time; gh
// missing or failing, a non-github.com origin, a slow answer, or an answer
// that is not provably this branch's PR all mean no warning — the lookup
// never delays a launch past the budget, blocks it or errors it.
type PRLookup struct {
	done chan prAnswer // buffered 1; nil on a handle that skipped
}

type prAnswer struct {
	state  string
	number int
}

// StartPRLookup starts the lookup for branch in dir and returns at once. list
// is the gh seam (the per-call hook, as cleanup.GatherInputs.PRList); nil is
// the real gh. A branch that is empty or the default branch, or a directory
// whose origin is not on github.com, skips the lookup: the returned handle
// resolves to "unknown" immediately. A nil *PRLookup is a skipped one.
func StartPRLookup(dir, branch string, list func(dir, branch string) ([]byte, error)) *PRLookup {
	if branch == "" || branch == "(detached)" || dir == "" {
		return &PRLookup{}
	}
	l := &PRLookup{done: make(chan prAnswer, 1)}
	go func() { l.done <- lookupBranchPR(dir, branch, list) }()
	return l
}

// Resolve is t with the PR fact filled in when the lookup answered within
// budget and proved the branch's PR merged: PRState "merged" and PRNumber, so
// Collect's pr_merged warning joins the one confirm line. Every other outcome
// (skipped, slow, failed, open PR, not provably this branch's) leaves the PR
// fields as they are, except that an unset state becomes "unknown".
func (l *PRLookup) Resolve(t Target, budget time.Duration) Target {
	if t.PRState == "" {
		t.PRState = "unknown"
	}
	if l == nil || l.done == nil || t.PRState == "merged" {
		return t
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case a := <-l.done:
		if a.state == "merged" {
			t.PRState, t.PRNumber = a.state, a.number
		}
	case <-timer.C:
	}
	return t
}

// lookupBranchPR asks gh about branch and reduces the answer: "merged" with a
// number only when no PR of this repository for the name is open and a merged
// one's head is the branch tip or an ancestor of it (the #389/#451 rule
// dropStalePRTags applies: a name reused after its PR merged must not warn).
func lookupBranchPR(dir, branch string, list func(dir, branch string) ([]byte, error)) prAnswer {
	own := cleanup.OwnRepoOf(dir)
	if own == "" || worktree.DefaultBranch(dir) == branch {
		return prAnswer{}
	}
	res := cleanup.LookupPRs(dir, []string{branch}, own, 1, cleanup.GatherInputs{PRList: list})[branch]
	if res.Lookup != cleanup.LookupKnown {
		return prAnswer{}
	}
	g := cleanup.Git{Dir: dir, Timeout: 5 * time.Second}
	o, err := g.Run("rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	tip := strings.TrimSpace(o.Stdout)
	if err != nil || o.Code != 0 || tip == "" {
		return prAnswer{}
	}
	best := 0
	for _, pr := range res.PRs {
		switch pr.State {
		case "OPEN":
			return prAnswer{} // a live PR for the name: nothing is merged yet
		case "MERGED":
			if pr.Number > best && headInBranch(g, pr.HeadRefOid, tip) {
				best = pr.Number
			}
		}
	}
	if best == 0 {
		return prAnswer{}
	}
	return prAnswer{state: "merged", number: best}
}

// headInBranch reports whether a PR head is provably the branch tip or in
// its history; anything git cannot answer is "no".
func headInBranch(g cleanup.Git, head, tip string) bool {
	if head == "" {
		return false
	}
	if head == tip {
		return true
	}
	o, err := g.Run("merge-base", "--is-ancestor", head, tip)
	return err == nil && o.Code == 0
}
