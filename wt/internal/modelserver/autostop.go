package modelserver

import (
	"context"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// jobAutoStop is the kind of the jobs the auto-stopper submits.
const jobAutoStop = "auto-stop"

// AutoStopper stops local models no live session has used for Grace. It only
// considers models some session registered (Sessions.Seen), so a cloud model
// or a model started by hand with no session is never touched. A seen model
// that is not running is forgotten once it is idle past Grace, so a later
// start by hand (or by llmbench) with no session is left alone too.
type AutoStopper struct {
	Deps     Deps
	Sessions *sessionreg.Registry
	Grace    time.Duration
	Jobs     *modeljobs.Manager
}

// Tick runs one scan. A model is stopped when it is running locally in a
// fresh inventory whose probe of its family is trusted, no live session uses
// it (family-wide on an Exclusive provider), its idle clock is at least Grace old and no job for it is queued
// or running. The stop is an ordinary StopJob without ConfirmInUse, so a
// session that registers between the scan and the job still blocks it.
func (a *AutoStopper) Tick(ctx context.Context) {
	seen := a.Sessions.Seen()
	if len(seen) == 0 || ctx.Err() != nil {
		return
	}
	cfg := a.Deps.Config()
	snap := a.Deps.Inventory(cfg)
	now := a.Deps.Now()
	for _, id := range seen {
		en, ok := runningEntry(snap, id)
		if !ok {
			a.forgetIdle(cfg, snap, id, now)
			continue
		}
		if !lifecycle.CanStop(en.ProviderID) {
			continue
		}
		// An untrusted probe would make StopJob refuse; skip the model
		// instead of submitting a failing job on every scan.
		if !lifecycle.ProbeTrusted(snap, localmodels.Family(en.ProviderID)) {
			continue
		}
		if len(stopBlockers(cfg, a.Sessions, en)) > 0 {
			continue
		}
		since, seenOK := a.Sessions.IdleSince(id)
		if !seenOK || now.Sub(since) < a.Grace {
			continue
		}
		if a.Jobs.Pending(id) {
			continue
		}
		a.Jobs.Submit(jobAutoStop, id, a.Deps.StopJob(modelapi.StopRequest{Model: id}, a.Sessions))
	}
}

// forgetIdle forgets a seen model a fresh inventory shows not running, when
// no live session uses it, no job for it is queued or running and its idle
// clock is at least Grace old. A model whose family's probe is not trusted
// is kept: "not running" may only mean the probe failed. A model with a job
// pending is kept too: the client registers before it asks for a start, but
// that session lapses after its TTL while the job may still be waiting, and
// a model forgotten then starts unseen (Touch does not bring a forgotten
// model back) and is never auto-stopped — the same job check the stop path
// above makes.
func (a *AutoStopper) forgetIdle(cfg *config.Config, snap localmodels.Snapshot, id string, now time.Time) {
	if fam := modelFamily(cfg, id); fam != "" && !lifecycle.ProbeTrusted(snap, fam) {
		return
	}
	if len(a.Sessions.Users(id)) > 0 {
		return
	}
	if a.Jobs.Pending(id) {
		return
	}
	if since, ok := a.Sessions.IdleSince(id); !ok || now.Sub(since) < a.Grace {
		return
	}
	a.Sessions.Forget(id)
}

// Run calls Tick every interval until ctx is cancelled.
func (a *AutoStopper) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick(ctx)
		}
	}
}
