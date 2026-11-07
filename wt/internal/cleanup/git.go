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
	// fsmonitor daemon). Env is added to the child's environment.
	Config []string
	Env    []string
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
