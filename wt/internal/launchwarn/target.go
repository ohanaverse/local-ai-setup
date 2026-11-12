package launchwarn

import (
	"errors"
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
// read-only (it prunes no claim). Only the PR facts stay zero now, for a
// caller that has a cleanup row to set them: the upstream fact is read here
// from git (upstreamGone), as ForNew reads it.
func ForDir(dir string) (Target, error) {
	return forDir(dir, nil)
}

// ForDirKnownDirt is ForDir with the dirt already known: dirty is the
// caller's gathered answer (a unified-list row's raw losses), so the
// full-tree `git status --porcelain` probe is skipped entirely and a
// resolved Target carries the value as Target.Dirty. (The zero-Target
// paths — outside a repository or a bare one — carry no dirt either way.)
// Everything else is read live as ForDir reads it. A caller with no
// gathered answer uses ForDir and gets a probed answer.
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
		if errors.Is(err, worktree.ErrNotACheckout) {
			return Target{}, nil // not a repository, or a bare one
		}
		// A real read failure is surfaced, not swallowed as "not a
		// repository": the partial Target carries what was read (the
		// directory) and the callers print one "wt: note:" and proceed.
		return Target{Dir: resolved}, err
	}
	t := rootTarget(primary)
	t.Dir = resolved
	t.Branch = branchOr(resolved)
	t.UpstreamGone = upstreamGone(resolved, t.Branch)
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
		// A nested launch under an ancestor that holds this root carries
		// that consent at the root (RootChosen) and never warns about the
		// ancestor — Others skips this session's ancestor pids, which is why
		// filling Holders below cannot bring the ancestor back. But the
		// root's other live sessions are strangers still: dropping them here
		// meant two agents at one root never warned about each other.
		t.RootChosen = t.IsRoot
	}
	t.Holders, err = sessionclaim.Others(commonDir, me)
	return t, err
}

// ForNew reads the Target for a launch that will create the worktree plan
// describes in the repository at repoRoot. Nothing is created. A directory
// outside a repository, or a bare one, yields the zero Target and a nil
// error, as ForDir reads it; any other failure returns the partial Target plus
// the error, and that partial keeps the plan's Creation — its facts do not
// depend on PrimaryOf, so the frozen creation line still announces exactly
// what the commit point creates.
func ForNew(repoRoot, branch string, plan *worktree.Plan) (Target, error) {
	t := Target{Dir: plan.Path(), Branch: plan.Branch()}
	if t.Branch == "" {
		t.Branch = branch
	}
	t.UpstreamGone = upstreamGone(repoRoot, t.Branch)
	t.Creation = &Creation{Branch: t.Branch, Tracks: plan.Tracks(), NewBranch: plan.Creates(), Path: plan.Path()}
	primary, err := worktree.PrimaryOf(repoRoot)
	if err != nil {
		if errors.Is(err, worktree.ErrNotACheckout) {
			return Target{}, nil // not a repository, or a bare one
		}
		return t, err
	}
	root := rootTarget(primary)
	t.Default, t.RootBranch, t.Hint = root.Default, root.RootBranch, root.Hint
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

// upstreamGone reports whether branch's configured upstream's remote-tracking
// ref no longer exists — the fact a cleanup row carries, read here from pure
// git for the flag and typed launch paths (no network, no gh). Git records the
// upstream on the branch itself (branch.<n>.remote/.merge, which
// `for-each-ref %(upstream)` prints whether or not the ref is still there —
// `@{upstream}` by contrast fails once the ref is gone), and show-ref says
// whether that ref exists. A branch with no configured upstream, or one git
// cannot answer for, is not gone: this is a warning's input, not a gate.
func upstreamGone(dir, branch string) bool {
	cmd := exec.Command("git", "for-each-ref", "--format=%(upstream)", "refs/heads/"+branch)
	cmd.Dir = dir
	out, err := cmd.Output()
	upstream := strings.TrimSpace(string(out))
	if err != nil || upstream == "" {
		return false
	}
	ref := exec.Command("git", "show-ref", "--verify", "--quiet", upstream)
	ref.Dir = dir
	return ref.Run() != nil
}
