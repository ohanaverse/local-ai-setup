package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// dsh-headless maps to --profile headless, a one-shot CLI that submits a
// single task, prints the final answer to stdout, and exits.
type dshHeadlessDriver struct {
	args []string // passthrough args after --
}

func init() {
	register("dsh-headless", func() Driver { return &dshHeadlessDriver{} })
}

// SetArgs stores the user's passthrough args. Called by BuildLaunchCmd via
// the ArgSetter interface before Build, so the driver can place them after
// the `--` separator rather than having them appended as launcher flags.
func (d *dshHeadlessDriver) SetArgs(args []string) { d.args = args }

func (d *dshHeadlessDriver) Build(_ config.Model, _ bool) LaunchCmd {
	// The profile selector is a dsh flag, so dshLaunch places it after the
	// `--` separator; the task string (positional arg) is forwarded after it.
	return dshLaunch("headless", "", d.args)
}

// IsCommand marks dsh-headless as a command (no model layer). wt skips the
// model picker / rotation for command agents.
func (d *dshHeadlessDriver) IsCommand() bool { return true }

func (d *dshHeadlessDriver) YoloFlag() string { return "" }

var _ Driver = (*dshHeadlessDriver)(nil)
