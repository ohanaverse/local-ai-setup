package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// shellDriver launches a shell command (or interactive bash) in the selected
// worktree. It is the Go equivalent of the legacy bash shell-wt wrapper.
// Unlike other agents, shell has no model rotation, no yolo flag, and no
// session resume. The user's passthrough args (after --) are exec'd
// directly as argv (no shell involved), so metacharacters like `|`/`>`/`&&`
// are never interpreted; use `shell-wt -- bash -lc 'cmd1 | cmd2'` for
// pipelines. With no args, an interactive bash shell opens.
type shellDriver struct {
	args []string // set via SetArgs before Build
}

func init() {
	register("shell", func() Driver { return &shellDriver{} })
}

// SetArgs stores the user's passthrough args. Called by BuildLaunchCmd via
// the ArgSetter interface before Build.
func (d *shellDriver) SetArgs(args []string) {
	d.args = args
}

// Build returns a LaunchCmd that execs the user's command directly, or an
// interactive bash shell when no command was given. The model parameter is
// ignored — shell does not use models.
func (d *shellDriver) Build(_ config.Model, _ bool) LaunchCmd {
	if len(d.args) > 0 {
		return LaunchCmd{
			Bin:  d.args[0],
			Args: d.args[1:],
		}
	}
	return LaunchCmd{Bin: "bash"}
}

// YoloFlag returns empty — shell has no permission-skip concept.
func (d *shellDriver) YoloFlag() string { return "" }

// IsCommand marks shell as a command (no model layer) rather than an
// agent. Used by IsCommand via the Commanded optional interface.
func (d *shellDriver) IsCommand() bool { return true }
