package modelserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeljobs"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
)

// stepGateway is the step a start reports while it waits for the gateway.
const stepGateway = "waiting for the gateway"

// StartJob is the job that starts req.Model. It calls the engine's Start
// exactly once: a refusal goes back to the client as a problem (occupied,
// others, occupancy_unknown) for it to answer by posting a new start, never
// answered here. A start succeeds only once the model is settled and its
// gateway id is listed, and then restarts the model's idle clock in sessions
// (nil skips that).
//
// An alias id (#596) starts the alias's local target: the job waits for the
// target's gateway id (the target's route write adds the alias's rows), then
// confirms LiteLLM lists the alias's row for that target (aliasRowCheck) and
// touches the alias, which touches the target. An alias with no local target
// has nothing to start.
func (d Deps) StartJob(req modelapi.StartRequest, sessions *sessionreg.Registry) modeljobs.RunFunc {
	return guarded(func(ctx context.Context, step func(string)) modelapi.JobResult {
		cfg := d.Config()
		model := req.Model
		alias, isAlias := serverAlias(cfg, req.Model)
		if isAlias {
			if fault := config.AliasFault(cfg, alias); fault != nil {
				return problem(modelapi.CodeBlocked, fault.Error())
			}
			m, ok := cfg.AliasLocalTarget(alias)
			if !ok {
				return problem(modelapi.CodeFailed, fmt.Sprintf("%s has no local target: there is nothing to start", alias.ID))
			}
			model = m.ID
		}
		snap := d.Inventory(cfg)
		rows := localmodels.CatalogRows(localmodels.CatalogInput{Config: cfg, Models: cfg.Models, Inventory: &snap})
		row, ok := catalog.Find(rows, model)
		if !ok || row.IsProfile() {
			return problem(modelapi.CodeUnknownModel, fmt.Sprintf("unknown model %q", model))
		}
		id := row.Model.ID
		if row.Location != config.LocationLocal {
			return problem(modelapi.CodeFailed, fmt.Sprintf("%s is a cloud model: there is nothing to start", id))
		}
		if row.Action() == catalog.ActionBlock {
			return problem(modelapi.CodeBlocked, row.BlockReason())
		}
		target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: id}
		out := &syncBuffer{}
		opts := lifecycle.Options{
			AllowReplace: req.AllowReplace,
			Out:          out,
			Progress:     func(s lifecycle.Stage) { step(lifecycle.StageLabel(s)) },
		}
		if req.Others != nil {
			opts.Others = lifecycle.OthersAnswer{Decided: true, Stop: append([]string(nil), req.Others.Stop...)}
		}
		if err := d.Start(ctx, cfg, target, opts); err != nil {
			return startRefusal(ctx, req, id, err, out)
		}
		if err := d.Settle(ctx, out, cfg, target); err != nil {
			var stopped *lifecycle.StoppedError
			if errors.As(err, &stopped) {
				return problem(modelapi.CodeStopped, withOutput(lifecycle.StartErrorMessage(id, err), out))
			}
			return problem(modelapi.CodeFailed, withOutput(lifecycle.StartErrorMessage(id, err), out))
		}
		step(stepGateway)
		if err := d.Routed(ctx, id); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return problem(modelapi.CodeCancelled, fmt.Sprintf("cancelled while waiting for %s to answer through the gateway", id))
			}
			return problem(modelapi.CodeFailed, withOutput(fmt.Sprintf("%s started but does not answer through the gateway: %v", id, err), out))
		}
		if isAlias {
			if res, ok := d.aliasRowCheck(ctx, cfg, alias.ID, id, step); !ok {
				return res
			}
		}
		if sessions != nil {
			// Restart the idle clock: a model idle past the grace that was
			// just started must not be auto-stopped on the next scan.
			if isAlias {
				sessions.Touch(alias.ID)
			}
			sessions.Touch(id)
		}
		return modelapi.JobResult{OK: true}
	})
}

// aliasRowCheck confirms, after an alias's local target answers through the
// gateway, that LiteLLM lists the alias's row for it. A row that cannot be
// confirmed fails the start only when the gateway does not list the alias id
// at all (a client would launch an unknown model); while the alias is routed
// through another row, or the gateway cannot be asked, it is one warning step.
func (d Deps) aliasRowCheck(ctx context.Context, cfg *config.Config, alias, target string, step func(string)) (modelapi.JobResult, bool) {
	var why string
	switch {
	case d.AliasListed == nil:
		why = "this server cannot list alias rows"
	default:
		listed, err := d.AliasListed(ctx, cfg, alias, target)
		switch {
		case err != nil:
			why = "the alias rows could not be listed: " + err.Error()
		case listed:
			return modelapi.JobResult{}, true
		default:
			why = "LiteLLM lists no row of " + alias + " for it"
		}
	}
	if d.RoutedIDs != nil {
		if ids, err := d.RoutedIDs(ctx); err == nil && !ids[alias] {
			return problem(modelapi.CodeFailed, fmt.Sprintf("%s started but %s is not routed (%s); run 'wt-model litellm sync' on the host", target, alias, why)), false
		}
	}
	step(fmt.Sprintf("warning: %s started but %s's row for it is not confirmed (%s); run 'wt-model litellm sync' on the host if requests to %s fail", target, alias, why, alias))
	return modelapi.JobResult{}, true
}

// startRefusal maps a failed Start. A refusal is a problem the client answers
// only when it asks something the request left open; one asking only what
// the request already answered is a failure, so the client cannot loop.
func startRefusal(ctx context.Context, req modelapi.StartRequest, id string, err error, out *syncBuffer) modelapi.JobResult {
	var occ *lifecycle.OccupiedError
	var unknown *lifecycle.OccupancyUnknownError
	switch {
	case errors.As(err, &unknown):
		if req.AllowReplace {
			break
		}
		return problem(modelapi.CodeOccupancyUnknown, err.Error())
	case errors.As(err, &occ):
		replaceAsked := len(occ.Occupants) > 0 && !req.AllowReplace
		othersAsked := (len(occ.Others) > 0 || len(occ.OthersUnknown) > 0) && req.Others == nil
		if !replaceAsked && !othersAsked {
			break
		}
		p := &modelapi.Problem{Code: modelapi.CodeOthers, Message: err.Error(), OthersUnknown: occ.OthersUnknown}
		if replaceAsked {
			p.Code = modelapi.CodeOccupied
			p.Occupants = occ.IDs()
		}
		for _, r := range occ.Others {
			p.Others = append(p.Others, modelapi.Resident{ID: r.Entry.ModelID, Label: r.Label(), Stoppable: r.Stoppable, Figure: r.Figure()})
		}
		return modelapi.JobResult{Error: p}
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		return problem(modelapi.CodeCancelled, fmt.Sprintf("cancelled before %s started", id))
	}
	return problem(modelapi.CodeFailed, withOutput(lifecycle.StartErrorMessage(id, err), out))
}
