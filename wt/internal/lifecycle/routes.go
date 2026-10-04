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
	applyRoutes            = litellm.ApplyChange
	restartProxy           = litellm.RestartContext
	waitProxy              = litellm.WaitReady
	probeProxy             = litellm.Listening
	routesWarn   io.Writer = os.Stderr
)

// routesOutMu guards routesWarn against the one concurrent pair this package
// has: SetRouteOutput swapping the writer while an async proxy restart
// (bounceProxyAsync) is printing a warning. Every print goes through
// routePrintf, which holds it for the whole write, so a writer handed to
// SetRouteOutput is never written by two goroutines at once and is never
// written again once the restore func has returned.
var routesOutMu sync.Mutex

// routePrintf is the single way this file prints: the "updated" line and every
// route warning. It reads the current writer and writes to it under
// routesOutMu, so a redirect cannot land between the two.
func routePrintf(format string, args ...any) {
	routesOutMu.Lock()
	defer routesOutMu.Unlock()
	fmt.Fprintf(routesWarn, format, args...)
}

// SetRouteOutput sends everything the route hooks print — the "updated" line
// and every warning, including those from the asynchronous proxy restart — to
// w until the returned restore func is called. It exists for a caller that
// owns the terminal: the TUI picker runs under Bubble Tea's alt screen, where
// a line written to stderr is not reliably visible and is gone once the agent
// takes the terminal, so the picker captures this output and prints it itself
// when the terminal is released.
//
// The restart's warnings are printed by a goroutine that outlives the call
// that started it, so a caller that wants them too must call restore only
// after WaitPendingRoutes() has returned; restoring earlier sends them to the
// previous writer. w is written under a mutex, never concurrently, so a plain
// bytes.Buffer will do.
//
// restore puts back the writer that was current when SetRouteOutput was
// called and is safe to call more than once. Redirects therefore do not nest
// freely: two live at once must be restored in reverse order of installation
// (last in, first out), because restoring the earlier one first lets the later
// restore reinstall the earlier caller's writer, which would then be written
// again after its own restore returned. Restored in order — or with only one
// redirect live, which is the case today: the picker is the one caller and it
// cannot nest — w is never written after restore returns and is safe to read
// from then on. A caller that never calls SetRouteOutput sees no change: the
// output goes to stderr.
func SetRouteOutput(w io.Writer) (restore func()) {
	routesOutMu.Lock()
	prev := routesWarn
	routesWarn = w
	routesOutMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			routesOutMu.Lock()
			routesWarn = prev
			routesOutMu.Unlock()
		})
	}
}

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
//     started not yet listed). The launch paths — cmd/wt's startForLaunch and
//     ensureRouteBeforeLaunch, the TUI start flow and the picker's routing
//     phase (checkLaunchRoute) before the launch, wt smoke before its one-shot
//     prompt — therefore wait here rather than only at process exit.
func WaitPendingRoutes() {
	routeWG.Wait()
}

const (
	// proxyReadyTimeout bounds the wait for the proxy after a route change.
	proxyReadyTimeout = 30 * time.Second
	// proxyAliveTimeout bounds the single probe taken just before a restart.
	// Short on purpose: it is paid on every restart. It only has to
	// tell "nothing there" (refused: no wait) from "something there" (any
	// other outcome, even a timeout: wait for it after the restart).
	proxyAliveTimeout = 1500 * time.Millisecond
	// ensureRouteLockTimeout bounds the config.yaml lock wait of a launch-time
	// route check. A launch must not hang behind another wt process holding
	// the lock; giving up only costs the check, and the launch proceeds.
	ensureRouteLockTimeout = 10 * time.Second
)

// restartMode says who bounces the proxy after a route write.
type restartMode int

const (
	restartIfChanged restartMode = iota // bounce (and wait) only when the write changed the file
	restartDeferred                     // write only; the caller owes the bounce
	restartForced                       // bounce (and wait) even if this write changed nothing
)

