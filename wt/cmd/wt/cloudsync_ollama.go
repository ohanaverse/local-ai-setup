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

// realOllamaCLI pins the CLI to origin with OLLAMA_HOST, as `wt stop` does
// (internal/lifecycle/ollama.go): the registry's ollama provider row says
// which daemon wt manages, and an OLLAMA_HOST inherited from the shell that
// pointed elsewhere would pull into, or remove from, a daemon the registry
// does not describe.
func realOllamaCLI(ctx context.Context, origin string, args ...string) (stdout, stderr string, err error) {
	bin, err := exec.LookPath("ollama")
	if err != nil {
		return "", "", errors.New("the ollama command is not installed (not on PATH)")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+origin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// ollamaFailure words a failed ollama command: what it printed on stderr,
// else why it could not run.
func ollamaFailure(stderr string, err error) string {
	if msg := strings.TrimSpace(stderr); msg != "" {
		return msg
	}
	return err.Error()
}

// ollamaTags is `ollama list`'s NAME column: every pulled tag, cloud stubs
// included. (wt's inventory probe cannot serve here: it leaves cloud stubs
// out on purpose, since they are not local models.)
func ollamaTags(ctx context.Context, origin string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ollamaListTimeout)
	defer cancel()
	stdout, stderr, err := ollamaCLI(ctx, origin, "list")
	if err != nil {
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

// ollamaRemove is `ollama rm`, reading "not found" as already done: a tag
// that vanished since `ollama list` (another `ollama rm`, ollama pruning a
// retired stub) is gone, which is what was wanted.
func ollamaRemove(ctx context.Context, origin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaRmTimeout)
	defer cancel()
	_, stderr, err := ollamaCLI(ctx, origin, "rm", tag)
	if err != nil && !strings.Contains(strings.ToLower(stderr), "not found") {
		return fmt.Errorf("`ollama rm %s` failed: %s", tag, ollamaFailure(stderr, err))
	}
	return nil
}

func ollamaPull(ctx context.Context, origin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, ollamaPullTimeout)
	defer cancel()
	if _, stderr, err := ollamaCLI(ctx, origin, "pull", tag); err != nil {
		return fmt.Errorf("`ollama pull %s` failed: %s", tag, ollamaFailure(stderr, err))
	}
	return nil
}
