package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// fullCommitID is a full object id as git prints it (sha-1 or sha-256).
var fullCommitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// CheckNew reports where CreateAt would put a new branch called name, or why
// it cannot. It writes nothing. It is classifyName's answer for a name that
// must be new: a branch of that name, a worktree that holds it, a path
// another branch's name flattens onto, a directory already there, an
// invalid branch name and a remote-tracking form are all errors. There is no
// reuse and no automatic suffix: a rerun must be told a name is taken.
func CheckNew(dir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("worktree name cannot be empty")
	}
	if err := checkNewBranchName(dir, name); err != nil {
		return "", err
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return "", err
	}
	cls, err := classifyName(dir, name, entries)
	if err != nil {
		return "", err
	}
	switch cls.kind {
	case nameCreate:
	case nameReuseWorktree:
		return "", fmt.Errorf("branch %q already exists: worktree %s holds it", name, cls.path)
	case nameExistingBranch:
		return "", fmt.Errorf("branch %q already exists", name)
	default:
		return "", fmt.Errorf("%q names a remote-tracking branch, not a new branch", name)
	}
	if _, err := os.Lstat(cls.path); err == nil {
		return "", fmt.Errorf("%s already exists", cls.path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return cls.path, nil
}

// checkNewBranchName is checkBranchName plus a refusal of "@" and a refs/
// prefix, which git accepts as a ref name but which would make a branch git
// cannot address by that name (or one under refs/heads/refs/).
func checkNewBranchName(dir, name string) error {
	if name == "@" || strings.HasPrefix(name, "refs/") {
		return fmt.Errorf("invalid branch name %q: not usable as a new branch name", name)
	}
	return checkBranchName(dir, name)
}

// NotCreatedError says CreateAt refused before it ran `git worktree add`: the
// start was unusable or CheckNew refused the name (taken, invalid, a path
// already there). Nothing was made by this call, so whatever the name now
// points at is not CreateAt's. NameUnavailable is true for CheckNew's
// refusals.
type NotCreatedError struct {
	NameUnavailable bool
	Err             error
}

func (e *NotCreatedError) Error() string { return e.Err.Error() }
func (e *NotCreatedError) Unwrap() error { return e.Err }

// AddFailedError says `git worktree add` ran and failed. BranchLeft and
// DirLeft report what that run left behind: a branch (or directory) that was
// absent just before the run and is there now, so it is this call's own
// leftover and not someone else's.
type AddFailedError struct {
	BranchLeft bool
	DirLeft    bool
	Err        error
}

func (e *AddFailedError) Error() string { return e.Err.Error() }
func (e *AddFailedError) Unwrap() error { return e.Err }

// CreateAt creates branch name and its linked worktree under dir's
// .worktrees at the commit start (a full object id of a commit, never a
// name that could move), without upstream tracking, and returns the
// worktree's path. dir is the repository root. Every check CheckNew makes
// runs first, so a refusal leaves no branch behind. It never reuses an
// existing branch or worktree. Hooks are disabled for the add
// (core.hooksPath=/dev/null): a post-checkout hook could rewrite or fail the
// new checkout, and a caller that needs a faithful copy must not run user
// code in it. The errors are *NotCreatedError (nothing was made) or
// *AddFailedError (git ran).
func CreateAt(dir, name, start string) (string, error) {
	if err := checkStartCommit(dir, start); err != nil {
		return "", err
	}
	path, err := CheckNew(dir, name)
	if err != nil {
		return "", &NotCreatedError{NameUnavailable: true, Err: err}
	}
	// Anything that appears after this look is not this call's: git refuses to
	// reuse a name, so a ref or directory found afterwards was either made by
	// the add or by someone racing it; only the first kind is claimed.
	// Known limit: a branch or directory created by someone else in the few
	// microseconds between CheckNew above and these two looks is
	// indistinguishable from the add's own leftover and would be claimed. It
	// cannot be tested without a race and is deliberately left as it is.
	_, e := runGit(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	branchBefore := e == nil
	_, e = os.Lstat(path)
	dirBefore := e == nil
	if out, err := runGitCombined(dir, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--no-track", "-b", name, path, start); err != nil {
		fail := &AddFailedError{}
		msg := fmt.Sprintf("git worktree add -b: %v: %s", err, strings.TrimSpace(string(out)))
		if _, e := runGit(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); e == nil && !branchBefore {
			fail.BranchLeft = true
			msg += fmt.Sprintf(" (branch %s was created and left behind)", name)
		}
		if _, e := os.Lstat(path); e == nil && !dirBefore {
			fail.DirLeft = true
		}
		fail.Err = fmt.Errorf("%s", msg)
		return "", fail
	}
	return path, nil
}

// runGitCombined is runGit with stderr folded into the output, for an error
// message that says why git failed.
func runGitCombined(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// checkStartCommit refuses (as a *NotCreatedError) a start that is not a full
// object id naming a commit of the repository at dir.
func checkStartCommit(dir, start string) error {
	if !fullCommitID.MatchString(start) {
		return &NotCreatedError{Err: fmt.Errorf("start %q is not a full commit id", start)}
	}
	out, err := runGit(dir, "cat-file", "-t", start)
	if err != nil || strings.TrimSpace(string(out)) != "commit" {
		return &NotCreatedError{Err: fmt.Errorf("start %s is not a commit of this repository", start)}
	}
	return nil
}

// CheckNewDetached reports where CreateDetachedAt would put a detached
// worktree for branch, named dirName, or why it cannot. The path is
// .worktrees/<dirName flattened>. It writes nothing. Unlike CheckNew it never
// asks classifyName (which would reuse the worktree that holds branch) and
// never refuses the default branch: the branch is expected to exist and be
// held elsewhere. It errors on a flattening failure, a path another branch's
// worktree uses and a path that already exists.
func CheckNewDetached(dir, dirName, branch string) (string, error) {
	path, err := worktreePath(dir, dirName)
	if err != nil {
		return "", err
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return "", err
	}
	if err := pathCollision(entries, path, branch); err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

// CreateDetachedAt adds a worktree under dir's .worktrees with a detached
// HEAD at start and returns its path. It is how a branch the primary checkout
// still holds gets a second checkout, which git refuses for a branch. start
// must be a full commit id equal to the tip of refs/heads/branch, so the copy
// is of the branch as it is now. No branch or ref is made. Hooks are
// disabled (core.hooksPath=/dev/null) as in CreateAt. The errors are
// *NotCreatedError (nothing was made; NameUnavailable for a name or path
// problem) or *AddFailedError (git ran; BranchLeft is always false, DirLeft
// says the path appeared).
func CreateDetachedAt(dir, dirName, branch, start string) (string, error) {
	if err := checkStartCommit(dir, start); err != nil {
		return "", err
	}
	out, err := runGit(dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", &NotCreatedError{Err: fmt.Errorf("branch %q does not exist", branch)}
	}
	if tip := strings.TrimSpace(string(out)); tip != start {
		return "", &NotCreatedError{Err: fmt.Errorf("branch %q is at %s, not the start %s", branch, tip, start)}
	}
	path, err := CheckNewDetached(dir, dirName, branch)
	if err != nil {
		return "", &NotCreatedError{NameUnavailable: true, Err: err}
	}
	_, e := os.Lstat(path)
	dirBefore := e == nil
	if out, err := runGitCombined(dir, "-c", "core.hooksPath="+os.DevNull, "worktree", "add", "--detach", path, start); err != nil {
		fail := &AddFailedError{Err: fmt.Errorf("git worktree add --detach: %v: %s", err, strings.TrimSpace(string(out)))}
		if _, e := os.Lstat(path); e == nil && !dirBefore {
			fail.DirLeft = true
		}
		return "", fail
	}
	return path, nil
}
