package modelserver

import (
	"context"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// jobAutoStop is the kind of the jobs the auto-stopper submits.
const jobAutoStop = "auto-stop"

// AutoStopper stops local models no live session has used for Grace. It only
// considers models some session registered (Sessions.Seen), so a cloud model
// or a model started by hand with no session is never touched.
type AutoStopper struct {
	Deps     Deps
	Sessions *sessionreg.Registry
	Grace    time.Duration
	Jobs     *modeljobs.Manager
}

// Tick runs one scan. A model is stopped when it is running locally in a
// fresh inventory, no live session uses it (family-wide on an Exclusive
// provider), its idle clock is at least Grace old and no job for it is queued
// or running. The stop is an ordinary StopJob without ConfirmInUse, so a
// session that registers between the scan and the job still blocks it.
func (a *AutoStopper) Tick(ctx context.Context) {
	seen := a.Sessions.Seen()
	if len(seen) == 0 || ctx.Err() != nil {
		return
	}
	cfg := a.Deps.Config
	snap := a.Deps.Inventory(cfg)
	now := a.Deps.Now()
	for _, id := range seen {
		en, ok := runningEntry(snap, id)
		if !ok || !lifecycle.CanStop(en.ProviderID) {
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
