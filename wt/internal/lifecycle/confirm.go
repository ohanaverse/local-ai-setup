package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// StoppedError means a start succeeded and its server was gone again by the
// time SettleStart had waited for the LiteLLM proxy: another wt stopped it, or
// it died, while the routes were updated (#343). Why names what the probe
// found.
type StoppedError struct{ Why string }

func (e *StoppedError) Error() string {
	return fmt.Sprintf("it started, and was stopped while wt updated the LiteLLM routes (%s)", e.Why)
}

// SettleStart is everything a caller owes a Start that returned nil, before it
// says "is running" or hands the model to an agent, in one call:
//
//  1. the wait for the proxy restart the start's route write left running
//     (WaitPendingRoutes): the agent dials the model through the proxy;
//  2. the check that the model's server is still there (confirmStarted);
//  3. when it is not, the wait for the restart the check's route removal
//     started, so a caller that goes on to read out, or to exit, does neither
//     under a restart.
//
// Start returns as soon as config.yaml is written, and the wait for the proxy
// that follows — the long part of "updating LiteLLM routes" — is where a `wt
// stop` in another terminal lands, after Start has returned nil (#343). It
// returns a *StoppedError for a server that is gone, and nil otherwise.
//
// Everything the check prints goes to out instead of stderr, as Options.Out
// does for the start it follows: a route that could not be removed, and the
// warnings of the proxy restart the removal starts. The caller that owns the
// screen (the model picker) hands it the writer it handed Start, so one
// start's lines stay together, and out is complete when SettleStart returns.
// A nil out means stderr.
//
// ctx gives the check its values and nothing else: its cancellation is
// dropped here, so no caller has to. A Ctrl+C that landed in the proxy wait,
// or a cancel that lost to a start that finished anyway, reaches here with a
// done context, and the model is about to be reported as running all the
// same. Under a done context the ollama probe settles nothing — a daemon that
// is gone would be reported as running — and the route removal gives up on a
// config.yaml lock another wt holds — the stop that took the server down is
// the likely holder — and the dead route would stay. The waits take no
// context: like WaitPendingRoutes anywhere, they end when the restart does.
func SettleStart(ctx context.Context, out io.Writer, cfg *config.Config, t Target) error {
	WaitPendingRoutes()
	err := confirmStarted(context.WithoutCancel(ctx), out, cfg, t)
	if err != nil {
		WaitPendingRoutes()
	}
	return err
}

// confirmStarted asks, once, whether the model a Start just reported started
// is still there. It is SettleStart's check, made after the wait for the
// proxy.
//
// It is one request to the provider's server, the one the inventory makes, and
// "gone" is only what that answer settles:
//
//   - mtplx (Exclusive): the port refuses the connection, or the server
//     answers and does not list the model — nothing, or a model another start
//     put there.
//   - omlx (Pool): the service refuses the connection, or its status lists the
//     model as neither loaded nor loading. A pool read through the fallback
//     lists only what is loaded, by names that can be aliases (see
//     reconcilePool), so a model missing from it settles nothing.
//   - ollama (Shared): the daemon refuses the connection. A model `ollama
//     stop` unloaded is not gone: ollama serves a pulled model on request and
//     its route follows the artifact, so the route is right and the next
//     request loads it.
//
// Every other answer — a timeout, an error status, a cancelled ctx — is not
// "gone": a start that worked is not failed on a probe that could not tell.
//
// When the server is gone the model's own route is removed, in whichever order
// the stop and this start wrote config.yaml: a stop that removed the family's
// routes before the start hook wrote this one would otherwise leave a route to
// a server wt now knows is down. Only this model's ids are removed, never the
// family: an Exclusive server that answers with another model is that other
// start's, and so is its route. The proxy restart that follows is asynchronous,
// like every route write's, and what it prints goes to out (nil: stderr) from
// a goroutine: SettleStart waits for it.
func confirmStarted(ctx context.Context, out io.Writer, cfg *config.Config, t Target) error {
	ctx = withRouteOutput(ctx, out)
	why := defaultEnv().startedGone(ctx, cfg, t)
	if why == "" {
		return nil
	}
	routeAfterGone(ctx, cfg, t)
	return &StoppedError{Why: why}
}

// startedGone is confirmStarted's probe: why t's server is gone, "" when it is
// there or the answer does not settle it.
func (e *env) startedGone(ctx context.Context, cfg *config.Config, t Target) string {
	family := localmodels.Family(t.ProviderID)
	b := e.backends[family]
	if b == nil {
		return ""
	}
	origin, _ := localmodels.FamilyOrigin(cfg, family)
	down := fmt.Sprintf("%s no longer answers at %s", family, origin)
	switch b.tenancy() {
	case Shared:
		if _, err := localmodels.OllamaLoaded(ctx, e.probeClient, origin); errors.Is(err, syscall.ECONNREFUSED) {
			return down
		}
	case Pool:
		pool, err := localmodels.OmlxPool(cfg, e.probeClient)
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			return down
		case err != nil:
			return ""
		}
		if m, ok := pool.Find(t.ModelName); ok && !m.Loaded && !m.Loading {
			return fmt.Sprintf("%s no longer has it loaded", family)
		}
	case Exclusive:
		ids, err := localmodels.ServedIDs(cfg, e.probeClient, family)
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			return down
		case err != nil:
			return ""
		}
		for _, id := range ids {
			if SameModel(family, id, t.ModelName) {
				return ""
			}
		}
		return fmt.Sprintf("%s at %s is no longer serving it", family, origin)
	}
	return ""
}

// routeAfterGone removes the route of a started model whose server
// confirmStarted found gone: the id the start hook wrote it under, and on a
// Pool every id the model can be routed under (poolRouteIDs).
func routeAfterGone(ctx context.Context, cfg *config.Config, t Target) {
	ids := []string{routeModel(cfg, t).ID}
	if TenancyOf(t.ProviderID) == Pool {
		ids = poolRouteIDs(cfg, localmodels.Entry{ProviderID: t.ProviderID, ModelName: t.ModelName, ModelID: t.ModelID})
	}
	applyAndReport(ctx, cfg, litellm.Change{Remove: ids}, restartIfChanged)
}
