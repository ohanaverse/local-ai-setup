package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Seams: production talks to the real config.yaml, proxy and stderr.
var (
	applyRoutes            = applyChangeWithAdmin
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

// routesInFlight counts the same restarts, for RoutesPending: a WaitGroup
// cannot be asked whether it would block.
var routesInFlight atomic.Int32

// RoutesPending reports whether a proxy restart is in flight, that is,
// whether WaitPendingRoutes would wait. It is for a caller that is about to
// wait and wants to say so on screen first; it decides nothing.
func RoutesPending() bool { return routesInFlight.Load() > 0 }

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
//     started not yet listed). The launch paths (all in-process ones were
//     deleted with Task 19: a session's start is the model server's) and
//     wt smoke before its one-shot
//     prompt — therefore wait here rather than only at process exit.
func WaitPendingRoutes() {
	routeWG.Wait()
}

// restartRun lets one proxy restart run at a time in this process (#598):
// the asynchronous bounces (bounceProxyAsync, BounceProxyTo) each hold it for
// their restart and readiness wait, so a model server's start-up route sync
// and a route job never restart the proxy at once. A failed restart is
// printed and nothing remembers it (the slot-profile launch that read it back
// is gone).
var restartRun sync.Mutex

const (
	// proxyReadyTimeout bounds the wait for the proxy after a route change;
	// litellm's own, so this package and `wt-model litellm sync` wait as long.
	proxyReadyTimeout = litellm.ProxyReadyTimeout
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
// id the catalog, -M and usage use). An Exclusive provider (mtplx) can serve
// only one model, so starting one replaced whatever ran before: every other
// route of its family — discovered siblings included — is removed in the
// same write. A Pool start (omlx) adds its model and clears nothing: its
// loaded siblings keep their routes, and a model omlx evicted to make room
// already lost its own (reconcilePool). restartOwed means an earlier deferred
// write (a replaced occupant's or an evicted model's route removal) has not
// been followed by a restart yet; this call's bounce settles it, so a replace
// costs one proxy restart, not two. It never fails the caller — a missing
// route is a warning, not a failed start.
func routeAfterStart(ctx context.Context, cfg *config.Config, t Target, restartOwed bool) {
	mode := restartIfChanged
	if restartOwed {
		mode = restartForced
	}
	applyAndReport(ctx, cfg, StartRouteChange(cfg, t), mode)
}

// StartRouteChange is the route change routeAfterStart writes for a started
// target: the model to route (registry overlay, else discovered — routeModel
// says how a target's id and name choose it) and, for an Exclusive provider,
// its family to clear. A Pool start adds its model and clears nothing: clearing
// the family there is what made sync and the start hook undo each other with
// two models loaded (#213). It is exported so the id the
// start hook writes can be pinned against the id `wt-model litellm sync` desires
// for the same model (cmd/wt-session) — if the two derivations drift, every sync
// after a start removes the hook's route and adds its own.
func StartRouteChange(cfg *config.Config, t Target) litellm.Change {
	ch := litellm.Change{Add: []config.Model{routeModel(cfg, t)}}
	if TenancyOf(t.ProviderID) == Exclusive {
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

// routeAfterStop removes the routes of a provider that was stopped as a whole
// (see routeRemoveFamily).
func routeAfterStop(ctx context.Context, cfg *config.Config, providerID string) {
	routeRemoveFamily(ctx, cfg, providerID, restartIfChanged)
}

// routeRemoveFamily writes the removal for a provider that was stopped as a
// whole (lifecycle.Stop): on an Exclusive or Pool server every route of the
// family goes, since the server is down. For a family whose routes follow its
// artifacts (ollama) it writes nothing: a pulled model is still served on
// request (#179).
func routeRemoveFamily(ctx context.Context, cfg *config.Config, providerID string, mode restartMode) bool {
	family := localmodels.Family(providerID)
	if localmodels.RoutesFollowArtifact(family) {
		return false
	}
	if ten := TenancyOf(providerID); ten != Exclusive && ten != Pool {
		return false
	}
	return applyAndReport(ctx, cfg, litellm.Change{RemoveFamilies: []string{family}}, mode)
}

// routeRemoveModel writes the removal for one model that stopped serving and
// reports whether config.yaml changed. On an Exclusive server the model was
// the provider, so the family goes; on a Pool only that model's ids go, and
// its loaded siblings keep their routes (#213).
func routeRemoveModel(ctx context.Context, cfg *config.Config, en localmodels.Entry, mode restartMode) bool {
	if TenancyOf(en.ProviderID) != Pool {
		return routeRemoveFamily(ctx, cfg, en.ProviderID, mode)
	}
	return applyAndReport(ctx, cfg, litellm.Change{Remove: poolRouteIDs(cfg, en)}, mode)
}

// poolRouteIDs lists the ids a pool model can be routed under: the id of every
// registry model that names it, under any provider id of the entry's family —
// omlx and omlx-6bit are one server, so a row under each can name the same
// directory and both are routed while it is loaded — plus the entry's own id.
// The discovered id is the fallback, used only when none of those gave an id.
// An entry built by the occupancy re-probe carries the server's raw id as
// ModelID, which is no route id, so ModelID is used only when it differs from
// the name.
//
// An entry with an id of its own is matched by name exactly: ModelFor's lenient
// match would let an artifact that merely resembles a registry model's
// ("org/name" beside "name", #195) pull in that other model's row, removing the
// route of a sibling omlx still has loaded — whose sessions then get "Invalid
// model name" — and leaving this model's own row behind. That is the same rule
// routeModel follows for the write. Only an entry with no id of its own (the
// raw served name the occupancy re-probe carries) falls back to ModelFor.
func poolRouteIDs(cfg *config.Config, en localmodels.Entry) []string {
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	own := en.ModelID != "" && en.ModelID != en.ModelName
	if own {
		add(en.ModelID)
	}
	for _, providerID := range localmodels.FamilyProviderIDs(localmodels.Family(en.ProviderID)) {
		if own {
			for _, m := range cfg.Models {
				if m.ProviderID == providerID && m.ModelName == en.ModelName {
					add(m.ID)
				}
			}
			continue
		}
		if m, ok := litellm.ModelFor(cfg, providerID, en.ModelName); ok {
			add(m.ID)
		}
	}
	if len(ids) == 0 {
		add(litellm.DiscoveredModel(en.ProviderID, en.ModelName).ID)
	}
	return ids
}

// routeAfterOccupantStopped removes the route of a model a start displaced: an
// Exclusive server's stopped occupant, or a model a Pool load evicted. It is
// the env hook defaultEnv wires in (env.go): the start may still fail, and a
// failed Start runs no success hook at all, so the removal has to be written
// at the moment the model went away. It only writes: the proxy restart is
// deferred to Start's single settling bounce (routeAfterStart on success,
// bounceRoutes on failure). It reports whether a restart is now owed.
func routeAfterOccupantStopped(ctx context.Context, cfg *config.Config, occ localmodels.Entry) bool {
	return routeRemoveModel(ctx, cfg, occ, restartDeferred)
}

// bounceRoutes settles route writes a deferred write already made — when the
// start that followed them failed, or when a batch of
// stops wrote its removals and owed the bounce (StopBatch.Stop/SettleRoutes).
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
// This is not a wall-clock saving for a plain `wt-model stop`: wt-model's
// main() calls WaitPendingRoutes() before the process exits, so it still pays
// the full restart. What it buys is that the work between the route write
// and that exit — the engine's own return, progress output, the stop picker,
// the survey — no longer sits behind the proxy, and that each caller decides
// for itself where it needs the proxy ready: the launch paths wait explicitly
// (see WaitPendingRoutes) because the agent they hand off to dials the model
// through the proxy.
//
// ErrMissing is silent (no config.yaml = LiteLLM not set up); any other
// failure of the write is one "route not updated" line. A restartForced mode
// settles the earlier deferred write even when this one failed (or the proxy
// keeps serving the dead occupant's route), and the restart itself runs
// asynchronously.
func applyAndReport(ctx context.Context, cfg *config.Config, ch litellm.Change, mode restartMode) bool {
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
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			routePrintf(ctx, "wt: LiteLLM route not updated: %v\n", err)
		}
		return false
	}
	for _, o := range res.Outcomes {
		switch {
		case o.Err != nil:
			routePrintf(ctx, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		case o.Action == litellm.ActionAPIBaseSet:
			// Any write repairs an ollama row with no api_base (#202), and the
			// row can be one the user wrote by hand that this start, stop or
			// launch never named. `wt-model litellm sync` prints it; a write made
			// here must not change such a row without saying so.
			routePrintf(ctx, "wt: LiteLLM route for %s: api_base set\n", o.ID)
		}
	}
	// The write's restart is deferred (NoRestart), so its warnings are the
	// database half's (#598): printed here, through the writer the restart's
	// own warnings use (bounceProxyAsync).
	printRouteWarnings(ctx, res.Warnings)
	restart := (res.Changed && mode != restartDeferred) || mode == restartForced
	if restart {
		bounceProxyAsync(ctx, cfg)
	}
	return res.Changed
}

// BounceProxyTo is the asynchronous, tracked proxy restart a route write
// makes (bounceProxyAsync), for a caller outside this package that wrote
// config.yaml with litellm.Options.NoRestart and owes the restart: the model
// server's start-up route sync (#598). It returns at once; the restart runs
// one at a time with every other restart of this process (restartRun),
// and WaitPendingRoutes waits for it. What it prints goes to out (nil:
// stderr).
func BounceProxyTo(ctx context.Context, out io.Writer, cfg *config.Config) {
	bounceProxyAsync(withRouteOutput(ctx, out), cfg)
}

// bounceProxyAsync restarts the LiteLLM proxy — and, when one was listening,
// waits for it to come back — asynchronously, on a goroutine tracked by routeWG
// / WaitPendingRoutes. It is the restart half shared by every path that decides
// to bounce: applyAndReport after a route write changed the file (or a forced
// mode asked for a bounce anyway), and bounceRoutes to settle writes an earlier
// deferred call already made.
//
// It returns nothing and fails no caller: a failed restart command or a
// readiness timeout is printed, and nothing remembers it.
func bounceProxyAsync(ctx context.Context, cfg *config.Config) {
	url := cfg.LitellmBaseURL()
	routeWG.Add(1)
	routesInFlight.Add(1)
	go func() {
		defer routeWG.Done()
		defer routesInFlight.Add(-1)
		// Detached from ctx: every real caller cancels its own ctx via a
		// scoped defer cancel() the instant its (now async) call returns —
		// which happens before this restart even starts. A caller's normal
		// completion is not a signal to abandon the restart; only litellm's
		// own bounds (RestartContext's: three minutes for a request in flight
		// to finish, plus 30s per launchctl command; Listening's and
		// WaitReady's) need to cap this goroutine's lifetime.
		// WaitPendingRoutes() is where a caller (a process about to exit, or
		// a launch path about to use the proxy) rejoins this goroutine.
		//
		// A context.WithTimeout child of ctx would not do: its defer cancel()
		// fires the moment this goroutine returns, and it would still inherit
		// the caller's post-return cancellation regardless.
		//
		// The probe before the restart, the restart and the readiness wait
		// are litellm.Bounce.Run, the sequence `wt-model litellm sync` runs too,
		// here over this package's seams. The proxy is probed once, lazily,
		// just before the restart (Listening: only a refused connection
		// counts as down, so a slow proxy is still waited for). Waiting for
		// readiness afterwards is only meaningful when a proxy was actually
		// serving: a configured URL with nothing listening (routing may even
		// be switched off) used to cost every start and stop the full
		// proxyReadyTimeout. Writes that never restart (deferred, or nothing
		// changed) never reach this goroutine, so they pay no probe at all.
		// The restart itself stays best-effort either way.
		//
		// The restart's own notices (the proxy is finishing a request in
		// flight; one was cut off) follow this operation's writer like the
		// warnings below (withRestartNotice).
		bgCtx := withRestartNotice(context.WithoutCancel(ctx))
		// The restart's warnings are printed as soon as it returns, before
		// the readiness wait (up to proxyReadyTimeout), not after it.
		restartPrinting := func(ctx context.Context) []string {
			warnings := restartProxy(ctx)
			for _, w := range warnings {
				routePrintf(ctx, "wt: %s\n", w)
			}
			return warnings
		}
		b := litellm.Bounce{Restart: restartPrinting, Listening: probeProxy, WaitReady: waitProxy}
		restartRun.Lock()
		_, waitErr := b.Run(bgCtx, url)
		restartRun.Unlock()
		// Warnings are always surfaced: bgCtx cannot be cancelled by the
		// caller, so there is no longer a "the user pressed Ctrl+C, a failed
		// restart is expected" case to stay quiet about — suppressing them on
		// that theory is exactly what hid this path failing silently.
		//
		// That a request was cut off at the drain bound is a notice, not a
		// warning: that restart was made.
		if waitErr != nil {
			routePrintf(bgCtx, "wt: %v\n", waitErr)
		}
	}()
}

// applyChangeWithAdmin is the production applyRoutes: litellm.ApplyChange with
// Options.Admin set from the proxy pair cfg's load resolved (litellm.AdminFor:
// the url and key `wt-model litellm set` stores, which the gateway forwards
// with), so a local route goes through LiteLLM's model API and a start or
// stop costs no restart (#598). With no pair (either half empty) Admin is nil
// and every route stays in config.yaml, as before. Tests that drive the real
// write set applyRoutes to litellm.ApplyChange (realRoutes), which never
// reaches a proxy, or to this with a fake proxy's pair.
func applyChangeWithAdmin(cfg *config.Config, ch litellm.Change, o litellm.Options) (litellm.Result, error) {
	o.Admin = litellm.AdminFor(cfg)
	return litellm.ApplyChange(cfg, ch, o)
}

// printRouteWarnings prints what a route write warns about through the
// writer the restart warnings use (routePrintf): the database half's lines
// (#598: a fallback to config.yaml, a database route NOT removed, routes not
// checked, a twin that could not be deleted, database routes being enabled).
// The write's own restart is always deferred here (NoRestart), so none of
// these is a restart warning.
func printRouteWarnings(ctx context.Context, warnings []string) {
	for _, w := range warnings {
		routePrintf(ctx, "wt: %s\n", w)
	}
}

// RestartNoticer is a route writer (Options.Out, the writer of a *To call)
// that takes the proxy restart's notices itself rather than having them
// printed into it: a caller with a live screen shows a notice marked
// InProgress while the restart runs, where a line printed into its buffer
// would only be read once the restart was over and the line no longer true.
// RestartNotice may be called from the restart's goroutine.
type RestartNoticer interface {
	RestartNotice(litellm.Notice)
}

// withRestartNotice returns ctx carrying the function the proxy restart's
// notices are delivered to (litellm.WithRestartNotice): the operation's
// writer when it is a RestartNoticer, else routePrintf on ctx, so the line
// goes where the operation's other output goes.
func withRestartNotice(ctx context.Context) context.Context {
	return litellm.WithRestartNotice(ctx, func(n litellm.Notice) {
		if rn, ok := ctx.Value(routeOutKey{}).(RestartNoticer); ok {
			rn.RestartNotice(n)
			return
		}
		routePrintf(ctx, "wt: %s\n", n.Msg)
	})
}
