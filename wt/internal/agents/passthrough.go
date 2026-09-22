package agents

import (
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// IsConfigured reports whether name has an agent entry in cfg, and therefore
// a model catalog to resolve a launch against. Commands (e.g. shell) are
// always "configured": they have no model layer and need no entry. A nil
// cfg (only ever produced by a caller working around a config load failure)
// is never configured for a model-driven agent.
func IsConfigured(cfg *config.Config, name string) bool {
	if IsCommand(name) {
		return true
	}
	if cfg == nil {
		return false
	}
	_, err := cfg.AgentByName(name)
	return err == nil
}

// passthroughModel is the sentinel model for a bare launch. Every
// model-driven driver's native branch treats it as "no model override": no
// --model, no gateway env. ModelName "native" is the value drivers already
// use to mean "launch bare" (claude/copilot add --model or COPILOT_MODEL
// only for a specific *named* native model, e.g. claude/opus).
func passthroughModel() config.Model {
	return config.Model{Native: true, ModelName: "native"}
}

// BuildPassthroughCmd builds the command that launches agent with no model
// routing — equivalent to running the installed binary directly, plus the
// agent's yolo flag and the passthrough args. Used when an agent has no
// config.toml entry (issue #147): the whole config being absent
// (config.ErrRegistryMissing) is just the extreme case of "no entry".
//
// Passing nil cfg is safe: BuildLaunchCmd substitutes an empty Config,
// ResolveRoute early-returns for a Native model, and a Syncer driver (pi)
// syncs against zero models — a no-op that does not create its models.json
// when the file does not already exist and LiteLLM routing is off.
func BuildPassthroughCmd(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error) {
	return BuildLaunchCmd(agent, passthroughModel(), worktreePath, yolo, nil, nil, extraArgs)
}
