// Package rootclaim is what both launch paths (cmd/wt's plain terminal and the
// picker) share around internal/sessionclaim: building this session's claim
// on a repository's root checkout, and asking the person what to do when
// another live session holds it. sessionclaim stays free of git; the git
// questions are worktree's.
package rootclaim

import (
	"fmt"
	"io"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// Root is a repository's root checkout as a claim needs it.
type Root struct {
	Path      string // the primary checkout, symlinks resolved
	CommonDir string // <git-common-dir>, where wt-sessions lives
	Branch    string // the branch checked out there, "(detached)" when none
	Default   string // the repository's default branch from origin/HEAD, "" when unknown
}

// Lookup reports the root checkout dir is. ok is false when dir is not a
// repository's root (a linked worktree, a subdirectory, a bare repository, no
// repository); then nothing else is set. err is a failure to read the common
// directory, with ok true and Path set so a note can name the root.
func Lookup(dir string) (r Root, ok bool, err error) {
	path, ok := worktree.RootOf(dir)
	if !ok {
		return Root{}, false, nil
	}
	r = Root{Path: path}
	if r.CommonDir, err = worktree.CommonDirAt(path); err != nil {
		return r, true, err
	}
	if r.Branch = worktree.CurrentBranchAt(path); r.Branch == "" {
		r.Branch = "(detached)"
	}
	r.Default = worktree.DefaultBranch(path)
	return r, true, nil
}

// Hint is the one line that points at `wt restore-root` when the root r is on
// a named branch other than its default, and "" otherwise: on the default, on
// a detached HEAD, or when the default is unknown (origin/HEAD unset, a
// declared limit: restore-root itself would still work there).
func Hint(r Root) string {
	if r.Default == "" || r.Branch == "(detached)" || r.Branch == r.Default {
		return ""
	}
	return "run `wt restore-root` to move it and put the root back on " + r.Default
}

// Self is this process's claim on r for agent. err is a failure to read this
// process's start time.
func (r Root) Self(agent string) (sessionclaim.Claim, error) {
	return sessionclaim.Self(r.Path, r.Branch, agent)
}

// AskContinue reads one line from in and reports whether to continue: an empty
// line, y or Y (surrounding space ignored) is yes, the default; n, Esc, Ctrl-C,
// any other text and the end of input are no. It prints no prompt (the caller
// owns the "[Y/n]"); it prints a newline only when input ended with no answer,
// so the next line starts clean. It reads byte by byte, so it takes nothing
// past the newline from a shared reader.
func AskContinue(in io.Reader, out io.Writer) bool {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			if len(line) == 0 {
				fmt.Fprintln(out)
				return false
			}
			break
		}
	}
	switch strings.TrimSpace(string(line)) {
	case "", "y", "Y":
		return true
	}
	return false
}
