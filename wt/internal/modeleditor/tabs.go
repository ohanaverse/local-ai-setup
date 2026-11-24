package modeleditor

import (
	"context"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelsetup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// Tab is one of the screen's tabs.
type Tab int

const (
	// TabModels manages the models in the shared registry.toml. Each change
	// there is written at once.
	TabModels Tab = iota
	// TabAliases manages the registry's aliases (one gateway id for one
	// model or a priority group): the same registry write and the same
	// checks as `wt-model alias set|rm`. It is a tab of its own, not a mode
	// of the Models table, so that table stays what it is.
	TabAliases
	// TabSetup sets up and controls wt-model on this machine (local mode:
	// its LaunchAgent, token and the LiteLLM it fronts) or chooses native.
	// Nothing is written until a plan is confirmed.
	TabSetup
)

// tabNames are the tabs in the order they are drawn and switched.
var tabNames = []string{"Models", "Aliases", "Setup"}

// nextTab is the tab the tab key goes to: Models, Aliases, Setup, and round
// to Models again.
func nextTab(t Tab) Tab { return Tab((int(t) + 1) % len(tabNames)) }

// Options are what the Models screen is opened with.
type Options struct {
	// StartTab is the tab shown first.
	StartTab Tab
	// Models are the functions the Models tab reaches the machine through.
	// Run fills in the real ones for any left nil.
	Models ModelsDeps
	// Aliases are the functions the Aliases tab reaches the machine through,
	// beside Models.Load, which reads its config. Run fills in the defaults
	// for any left nil.
	Aliases AliasDeps
	// Setup are the functions the Setup tab reaches the machine through.
	// Run fills in the real ones for any left nil.
	Setup SetupDeps
	// UserHome is the home directory paths are abbreviated against; nil
	// means os.UserHomeDir. It is read once, in Run.
	UserHome func() (string, error)
}

// ModelsDeps are the Models tab's ways out of the program: reading the
// config, probing the providers, and what an add needs. Each is called from a
// tea.Cmd, never from Update, so a slow provider cannot freeze the screen.
// They are fields rather than package variables so that a test hands the tab
// a machine of its own.
type ModelsDeps struct {
	// Load re-reads config.toml and the registry (config.Load).
	Load func() (*config.Config, error)
	// Probe is one inventory round (localmodels.Inventory).
	Probe func(*config.Config) localmodels.Snapshot
	// SeedEnv describes the machine an add seeds provider rows for
	// (config.DefaultSeedEnv).
	SeedEnv func() config.SeedEnv
	// Capabilities asks ollama what a model can do
	// (modeladmin.OllamaCapabilities).
	Capabilities func(cfg *config.Config, name string) (map[string]any, error)
}

func (d ModelsDeps) withDefaults() ModelsDeps {
	if d.Load == nil {
		d.Load = config.Load
	}
	if d.Probe == nil {
		d.Probe = localmodels.Inventory
	}
	if d.SeedEnv == nil {
		d.SeedEnv = config.DefaultSeedEnv
	}
	if d.Capabilities == nil {
		d.Capabilities = modeladmin.OllamaCapabilities
	}
	return d
}

// AliasDeps are the Aliases tab's ways out of the program, beside
// ModelsDeps.Load (the config). Each is called from a tea.Cmd, never from
// Update, and each is a field rather than a package variable so that a test
// hands the tab a machine of its own.
type AliasDeps struct {
	// Routed is the alias ids LiteLLM's database has a row for, and a warning
	// when it could not be read. A nil map means unknown: the ROUTED column
	// shows "?". It reads ids only, never a row's params.
	Routed func(ctx context.Context, cfg *config.Config) (map[string]bool, string)
	// Sessions is the running model server's live client sessions per alias
	// id. An error means the server is running and its sessions could not be
	// read: the remove guard then refuses, as `alias rm` does without
	// --force-in-use. A nil map and no error means no server is running: the
	// column shows "-" and nothing is in use.
	Sessions func(ctx context.Context) (map[string]int, error)
	// Index is the tiers file, which the windows of cloud targets are read
	// from.
	Index func() tiers.Index
}

func (d AliasDeps) withDefaults() AliasDeps {
	if d.Routed == nil {
		d.Routed = func(context.Context, *config.Config) (map[string]bool, string) { return nil, "" }
	}
	if d.Sessions == nil {
		d.Sessions = func(context.Context) (map[string]int, error) { return nil, nil }
	}
	if d.Index == nil {
		d.Index = func() tiers.Index { return tiers.Index{} }
	}
	return d
}

// SetupDeps are the Setup tab's ways out of the program. Each is called from
// a tea.Cmd, never from Update.
type SetupDeps struct {
	// Observe reads the machine's model-server state.
	Observe func(ctx context.Context, o modelsetup.ObserveOptions) modelsetup.State
	// Plan turns a state and a wish into the steps that would apply it.
	Plan func(st modelsetup.State, want modelsetup.Desired) (modelsetup.Plan, error)
	// Apply runs a plan, reporting each step through progress.
	Apply func(ctx context.Context, p modelsetup.Plan, progress func(string)) modelsetup.Result
	// Control starts, stops or restarts a service.
	Control func(ctx context.Context, a modelsetup.ControlAction) error
}

// SetupDepsFromEnv builds the four functions over env, so a caller can
// inject its own Env (cmd/wt-model adds the routes it wants this way).
func SetupDepsFromEnv(env modelsetup.Env) SetupDeps {
	return SetupDeps{
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

func (d SetupDeps) withDefaults() SetupDeps {
	def := SetupDepsFromEnv(modelsetup.DefaultEnv())
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

// Result is what the screen did that its caller has to act on.
type Result struct {
	// RegistryChanged is true when the Models or Aliases tab wrote registry.toml. The
	// tab does not sync the LiteLLM routes itself — several changes in a row
	// would restart the proxy once each — so the caller runs one sync now.
	RegistryChanged bool
}

// tabBar is the screen's first line: every tab, the current one in brackets
// (and in the accent colour, where there is colour) so that it reads as
// current on a terminal without any.
func tabBar(theme themes.Theme, current Tab) string {
	return editorkit.TabBar(theme, tabNames, int(current))
}
