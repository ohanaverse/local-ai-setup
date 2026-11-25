package modelserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
// An alias id (#596) starts the alias's local targets (#690): every one, in
// (order, file position) order, each through the same per-model path a plain
// start takes (catalog row, block refusal, the engine's own memory and
// occupancy checks, settle, wait for the gateway id), so no second memory
// check exists. A target already running and routed is skipped, which is what
// lets a client resume after answering an occupied or others problem with a
// re-post. After the last start the inventory is read again and every target
// must still be running: a Pool provider may have evicted an earlier one.
// There is no rollback on a partial failure (stopping a model other sessions
// may use is worse than leaving it): the failure names what started, what
// failed and why, and says to run the start again. Cancellation stops
// between targets. Then aliasRowCheck runs per started target and the alias
// and each target are touched. An alias with no local target has nothing to
// start.
func (d Deps) StartJob(req modelapi.StartRequest, sessions *sessionreg.Registry) modeljobs.RunFunc {
	return guarded(func(ctx context.Context, step func(string)) modelapi.JobResult {
		cfg := d.Config()
		alias, isAlias := serverAlias(cfg, req.Model)
		if !isAlias {
			if _, res, ok := d.startOne(ctx, cfg, req, req.Model, "", step); !ok {
				return res
			}
			if sessions != nil {
				// Restart the idle clock: a model idle past the grace that was
				// just started must not be auto-stopped on the next scan.
				sessions.Touch(req.Model)
			}
			return modelapi.JobResult{OK: true}
		}
		if fault := config.AliasFault(cfg, alias); fault != nil {
			return problem(modelapi.CodeBlocked, fault.Error())
		}
		locals := cfg.AliasLocalTargets(alias)
		if len(locals) == 0 {
			return problem(modelapi.CodeFailed, fmt.Sprintf("%s has no local target: there is nothing to start", alias.ID))
		}
		var started, ids []string
		for i, m := range locals {
			ids = append(ids, m.ID)
			if err := ctx.Err(); err != nil {
				return problem(modelapi.CodeCancelled, partialText(alias.ID, started, "cancelled before "+m.ID+" started", modelapi.CodeCancelled))
			}
			if len(locals) > 1 {
				step(fmt.Sprintf("starting %s (%d of %d)", m.ID, i+1, len(locals)))
			}
			// A running, routed target of a several-target alias is skipped
			// while the alias itself is routed too; a one-target alias always
			// starts, as before, since its start re-runs the route write.
			skipFor := ""
			if len(locals) > 1 {
				skipFor = alias.ID
			}
			skipped, res, ok := d.startOne(ctx, cfg, req, m.ID, skipFor, step)
			if !ok {
				return partialFailure(alias.ID, started, res)
			}
			if !skipped {
				started = append(started, m.ID)
			}
		}
		if len(locals) > 1 {
			// A later start may have displaced an earlier one (Pool eviction).
			snap := d.Inventory(cfg)
			for _, id := range ids {
				if _, ok := runningEntry(snap, id); !ok {
					return problem(modelapi.CodeFailed, fmt.Sprintf("%s: a later start displaced %s, so the alias cannot hold all its local targets at once (the provider's memory ceiling evicted it); free memory or remove a target, then run the start again",
						alias.ID, id))
				}
			}
		}
		for _, id := range ids {
			if res, ok := d.aliasRowCheck(ctx, cfg, alias.ID, id, step); !ok {
				return res
			}
		}
		if sessions != nil {
			sessions.Touch(alias.ID)
			for _, id := range ids {
				sessions.Touch(id)
			}
		}
		return modelapi.JobResult{OK: true}
	})
}

// startOne is the per-model start path: the catalog row, the block refusal,
// the engine's Start, Settle and the wait for the gateway id. skipped is true
// when the model was already running and routed, so nothing was started;
// ok is false when res is the failure to return. skipFor is the alias id
// whose routed state allows the skip, "" never skips.
func (d Deps) startOne(ctx context.Context, cfg *config.Config, req modelapi.StartRequest, model, skipFor string, step func(string)) (skipped bool, res modelapi.JobResult, ok bool) {
	snap := d.Inventory(cfg)
	rows := localmodels.CatalogRows(localmodels.CatalogInput{Config: cfg, Models: cfg.Models, Inventory: &snap})
	row, found := catalog.Find(rows, model)
	if !found || row.IsProfile() {
		return false, problem(modelapi.CodeUnknownModel, fmt.Sprintf("unknown model %q", model)), false
	}
	id := row.Model.ID
	if row.Location != config.LocationLocal {
		return false, problem(modelapi.CodeFailed, fmt.Sprintf("%s is a cloud model: there is nothing to start", id)), false
	}
	if skipFor != "" && d.RoutedIDs != nil {
		if _, run := runningEntry(snap, id); run {
			if ids, err := d.RoutedIDs(ctx); err == nil && ids[id] && ids[skipFor] {
				return true, modelapi.JobResult{}, true
			}
		}
	}
	if row.Action() == catalog.ActionBlock {
		return false, problem(modelapi.CodeBlocked, row.BlockReason()), false
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
		return false, startRefusal(ctx, req, id, err, out), false
	}
	if err := d.Settle(ctx, out, cfg, target); err != nil {
		var stopped *lifecycle.StoppedError
		if errors.As(err, &stopped) {
			return false, problem(modelapi.CodeStopped, withOutput(lifecycle.StartErrorMessage(id, err), out)), false
		}
		return false, problem(modelapi.CodeFailed, withOutput(lifecycle.StartErrorMessage(id, err), out)), false
	}
	step(stepGateway)
	if err := d.Routed(ctx, id); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return false, problem(modelapi.CodeCancelled, fmt.Sprintf("cancelled while waiting for %s to answer through the gateway", id)), false
		}
		return false, problem(modelapi.CodeFailed, withOutput(fmt.Sprintf("%s started but does not answer through the gateway: %v", id, err), out)), false
	}
	return false, modelapi.JobResult{}, true
}

// partialFailure words a failed target of an alias start after earlier
// targets started this run (#690): nothing is rolled back, so the message says
// what is running and how to resume. A problem the client answers (occupied,
// others, occupancy_unknown) keeps its code and fields so the answer can be
// posted; only its message gains the prefix. Nothing started: res as is.
func partialFailure(alias string, started []string, res modelapi.JobResult) modelapi.JobResult {
	if len(started) == 0 || res.Error == nil {
		return res
	}
	p := *res.Error
	p.Message = partialText(alias, started, p.Message, p.Code)
	return modelapi.JobResult{Error: &p}
}

func partialText(alias string, started []string, msg, code string) string {
	if len(started) == 0 {
		return msg
	}
	tail := "; run the start again to resume (nothing is rolled back: " + strings.Join(started, ", ") + " keeps running)"
	if code == modelapi.CodeOccupied || code == modelapi.CodeOthers || code == modelapi.CodeOccupancyUnknown {
		tail = ""
	}
	return fmt.Sprintf("%s: started %s, then failed: %s%s", alias, strings.Join(started, ", "), msg, tail)
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
