// Package cleanup judges which worktrees and branches are safe to remove. It
// is read-only by design: nothing here deletes a worktree, branch or ref.
// internal/removal acts on its rows (re-checking each with Rescan first) and
// is the only package in wt allowed to delete.
package cleanup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// defaultTimeout bounds a git call when Git.Timeout is zero.
const defaultTimeout = 30 * time.Second

// Git runs git against one repository directory.
type Git struct {
	Dir     string
	Timeout time.Duration // zero means 30s
	// Config are `key=value` settings passed as `-c` options before the
	// subcommand (core.fsmonitor=false for a caller that must not start the
	// fsmonitor daemon).
	Config []string
	// Env is appended to the process environment (last entry wins). Use
	// NonInteractive(dir) for any call that may reach a remote.
	Env []string
}

// NonInteractive is the environment of a git call in dir that must never
// prompt: no terminal credential prompt, no credential-manager dialog, and ssh
// in BatchMode (no passphrase or host-key question). The ssh part is added only
// when the user has set neither GIT_SSH_COMMAND nor core.sshCommand (read in
// dir, so repository config counts): their own ssh command is left alone, and
// a prompt it raises is theirs.
func NonInteractive(dir string) []string {
	env := []string{"GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"}
	if os.Getenv("GIT_SSH_COMMAND") != "" {
		return env
	}
	// Exit 1 is "unset"; any other failure is read as "set" (fail toward
	// leaving the user's ssh alone).
	if o, err := (Git{Dir: dir, Timeout: 5 * time.Second}).Run("config", "--get", "core.sshCommand"); err != nil || o.Code != 1 {
		return env
	}
	return append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
}

// PathFold returns how the git in dir compares path names: unchanged, or
// case-folded when core.ignorecase is true. A config that cannot be read, or
// reads as anything but a clean "unset" or a boolean, folds (fail closed:
// folding only finds more conflicts). The fold is Unicode simple case folding
// (upper then lower, so EqualFold-equal names share a key). It does not
// normalize NFD/NFC: a caller that must compare names on disk asks the
// filesystem.
func PathFold(g Git) func(string) string { return FoldFor(IgnoreCase(g)) }

// IgnoreCase reads core.ignorecase the way PathFold does (true unless the
// config clearly reads false or unset), for a caller that reads it once and
// hands the answer down with FoldFor.
func IgnoreCase(g Git) bool {
	ignoreCase := true
	if o, err := g.Run("config", "--get", "--type=bool", "core.ignorecase"); err == nil {
		switch v := strings.TrimSpace(o.Stdout); {
		case o.Code == 0 && v == "false":
			ignoreCase = false
		case o.Code == 1 && v == "":
			ignoreCase = false // unset: git's default is false
		}
	}
	return ignoreCase
}

// FoldFor is PathFold's comparison for an already-read IgnoreCase.
func FoldFor(ignoreCase bool) func(string) string {
	if !ignoreCase {
		return func(s string) string { return s }
	}
	return func(s string) string { return strings.ToLower(strings.ToUpper(s)) }
}

// PathSet maps a folded path name to the first original path that put it in
// the set (see PathSets).
type PathSet map[string]string

// Has reports whether a folded name is in the set.
func (s PathSet) Has(folded string) bool { _, ok := s[folded]; return ok }

// PathSets returns the set of paths and the set of their ancestor
// directories (slash-separated, relative), keyed by fold. Each entry holds the
// first path that produced it (for a directory, the first path under it). An
// empty path is skipped.
func PathSets(paths []string, fold func(string) string) (files, dirs PathSet) {
	files, dirs = PathSet{}, PathSet{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, ok := files[fold(p)]; !ok {
			files[fold(p)] = p
		}
		for d := path.Dir(p); d != "." && d != "/" && d != ""; d = path.Dir(d) {
			if dirs.Has(fold(d)) {
				break
			}
			dirs[fold(d)] = p
		}
	}
	return files, dirs
}

// WithoutFSMonitor returns g with core.fsmonitor=false appended after its -c
// settings (so a caller cannot turn it back on): a stale or wrong fsmonitor
// hook must not hide an edit. g.Config is cloned, never aliased.
func WithoutFSMonitor(g Git) Git {
	g.Config = append(slices.Clone(g.Config), "core.fsmonitor=false")
	return g
}

// UnsafeRel says why p cannot name a file inside a root (and outside any .git
// directory of it, repository metadata a rescue never carries), or "". It is
// the union of the rules every caller needs: empty or ".", absolute, not a
// clean relative path, a NUL byte, an empty, "." or ".." component, a .git
// component.
func UnsafeRel(p string) string {
	comps := strings.Split(filepath.ToSlash(p), "/")
	switch {
	case p == "" || p == ".":
		return "is empty"
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/"):
		return "is absolute"
	case filepath.Clean(p) != filepath.FromSlash(p) || strings.Contains(p, "\x00"):
		return "is not a clean relative path"
	case slices.Contains(comps, ".."):
		return "leaves the root"
	case slices.ContainsFunc(comps, func(c string) bool { return strings.EqualFold(c, ".git") }):
		return "names a .git directory, which is repository metadata, never a carried file"
	case slices.ContainsFunc(comps, func(c string) bool { return c == "" || c == "." }):
		return "is not a clean relative path"
	}
	return ""
}

// Out is a finished git process: its output and exit code.
type Out struct {
	Stdout string
	Stderr string
	Code   int
}

// Run runs `git -C g.Dir args...`. A non-zero exit is an answer, not an
// error: Out.Code carries it. The error is only for a failure to spawn git or
// for the timeout.
func (g Git) Run(args ...string) (Out, error) { return g.RunInput("", args...) }

// RunInput is Run with stdin fed from input (for `git patch-id`).
func (g Git) RunInput(input string, args ...string) (Out, error) {
	timeout := g.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	full := []string{"-C", g.Dir}
	for _, c := range g.Config {
		full = append(full, "-c", c)
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	// This package is read-only: GIT_OPTIONAL_LOCKS=0 stops `git status` and
	// `git diff` from rewriting the index's stat cache as a side effect.
	cmd.Env = append(append(os.Environ(), "GIT_OPTIONAL_LOCKS=0"), g.Env...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A grandchild (an alias shell's sleep) holding the pipes open must not
	// outlast the timeout.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	out := Out{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			out.Code = ee.ExitCode()
			return out, nil
		}
		return out, err
	}
	return out, nil
}

// StatusArgs returns the arguments that read every uncommitted change of a
// checkout in full: porcelain v2, untracked files one by one (not a directory
// summary), and submodule changes whatever their ignore config says. It is a
// fresh slice each call, so callers may append to it.
func StatusArgs() []string {
	return []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none"}
}

// WriteConfig returns the -c options every checkout-changing write (reset,
// switch, fast-forward) carries: hooks off (a hook is arbitrary code) and no
// submodule recursion (a user's submodule.recurse=true would move submodule
// checkouts as well). It is a fresh slice each call.
func WriteConfig() []string {
	return []string{"-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false"}
}
