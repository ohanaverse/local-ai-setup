package removal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// runGit runs one removal git command. It is a seam: tests stub it to record
// the argument vector or to fail part-way.
var runGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) { return g.Run(args...) }

// removeWorktree runs `git worktree remove [--force] -- path` with the
// removal timeout, then checks that the directory is gone from disk and from
// git's worktree list. It returns WtRemoved, or WtFailed with a reason naming
// git's error and what is left over. It never prunes and never deletes
// anything itself.
func (e Engine) removeWorktree(path string, force bool) (WorktreeOutcome, string) {
	timeout := e.Opts.RemoveTimeout
	if timeout == 0 {
		timeout = DefaultRemoveTimeout
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", path)

	out, err := runGit(cleanup.Git{Dir: e.G.Dir, Timeout: timeout}, args...)
	var gitErr string
	switch {
	case err != nil:
		gitErr = fmt.Sprintf("git worktree remove did not finish (timeout %s): %v", timeout, err)
	case out.Code != 0:
		gitErr = fmt.Sprintf("git worktree remove: exit %d: %s", out.Code, cleanup.OneLine(out.Stderr))
	}

	left, verr := e.leftOver(path, gitErr != "")
	switch {
	case verr != "" && gitErr != "":
		return WtFailed, gitErr + "; " + verr
	case verr != "":
		return WtFailed, verr
	case left != "" && gitErr != "":
		// What is left first: the cap may cut git's text, never this.
		return WtFailed, left + " (" + gitErr + ")"
	case left != "":
		// git said it was done; it is not: never take its word for it.
		return WtFailed, "git reported success but " + left
	case gitErr != "":
		// Gone from disk and from the list, yet git failed: fail closed, so
		// the branch stage does not run on a removal git did not confirm.
		return WtFailed, "the worktree is gone but git did not confirm it (" + gitErr + ")"
	}
	return WtRemoved, ""
}

// leftOver describes what of the worktree at path remains ("" nothing), or
// returns why that could not be checked (a non-empty second value). After a
// git failure (gitFailed: an exit, a timeout, a kill) a directory still there
// may be partly deleted, and the text says so.
func (e Engine) leftOver(path string, gitFailed bool) (left, verifyErr string) {
	onDisk := true
	if _, err := os.Lstat(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", "could not check whether the worktree is gone: " + err.Error()
		}
		onDisk = false
	}
	facts, err := cleanup.ListWorktrees(e.G, e.In.CWD)
	if err != nil {
		return "", "could not check whether the worktree is gone: " + err.Error()
	}
	self := cleanup.ResolvePath(path)
	listed := false
	for _, w := range facts {
		if cleanup.ResolvePath(w.Path) == self {
			listed = true
			break
		}
	}
	partly := ""
	if gitFailed && onDisk {
		partly = "the worktree may be partly removed: "
	}
	switch {
	case onDisk && listed:
		return partly + "the directory is still there and still registered", ""
	case onDisk:
		return partly + "the directory is still there but git no longer lists it as a worktree", ""
	case listed:
		return "the directory is gone but a prunable registration remains; run git worktree prune", ""
	}
	return "", ""
}
