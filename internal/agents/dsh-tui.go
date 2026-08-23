package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// dsh-tui maps to --profile tui. Requires the turtle-ui plugin:
//
//	dsh plugin --profile tui add github:deepseek-ai/turtle-ui
type dshTuiDriver struct {
	args []string // passthrough args after --
}

func init() {
	register("dsh-tui", func() Driver { return &dshTuiDriver{} })
}

// SetArgs stores the user's passthrough args. Called by BuildLaunchCmd via
// the ArgSetter interface before Build, so the driver can place them after
// the `--` separator rather than having them appended as launcher flags.
func (d *dshTuiDriver) SetArgs(args []string) { d.args = args }

func (d *dshTuiDriver) Build(_ config.Model, _ bool) LaunchCmd {
	// The profile selector is a dsh flag, so dshLaunch places it after the
	// `--` separator; the model is reused from dsh's stored settings.
	return dshLaunch("tui", "", d.args)
}

// IsCommand marks dsh-tui as a command (no model layer). wt skips the model
// picker / rotation for command agents.
func (d *dshTuiDriver) IsCommand() bool { return true }

func (d *dshTuiDriver) YoloFlag() string { return "" }

var _ Driver = (*dshTuiDriver)(nil)
