package sessioneditor

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// Tab is one of the editor's tabs.
type Tab int

const (
	// TabAgents edits the agents in wt's config.toml. Its edits are buffered
	// and saved with ctrl+s.
	TabAgents Tab = iota
	// TabMode chooses how this session reaches a model server (native,
	// remote, or a pointer to wt-model on this machine).
	TabMode
)

// nextTab is the tab the tab key goes to, in tabNames order and round again.
func nextTab(t Tab) Tab { return Tab((int(t) + 1) % len(tabNames)) }

// Options are what `wt config` is opened with.
type Options struct {
	// StartTab is the tab shown first.
	StartTab Tab
	// Mode are the Mode tab's deps; a nil func is filled from the real
	// machine (ModeDeps.withDefaults, in Run).
	Mode ModeDeps
	// LookPath finds a program on PATH (wt-model, for the offer to run
	// `wt-model setup`); nil means exec.LookPath. It is resolved once, in Run.
	LookPath func(string) (string, error)
	// UserHome is the home directory paths are abbreviated against; nil
	// means os.UserHomeDir. It is read once, in Run.
	UserHome func() (string, error)
}

// Result is what the editor did that its caller has to act on.
type Result struct {
	// RunModelSetup asks the caller to run `wt-model setup` once the editor
	// has closed; ModelSetup is the path of the wt-model it found.
	RunModelSetup bool
	ModelSetup    string
}

// tabNames are the tabs in the order they are drawn and switched.
var tabNames = []string{"Agents", "Mode"}

// tabBar is the editor's first line: every tab, the current one in brackets
// (and in the accent colour, where there is colour) so that it reads as
// current on a terminal without any.
func tabBar(theme themes.Theme, current Tab) string {
	return editorkit.TabBar(theme, tabNames, int(current))
}
