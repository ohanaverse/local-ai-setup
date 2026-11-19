package configeditor

import (
	"context"

	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// Tab is one of the editor's tabs.
type Tab int

const (
	// TabAgents edits the agents in wt's config.toml. Its edits are buffered
	// and saved with ctrl+s.
	TabAgents Tab = iota
	// TabServer sets up and controls the model server (wt-model and the
	// LiteLLM it fronts).
	TabServer
)

// nextTab is the tab the tab key goes to: Agents, Model server, and round to
// Agents again.
func nextTab(t Tab) Tab { return Tab((int(t) + 1) % len(tabNames)) }

// Options are what `wt config` is opened with.
type Options struct {
	// StartTab is the tab shown first.
	StartTab Tab
	// Server are the functions the Model server tab reaches the machine
	// through. Run fills in the real ones for any left nil.
	Server ServerDeps
	// UserHome is the home directory paths are abbreviated against when the
	// observed state names none; nil means os.UserHomeDir. It is read once,
	// in Run.
	UserHome func() (string, error)
}

// ServerDeps are the Model server tab's ways out of the program. Each is
// called from a tea.Cmd, never from Update.
type ServerDeps struct {
	// Observe reads the machine's model-server state.
	Observe func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State
	// Plan turns a state and a wish into the steps that would apply it.
	Plan func(st modelsetup.State, want modelsetup.Desired) (modelsetup.Plan, error)
	// Apply runs a plan, reporting each step through progress.
	Apply func(ctx context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result
	// Control starts, stops or restarts a service.
	Control func(ctx context.Context, a modelsetup.ControlAction) error
}

// ServerDepsFromEnv builds the four functions over env, so a caller can
// inject its own Env (cmd/wt-session adds the routes it wants this way).
func ServerDepsFromEnv(env modelsetup.Env) ServerDeps {
	return ServerDeps{
		Observe: func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State {
			return modelsetup.Observe(ctx, env, o)
		},
		Plan: modelsetup.BuildPlan,
		Apply: func(ctx context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result {
			return modelsetup.Apply(ctx, env, p, progress)
		},
		Control: func(ctx context.Context, a modelsetup.ControlAction) error {
			return modelsetup.Control(ctx, env, a)
		},
	}
}

func (d ServerDeps) withDefaults() ServerDeps {
	def := ServerDepsFromEnv(modelsetup.DefaultEnv())
	if d.Observe == nil {
		d.Observe = def.Observe
	}
	if d.Plan == nil {
		d.Plan = def.Plan
	}
	if d.Apply == nil {
		d.Apply = def.Apply
	}
	if d.Control == nil {
		d.Control = def.Control
	}
	return d
}

// Result is what the editor did that its caller has to act on: nothing,
// since the Models tab moved to internal/modeleditor.
type Result struct{}

// tabNames are the tabs in the order they are drawn and switched.
var tabNames = []string{"Agents", "Model server"}

// tabBar is the editor's first line: every tab, the current one in brackets
// (and in the accent colour, where there is colour) so that it reads as
// current on a terminal without any.
func tabBar(theme themes.Theme, current Tab) string {
	return editorkit.TabBar(theme, tabNames, int(current))
}