// routeAfterStart adds the started model's LiteLLM route: its registry
// overlay's id when one matches, else its discovered id (#179 Phase B — the
// id the catalog, -M and usage use). A single-model provider (omlx, mtplx)
// can serve only one model, so starting one replaced whatever ran before:
// every other route of its family — discovered siblings included — is
// removed in the same write. restartOwed means an earlier deferred write (the
// replaced occupant's route removal) has not been followed by a restart yet;
// this call's bounce settles it, so a replace costs one proxy restart, not
// two. It never fails the caller — a missing route is a warning, not a failed
// start.
func routeAfterStart(ctx context.Context, cfg *config.Config, t Target, restartOwed bool) {
	mode := restartIfChanged
	if restartOwed {
		mode = restartForced
	}
	applyAndReport(ctx, cfg, StartRouteChange(cfg, t), mode)
}

// StartRouteChange is the route change routeAfterStart writes for a started
// target: the model to route (registry overlay, else discovered — routeModel
// says how a target's id and name choose it) and, for a single-model
// provider, its family to clear. It is exported so the id the
// start hook writes can be pinned against the id `wt litellm sync` desires
// for the same model (cmd/wt) — if the two derivations drift, every sync
// after a start removes the hook's route and adds its own.
func StartRouteChange(cfg *config.Config, t Target) litellm.Change {
	ch := litellm.Change{Add: []config.Model{routeModel(cfg, t)}}
	if SingleModel(t.ProviderID) {
		ch.RemoveFamilies = []string{localmodels.Family(t.ProviderID)}
	}
	return ch
}

// routeModel is the model a target's route is written for. A target that
// carries its row's id is resolved by that id alone: the registry model with
// exactly that id, else the discovered model. ModelFor's lenient name match is
// skipped on purpose — an artifact whose name merely resembles a registry
// model's ("org/name" beside "name") has its own discovered id, and matching
// it to the registry model would route an id the picker never showed (#195).
// Only a target with no id falls back to ModelFor.
func routeModel(cfg *config.Config, t Target) config.Model {
	if t.ModelID != "" {
		if i := config.IndexModelByID(cfg.Models, t.ModelID); i >= 0 {
			return cfg.Models[i]
		}
		return litellm.DiscoveredModel(t.ProviderID, t.ModelName)
	}
	if m, ok := litellm.ModelFor(cfg, t.ProviderID, t.ModelName); ok {
		return m
	}
	return litellm.DiscoveredModel(t.ProviderID, t.ModelName)
}

// EnsureRoute writes t's LiteLLM route when it is missing (#192). It is for a
// model that is already running but that wt did not start — an omlx or mtplx
// server started by hand, an ollama model pulled since the last sync — which
// has no route until something writes one, so an agent launched on it gets
// "Invalid model name" from the proxy.
//
// It routes the model the start hook would (StartRouteChange's Add), so the
// two cannot name a model's route differently — and it removes nothing. The
// start hook also clears a single-model provider's family, which is safe
// there because Start has just stopped or refused any occupant. The ensure
// has no such guard: an omlx or mtplx server can list sibling variants as
// running together, and sync routes all of them, so a clear here would delete
// a running sibling's route and bounce the proxy on every alternating launch.
// Stale sibling routes are left to the next start, stop or sync.
//
// Unlike Start it never starts or stops anything: a caller holding a stale
// probe gets at worst a route for a model that has since stopped, which the
// next sync or start removes.
//
// It never fails the caller: a failed write is a warning (applyAndReport) and
// a missing config.yaml is silent. It reports whether config.yaml changed, and
// says so in one line when it did. The proxy restart that a change triggers is
// asynchronous — a caller about to use the proxy owes WaitPendingRoutes().
func EnsureRoute(ctx context.Context, cfg *config.Config, t Target) bool {
	return ensureChange(ctx, cfg, litellm.Change{Add: StartRouteChange(cfg, t).Add})
}

// ensureChange is EnsureRoute's body for a change already built: it writes it,
// reports whether config.yaml changed, and says so in one line when it did.
// One change builder serving every launch-time check is what keeps a model from
// being routed under one id by one caller and another id by the next.
func ensureChange(ctx context.Context, cfg *config.Config, ch litellm.Change) bool {
	changed := applyAndReport(ctx, cfg, ch, restartIfChanged)
	if changed {
		routePrintf("wt: LiteLLM route for %s updated\n", ch.Add[0].ID)
	}
	return changed
}

