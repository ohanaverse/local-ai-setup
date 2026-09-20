package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// probeInventory is a test seam: production probes the live local-model
// inventory. cmd/wt's TestMain stubs it to an empty snapshot so no test dials
// the developer's real ollama/omlx/mtplx servers, and tests that need a
// verdict stub it with their own snapshot.
var probeInventory = localmodels.Inventory

// allowReplace is the process-wide --replace mode: set once from the root
// command's flag and read here. It is not a parameter because it is CLI mode
// state, not per-launch state — threading it through launchFiltered (and its
// dozen test call sites) would be churn for a flag only this function reads.
var allowReplace bool

// errCommandAgent is the sentinel returned by resolveModel when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")

// resolveModel computes the single model to launch for a non-TUI flow and
// returns the LAUNCHABLE list (cloud models plus local models already
// running) so callers do not recompute it and rotation can never land on a
// local model that is not up.
// agent is the resolved agent name (from -A; main routes unpinned launches
// through the agent picker, so launchFiltered never sees an empty agent).
// tags and family are the -T/-F flag values (comma-delimited).
// pinned is the -M flag value ("" = not pinned).
//
// Behavior:
//   - command agent → errCommandAgent, nil launchable
//   - pinned != "" → the pin's row decides: launch → return it; start →
//     start it through startForLaunch, then return it; block → error carrying
//     the row's block reason. A pin absent from the rows → "not in the
//     eligible list".
//   - no pin, empty launchable → error (pin-the-model wording when rows
//     existed, generic "no models match" when none did)
//   - no pin, one launchable → return it
//   - no pin, several launchable → "multiple models match"
//
// Note: rotation lives outside this function. launchFiltered catches
// the "multiple models match" error and advances through the global
// rotation when pinned == "".
func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error) {
	if agents.IsCommand(agent) {
		return config.Model{}, nil, errCommandAgent
	}
	eligible, err := cfg.EligibleModels(agent, tags, family)
	if err != nil {
		return config.Model{}, nil, err
	}

	snap := probeInventory(cfg)
	rows := catalog.Build(catalog.Input{
		Config: cfg, Agent: agent, Models: eligible, Inventory: &snap,
		// Discovered rows carry no tags, so when -T/-F are active they are
		// dropped — the same rule the picker applies (app.go's
		// hideDiscovered), so rotation can never land on a model the
		// filter cannot describe.
		HideDiscovered: tags != "" || family != "",
	})
	launchable := launchableModels(rows)

	// A -M pin is looked up among ALL rows, discovered ones included: the
	// launch path accepts a model the registry does not name.
	if pinned != "" {
		row, ok := catalog.Find(rows, pinned)
		if !ok {
			return config.Model{}, launchable, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
		}
		switch row.Action() {
		case catalog.ActionLaunch:
			return row.Model, launchable, nil
		case catalog.ActionStart:
			if err := startModel(cfg, row, allowReplace); err != nil {
				return config.Model{}, launchable, err
			}
			return row.Model, launchable, nil
		}
		return config.Model{}, launchable, fmt.Errorf("%s", row.BlockReason())
	}

	if len(launchable) == 0 {
		if len(rows) == 0 {
			return config.Model{}, nil, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
		}
		// Models matched; none is usable without starting one. Say which
		// fix applies instead of the generic wording, which would send the
		// operator hunting for a -T/-F/config problem that isn't there.
		return config.Model{}, launchable, fmt.Errorf(
			"no cloud or running local model for agent %q — start one with `wt -M <id>`", agent)
	}
	m, err := resolveModelFromEligible(agent, launchable)
	return m, launchable, err
}

// launchableModels narrows rows to the models a launch can use right now:
// cloud rows plus local rows the probe reports as running. A start row is
// deliberately excluded — rotation and auto-resolution must never start a
// server, only a -M pin may.
func launchableModels(rows []catalog.Row) []config.Model {
	var out []config.Model
	for _, r := range rows {
		if r.Action() == catalog.ActionLaunch {
			out = append(out, r.Model)
		}
	}
	return out
}

// resolveModelFromEligible resolves the single model to launch from a
// precomputed launchable list, applying the ambiguity rule without
// recomputing anything. Callers that already hold the slice
// (launchFilteredImpl, resolveModelForLaunch) use this to avoid a second
// EligibleModels call. The list must be non-empty; the empty case is handled
// by resolveModel before this is called.
//
// There is no pinned branch: a -M pin is resolved against ALL rows in
// resolveModel, so by the time a pin reaches here it is already a launch row
// and the list holds it.
func resolveModelFromEligible(agent string, launchable []config.Model) (config.Model, error) {
	if len(launchable) > 1 {
		return config.Model{}, fmt.Errorf("multiple models match for agent %q", agent)
	}
	return launchable[0], nil
}
