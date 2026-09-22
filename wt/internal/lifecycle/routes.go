package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Seams: production talks to the real config.yaml, proxy and stderr.
var (
	applyRoutes            = litellm.Apply
	restartProxy           = litellm.RestartContext
	waitProxy              = litellm.WaitReady
	probeProxy             = litellm.Listening
	routesWarn   io.Writer = os.Stderr
)

// routeWG tracks the in-flight async proxy restarts applyAndReport started.
var routeWG sync.WaitGroup

// WaitPendingRoutes blocks until every async proxy restart started by
// applyAndReport has finished. It has two distinct callers:
//
//   - Every wt command that can reach a route hook (start, stop, smoke, a
//     launch's exit-flow stop picker, the TUI start flow) must call it before
//     the process exits — goroutines do not survive main returning, so a
//     restart wt kicked off would be killed mid-flight and the proxy would
//     never pick up the route change.
//   - Every path that is about to USE the proxy must call it before handing
//     off: an agent launched after a local-model start talks to that model
//     THROUGH LiteLLM, so proceeding while the restart is still in flight can
//     meet a refused connection or the pre-restart route table (the model just
//     started not yet listed). The launch paths — cmd/wt's startForLaunch, the
//     TUI start flow before proceedToLaunch, wt smoke before its one-shot
//     prompt — therefore wait here rather than only at process exit.
func WaitPendingRoutes() {
	routeWG.Wait()
}

const (
	// proxyReadyTimeout bounds the wait for the proxy after a route change.
	proxyReadyTimeout = 30 * time.Second
	// settleTimeout bounds the SYNCHRONOUS half of bounceRoutes: the
	// config.yaml write, whose lock wait takes the context bounceRoutes hands
	// to litellm.Options.Ctx. Without it a contended flock (another wt, or
	// modelman, holding <config>.lock) blocks this cleanup path forever.
	// It deliberately does NOT bound the proxy restart: applyAndReport's
	// goroutine derives context.WithoutCancel(ctx) for itself, which strips
	// this deadline along with cancellation, so bounceRoutes' defer cancel()
	// cannot cut the restart short.
	settleTimeout = 15 * time.Second
	// proxyAliveTimeout bounds the single probe taken just before a restart.
	// Short on purpose: it is paid on every restart. It only has to
	// tell "nothing there" (refused: no wait) from "something there" (any
	// other outcome, even a timeout: wait for it after the restart).
	proxyAliveTimeout = 1500 * time.Millisecond
)

// restartMode says who bounces the proxy after a route write.
type restartMode int

const (
	restartIfChanged restartMode = iota // bounce (and wait) only when the write changed the file
	restartDeferred                     // write only; the caller owes the bounce
	restartForced                       // bounce (and wait) even if this write changed nothing
)

// routeAfterStart adds the started model's LiteLLM route. A single-model
// provider (omlx, mtplx) can serve only one model, so starting one replaced
// whatever ran before: its siblings' routes are removed in the same write.
// restartOwed means an earlier deferred write (the replaced occupant's route
// removal) has not been followed by a restart yet; this call's bounce settles
// it, so a replace costs one proxy restart, not two.
// It never fails the caller — a missing route is a warning, not a failed start.
func routeAfterStart(ctx context.Context, cfg *config.Config, t Target, restartOwed bool) {
	mode := restartIfChanged
	if restartOwed {
		mode = restartForced
	}
	m, ok := litellm.ModelFor(cfg, t.ProviderID, t.ModelName)
	if !ok {
		fmt.Fprintf(routesWarn, "wt: %s/%s is not in the registry; no LiteLLM route added (register it with modelman)\n", t.ProviderID, t.ModelName)
		if restartOwed {
			// No route to add, but the occupant's removal is still unsettled.
			bounceRoutes(ctx, cfg)
		}
		return
	}
	var remove []string
	if SingleModel(t.ProviderID) {
		for _, id := range familyModelIDs(cfg, t.ProviderID) {
			if id != m.ID {
				remove = append(remove, id)
			}
		}
	}
	applyAndReport(ctx, cfg, []string{m.ID}, remove, mode)
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every one of its models
// loses its route. modelName may be "" for a provider-wide stop.
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID, modelName string) {
	routeRemove(ctx, cfg, providerID, modelName, restartIfChanged)
}

func routeRemove(ctx context.Context, cfg *config.Config, providerID, modelName string, mode restartMode) bool {
	var remove []string
	switch {
	case SingleModel(providerID):
		remove = familyModelIDs(cfg, providerID)
	default:
		m, ok := litellm.ModelFor(cfg, providerID, modelName)
		if !ok {
			return false
		}
		remove = []string{m.ID}
	}
	return applyAndReport(ctx, cfg, nil, remove, mode)
}

// routeAfterOccupantStopped removes the route of the occupant a replace just
// stopped. It is the env hook defaultEnv wires in (env.go): the start that
// follows may still fail, and a failed Start runs no success hook at all, so
// the removal has to be written at the moment the occupant went down. It only
// writes: the proxy restart is deferred to Start's single settling bounce
// (routeAfterStart on success, bounceRoutes on failure). It reports whether a
// restart is now owed.
func routeAfterOccupantStopped(ctx context.Context, cfg *config.Config, occ localmodels.Entry) bool {
	return routeRemove(ctx, cfg, occ.ProviderID, occ.ModelName, restartDeferred)
}