// modelRouteChange is the route change a launch row's model needs, and whether
// the launch-time check applies to it at all: only a local model is wt's to
// route — a cloud route is sync's business, and a model whose location cannot
// be resolved is not one wt can route. The row's id travels with it, so
// StartRouteChange resolves the model by id (see routeModel).
func modelRouteChange(cfg *config.Config, m config.Model) (litellm.Change, bool) {
	if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
		return litellm.Change{}, false
	}
	t := Target{ProviderID: m.ProviderID, ModelName: m.ModelName, ModelID: m.ID}
	return litellm.Change{Add: StartRouteChange(cfg, t).Add}, true
}

// EnsureModelRoute is EnsureRoute for a launch row's model, the form the
// launch paths call. It skips a model that is not local (modelRouteChange) and
// bounds the config.yaml lock wait so a launch cannot hang behind another wt
// process. The caller must only pass a model the probe reported running.
func EnsureModelRoute(cfg *config.Config, m config.Model) bool {
	ch, ok := modelRouteChange(cfg, m)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ensureRouteLockTimeout)
	defer cancel()
	return ensureChange(ctx, cfg, ch)
}

// TryEnsureModelRoute is EnsureModelRoute for a caller that cannot stop what it
// is doing — the TUI picker, whose update goroutine is also the one that
// repaints the screen and answers keys. EnsureModelRoute bounds its lock wait
// at ensureRouteLockTimeout (10s), which is a long time for a goroutine that
// cannot redraw: the picker would freeze, ctrl+c included, and it would do so
// *before* its routing phase had been entered, so the one screen built to cover
// a route wait would be the one thing the wait prevented (#192 review).
//
// So this form never waits. It attempts the config.yaml lock exactly once:
// WithLock runs its first flock before it consults a deadline, and the
// already-cancelled context below is what a contended lock hits, so a free lock
// is still taken and a held one returns at once rather than polling
// (lockPollInterval) up to a deadline.
//
// done reports whether the check finished at all: changed says whether
// config.yaml was written, and is only meaningful when done is true. done ==
// false means the check got nowhere — most often another wt holds the lock
// mid-write, but any failure to read or write config.yaml lands here too — and
// the caller owes a retry through EnsureModelRoute, where waiting is
// affordable, before it uses the proxy. That path prints nothing: the retry
// reports, since a contended lock is not itself a warning.
func TryEnsureModelRoute(cfg *config.Config, m config.Model) (changed, done bool) {
	ch, ok := modelRouteChange(cfg, m)
	if !ok {
		return false, true // not wt's to route: nothing to retry, nothing to say
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed, err := applyReported(ctx, cfg, ch, restartIfChanged)
	if err != nil {
		return false, false
	}
	if changed {
		routePrintf("wt: LiteLLM route for %s updated\n", ch.Add[0].ID)
	}
	return changed, true
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every route of its
// family goes. Stopping an ollama model only unloads it, so its route stays
// (see routeRemove). modelName may be "" for a provider-wide stop.
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID, modelName string) {
	routeRemove(ctx, cfg, providerID, modelName, restartIfChanged)
}

// routeRemove writes a stop's route removal and reports whether config.yaml
// changed. For a family whose routes follow its artifact (ollama) it writes
// nothing and returns false: stopping unloads the model, but a pulled model is
// still served on request, so its route stays (#179).
func routeRemove(ctx context.Context, cfg *config.Config, providerID, modelName string, mode restartMode) bool {
	if localmodels.RoutesFollowArtifact(localmodels.Family(providerID)) {
		return false
	}
	var ch litellm.Change
	switch {
	case SingleModel(providerID):
		ch.RemoveFamilies = []string{localmodels.Family(providerID)}
	default:
		// A multi-tenant backend whose routes do not follow artifacts; none
		// exists today (ollama, the only multi-tenant one, returned above).
		m, ok := litellm.ModelFor(cfg, providerID, modelName)
		if !ok {
			m = litellm.DiscoveredModel(providerID, modelName)
		}
		ch.Remove = []string{m.ID}
	}
	return applyAndReport(ctx, cfg, ch, mode)
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

// bounceRoutes settles route writes a deferred write already made — when the
// start that followed them failed, or when a batch of
// stops wrote its removals and owed the bounce (StopModelDeferred/SettleRoutes).
// It restarts the proxy and waits for it; it writes nothing.
//
// That is deliberate. The removals are already in config.yaml — every caller
// reaches here only after a deferred write succeeded, which is exactly what
// restartOwed means — so re-entering applyAndReport with an empty change would
// buy a second locked read and parse of the file it had just written: the one
// cost the batching change added to a single-model stop, even though it is the
// batch case (N removals, one bounce) that the change exists for (#142). It
// also means the settle
// has no config.yaml write whose lock wait needs bounding: the restart is
// asynchronous and detached (bounceProxyAsync), so a contended flock cannot
// hang this path.
func bounceRoutes(ctx context.Context, cfg *config.Config) {
	bounceProxyAsync(ctx, cfg)
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
func applyAndReport(ctx context.Context, cfg *config.Config, ch litellm.Change, mode restartMode) bool {
	changed, err := applyReported(ctx, cfg, ch, mode)
	if err != nil {
		routePrintf("wt: LiteLLM route not updated: %v\n", err)
	}
	return changed
}

// applyReported is applyAndReport without the not-updated line: it returns the
// failure instead of printing it, so a caller bound by its own deadline can
// tell "the config.yaml lock is held" from "the write failed" and decide where
// to say so. A contended lock is not a warning — TryEnsureModelRoute hands it
// to a retry that can afford to wait (see EnsureModelRoute), and only that
// retry, or applyAndReport, reports.
//
// Everything else is applyAndReport's: ErrMissing is silent (no config.yaml =
// LiteLLM not set up), a restartForced mode settles the earlier deferred write
// even when this one failed (or the proxy keeps serving the dead occupant's
// route), and the restart itself runs asynchronously.
func applyReported(ctx context.Context, cfg *config.Config, ch litellm.Change, mode restartMode) (bool, error) {
	res, err := applyRoutes(cfg, ch, litellm.Options{
		// The restart is always deferred here: the caller decides whether to
		// restart, and runs that restart asynchronously, below.
		// ForceRestart is deliberately not passed — ApplyChange restarts on it even
		// with NoRestart set, which would put the bounce back on this path.
		NoRestart: true,
		// The caller's ctx bounds the config.yaml lock wait too, so a
		// contended lock cannot outlive a caller that set a deadline.
		Ctx: ctx,
	})
	if err != nil {
		// This write failed, but restartForced means an earlier deferred
		// write (the replaced occupant's route removal) is already in
		// config.yaml and still owes its restart: settle it, or the proxy
		// keeps serving the dead occupant's route.
		if mode == restartForced {
			bounceProxyAsync(ctx, cfg)
		}
		if errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			return false, nil
		}
		return false, err
	}
	for _, o := range res.Outcomes {
		if o.Err != nil {
			routePrintf("wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		}
	}
	// res.Warnings is only ever populated by ApplyChange's restart hook, which
	// NoRestart disables; the restart's own warnings are reported below.
	restart := (res.Changed && mode != restartDeferred) || mode == restartForced
	if restart {
		bounceProxyAsync(ctx, cfg)
	}
	return res.Changed, nil
}

// bounceProxyAsync restarts the LiteLLM proxy — and, when one was listening,
// waits for it to come back — asynchronously, on a goroutine tracked by routeWG
// / WaitPendingRoutes. It is the restart half shared by every path that decides
// to bounce: applyAndReport after a route write changed the file (or a forced
// mode asked for a bounce anyway), and bounceRoutes to settle writes an earlier
// deferred call already made.
func bounceProxyAsync(ctx context.Context, cfg *config.Config) {
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
			routePrintf("wt: %s\n", w)
		}
		if wasUp {
			if err := waitProxy(bgCtx, url, proxyReadyTimeout); err != nil {
				routePrintf("wt: %v\n", err)
			}
		}
	}()
}
