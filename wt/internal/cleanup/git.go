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

	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.Dir}, args...)...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	if len(g.Env) > 0 {
		cmd.Env = append(os.Environ(), g.Env...)
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
