package launchwarn

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// ForDir reads the Target for a launch that starts in dir, from live git and
// the session claims. A directory outside a repository, or in a bare one,
// yields the zero Target and a nil error: no launch warns there. It is
// read-only (it prunes no claim), and the PR and upstream facts stay zero for
// a caller that has a cleanup row to set them.
func ForDir(dir string) (Target, error) {
	return forDir(dir, nil)
}

// ForDirKnownDirt is ForDir with the dirt already known: dirty is the
// caller's gathered answer (a unified-list row's raw losses), so the
// full-tree `git status --porcelain` probe is skipped entirely and
// Target.Dirty is exactly that value. Everything else is read live as ForDir
// reads it. A caller with no gathered answer uses ForDir and gets a probed
// answer.
func ForDirKnownDirt(dir string, dirty bool) (Target, error) {
	return forDir(dir, &dirty)
}

// forDir is the body ForDir and ForDirKnownDirt share; knownDirt, non-nil, is
// the caller's gathered dirt answer in place of the probe.
func forDir(dir string, knownDirt *bool) (Target, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Target{}, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Target{}, err
	}
	primary, err := worktree.PrimaryOf(resolved)
	if err != nil {
		return Target{}, nil // not a repository, or a bare one
	}
	t := rootTarget(primary)
	t.Dir = resolved
	t.Branch = branchOr(resolved)
	_, t.IsRoot = worktree.RootOf(resolved)
	if knownDirt != nil {
		t.Dirty = *knownDirt
	} else {
		t.Dirty = dirty(resolved)
	}

	commonDir, err := worktree.CommonDirAt(resolved)
	if err != nil {
		return t, err
	}
	claimPath := resolved
	if top, err := worktree.RepoRootAt(resolved); err == nil {
		if r, err := filepath.EvalSymlinks(top); err == nil {
			claimPath = r
		}
	}
	me := sessionclaim.Claim{PID: os.Getpid(), Path: claimPath}
	held, err := sessionclaim.AncestorHolds(commonDir, me)
	if err != nil {
		return t, err
	}
	if held {
		t.RootChosen = t.IsRoot
		return t, nil
	}
	t.Holders, err = sessionclaim.Others(commonDir, me)
	return t, err
}

// ForNew reads the Target for a launch that will create the worktree plan
// describes in the repository at repoRoot. Nothing is created.
func ForNew(repoRoot, branch string, plan *worktree.Plan) (Target, error) {
	primary, err := worktree.PrimaryOf(repoRoot)
	if err != nil {
		return Target{}, nil
	}
	t := rootTarget(primary)
	t.Dir = plan.Path()
	t.Branch = plan.Branch()
	if t.Branch == "" {
		t.Branch = branch
	}
	t.Creation = &Creation{Branch: t.Branch, Tracks: plan.Tracks(), NewBranch: plan.Creates(), Path: plan.Path()}
	return t, nil
}

// rootTarget fills what comes from the repository's root checkout, read live:
// its current branch (never cached), the default, and the drift hint.
func rootTarget(primary string) Target {
	r := rootclaim.Root{Path: primary, Branch: branchOr(primary), Default: worktree.DefaultBranch(primary)}
	return Target{Default: r.Default, RootBranch: r.Branch, Hint: rootclaim.Hint(r)}
}

func branchOr(dir string) string {
	if b := worktree.CurrentBranchAt(dir); b != "" {
		return b
	}
	return "(detached)"
}

// dirty reports uncommitted, non-ignored changes at dir; a git failure is not
// dirty (this is a warning's input, not a gate).
func dirty(dir string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}
