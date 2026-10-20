package main

import (
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
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
)

// sessionCounts is the number of live wt sessions using each model id. A test
// seam; production sweeps dead sessions first, as the stop picker does
// (refcount.SessionCounts, which the picker's replace dialog reads too).
var sessionCounts = refcount.SessionCounts

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
// hook's proxy restart runs asynchronously, and a launch hands straight off to
// an agent that dials the model THROUGH the LiteLLM proxy. Returning before
// the restart has finished lets the agent hit a refused connection or the
// pre-restart route table, in which the model is not listed yet. It is the
// wait of every path but a start's own, which is lifecycleSettleStart's.
// Tests stub it to observe that ordering without a proxy.
var waitPendingRoutes = lifecycle.WaitPendingRoutes

// lifecycleSettleStart is a test seam over lifecycle.SettleStart: what a start
// the engine reported done is owed before anyone is told it is running — the
// wait for the route hook's proxy restart, one probe of the provider's server
// once that wait is over, since the wait is where another terminal's `wt
// stop` lands (#343), and for a server that is gone the wait for the restart
// its route removal starts. TestMain stubs it so no test probes a real
// provider.
var lifecycleSettleStart = lifecycle.SettleStart

// settleStart is what follows a start the engine reported done: that one
// call, with what it prints left on stderr. A server that is gone is a failed
// start, worded by startFailure. ctx is handed over as it is — Ctrl+C in the
// proxy wait leaves it done, and lifecycle.SettleStart is what keeps the
// check from being cut short by that.
func settleStart(ctx context.Context, cfg *config.Config, id string, t lifecycle.Target) error {
	if err := lifecycleSettleStart(ctx, nil, cfg, t); err != nil {
		return startFailure(id, err)
	}
	return nil
}

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
// restart plus the readiness poll behind it runs 10–20s with an idle proxy, and
// up to three minutes more while the proxy finishes a request in flight
// (litellm.RestartContext), and the only thing on
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
	return askYesNoDefault(r, false)
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

// startForLaunch starts the local model behind a -M pin on the non-TUI path;
// it is also the driver of `wt start` and `wt smoke` (startModel). It reports
// progress on stderr and cancels on Ctrl+C; a second Ctrl+C exits the process
// (the signal handler is restored once the first cancels, so a hung teardown
// cannot trap the user).
//
// It never asks for permission to stop an occupant unless the pin needs it:
// only an OccupiedError or an OccupancyUnknownError reaches the confirm path,
// and each question is asked at most once. Any other failure is returned
// as-is. Other providers' models loaded beside the target (#412) are the
// others question: answered by --others (othersMode), else asked on the
// terminal, else, without a TTY, kept with one warning line (decideOthers).
// --replace answers only the same-family question.
//
// A Ctrl+C that came too late to stop the start is not an error here: the
// model is running, which is what `wt start` reports.
func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error {
	_, _, err := driveStart(cfg, row, allowReplace, false)
	return err
}

// startForLaunchOwed is startForLaunch for a caller that already owes a proxy
// restart: a slot-profile launch that wrote a cloud slot's route before
// starting its local model. restartOwed goes to every engine attempt as
// lifecycle.Options.RestartOwed, so the start's own restart publishes that
// route too.
//
// stillOwed is true only when restartOwed was true and the function returns
// after a refusal it did not retry: the y/N or the others prompt was
// declined, failed, or was interrupted. The engine touched nothing then, so the caller settles the
// restart (lifecycle.SettleRoutes). On every other return the engine has
// started the one restart, or nothing was owed.
//
// Its caller launches an agent next, so a Ctrl+C that lost to the start (at
// the routing stage, or as the load finished) is an error here, in the TUI's
// words for the same key: "started <id>; launch cancelled". The model stays
// running and its start is settled first.
func startForLaunchOwed(cfg *config.Config, row catalog.Row, allowReplace, restartOwed bool) (stillOwed bool, err error) {
	stillOwed, cancelled, err := driveStart(cfg, row, allowReplace, restartOwed)
	if err == nil && cancelled {
		return false, fmt.Errorf("started %s; launch cancelled", row.Model.ID)
	}
	return stillOwed, err
}

