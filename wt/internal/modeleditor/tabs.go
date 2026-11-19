package modeleditor

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/editorkit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// Tab is one of the screen's tabs.
type Tab int

const (
	// TabModels manages the models in the shared registry.toml. Each change
	// there is written at once.
	TabModels Tab = iota
)

// tabNames are the tabs in the order they are drawn.
var tabNames = []string{"Models"}

// Options are what the Models screen is opened with.
type Options struct {
	// StartTab is the tab shown first.
	StartTab Tab
	// Models are the functions the Models tab reaches the machine through.
	// Run fills in the real ones for any left nil.
	Models ModelsDeps
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

// Result is what the screen did that its caller has to act on.
type Result struct {
	// RegistryChanged is true when the Models tab wrote registry.toml. The
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
