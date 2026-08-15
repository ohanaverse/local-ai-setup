package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("pi", func() Driver { return piDriver{} }) }

type piDriver struct{}

// Pi has no documented permission-bypass flag.
func (piDriver) YoloFlag() string { return "" }

func (piDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "pi"}
	if !m.IsNative() {
		lc.Args = append(lc.Args, "--model", m.ID)
	}
	return lc
}
