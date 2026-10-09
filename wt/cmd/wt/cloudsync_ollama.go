// The ollama CLI as wt cloud-sync's catalog flow uses it: list what is
// pulled, pull a tag, remove a tag. Always the CLI, never the HTTP API, and
// always pinned to the daemon the registry's ollama provider row names.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ollamaCLI runs the ollama command against the daemon at origin and returns
// what it printed. A seam: cmd/wt's TestMain makes it fail, so no test runs
// the developer's ollama.
var ollamaCLI = realOllamaCLI

// How long one ollama command may take. A cloud model's pull fetches a
// manifest, not weights.
const (
	ollamaListTimeout = 30 * time.Second
	ollamaRmTimeout   = 60 * time.Second
	ollamaPullTimeout = 10 * time.Minute
)

// ollamaWaitDelay bounds what a time limit leaves open. The limit kills the
// command wt started; if that was a wrapper script, the process it started in
// turn lives on holding the output pipes, and without this Run would wait
// for it to exit, however long that takes.
var ollamaWaitDelay = 5 * time.Second

// errOllamaNotInstalled is realOllamaCLI's answer when there is no ollama
// command to run, which is not a daemon that is down.
var errOllamaNotInstalled = errors.New("the ollama command is not installed (not on PATH)")

// realOllamaCLI pins the CLI to origin with OLLAMA_HOST, as `wt stop` does
// (internal/lifecycle/ollama.go): the registry's ollama provider row says
// which daemon wt manages, and an OLLAMA_HOST inherited from the shell that
// pointed elsewhere would pull into, or remove from, a daemon the registry
// does not describe.
func realOllamaCLI(ctx context.Context, origin string, args ...string) (stdout, stderr string, err error) {
	bin, err := exec.LookPath("ollama")
	if err != nil {
		return "", "", errOllamaNotInstalled
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = ollamaWaitDelay
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+origin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// ansiEscape matches a terminal control sequence. `ollama pull` writes its
// progress to stderr with cursor escapes in it.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// ollamaFailure words a failed ollama command in one line: the last line it
// printed on stderr that says anything (ollama prints its `Error:` line
// last, after any progress), with terminal escapes removed, else why it
// could not run. One line, because the caller prints it after a `catalog:`
// prefix and a second line would have none.
func ollamaFailure(stderr string, err error) string {
	lines := strings.FieldsFunc(ansiEscape.ReplaceAllString(stderr, ""), func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		clean := strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return -1
			}
			return r
		}, lines[i]))
		if clean != "" {
			return clean
		}
	}
	return err.Error()
}

// ollamaTimedOut words a command that wt's own time limit stopped, or
// returns "" when that is not why it failed. Asked before stderr is read:
// the killed process reports "signal: killed", or the progress it had
// printed, and neither says that the limit was wt's.
func ollamaTimedOut(ctx context.Context, limit time.Duration) string {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ""
	}
	d := limit.String()
	if strings.HasSuffix(d, "m0s") {
		d = strings.TrimSuffix(d, "0s")
	}
	return "timed out after " + d + " (wt's own limit)"
}

// ollamaTags is `ollama list`'s NAME column: every pulled tag, cloud stubs
// included. (wt's inventory probe cannot serve here: it leaves cloud stubs
// out on purpose, since they are not local models.)
func ollamaTags(ctx context.Context, origin string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ollamaListTimeout)
	defer cancel()
	stdout, stderr, err := ollamaCLI(ctx, origin, "list")
	if err != nil {
		if why := ollamaTimedOut(ctx, ollamaListTimeout); why != "" {
			return nil, errors.New(why)
		}
		if errors.Is(err, errOllamaNotInstalled) {
			return nil, err
		}
		return nil, errors.New(ollamaFailure(stderr, err))
	}
	var tags []string
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "NAME" {
			continue
		}
		tags = append(tags, fields[0])
	}
	return tags, nil
}

// ollamaRemove is `ollama rm`, reading ollama's "model '<tag>' not found" as
// already done: a tag that vanished since `ollama list` (another `ollama rm`,
// ollama pruning a retired stub) is gone, which is what was wanted. The
// answer must name the tag: some other failure with "not found" in it (a 404
// from something that is not ollama) is not a removal.
func ollamaRemove(ctx context.Context, origin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaRmTimeout)
	defer cancel()
	_, stderr, err := ollamaCLI(ctx, origin, "rm", tag)
	if err == nil {
		return nil
	}
	if why := ollamaTimedOut(ctx, ollamaRmTimeout); why != "" {
		return fmt.Errorf("`ollama rm %s` %s", tag, why)
	}
	if low := strings.ToLower(stderr); strings.Contains(low, "not found") && strings.Contains(low, strings.ToLower(tag)) {
		return nil
	}
	return fmt.Errorf("`ollama rm %s` failed: %s", tag, ollamaFailure(stderr, err))
}

func ollamaPull(ctx context.Context, origin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaPullTimeout)
	defer cancel()
	if _, stderr, err := ollamaCLI(ctx, origin, "pull", tag); err != nil {
		if why := ollamaTimedOut(ctx, ollamaPullTimeout); why != "" {
			return fmt.Errorf("`ollama pull %s` %s", tag, why)
		}
		return fmt.Errorf("`ollama pull %s` failed: %s", tag, ollamaFailure(stderr, err))
	}
	return nil
}
