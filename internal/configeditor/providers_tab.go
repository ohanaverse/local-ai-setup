package configeditor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/ohanaverse/agent-worktree/internal/tui"
)

// providerItem adapts config.Provider to list.Item.
type providerItem struct {
	provider config.Provider
}

func (p providerItem) FilterValue() string { return p.provider.ID }
func (p providerItem) Title() string {
	auth := p.provider.Auth.Type
	if auth == "" {
		auth = "-"
	}
	url := p.provider.Auth.BaseURL
	if url == "" {
		url = "-"
	}
	return fmt.Sprintf("%s  %s  %s  %s",
		p.provider.ID,
		strings.ToUpper(string(p.provider.Location)),
		auth,
		url,
	)
}
func (p providerItem) Description() string { return "" }

// buildProvidersList constructs the providers list sorted by ID.
func buildProvidersList(theme themes.Theme, width, height int, cfg *config.Config) list.Model {
	items := make([]list.Item, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		items = append(items, providerItem{provider: p})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].(providerItem).provider.ID < items[j].(providerItem).provider.ID
	})
	l := list.New(items, tui.ThemedListDelegate(theme), width, height)
	l.Title = "Providers"
	l.SetShowStatusBar(false)
	return l
}
