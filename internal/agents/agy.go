package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("agy", func() Driver { return agyDriver{} }) }

type agyDriver struct{}

func (agyDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

// agy does not accept --model on the CLI; model selection happens inside its
// TUI via /model. The model argument is ignored.
func (agyDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	lc := LaunchCmd{Bin: "agy"}
	if yolo {
		lc.Args = append(lc.Args, agyDriver{}.YoloFlag())
	}
	return lc
}
