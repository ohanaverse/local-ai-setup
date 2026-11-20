package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
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
// pre-restart route table, in which the model is not listed yet. Nothing in
// this binary calls it since `wt stop <provider>|--all` moved to wt-model
// (which has its own waitPendingRoutes seam).
var waitPendingRoutes = lifecycle.WaitPendingRoutes

// osStderr is the progress stream, a seam so tests can capture it.
var osStderr io.Writer = os.Stderr

// promptReplace asks a yes/no question on the controlling terminal and
// defaults to No. It writes to and reads from /dev/tty rather than stdout and
// stdin: the non-TUI path routinely runs with a piped stdin, and a piped
// answer must never be able to authorise stopping a running model.
func promptReplace(question string) (bool, error) {
	return cli.AskOnTTY(func() (*os.File, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }, question, cli.ErrNoTerminal)
}

// askYesNo reads one line from r and applies the confirm rule: only an
// explicit y/yes consents; anything else — empty line, other text, EOF — is
// a refusal. It is the parse half of promptReplace, split out so tests can
// drive it without a terminal.
func askYesNo(r io.Reader) (bool, error) {
	return cli.ReadYesNo(r, false)
}

// askReplace resolves a replacement question: with a TTY it asks on
// /dev/tty; without one it refuses and names the flag that would opt in, so a
// scripted launch tells the operator what to add instead of hanging or
// guessing.
func askReplace(question string) (bool, error) {
	if !stdinTTY() {
		return false, cli.ErrNoTerminal
	}
	return confirmReplace(question)
}
