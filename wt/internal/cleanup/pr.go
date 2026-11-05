package cleanup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// prTimeout bounds one `gh pr list` call; a timeout makes the branch UNKNOWN.
const prTimeout = 15 * time.Second

// prLimit is the page size requested per branch. A response of exactly this
// many PRs may be cut off, so it is treated as UNKNOWN.
const prLimit = 100

// PRInfo is one pull request of this repository's own head repo.
type PRInfo struct {
	Number int
	// State is OPEN, MERGED or CLOSED.
	State       string
	HeadRefOid  string
	BaseRef     string
	MergeCommit string // oid; "" when not merged
}

// Lookup is the tri-state answer of a PR query.
type Lookup int

const (
	// LookupUnknown means the answer is unusable: failure, timeout, bad
	// output or a possibly truncated page. Never treat it as none. It is the
	// zero value, so a zero PRResult (a map miss) is never read as an answer.
	LookupUnknown Lookup = iota
	// LookupKnown means at least one own-repo PR exists.
	LookupKnown
	// LookupNone means gh answered completely and there is no own-repo PR.
	LookupNone
)

// PRResult is the PR facts for one branch.
type PRResult struct {
	Lookup Lookup
	PRs    []PRInfo
	Err    string
}

// ghPRList is the test seam: it returns `gh pr list --head <branch>` JSON.
var ghPRList = realGhPRList

var errGhUnderTest = errors.New("gh pr list is not run from a test binary")

func realGhPRList(dir, branch string) ([]byte, error) {
	if testing.Testing() {
		return nil, errGhUnderTest
	}
	ctx, cancel := context.WithTimeout(context.Background(), prTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--head", branch, "--state", "all", "--limit", strconv.Itoa(prLimit),
		"--json", "number,state,headRefOid,baseRefName,mergeCommit,headRepository,headRepositoryOwner,isCrossRepository")
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GH_PROMPT_DISABLED=1", "GH_PAGER=cat", "PAGER=cat",
		"GH_NO_UPDATE_NOTIFIER=1", "GH_SPINNER_DISABLED=1", "NO_COLOR=1")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("gh pr list timed out after %s", prTimeout)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, ghFailure(err, ee.Stderr)
		}
		return nil, err
	}
	return out, nil
}

// ghErrMax caps a gh error's length in runes.
const ghErrMax = 120

// ghFailure is a failed gh call as a one-line error: the exit and the first
// non-empty line of gh's stderr. It becomes every affected row's detail, so
// gh's multi-line text (an unauthenticated gh prints a login speech) must not
// be pasted whole.
func ghFailure(err error, stderr []byte) error {
	return errors.New(oneLine(fmt.Sprintf("gh pr list: %v: %s", err, oneLine(string(stderr)))))
}

// oneLine is the first line of s that is not blank once sanitised: made
// printable (tab and CR become spaces; control runes such as ESC and BEL,
// and bidi controls, are dropped), its whitespace runs collapsed to single
// spaces, cut to ghErrMax runes (ending in "…"). Every gh error text goes
// through it, so a row's detail, the JSON and the header's gh line never
// carry terminal control sequences from gh's stderr.
func oneLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = flat(l); l != "" {
			return capRunes(l, ghErrMax)
		}
	}
	return ""
}

type ghPRRow struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
	HeadRepository *struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	HeadRepositoryOwner *struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	IsCrossRepository bool `json:"isCrossRepository"`
}

// headRepo is the lower-cased owner/repo of the PR's head: nameWithOwner if
// gh gives it, else headRepositoryOwner.login + "/" + headRepository.name
// (headRepository alone carries no owner).
func (r ghPRRow) headRepo() string {
	if r.HeadRepository == nil {
		return ""
	}
	if r.HeadRepository.NameWithOwner != "" {
		return strings.ToLower(r.HeadRepository.NameWithOwner)
	}
	if r.HeadRepositoryOwner != nil && r.HeadRepositoryOwner.Login != "" && r.HeadRepository.Name != "" {
		return strings.ToLower(r.HeadRepositoryOwner.Login + "/" + r.HeadRepository.Name)
	}
	return ""
}

// lookupOne is lookupBranch with its error text made one sanitised line
// (oneLine): it is a row's detail, goes into the JSON and reaches the
// terminal through the header's gh line.
func lookupOne(dir, branch, ownRepo string) PRResult {
	r := lookupBranch(dir, branch, ownRepo)
	if r.Err != "" {
		r.Err = cmp.Or(oneLine(r.Err), "gh pr list failed")
	}
	return r
}

func lookupBranch(dir, branch, ownRepo string) PRResult {
	out, err := ghPRList(dir, branch)
	if err != nil {
		return PRResult{Lookup: LookupUnknown, Err: cmp.Or(err.Error(), "gh pr list failed")}
	}
	var rows []ghPRRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return PRResult{Lookup: LookupUnknown, Err: "gh pr list: unexpected output: " + err.Error()}
	}
	if rows == nil {
		return PRResult{Lookup: LookupUnknown, Err: "gh pr list: output was null, not a list"}
	}
	if len(rows) >= prLimit {
		return PRResult{Lookup: LookupUnknown, Err: fmt.Sprintf("gh pr list returned %d PRs, the page limit; may be truncated", len(rows))}
	}
	own := strings.ToLower(ownRepo)
	var prs []PRInfo
	for _, r := range rows {
		state := strings.ToUpper(r.State)
		if r.Number <= 0 || (state != "OPEN" && state != "MERGED" && state != "CLOSED") {
			return PRResult{Lookup: LookupUnknown, Err: fmt.Sprintf("gh pr list: unrecognised PR (number %d, state %q)", r.Number, r.State)}
		}
		// isCrossRepository false means the head is the repository gh
		// queried, whatever name it now has (rename, transfer): ours. A
		// cross-repository PR is ours only if its head repo is ownRepo.
		if r.IsCrossRepository && r.headRepo() != own {
			continue
		}
		p := PRInfo{Number: r.Number, State: state, HeadRefOid: r.HeadRefOid, BaseRef: r.BaseRefName}
		if r.MergeCommit != nil {
			p.MergeCommit = r.MergeCommit.Oid
		}
		prs = append(prs, p)
	}
	if len(prs) == 0 {
		return PRResult{Lookup: LookupNone}
	}
	return PRResult{Lookup: LookupKnown, PRs: prs}
}

// LookupPRs asks gh for each branch's PRs (all states) in dir, keeping those
// whose head repository is ownRepo (owner/repo, compared without case), with
// at most parallel calls at once (<1 means 1). It is read-only. Every branch
// gets an entry; any failure for a branch is LookupUnknown, never LookupNone.
func LookupPRs(dir string, branches []string, ownRepo string, parallel int) map[string]PRResult {
	if parallel < 1 {
		parallel = 1
	}
	res := make(map[string]PRResult, len(branches))
	if strings.TrimSpace(ownRepo) == "" {
		for _, b := range branches {
			res[b] = PRResult{Lookup: LookupUnknown, Err: "own repository unknown; cannot filter fork PRs"}
		}
		return res
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	for _, b := range branches {
		wg.Add(1)
		sem <- struct{}{}
		go func(b string) {
			defer wg.Done()
			defer func() { <-sem }()
			r := lookupOne(dir, b, ownRepo)
			mu.Lock()
			res[b] = r
			mu.Unlock()
		}(b)
	}
	wg.Wait()
	return res
}
