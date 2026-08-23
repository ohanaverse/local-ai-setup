package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// dsh-webui is the browser TUI (dsh's default run mode): model-driven via
// `ollama launch dsh --model <name>`. It is an agent (not a command) because
// wt pins the model in the launch command, so it goes through the model
// picker like any other model-driven agent.
type dshWebuiDriver struct {
	args []string // passthrough args after --
}

func init() {
	register("dsh-webui", func() Driver { return &dshWebuiDriver{} })
}

// SetArgs stores the user's passthrough args. Called by BuildLaunchCmd via
// the ArgSetter interface before Build, so the driver can place them after
// the `--` separator rather than having them appended as launcher flags.
func (d *dshWebuiDriver) SetArgs(args []string) { d.args = args }

func (d *dshWebuiDriver) Build(m config.Model, _ bool) LaunchCmd {
	// Pass the bare model name; m.ModelName carries just the model name
	// without the provider/ prefix (e.g. "deepseek-v4-pro:cloud").
	return dshLaunch("", m.ModelName, d.args)
}

// YoloFlag returns empty — dsh has no skip-permissions flag on ollama launch.
func (d *dshWebuiDriver) YoloFlag() string { return "" }

var _ Driver = (*dshWebuiDriver)(nil)
