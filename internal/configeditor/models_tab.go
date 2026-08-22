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

// modelItem adapts config.Model to list.Item.
type modelItem struct {
	model config.Model
	cfg   *config.Config
}

func (m modelItem) FilterValue() string { return m.model.ID }
func (m modelItem) Title() string {
	family := m.model.Family
	if family == "" {
		family = "-"
	}
	loc := m.model.Location
	if loc == "" && m.cfg != nil {
		if resolved, err := m.cfg.ResolveLocation(m.model); err == nil {
			loc = resolved
		}
	}
	locStr := string(loc)
	if locStr == "" {
		locStr = "-"
	}
	tags := strings.Join(m.model.Tags, ", ")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%s  %s  %s  %s",
		m.model.ID,
		family,
		strings.ToUpper(locStr),
		tags,
	)
}
func (m modelItem) Description() string { return "" }

// buildModelsList constructs the models list sorted by provider, family, name.
func buildModelsList(theme themes.Theme, width, height int, cfg *config.Config) list.Model {
	items := make([]list.Item, 0, len(cfg.Models))
	for _, m := range cfg.Models {
		items = append(items, modelItem{model: m, cfg: cfg})
	}
	sort.SliceStable(items, func(i, j int) bool {
		mi := items[i].(modelItem).model
		mj := items[j].(modelItem).model
		if mi.ProviderID != mj.ProviderID {
			return mi.ProviderID < mj.ProviderID
		}
		if mi.Family != mj.Family {
			return mi.Family < mj.Family
		}
		return mi.ModelName < mj.ModelName
	})
	l := list.New(items, tui.ThemedListDelegate(theme), width, height)
	l.Title = "Models"
	l.SetShowStatusBar(false)
	return l
}
