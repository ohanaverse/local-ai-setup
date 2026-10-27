// Package rootclaim is what both launch paths (cmd/wt's plain terminal and the
// picker) share around internal/sessionclaim: building this session's claim
// on a repository's root checkout, and asking the person what to do when
// another live session holds it. sessionclaim stays free of git; the git
// questions are worktree's.
package rootclaim

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// Root is a repository's root checkout as a claim needs it.
type Root struct {
	Path      string // the primary checkout, symlinks resolved
	CommonDir string // <git-common-dir>, where wt-sessions lives
	Branch    string // the branch checked out there, "(detached)" when none
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
	return r, true, nil
}

// Self is this process's claim on r for agent. err is a failure to read this
// process's start time.
func (r Root) Self(agent string) (sessionclaim.Claim, error) {
	return sessionclaim.Self(r.Path, r.Branch, agent)
}

// Choose prints the shared-root menu on out after "wt: " and reads one key
// from in: c continue here, w new worktree, q quit (Esc and Ctrl-C are q, and
// so is the end of input). On a terminal it reads one key at a time.
func Choose(r Root, others []sessionclaim.Claim, in io.Reader, out io.Writer) byte {
	fmt.Fprint(out, "wt: "+sessionclaim.Menu(r.Path, others, time.Now())+"choose [c/w/q]: ")
	choice := readChoice(in)
	fmt.Fprintln(out)
	return choice
}

// readChoice reads keys from in until one is c, w or q.
func readChoice(in io.Reader) byte {
	if f, ok := in.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if state, err := term.MakeRaw(f.Fd()); err == nil {
			defer term.Restore(f.Fd(), state)
		}
	}
	buf := make([]byte, 1)
	for {
		if n, err := in.Read(buf); n == 0 && err != nil {
			return 'q'
		} else if n == 0 {
			continue
		}
		switch b := buf[0]; b {
		case 'c', 'C':
			return 'c'
		case 'w', 'W':
			return 'w'
		case 'q', 'Q', 0x1b, 0x03:
			return 'q'
		}
	}
}