// driveStart is the driver behind startForLaunch and startForLaunchOwed.
// cancelled is meaningful only beside a nil error: the start succeeded and was
// settled, and Ctrl+C had arrived by then. The engine cannot report that (it
// returns nil once the model is up) and the settle drops cancellation on
// purpose, so the signal context is the only record of the key.
func driveStart(cfg *config.Config, row catalog.Row, allowReplace, restartOwed bool) (stillOwed, cancelled bool, err error) {
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
	// replacing is the "replacing whatever <provider> is serving" line of a
	// confirmed unknown occupancy. It prints on the engine's first progress
	// stage, which the engine reports only once it is past every refusal and
	// about to act: a refusal about other providers' models still follows
	// the confirmation, and a cancel there replaces nothing.
	var replacingMu sync.Mutex
	replacing := ""
	progress := func(s lifecycle.Stage) {
		replacingMu.Lock()
		if replacing != "" {
			fmt.Fprint(osStderr, replacing)
			replacing = ""
		}
		replacingMu.Unlock()
		report(s)
	}
	opts := lifecycle.Options{Progress: progress, RestartOwed: restartOwed}
	// --others=keep answers the others question before it is asked, so the
	// engine never refuses for them. Every other mode starts undecided: the
	// refusal lists what to ask about, warn about, or stop.
	if othersMode == "keep" {
		opts.Others = lifecycle.OthersAnswer{Decided: true}
	}

	// settled ends a start the engine reported done. ctx is done only when a
	// signal arrived: the goroutine above calls stop() after ctx.Done(), and
	// the deferred stop() runs after this result is evaluated.
	settled := func(serr error) (bool, bool, error) {
		if serr != nil {
			return false, false, serr
		}
		return false, ctx.Err() != nil, nil
	}

	// race runs a prompt against ctx: the read on /dev/tty cannot be
	// interrupted, and the signal handler swallows Ctrl+C, so without the
	// race Ctrl+C at a prompt would only cancel ctx and leave the read
	// blocked until Enter. The abandoned read goroutine dies with the process.
	race := func(prompt func() (any, error)) (any, error) {
		type answer struct {
			v   any
			err error
		}
		ch := make(chan answer, 1)
		go func() {
			v, aerr := prompt()
			ch <- answer{v, aerr}
		}()
		select {
		case a := <-ch:
			return a.v, a.err
		case <-ctx.Done():
			return nil, fmt.Errorf("cancelled before %s started: %w", id, ctx.Err())
		}
	}
	ask := func(question string) (bool, error) {
		v, err := race(func() (any, error) { return askReplace(question) })
		ok, _ := v.(bool)
		return ok, err
	}
	askOthers := func(question string, canStop bool) (othersChoice, error) {
		v, err := race(func() (any, error) { return chooseOthers(question, canStop) })
		c, _ := v.(othersChoice)
		return c, err
	}

	// Each pass answers at least one question the engine had not been given
	// an answer to (the same-family replace, the others), and a refusal that
	// asks only what was already answered is a failure, so this ends after at
	// most three engine calls. A same-family OccupiedError and the others come
	// in one refusal, answered together with one retry; an
	// OccupancyUnknownError comes alone, before the engine reads the others.
	for {
		err = lifecycleStart(ctx, cfg, target, opts)
		if err == nil {
			// The agent launch follows immediately and routes through
			// LiteLLM: settle the route hook's async proxy restart first (and,
			// after a replace, the drop of the replaced occupant's route).
			return settled(settleStart(ctx, cfg, id, target))
		}
		if errors.Is(err, context.Canceled) {
			return false, false, fmt.Errorf("cancelled before %s started: %w", id, err)
		}

		var occ *lifecycle.OccupiedError
		var unk *lifecycle.OccupancyUnknownError
		replaceAsked := false
		othersAsked := false
		switch {
		case errors.As(err, &occ):
			replaceAsked = len(occ.Occupants) > 0 && !opts.AllowReplace
			othersAsked = (len(occ.Others) > 0 || len(occ.OthersUnknown) > 0) && !opts.Others.Decided
		case errors.As(err, &unk):
			replaceAsked = !opts.AllowReplace
		}
		if !replaceAsked && !othersAsked {
			// Not a refusal, or one that asks only what was already answered:
			// a different failure — report it rather than prompting again.
			// The engine cannot refuse with every question answered today;
			// if it ever does, that refusal touched nothing and did not spend
			// the owed restart, so the caller still settles it.
			return restartOwed && lifecycle.RestartStillOwed(err), false, startFailure(id, err)
		}

		if replaceAsked && unk != nil {
			if !allowReplace {
				ok, perr := ask(fmt.Sprintf("cannot tell whether %s at %s is already serving a model; replace it with %s?", unk.ProviderID, unk.Origin, id))
				if perr != nil {
					return restartOwed, false, perr
				}
				if !ok {
					return restartOwed, false, fmt.Errorf("cancelled — %s may still be serving a model", unk.ProviderID)
				}
			}
			replacingMu.Lock()
			replacing = fmt.Sprintf("wt: replacing whatever %s is serving at %s\n", unk.ProviderID, unk.Origin)
			replacingMu.Unlock()
			opts.AllowReplace = true
			continue
		}

		names := ""
		if replaceAsked {
			counts := sessionCounts(occ.IDs())
			parts := make([]string, len(occ.Occupants))
			for i, oid := range occ.IDs() {
				parts[i] = oid
				if n := counts[oid]; n > 0 {
					parts[i] = fmt.Sprintf("%s (in use by %d wt session(s))", oid, n)
				}
			}
			names = strings.Join(parts, ", ")
			if !allowReplace {
				ok, perr := ask(fmt.Sprintf("starting %s will stop %s; continue?", id, names))
				if perr != nil {
					return restartOwed, false, perr
				}
				if !ok {
					return restartOwed, false, fmt.Errorf("cancelled — %s still running", names)
				}
			}
		}
		if othersAsked {
			answer, aerr := decideOthers(cfg, id, occ, askOthers)
			if aerr != nil {
				return restartOwed, false, aerr
			}
			opts.Others = answer
		}
		if replaceAsked {
			// Said only once both answers are in: a cancel at the others
			// question replaces nothing.
			fmt.Fprintf(osStderr, "wt: replacing %s\n", names)
			opts.AllowReplace = true
		}
	}
}

