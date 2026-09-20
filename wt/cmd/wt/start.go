package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startModel is a test seam: production runs the non-TUI start driver. Tests
// stub it so no test starts a real model process.
var startModel = startForLaunch

// lifecycleStart is a test seam over the engine, so the driver's two-phase
// replace dance can be exercised without touching a real provider.
var lifecycleStart = lifecycle.Start

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

// osStderr is the progress stream, a seam so tests can capture it.
var osStderr io.Writer = os.Stderr

// promptReplace asks a yes/no question on the controlling terminal and
// defaults to No. It writes to and reads from /dev/tty rather than stdout and
// stdin: the non-TUI path routinely runs with a piped stdin, and a piped
// answer must never be able to authorise stopping a running model.
func promptReplace(question string) (bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("%s — rerun with --replace to confirm", question)
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
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// startProgress returns the engine's Progress callback: one timestamped stderr
// line per stage change, plus a repeat of the current stage every
// startProgressInterval until done is closed. began is threaded in so a
// retried start keeps reporting total elapsed time rather than restarting the
// clock.
func startProgress(id string, done <-chan struct{}, began time.Time) func(lifecycle.Stage) {
	// Capture the seams as locals: the heartbeat goroutine outlives
	// startForLaunch, so reading the package variables from the goroutine
	// would race with a later test's Cleanup restoring them.
	w := osStderr
	interval := startProgressInterval
	var mu sync.Mutex
	stage := lifecycle.Stage("")
	emit := func() {
		mu.Lock()
		s := stage
		mu.Unlock()
		if s == "" {
			return
		}
		fmt.Fprintf(w, "wt: starting %s — %s (%s)\n", id, lifecycle.StageLabel(s), time.Since(began).Round(time.Second))
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				emit()
			}
		}
	}()
	return func(s lifecycle.Stage) {
		mu.Lock()
		stage = s
		mu.Unlock()
		emit()
	}
}

// startForLaunch starts the local model behind a -M pin on the non-TUI path.
// It reports progress on stderr and cancels on Ctrl+C; a second Ctrl+C exits
// the process (the signal handler is restored once the first cancels, so a
// hung teardown cannot trap the user).
//
// It never asks for permission to stop an occupant unless the pin needs it:
// only an OccupiedError or an OccupancyUnknownError reaches the confirm path,
// and only on the first attempt. Any other failure is returned as-is.
func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error {
	ctx, stop := startSignalCtx()
	defer stop()
	go func() {
		<-ctx.Done()
		// Restore the default handler so a second Ctrl+C kills wt.
		stop()
	}()

	id := row.Model.ID
	done := make(chan struct{})
	defer close(done)
	began := time.Now()
	report := startProgress(id, done, began)

	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName}
	opts := lifecycle.Options{Progress: report}

	err := lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled before %s started: %w", id, err)
	}

	// ask races the prompt against ctx: the read on /dev/tty cannot be
	// interrupted, and the signal handler swallows Ctrl+C, so without the race
	// Ctrl+C at the y/N prompt would only cancel ctx and leave the read
	// blocked until Enter. The abandoned read goroutine dies with the process.
	ask := func(question string) (bool, error) {
		type answer struct {
			ok  bool
			err error
		}
		ch := make(chan answer, 1)
		go func() {
			ok, err := askReplace(question)
			ch <- answer{ok, err}
		}()
		select {
		case a := <-ch:
			return a.ok, a.err
		case <-ctx.Done():
			return false, fmt.Errorf("cancelled before %s started: %w", id, ctx.Err())
		}
	}

	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	switch {
	case errors.As(err, &occ):
		if !allowReplace {
			ok, perr := ask(fmt.Sprintf("%s is running; stop it and start %s?", occ.Occupant.ModelID, id))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("cancelled — %s is still running", occ.Occupant.ModelID)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing %s\n", occ.Occupant.ModelID)
	case errors.As(err, &unk):
		if !allowReplace {
			ok, perr := ask(fmt.Sprintf("cannot tell whether %s at %s is already serving a model; replace it with %s?", unk.ProviderID, unk.Origin, id))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("cancelled — %s may still be serving a model", unk.ProviderID)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing whatever %s is serving at %s\n", unk.ProviderID, unk.Origin)
	default:
		return startFailure(id, err)
	}

	opts.AllowReplace = true
	err = lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled before %s started: %w", id, err)
	}
	// Replace was already granted; anything that fails now is a different
	// failure — report it rather than prompting again.
	return startFailure(id, err)
}

// startError carries the shared one-line wording while keeping the engine's
// typed error on the chain, so callers can still errors.As it.
type startError struct {
	msg string
	err error
}

func (e *startError) Error() string { return e.msg }
func (e *startError) Unwrap() error { return e.err }

// startFailure words a failed start the way the TUI does.
func startFailure(id string, err error) error {
	return &startError{msg: lifecycle.StartErrorMessage(id, err), err: err}
}

// askReplace resolves a replacement question: with a TTY it asks on
// /dev/tty; without one it refuses and names the flag that would opt in, so a
// scripted launch tells the operator what to add instead of hanging or
// guessing.
func askReplace(question string) (bool, error) {
	if !stdinTTY() {
		return false, fmt.Errorf("%s — rerun with --replace to confirm", question)
	}
	return confirmReplace(question)
}
