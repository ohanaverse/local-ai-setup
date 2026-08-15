package agents

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("codex", func() Driver { return codexDriver{} }) }

type codexDriver struct{}

func (codexDriver) YoloFlag() string { return "--dangerously-bypass-approvals-and-sandbox" }

func (codexDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "codex"}
	if yolo {
		lc.Args = append(lc.Args, codexDriver{}.YoloFlag())
	}
	if !m.IsNative() {
		lc.Args = append(lc.Args, "--model", m.ID)
	}
	return lc
}
