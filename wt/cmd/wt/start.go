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

// waitPendingRoutes is a test seam over lifecycle.WaitPendingRoutes: the route
// hook's proxy restart runs asynchronously, and a successful start hands
// straight off to an agent that dials the model THROUGH the LiteLLM proxy.
// Returning before the restart has finished lets the agent hit a refused
// connection or the pre-restart route table, in which the model just started
// is not listed yet. Tests stub it to observe that ordering without a proxy.
var waitPendingRoutes = lifecycle.WaitPendingRoutes

// ensureModelRoute is a test seam over lifecycle.EnsureModelRoute. Production
// rewrites config.yaml and restarts the LiteLLM proxy; TestMain stubs it so no
// test touches the developer's real proxy.
var ensureModelRoute = lifecycle.EnsureModelRoute

// routeWaitInterval is how often the route wait's line is repeated, so a wait
// that runs for the better part of a minute keeps showing signs of life. Same
// job as startProgressInterval, for the one wait that has no engine stage to
// report.
var routeWaitInterval = 10 * time.Second

// ensureRouteBeforeLaunch makes sure the model about to be launched has its
// LiteLLM route, then waits for the proxy to carry it (#192). A model wt did not
// start — an omlx or mtplx server started by hand, an ollama model pulled
// since the last sync — has no route until something writes one; without this
// the launch reaches the proxy and gets "Invalid model name". It only ever
// adds that one route: unlike the start hook it removes nothing, so a running
// sibling's route survives. A registry cloud model gets the same repair: sync
// is what routes those, but a config.yaml that lost its cloud rows would
// otherwise fail every cloud launch until someone ran `wt litellm sync`. It
// costs one read of config.yaml when the route is already there, and never
// fails the launch. A local m must be a model the probe reported running:
// this never starts one.
//
// A write means the proxy is restarting, and the wait for it is announced and
// kept alive rather than run in silence (see waitForProxyRestart). The check's
// own return value says whether there is such a wait: restartIfChanged bounces
// the proxy if and only if config.yaml changed. That is also what keeps the
// common case quiet — a route already in place prints nothing at all.
func ensureRouteBeforeLaunch(cfg *config.Config, m config.Model) {
	// The wait is called on both paths: every path about to use the proxy
	// settles the route hook's restart first, and with nothing pending — the
	// unchanged case, and the only case that reaches here without a bounce —
	// it returns at once.
	if !ensureModelRoute(cfg, m) {
		waitPendingRoutes()
		return
	}
	waitForProxyRestart()
}

// waitForProxyRestart blocks until the route hook's asynchronous proxy restart
// has finished, printing a line first and repeating it with elapsed time. The
// restart plus the readiness poll behind it runs 10–20s, and the only thing on
// stderr before it is the route check's one-line "updated" notice — so without
// this the user sees wt stop moving and cannot tell that from a hang. It is the
// CLI's counterpart to the TUI picker's route screen (#192 review).
func waitForProxyRestart() {
	// The seams are read HERE, and the ticker goroutine is waited for before
	// returning: it must not outlive this call, or reading the package
	// variables from it would race with a later test's Cleanup restoring them
	// (the same reason startProgress captures them).
	w := osStderr
	interval := routeWaitInterval
	began := time.Now()
	line := func() {
		fmt.Fprintf(w, "wt: waiting for the LiteLLM proxy to pick up the route (%s)\n",
			time.Since(began).Round(time.Second))
	}
	line()
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				line()
			}
		}
	}()
	waitPendingRoutes()
	close(done)
	<-exited
}

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

	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID}
	opts := lifecycle.Options{Progress: report}

	err := lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		// The agent launch follows immediately and routes through LiteLLM:
		// settle the route hook's async proxy restart first.
		waitPendingRoutes()
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
		counts := sessionCounts(occ.IDs())
		parts := make([]string, len(occ.Occupants))
		for i, oid := range occ.IDs() {
			parts[i] = oid
			if n := counts[oid]; n > 0 {
				parts[i] = fmt.Sprintf("%s (in use by %d wt session(s))", oid, n)
			}
		}
		names := strings.Join(parts, ", ")
		if !allowReplace {
			ok, perr := ask(fmt.Sprintf("starting %s will stop %s; continue?", id, names))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("cancelled — %s still running", names)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing %s\n", names)
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
		// Same as the first attempt: the proxy must carry the new route (and
		// have dropped the replaced occupant's) before the agent launches.
		waitPendingRoutes()
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
