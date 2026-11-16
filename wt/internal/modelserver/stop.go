package modelserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// StopJob is the job that stops req.Model. A model some live session other
// than the requester (req.Session) uses is refused (in_use, naming those
// sessions) unless req.ConfirmInUse; on an Exclusive provider every session on
// any model of the family counts, since the provider stops as a whole. A
// local model that is not running is an OK no-op. A stop the provider
// accepted but has not finished is OK with a step that begins
// modelapi.StepStillFinishing (style guide S5).
func (d Deps) StopJob(req modelapi.StopRequest, sessions *sessionreg.Registry) modeljobs.RunFunc {
	return guarded(func(ctx context.Context, step func(string)) modelapi.JobResult {
		cfg := d.Config()
		snap := d.Inventory(cfg)
		en, found := runningEntry(snap, req.Model)
		if !found {
			return notRunning(cfg, snap, req.Model, step)
		}
		id := en.ModelID
		if !lifecycle.CanStop(en.ProviderID) {
			return problem(modelapi.CodeFailed, fmt.Sprintf("wt cannot stop %s: its provider has no stop backend", id))
		}
		if fam := localmodels.Family(en.ProviderID); !lifecycle.ProbeTrusted(snap, fam) {
			return problem(modelapi.CodeFailed, fmt.Sprintf("cannot tell what %s is serving; not stopping %s", fam, id))
		}
		// The requester is no user to protect (no session id is empty, so an
		// absent Session excludes nobody).
		users := slices.DeleteFunc(stopBlockers(cfg, sessions, en), func(u string) bool { return u == req.Session })
		if len(users) > 0 && !req.ConfirmInUse {
			return modelapi.JobResult{Error: &modelapi.Problem{
				Code:     modelapi.CodeInUse,
				Message:  inUseMessage(en, len(users)),
				Sessions: users,
			}}
		}
		step("stopping " + id)
		out := &syncBuffer{}
		batch := d.BeginStops(ctx, cfg, []localmodels.Entry{en})
		owed, err := batch.Stop(ctx, en)
		if owed {
			step(lifecycle.StageLabel(lifecycle.StageRouting))
			d.SettleRoutes(ctx, out, cfg)
		}
		var still *lifecycle.StillFinishingError
		switch {
		case err == nil:
			return modelapi.JobResult{OK: true}
		case errors.As(err, &still):
			step(modelapi.StepStillFinishing + ": " + err.Error())
			return modelapi.JobResult{OK: true}
		case ctx.Err() != nil || errors.Is(err, context.Canceled):
			return problem(modelapi.CodeCancelled, fmt.Sprintf("cancelled while stopping %s", id))
		}
		return problem(modelapi.CodeFailed, withOutput(fmt.Sprintf("failed to stop %s: %v", id, err), out))
	})
}

// runningEntry finds the inventory entry for id that is running.
func runningEntry(snap localmodels.Snapshot, id string) (localmodels.Entry, bool) {
	for _, e := range snap.Entries {
		if e.ModelID == id && e.Running {
			return e, true
		}
	}
	return localmodels.Entry{}, false
}

// notRunning answers a stop of a model with no running entry: unknown when
// nothing knows the id, failed for a cloud model, otherwise an OK no-op.
func notRunning(cfg *config.Config, snap localmodels.Snapshot, id string, step func(string)) modelapi.JobResult {
	if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
		if loc, err := cfg.ResolveLocation(cfg.Models[i]); err != nil || loc != config.LocationLocal {
			return problem(modelapi.CodeFailed, fmt.Sprintf("%s is a cloud model: there is nothing to stop", id))
		}
		step(id + " is not running")
		return modelapi.JobResult{OK: true}
	}
	if slices.ContainsFunc(snap.Entries, func(e localmodels.Entry) bool { return e.ModelID == id }) {
		step(id + " is not running")
		return modelapi.JobResult{OK: true}
	}
	return problem(modelapi.CodeUnknownModel, fmt.Sprintf("unknown model %q", id))
}

// stopBlockers are the sorted ids of the live sessions a stop of en would
// take a model from: en's own users on a Pool or Shared provider, and on an
// Exclusive one the users of every model of the family.
func stopBlockers(cfg *config.Config, sessions *sessionreg.Registry, en localmodels.Entry) []string {
	if lifecycle.TenancyOf(en.ProviderID) != lifecycle.Exclusive {
		return sessions.Users(en.ModelID)
	}
	fam := localmodels.Family(en.ProviderID)
	var users []string
	for id := range sessions.Live() {
		if id == en.ModelID || modelFamily(cfg, id) == fam {
			users = append(users, sessions.Users(id)...)
		}
	}
	slices.Sort(users)
	return slices.Compact(users)
}

// modelFamily is the local provider family a session's model id belongs to:
// the registry model's provider when the id is registered (a cloud model has
// none: ""), else the id's own prefix, since a discovered id is
// family-prefixed (config.DiscoveredModelID). The same rule as
// internal/survey's stop state (modelFamily).
func modelFamily(cfg *config.Config, id string) string {
	if i := config.IndexModelByID(cfg.Models, id); i >= 0 {
		m := cfg.Models[i]
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			return ""
		}
		return localmodels.Family(m.ProviderID)
	}
	prefix, _, _ := strings.Cut(id, "/")
	return localmodels.Family(prefix)
}

func inUseMessage(en localmodels.Entry, n int) string {
	what := en.ModelID
	if lifecycle.TenancyOf(en.ProviderID) == lifecycle.Exclusive {
		what = fmt.Sprintf("%s (the %s server stops as a whole)", en.ModelID, localmodels.Family(en.ProviderID))
	}
	return fmt.Sprintf("%s is in use by %d live client session(s); confirm to stop it anyway", what, n)
}
