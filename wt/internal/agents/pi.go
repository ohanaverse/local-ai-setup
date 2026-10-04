package agents

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

func init() { register("pi", func() Driver { return piDriver{} }) }

type piDriver struct{}

// Pi has no documented permission-bypass flag.
func (piDriver) YoloFlag() string { return "" }

func (piDriver) Protocols() []Protocol { return []Protocol{config.ProtocolOpenAIChat} }

// SyncModels adds any non-native models from cfg that are missing from pi's
// models.json, so rotation-selected models are always available to pi.
// target is the model this launch is about to use (see Syncer's doc
// comment): it is synced too even when cfg does not hold it (a discovered
// local model has no registry entry), and syncDirectProviders uses it to
// decide which provider's secret_ref failure, if any, should actually fail
// this launch. r is target's resolved route — the same one Build gets — so
// the entry is written where Build will look for it (see syncModels).
func (piDriver) SyncModels(cfg *config.Config, target config.Model, r Route) error {
	path, err := piModelsPath()
	if err != nil {
		return err
	}
	return syncModels(cfg, path, target, r)
}

// Build passes --model only when the target model is present in pi's
// models.json and marked _launch: true. Direct mode launches
// "<provider-id>/<ModelName>" from a pi provider named after the registry
// provider (ollama, openrouter, etc.); litellm mode launches
// "litellm/<registry-id>" from the wt-created litellm provider — pi splits
// --model on the first slash, so a registry id under the "ollama" provider
// could never be addressed and its bare form would be sent upstream (400 at
// the gateway). When the entry is missing, Build falls back to pi's default
// model, surfaces a warning and sets ModelFallback (which wt smoke turns into
// a FAIL). SyncModels writes the launch target's entry first, so this is left
// for a direct route with no models.json yet (the sync creates the file only
// for a LiteLLM route), a user-disabled entry (_launch: false), a provider
// with no usable base_url/secret, or an unreadable models.json.
func (piDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "pi"}
	if m.Native {
		return lc
	}
	path, err := piModelsPath()
	if err != nil {
		lc.Warn = fmt.Sprintf("pi: cannot locate models.json (%v), using default model", err)
		lc.ModelFallback = true
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
		lc.ModelFallback = true
	}
	return lc
}

// ProfileMechanisms declares the profile mechanisms pi supports: wrapper
// (the little-coder integration) and env. There is no pi-specific env
// lever, but profile env lands on the launched process (cmd/wt applies
// env before the wrapper), and with the little-coder wrapper that process
// is little-coder itself — which reads LITTLE_CODER_* env vars.
func (piDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismWrapper, profiles.MechanismEnv}
}

// RequiredMechanism declares that env has no effect on pi without wrapper
// on the same profile entry: bare pi has no env lever of its own, only the
// little-coder wrapper reads LITTLE_CODER_* vars. See profiles.Validate.
func (piDriver) RequiredMechanism(m profiles.Mechanism) (profiles.Mechanism, bool) {
	if m == profiles.MechanismEnv {
		return profiles.MechanismWrapper, true
	}
	return "", false
}

// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (piDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