// bounceRoutes restarts the proxy (and waits for it) to settle deferred route
// writes when the start that followed them failed or found nothing to route.
// It runs on a context detached from ctx, and still needs to even though
// applyAndReport's async restart detaches again for itself: the common trigger
// is the user's Ctrl+C, and applyAndReport's SYNCHRONOUS half — the config.yaml
// write — takes this ctx as litellm.Options.Ctx. An already-cancelled one makes
// WithLock fail instantly on any lock contention, so the route removal this
// exists to settle would never be written at all. Detaching twice is harmless.
//
// The detached context is bounded by settleTimeout, which bounds exactly one
// thing: that synchronous config.yaml write's lock wait. The defer cancel()
// fires as soon as bounceRoutes returns — before the async restart has even
// begun — but the restart does not run on this context: applyAndReport's
// goroutine derives its own context.WithoutCancel(ctx), which strips both the
// cancellation and the deadline. Deleting the wrapper (as the async change
// first did) leaves the lock wait unbounded on this cleanup path.
func bounceRoutes(ctx context.Context, cfg *config.Config) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	applyAndReport(ctx, cfg, nil, nil, restartForced)
}

// familyModelIDs lists the LiteLLM-managed local model ids whose provider
// shares providerID's family (omlx and omlx-6bit are one physical server).
func familyModelIDs(cfg *config.Config, providerID string) []string {
	fam := localmodels.Family(providerID)
	var ids []string
	for _, m := range litellm.LocalModels(cfg) {
		if localmodels.Family(m.ProviderID) == fam {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// applyAndReport writes the route change and reports whether config.yaml
// changed. The write itself is synchronous: it is the source of truth and it
// is fast. The proxy restart and the readiness poll that follows it — the slow
// part, worst case the restart command's own 30s bound plus proxyReadyTimeout
// on top of the pre-probe — run asynchronously, tracked by routeWG /
// WaitPendingRoutes, so control returns to the caller as soon as the write is
// done rather than at the end of the LiteLLM proxy machinery that is off the
// model-serving path.
//
// This is not a wall-clock saving for a plain `wt start`/`wt stop`: main()
// calls WaitPendingRoutes() before the process exits, so those commands still
// pay the full restart. What it buys is that the work between the route write
// and that exit — the engine's own return, progress output, the stop picker,
// the survey — no longer sits behind the proxy, and that each caller decides
// for itself where it needs the proxy ready: the launch paths wait explicitly
// (see WaitPendingRoutes) because the agent they hand off to dials the model
// through the proxy.
func applyAndReport(ctx context.Context, cfg *config.Config, add, remove []string, mode restartMode) bool {
	res, err := applyRoutes(cfg, add, remove, litellm.Options{
		SkipReadyGate: true,
		// The restart is always deferred here: applyAndReport itself decides
		// whether to restart, and runs that restart asynchronously, below.
		// ForceRestart is deliberately not passed — Apply restarts on it even
		// with NoRestart set, which would put the bounce back on this path.
		NoRestart: true,
		// The caller's ctx bounds the config.yaml lock wait too, so a
		// contended lock cannot outlive a caller that set a deadline —
		// bounceRoutes passes a settleTimeout-bounded context for exactly
		// this reason.
		Ctx: ctx,
	})
	if err != nil {
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			fmt.Fprintf(routesWarn, "wt: LiteLLM route not updated: %v\n", err)
		}
		return false
	}
	for _, o := range res.Outcomes {
		if o.Err != nil {
			fmt.Fprintf(routesWarn, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		}
	}
	// res.Warnings is only ever populated by Apply's restart hook, which
	// NoRestart disables; the restart's own warnings are reported below.
	restart := (res.Changed && mode != restartDeferred) || mode == restartForced
	if !restart {
		return res.Changed
	}
	url := cfg.LitellmBaseURL()
	routeWG.Add(1)
	go func() {
		defer routeWG.Done()
		// Detached from ctx: every real caller cancels its own ctx via a
		// scoped defer cancel() the instant its (now async) call returns —
		// which happens before this restart even starts. A caller's normal
		// completion is not a signal to abandon the restart; only litellm's
		// own per-call timeouts (RestartContext's ~30s, Listening's and
		// WaitReady's own bounds) need to cap this goroutine's lifetime.
		// WaitPendingRoutes() is where a caller (a process about to exit, or
		// a launch path about to use the proxy) rejoins this goroutine.
		//
		// A context.WithTimeout child of ctx would not do: its defer cancel()
		// fires the moment this goroutine returns, and it would still inherit
		// the caller's post-return cancellation regardless.
		//
		// The proxy is probed once, lazily, just before the restart
		// (Listening: only a refused connection counts as down, so a slow
		// proxy is still waited for). Waiting for readiness afterwards is
		// only meaningful when a proxy was actually serving: a configured URL
		// with nothing listening (routing may even be switched off) used to
		// cost every start and stop the full proxyReadyTimeout. Writes that
		// never restart (deferred, or nothing changed) never reach this
		// goroutine, so they pay no probe at all. The restart itself stays
		// best-effort either way.
		bgCtx := context.WithoutCancel(ctx)
		wasUp := url != "" && probeProxy(bgCtx, url, proxyAliveTimeout)
		// Warnings are always surfaced: bgCtx cannot be cancelled by the
		// caller, so there is no longer a "the user pressed Ctrl+C, a failed
		// restart is expected" case to stay quiet about — suppressing them on
		// that theory is exactly what hid this path failing silently.
		for _, w := range restartProxy(bgCtx) {
			fmt.Fprintf(routesWarn, "wt: %s\n", w)
		}
		if wasUp {
			if err := waitProxy(bgCtx, url, proxyReadyTimeout); err != nil {
				fmt.Fprintf(routesWarn, "wt: %v\n", err)
			}
		}
	}()
	return res.Changed
}
