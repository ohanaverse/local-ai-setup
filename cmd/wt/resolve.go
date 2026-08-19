package main

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// errCommandAgent is the sentinel returned by resolveModel when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")

// resolveModel computes the single model to launch for a non-TUI flow.
// agent is the resolved agent name (from -A or cfg.DefaultAgent).
// tags and family are the -T/-F flag values (comma-delimited).
// pinned is the -M flag value ("" = not pinned).
//
// Behavior:
//   - command agent → errCommandAgent
//   - pinned != "" and not in eligible → error
//   - len(eligible) == 0 → error "no models match"
//   - len(eligible) == 1 → return it
//   - len(eligible) > 1 and pinned != "" → return pinned
//   - len(eligible) > 1 and pinned == "" and no -T/-F → defaultModel fallback
//     (preserves the pre-flag-surface behavior so `wt -W foo --agent claude`
//     still launches instead of erroring)
//   - len(eligible) > 1 and pinned == "" and -T/-F supplied → error "specify -M"
//     (the user opted into filtering; an ambiguous result must be pinned)
//
// Note: rotation lives outside this function. PR 3 will replace the
// defaultModel fallback with rotation through the eligible list.
func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, error) {
	if agents.IsCommand(agent) {
		return config.Model{}, errCommandAgent
	}
	eligible, err := cfg.EligibleModels(agent, tags, family)
	if err != nil {
		return config.Model{}, err
	}
	if len(eligible) == 0 {
		return config.Model{}, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
	}
	if pinned != "" {
		for _, m := range eligible {
			if m.ID == pinned {
				return m, nil
			}
		}
		return config.Model{}, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
	}
	if len(eligible) > 1 {
		// When the user supplied no filters at all, fall back to the
		// pre-flag-surface default so existing invocations keep launching
		// rather than erroring. Only demand -M when the user opted into
		// -T/-F and the filtered set is still ambiguous.
		if len(config.ParseFilterList(tags)) == 0 && len(config.ParseFilterList(family)) == 0 {
			return defaultModel(cfg, agent), nil
		}
		return config.Model{}, fmt.Errorf("multiple models match for agent %q with tags %q and family %q; specify -M to pin one", agent, tags, family)
	}
	return eligible[0], nil
}
