package agents

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// Build passes --model only when the target model is present in pi's
// models.json and marked _launch: true. Direct mode launches
// "<provider-id>/<ModelName>" from a pi provider named after the registry
// provider (ollama, openrouter, etc.); litellm mode launches
// "litellm/<registry-id>" from the wt-created litellm provider — pi splits
// --model on the first slash, so a registry id under the "ollama" provider
// could never be addressed and its bare form would be sent upstream (400 at
// the gateway). When the entry is missing, Build falls back to pi's default
// model and surfaces a warning.
func (piDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "pi"}
	if m.Native {
		return lc
	}
	path, err := piModelsPath()
	if err != nil {
		lc.Warn = fmt.Sprintf("pi: cannot locate models.json (%v), using default model", err)
		return lc
	}
	modelArg := r.ProviderID + "/" + r.ModelRef
	providerID := r.ProviderID
	launchID := r.ModelRef
	if r.Litellm {
		modelArg = piLitellmProviderID + "/" + r.ModelRef
		providerID = piLitellmProviderID
		launchID = r.ModelRef
	}
	if isLaunchable(providerID, launchID, path) {
		lc.Args = append(lc.Args, "--model", modelArg)
	} else {
		lc.Warn = fmt.Sprintf("pi: model %q not configured for pi, using default model", modelArg)
	}
	return lc
}
