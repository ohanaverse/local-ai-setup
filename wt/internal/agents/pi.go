package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

func init() { register("pi", func() Driver { return piDriver{} }) }

type piDriver struct{}

// Pi has no documented permission-bypass flag.
func (piDriver) YoloFlag() string { return "" }

// SyncGateway writes pi's models.json for a gateway launch: only the "litellm"
// provider block (gateway origin + token, one entry per non-native catalog
// model keyed by its gateway id), no provider origin or key. It removes every
// _wtOwned block for another provider, the leftovers of the direct rewrite,
// and leaves a user's own block (no _wtOwned) alone. The file holds the
// token, so it is written 0600, temp file then rename, and tightened even when
// nothing else changed.
func (piDriver) SyncGateway(gw Gateway, models []config.Model, target config.Model) error {
	path, err := piModelsPath()
	if err != nil {
		return err
	}
	return syncGateway(gw, path, models, target)
}

// Build passes --model only when the target model is present in pi's
// models.json and marked _launch: true. A gateway launch runs
// "litellm/<gateway-id>" from the wt-created litellm provider (the only one
// pi's file points at the gateway with) — pi splits --model on the first
// slash, so a registry id under a provider named like its first segment
// could never be addressed. When the entry is missing, Build falls back to
// pi's default model, surfaces a warning and sets ModelFallback (which wt
// smoke turns into a FAIL). SyncGateway writes the launch target's entry
// first, so this is left for a user-disabled entry (_launch: false) or an
// unreadable models.json.
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
	providerID := piLitellmProviderID
	modelArg := providerID + "/" + r.ModelRef
	if isLaunchable(providerID, r.ModelRef, path) {
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

// piSessionDirChars are what pi's session manager turns into a dash when it
// names a working directory's session directory.
var piSessionDirChars = strings.NewReplacer("/", "-", `\`, "-", ":", "-")

// StateDir is pi's session directory for a working directory:
// <agent dir>/sessions/--<path>--, the agent dir being PI_CODING_AGENT_DIR or
// ~/.pi/agent.
func (piDriver) StateDir(path string) string {
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		agentDir = filepath.Join(os.Getenv("HOME"), ".pi", "agent")
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		path = path[1:]
	}
	return filepath.Join(agentDir, "sessions", "--"+piSessionDirChars.Replace(path)+"--")
}

// OneShotArgs runs a single prompt non-interactively and exits — used by
// wt smoke to verify a model works through this agent.
func (piDriver) OneShotArgs(prompt string) []string { return []string{"-p", prompt} }
