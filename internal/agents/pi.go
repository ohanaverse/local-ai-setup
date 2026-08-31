package agents

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("pi", func() Driver { return piDriver{} }) }

type piDriver struct{}

// Pi has no documented permission-bypass flag.
func (piDriver) YoloFlag() string { return "" }

// SyncModels adds any non-native models from cfg that are missing from pi's
// models.json, so rotation-selected models are always available to pi.
func (piDriver) SyncModels(cfg *config.Config) error {
	path, err := piModelsPath()
	if err != nil {
		return err
	}
	return syncModels(cfg, path)
}

// Build passes --model <ModelName> in normal mode or --model <ID> in litellm
// mode only when the model is present in pi's models.json and marked
// _launch: true. Otherwise it falls back to pi's default model and surfaces
// a warning.
func (piDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	lc := LaunchCmd{Bin: "pi"}
	if m.Native {
		return lc
	}
	path, err := piModelsPath()
	if err != nil {
		lc.Warn = fmt.Sprintf("pi: cannot locate models.json (%v), using default model", err)
		return lc
	}
	modelArg := m.ModelName
	if gw.IsLitellm() {
		modelArg = m.ID
	}
	if isLaunchable(modelArg, path) {
		lc.Args = append(lc.Args, "--model", modelArg)
	} else {
		lc.Warn = fmt.Sprintf("pi: model %q not configured for pi, using default model", modelArg)
	}
	return lc
}