// decideOthers answers a refusal about other providers' models (#412):
// --others=stop stops every resident wt can stop, unasked; without a TTY and
// without --others the start keeps them and prints one warning line, so a
// script neither hangs nor stops a model; otherwise ask is put the question.
// A cancel, or a prompt that failed, is the error the start returns.
func decideOthers(cfg *config.Config, id string, occ *lifecycle.OccupiedError, ask func(string, bool) (othersChoice, error)) (lifecycle.OthersAnswer, error) {
	switch othersMode {
	case "stop":
		return lifecycle.OthersAnswer{Decided: true, Stop: stoppableIDs(occ.Others)}, nil
	case "keep":
		return lifecycle.OthersAnswer{Decided: true}, nil
	}
	if !stdinTTY() {
		fmt.Fprint(osStderr, othersWarning(cfg, id, occ))
		return lifecycle.OthersAnswer{Decided: true}, nil
	}
	question, canStop := othersQuestion(cfg, id, occ)
	choice, err := ask(question, canStop)
	if err != nil {
		return lifecycle.OthersAnswer{}, err
	}
	switch choice {
	case othersStop:
		return lifecycle.OthersAnswer{Decided: true, Stop: stoppableIDs(occ.Others)}, nil
	case othersKeep:
		return lifecycle.OthersAnswer{Decided: true}, nil
	}
	return lifecycle.OthersAnswer{}, othersCancelled(occ)
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

// asStartError marks err as the outcome of a start the launch attempted —
// failed, refused or cancelled — keeping its text and its chain. The root
// command reads the mark to leave such an error to main's one print
// (printOnce), and `wt smoke` to print no usage after it.
func asStartError(err error) error {
	if isStartError(err) {
		return err
	}
	return &startError{msg: err.Error(), err: err}
}

// isStartError reports whether err is, or wraps, the outcome of a start
// (startFailure, asStartError).
func isStartError(err error) bool {
	var se *startError
	return errors.As(err, &se)
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
