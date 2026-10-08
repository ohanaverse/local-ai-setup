package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
)

// sessionCounts is the number of live wt sessions using each model id. A test
// seam; production sweeps dead sessions first, as the stop picker does.
var sessionCounts = func(ids []string) map[string]int {
	store := refcount.NewStore()
	_ = store.Sweep()
	return store.Counts(ids)
}

type startPlanUnload struct {
	ID       string `json:"id"`
	Sessions int    `json:"sessions"`
}

// startPlanJSON is `wt start <id> --plan --json`: what a start would do,
// decided from one inventory snapshot, with nothing changed.
type startPlanJSON struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"` // running | fits | would_unload | unknown
	WouldUnload []startPlanUnload `json:"would_unload"`
}

// startResultJSON is `wt start <id> --json` after a start.
type startResultJSON struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"` // started | already_running
	Unloaded []string `json:"unloaded"`
}

// runStartJSON implements `wt start <id> --json`, the form modelman drives. It
// never prompts: with plan it reports and changes nothing; without replace a
// start that would displace a running model prints the plan and fails; with
// replace it starts and reports what was unloaded. Both shapes are pinned by
// docs/contracts/wt-start-cli.sample.json.
func runStartJSON(out io.Writer, cfg *config.Config, id string, plan, replace bool) error {
	if id == "" {
		return errors.New("wt start --json needs a model id")
	}
	rows, snap := localRowsSnap(cfg)
	row, ok := catalog.Find(rows, id)
	if !ok {
		if reason := catalog.MissingReason(cfg, &snap, id); reason != "" {
			return errors.New(reason)
		}
		return fmt.Errorf("unknown model %q", id)
	}
	emit := func(v any) error { return json.NewEncoder(out).Encode(v) }
	switch row.Action() {
	case catalog.ActionLaunch:
		if plan {
			return emit(startPlanJSON{ID: id, Status: "running", WouldUnload: []startPlanUnload{}})
		}
		ensureRouteBeforeLaunch(cfg, row.Model)
		return emit(startResultJSON{ID: id, Status: "already_running", Unloaded: []string{}})
	case catalog.ActionBlock:
		return errors.New(row.BlockReason())
	}

	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName, ModelID: row.Model.ID}
	victims, known := lifecycle.Evictions(target, snap)
	p := startPlanJSON{ID: id, Status: "fits", WouldUnload: []startPlanUnload{}}
	switch {
	case !known:
		p.Status = "unknown"
	case len(victims) > 0:
		p.Status = "would_unload"
		ids := make([]string, len(victims))
		for i, v := range victims {
			ids[i] = v.ModelID
		}
		counts := sessionCounts(ids)
		for _, vid := range ids {
			p.WouldUnload = append(p.WouldUnload, startPlanUnload{ID: vid, Sessions: counts[vid]})
		}
	}
	if plan {
		return emit(p)
	}
	if p.Status != "fits" && !replace {
		if err := emit(p); err != nil {
			return err
		}
		if p.Status == "unknown" {
			return fmt.Errorf("cannot tell what starting %s would unload — rerun with --replace to confirm", id)
		}
		names := make([]string, len(p.WouldUnload))
		for i, u := range p.WouldUnload {
			names[i] = u.ID
		}
		return fmt.Errorf("starting %s would stop %s — rerun with --replace to confirm", id, strings.Join(names, ", "))
	}

	ctx, cancel := startSignalCtx()
	defer cancel()
	res := startResultJSON{ID: id, Status: "started", Unloaded: []string{}}
	opts := lifecycle.Options{
		AllowReplace: replace,
		OnUnloaded:   func(en localmodels.Entry) { res.Unloaded = append(res.Unloaded, en.ModelID) },
	}
	if err := lifecycleStart(ctx, cfg, target, opts); err != nil {
		return startFailure(id, err)
	}
	waitPendingRoutes()
	return emit(res)
}
