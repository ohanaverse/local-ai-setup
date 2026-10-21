// Package gitenv knows which environment variables decide the repository git
// acts on. It imports nothing from this module, so the test binary of any
// package here can call IsolateForTest without an import cycle.
package gitenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// repoVars names the variables that tell git which repository, work tree,
// index, object store or command-line config to use: the ones git itself
// drops before it runs a command in another repository
// (`git rev-parse --local-env-vars`), and GIT_NAMESPACE. A git hook exports
// several of them to whatever it runs, `go test` included.
var repoVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX",
	"GIT_NAMESPACE", "GIT_IMPLICIT_WORK_TREE", "GIT_GRAFT_FILE",
	"GIT_SHALLOW_FILE", "GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE",
	"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
}

// ceilingVar is the variable that lists the directories git does not walk up
// past when it looks for a repository.
const ceilingVar = "GIT_CEILING_DIRECTORIES"

// networkGuards are set so that no test reaches a git remote over ssh or
// waits on a terminal: GIT_SSH_COMMAND=false makes every ssh transport fail
// at once without running ssh (it outranks core.sshCommand and GIT_SSH), and
// GIT_TERMINAL_PROMPT=0 makes git fail instead of asking for a username or
// password on the terminal.
var networkGuards = map[string]string{
	"GIT_SSH_COMMAND":     "false",
	"GIT_TERMINAL_PROMPT": "0",
}

// IsolateForTest is for a package's TestMain: for the rest of the process,
// git finds a repository only where a test put one. Every package whose test
// binary can run git calls it, whether a test runs git itself or reaches
// production code that does (internal/worktree, internal/guard,
// internal/smoke); TestEveryPackageThatRunsGitIsolatesIt holds them to it.
// Production code never calls it — like config.IsolateConfigHomeForTest, it
// exists so cross-package tests share one definition.
//
// Two things on the developer's machine otherwise decide what git finds:
//
//   - an exported GIT_DIR (tests run from a git hook) names a repository
//     whatever the working directory is. Every temp directory is then "inside
//     a repo", and `git init <dir>` and `git -C <dir> commit` act on that
//     repository. The repoVars are unset, not emptied: an empty GIT_DIR is a
//     fatal error to git.
//   - a TMPDIR or GOTMPDIR inside a checkout puts every temp directory inside
//     that checkout, which git discovers by walking up with no variable set.
//     GIT_CEILING_DIRECTORIES stops that walk at the temp roots (tempRoots),
//     so a directory below one is in a repository only when a test ran
//     `git init` there or in a parent it made itself.
//
// It also keeps git off the network where it can (#426): networkGuards sets
// GIT_SSH_COMMAND=false, so a fixture remote with an ssh URL (git@host:...,
// ssh://) fails at once instead of connecting with the developer's keys, and
// GIT_TERMINAL_PROMPT=0, so git never waits on the terminal for credentials.
// That is all it blocks. An https remote is still fetched, and a credential
// helper configured in the developer's git config still runs for it; a
// git:// remote still connects; url.insteadOf in that config can still turn
// an ssh URL into an https one. A test that names a remote it must not reach
// gives it a URL git cannot fetch over those either (a path that does not
// exist, or a host under .invalid).
//
// The environment is changed with os.Setenv and os.Unsetenv and never
// restored: the process is a test binary. A test that sets one of the
// variables on purpose with t.Setenv has it for its own duration, and
// t.Setenv's cleanup puts back what it found, which is the variable unset. A
// test that moves TMPDIR or GOTMPDIR has moved the temp roots, and calls
// IsolateForTest again after it for the ceilings to follow; it registers the
// restore of GIT_CEILING_DIRECTORIES itself, with t.Setenv, first.
func IsolateForTest() {
	for _, name := range repoVars {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	if err := os.Setenv(ceilingVar, strings.Join(tempRoots(), string(os.PathListSeparator))); err != nil {
		panic(err)
	}
	for name, value := range networkGuards {
		if err := os.Setenv(name, value); err != nil {
			panic(err)
		}
	}
}

// tempRoots returns the directories a test's temp directories are made in:
// os.TempDir() (TMPDIR), where os.MkdirTemp("", ...) puts them, and GOTMPDIR
// when it is set, where t.TempDir() puts them instead. Each is given as an
// absolute path and, where that differs, as the path its symlinks resolve
// to: git compares a ceiling with the working directory it resolved, and
// macOS's default TMPDIR sits behind a symlink.
func tempRoots() []string {
	roots := []string{os.TempDir()}
	if dir := os.Getenv("GOTMPDIR"); dir != "" {
		roots = append(roots, dir)
	}
	var out []string
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			panic(err)
		}
		out = append(out, abs)
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			out = append(out, resolved)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
