package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
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

// routesOutMu serialises every write this file makes. A route operation prints
// from two goroutines — the caller's, and the asynchronous proxy restart it may
// have started (bounceProxyAsync) — and both can be printing to one writer: the
// "updated" line and a restart warning of the same check are the usual pair.
// routePrintf holds it for the whole write, so no writer, routesWarn or a
// caller's own, is ever written by two goroutines at once.
var routesOutMu sync.Mutex

// routeOutKey is the context key of a route operation's own writer.
type routeOutKey struct{}

// withRouteOutput returns a context whose route operation prints to w instead
// of routesWarn. A nil w returns ctx unchanged: the output goes to stderr.
//
// The writer rides on the context because that is the one thing an operation
// hands to everything it starts: bounceProxyAsync detaches the restart from the
// caller's cancellation with context.WithoutCancel, which keeps the context's
// values, so the restart's warnings follow the operation that caused them to
// the same writer. A writer installed process-wide for the length of a check
// (what this replaced, #192 review) cannot do that: it also catches whatever an
// unrelated operation's restart prints while it is installed, and reports it as
// this check's.
func withRouteOutput(ctx context.Context, w io.Writer) context.Context {
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, routeOutKey{}, w)
}

// routePrintf is the single way this file prints: the "updated" line and every
// route warning. It writes to the operation's own writer when ctx carries one
// (withRouteOutput) and to routesWarn otherwise, under routesOutMu.
func routePrintf(ctx context.Context, format string, args ...any) {
	routesOutMu.Lock()
	defer routesOutMu.Unlock()
	w := routesWarn
	if own, ok := ctx.Value(routeOutKey{}).(io.Writer); ok {
		w = own
	}
	fmt.Fprintf(w, format, args...)
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
// reports whether config.yaml changed, and says so in one line when it did
// (reportEnsured). One change builder serving every launch-time check is what
// keeps a model from being routed under one id by one caller and another id by
// the next.
func ensureChange(ctx context.Context, cfg *config.Config, ch litellm.Change) bool {
	w, err := applyReported(ctx, cfg, ch, restartIfChanged)
	if err != nil {
		routePrintf(ctx, "wt: LiteLLM route not updated: %v\n", err)
	}
	reportEnsured(ctx, w, ch.Add[0].ID)
	return w.changed
}

// routeWrite is what one route write did: whether config.yaml changed, the ids
// whose own row was added or differs from the one on disk, and whether a line
// about this write has already been printed.
type routeWrite struct {
	changed bool
	rows    []string
	said    bool
}

// reportEnsured prints the one line a launch-time check owes for a write that
// changed config.yaml. A write also repairs other rows' api_base and enforces
// settings, so "changed" does not mean id's route changed (#206): "route for
// <id> updated" is printed only when id's own row was written. Otherwise the
// line that explains the write is the one applyReported already printed (the
// repaired row's "api_base set", or why id's row could not be built) — and
// when there is none, as for an enforced setting, the write is named as what
// it was, because the caller is about to wait for a proxy restart and that
// wait must not come unexplained.
func reportEnsured(ctx context.Context, w routeWrite, id string) {
	switch {
	case !w.changed:
	case slices.Contains(w.rows, id):
		routePrintf(ctx, "wt: LiteLLM route for %s updated\n", id)
	case !w.said:
		routePrintf(ctx, "wt: LiteLLM config.yaml updated\n")
	}
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
	return EnsureModelRouteTo(nil, cfg, m)
}

// EnsureModelRouteTo is EnsureModelRoute with everything the check prints sent
// to out instead of stderr: the "updated" line, every warning, and the
// warnings of the asynchronous proxy restart the check may start. It exists
// for a caller that owns the terminal — the TUI picker runs under Bubble Tea's
// alt screen, where a line written to stderr is not reliably visible and is
// gone once the agent takes the terminal, so the picker collects this output
// and prints it itself when the terminal is released.
//
// Only this check's output goes to out. Another route operation in flight —
// the restart a failed replace is still settling, say — keeps printing where
// it always did, so a caller can report what it collected as this model's.
//
// out is written under a mutex, never concurrently, so a plain bytes.Buffer
// will do. The restart's warnings are written by a goroutine that outlives
// this call: out is safe to read only once WaitPendingRoutes() has returned,
// and is never written after that. A nil out means stderr.
func EnsureModelRouteTo(out io.Writer, cfg *config.Config, m config.Model) bool {
	ch, ok := modelRouteChange(cfg, m)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(withRouteOutput(context.Background(), out), ensureRouteLockTimeout)
	defer cancel()
	return ensureChange(ctx, cfg, ch)
}

// TryEnsureModelRouteTo is EnsureModelRouteTo for a caller that cannot stop
// what it is doing — the TUI picker, whose update goroutine is also the one that
// repaints the screen and answers keys. EnsureModelRouteTo bounds its lock wait
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
// the caller owes a retry through EnsureModelRouteTo, where waiting is
// affordable, before it uses the proxy. That path prints nothing: the retry
// reports, since a contended lock is not itself a warning. What it does print
// goes to out, on EnsureModelRouteTo's terms.
func TryEnsureModelRouteTo(out io.Writer, cfg *config.Config, m config.Model) (changed, done bool) {
	ch, ok := modelRouteChange(cfg, m)
	if !ok {
		return false, true // not wt's to route: nothing to retry, nothing to say
	}
	ctx, cancel := context.WithCancel(withRouteOutput(context.Background(), out))
	cancel()
	w, err := applyReported(ctx, cfg, ch, restartIfChanged)
	if err != nil {
		return false, false
	}
	reportEnsured(ctx, w, ch.Add[0].ID)
	return w.changed, true
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every route of its
// family goes. Stopping an ollama model only unloads it, so its route stays
// (see routeRemove).
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID string) {
	routeRemove(ctx, cfg, providerID, restartIfChanged)
}

// routeRemove writes a stop's route removal and reports whether config.yaml
// changed. For a family whose routes follow its artifact (ollama) it writes
// nothing and returns false: stopping unloads the model, but a pulled model is
// still served on request, so its route stays (#179).
func routeRemove(ctx context.Context, cfg *config.Config, providerID string, mode restartMode) bool {
	if localmodels.RoutesFollowArtifact(localmodels.Family(providerID)) {
		return false
	}
	// Every family whose routes do not follow its artifacts is single-model
	// (omlx, mtplx; mlx_lm_server has no stop backend): stopping its model
	// takes the provider down, so the whole family's routes go. A backend
	// that is neither would need a per-model removal here; none exists.
	if !SingleModel(providerID) {
		return false
	}
	ch := litellm.Change{RemoveFamilies: []string{localmodels.Family(providerID)}}
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
	return routeRemove(ctx, cfg, occ.ProviderID, restartDeferred)
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
	w, err := applyReported(ctx, cfg, ch, mode)
	if err != nil {
		routePrintf(ctx, "wt: LiteLLM route not updated: %v\n", err)
	}
	return w.changed
}

// applyReported is applyAndReport without the not-updated line: it returns
// what the write did (routeWrite) and the failure instead of printing it, so a caller bound by its own deadline can
// tell "the config.yaml lock is held" from "the write failed" and decide where
// to say so. A contended lock is not a warning — TryEnsureModelRouteTo hands it
// to a retry that can afford to wait (see EnsureModelRoute), and only that
// retry, or applyAndReport, reports.
//
// Everything else is applyAndReport's: ErrMissing is silent (no config.yaml =
// LiteLLM not set up), a restartForced mode settles the earlier deferred write
// even when this one failed (or the proxy keeps serving the dead occupant's
// route), and the restart itself runs asynchronously.
func applyReported(ctx context.Context, cfg *config.Config, ch litellm.Change, mode restartMode) (routeWrite, error) {
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
			return routeWrite{}, nil
		}
		return routeWrite{}, err
	}
	w := routeWrite{changed: res.Changed}
	for _, o := range res.Outcomes {
		if o.Written {
			w.rows = append(w.rows, o.ID)
		}
		switch {
		case o.Err != nil:
			w.said = true
			routePrintf(ctx, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		case o.Action == litellm.ActionAPIBaseSet:
			w.said = true
			// Any write repairs an ollama row with no api_base (#202), and the
			// row can be one the user wrote by hand that this start, stop or
			// launch never named. `wt litellm sync` prints it; a write made
			// here must not change such a row without saying so.
			routePrintf(ctx, "wt: LiteLLM route for %s: api_base set\n", o.ID)
		}
	}
	// res.Warnings is only ever populated by ApplyChange's restart hook, which
	// NoRestart disables; the restart's own warnings are reported below.
	restart := (res.Changed && mode != restartDeferred) || mode == restartForced
	if restart {
		bounceProxyAsync(ctx, cfg)
	}
	return w, nil
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
			routePrintf(bgCtx, "wt: %s\n", w)
		}
		if wasUp {
			if err := waitProxy(bgCtx, url, proxyReadyTimeout); err != nil {
				routePrintf(bgCtx, "wt: %v\n", err)
			}
		}
	}()
}
