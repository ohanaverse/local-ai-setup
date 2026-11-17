package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startSignalCtx is a test seam over signal.NotifyContext: a test needs a
// cancellable context without installing a real signal handler.
var startSignalCtx = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// startProgressInterval is how often the current stage is repeated while the
// engine works, so a multi-minute warmup keeps showing signs of life.
var startProgressInterval = 10 * time.Second

// confirmReplace is a test seam over the y/N prompt on /dev/tty.
var confirmReplace = promptReplace

// waitPendingRoutes is a test seam over lifecycle.WaitPendingRoutes: the route
// hook's proxy restart runs asynchronously, and a launch hands straight off to
// an agent that dials the model THROUGH the LiteLLM proxy. Returning before
// the restart has finished lets the agent hit a refused connection or the
// pre-restart route table, in which the model is not listed yet. It is the
// wait of `wt stop <provider>|--all` between halts (the one host path that
// waits on it). Tests stub it to observe that ordering without a proxy.
var waitPendingRoutes = lifecycle.WaitPendingRoutes

// osStderr is the progress stream, a seam so tests can capture it.
var osStderr io.Writer = os.Stderr

// promptReplace asks a yes/no question on the controlling terminal and
// defaults to No. It writes to and reads from /dev/tty rather than stdout and
// stdin: the non-TUI path routinely runs with a piped stdin, and a piped
// answer must never be able to authorise stopping a running model.
func promptReplace(question string) (bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, errNoTerminal
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

// askYesNo reads one line from r and applies the confirm rule: only an
// explicit y/yes consents; anything else — empty line, other text, EOF — is
// a refusal. It is the parse half of promptReplace, split out so tests can
// drive it without a terminal.
func askYesNo(r io.Reader) (bool, error) {
	return askYesNoDefault(r, false)
}

// askReplace resolves a replacement question: with a TTY it asks on
// /dev/tty; without one it refuses and names the flag that would opt in, so a
// scripted launch tells the operator what to add instead of hanging or
// guessing.
func askReplace(question string) (bool, error) {
	if !stdinTTY() {
		return false, errNoTerminal
	}
	return confirmReplace(question)
}
