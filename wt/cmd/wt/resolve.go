package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// errCommandAgent is the sentinel returned by resolveModel when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")

// resolveModel computes the single model to launch for a non-TUI flow and
// returns the full eligible list so callers do not recompute it.
// agent is the resolved agent name (from -A; main routes unpinned launches
// through the agent picker, so launchFiltered never sees an empty agent).
// tags and family are the -T/-F flag values (comma-delimited).
// pinned is the -M flag value ("" = not pinned).
//
// Behavior:
//   - command agent → errCommandAgent, nil eligible
//   - pinned != "" and not in eligible → error
//   - len(eligible) == 0 → error "no models match"
//   - len(eligible) == 1 → return it
//   - len(eligible) > 1 and pinned != "" → return pinned
//   - len(eligible) > 1 and pinned == "" → error "multiple models match"
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
	if len(eligible) == 0 {
		return config.Model{}, eligible, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
	}
	m, err := resolveModelFromEligible(agent, eligible, pinned)
	return m, eligible, err
}

// resolveModelFromEligible resolves the single model to launch from a
// precomputed eligible list, applying the -M pin and ambiguity rules without
// recomputing cfg.EligibleModels. Callers that already hold the eligible
// slice (launchFilteredImpl, resolveModelForLaunch) use this to avoid a
// second EligibleModels call. The eligible list must be non-empty; the
// "no models match" case is handled by resolveModel before this is called.
func resolveModelFromEligible(agent string, eligible []config.Model, pinned string) (config.Model, error) {
	if pinned != "" {
		for _, m := range eligible {
			if m.ID == pinned {
				return m, nil
			}
		}
		return config.Model{}, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
	}
	if len(eligible) > 1 {
		return config.Model{}, fmt.Errorf("multiple models match for agent %q", agent)
	}
	return eligible[0], nil
}
