package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// newUsageStore is a seam for tests: production uses realNewUsageStore (the
// default config dir); tests swap it to isolate from the real usage.jsonl.
var newUsageStore = realNewUsageStore

// realNewUsageStore is the production implementation of the newUsageStore
// seam: a Store rooted at the default config dir.
func realNewUsageStore() *usage.Store { return usage.NewStore() }

// modelItem adapts a config.Model to a list.Item for the model picker.
type modelItem struct {
	model       config.Model
	counts      usage.UsageCounts
	familyCount int // 30-day launches for the model's family
}

// FilterValue returns the model ID so the list's built-in fuzzy filter
// (currently unused) would narrow by ID.
func (m modelItem) FilterValue() string { return m.model.ID }

// Title renders the model ID — the primary identifier users scan for.
func (m modelItem) Title() string { return m.model.ID }

// Description renders the metadata columns: provider, location, tags, family
// usage, and 1d/7d/30d model usage. Family usage helps the user see the sort
// basis and spot family-selection bias.
func (m modelItem) Description() string {
	tags := strings.Join(m.model.Tags, ",")
	if tags == "" {
		tags = "-"
	}
	return fmt.Sprintf("%-10s %-6s %-20s fam:%-3d 1d:%-3d 7d:%-3d 30d:%-3d",
		m.model.ProviderID,
		string(m.model.Location),
		tags,
		m.familyCount,
		m.counts.OneDay,
		m.counts.SevenDay,
		m.counts.ThirtyDay,
	)
}

// buildModelList builds a bubble/list from the given models. The caller
// passes the desired width/height. The list is created with a themed
// delegate (ThemedListDelegate) so the picker honors the active color
// theme, and a fixed title. counts is attached to each item for display.
func buildModelList(models []config.Model, counts map[string]usage.UsageCounts, theme themes.Theme, width, height int) list.Model {
	items := make([]list.Item, 0, len(models))
	for _, m := range models {
		items = append(items, modelItem{model: m, counts: counts[m.ID]})
	}
	l := list.New(items, ThemedListDelegate(theme), width, height)
	l.Title = "Models"
	l.SetShowStatusBar(false)
	return l
}

// indexOfModelID returns the index of the model with the given ID in models,
// or -1 if not found. Used to validate a pinned -M model against the agent's
// eligible list, and to position the list cursor on the rotation's
// next-to-use model.
func indexOfModelID(models []config.Model, id string) int {
	for i, m := range models {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// otherFamily is the display label for models whose Family is empty.
const otherFamily = "— other"

// familyDividerLabel renders a family-group header: the family name (or
// otherFamily when empty) plus its 30-day launch count.
func familyDividerLabel(family string, thirtyDay int) string {
	if family == "" {
		family = otherFamily
	}
	return fmt.Sprintf("◈ %s · 30d:%d", family, thirtyDay)
}

// dividerItem is a non-selectable family header row in the model picker. It
// renders the family name plus its 30-day count, separating model rows of
// different families. It is never a launch target.
type dividerItem struct {
	label string
}

// FilterValue returns "" so dividers never match the list's fuzzy filter
// (filtering shows only model rows).
func (d dividerItem) FilterValue() string { return "" }

// modelListDelegate wraps ThemedListDelegate so dividerItem rows render as
// unhighlighted family headers regardless of the cursor position. Model rows
// fall through to the themed DefaultDelegate. Embedding the value (not a
// pointer) preserves Height/Spacing/Update/ShortHelp/FullHelp from
// DefaultDelegate, all of which have value receivers.
type modelListDelegate struct {
	list.DefaultDelegate
	headerStyle lipgloss.Style
}

func (d modelListDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	if dv, ok := item.(dividerItem); ok {
		// No trailing newline: populatedView joins rows with its own
		// Spacing()+1 newlines, matching DefaultDelegate's "title\ndesc"
		// (no trailing newline) contract. A trailing "\n" here would double
		// the blank-line gap under every family header.
		_, _ = w.Write([]byte(d.headerStyle.Render(dv.label)))
		return
	}
	d.DefaultDelegate.Render(w, m, index, item)
}

// sortModelsByUsage sorts models in place (stable) descending by family
// composite score, then family first-occurrence order, then model composite
// score. Same-family models end up adjacent and higher-usage families float
// to the top. familyCounts and modelCounts come from usage.Store (missing
// entries read as zero). The first-occurrence tie-break is what guarantees
// adjacency: relying on sort.SliceStable's stability alone only preserves
// registry order on a score tie, and the registry does not list one
// family's models contiguously — without this key, a tie (e.g. every score
// 0 on a fresh install with no usage.jsonl) would split a family's models
// across two non-adjacent runs, producing a duplicate divider header for it.
// First-occurrence (rather than alphabetical) keeps the pre-existing visible
// order for the common single-score-tier case and avoids a surprise
// reordering where the empty "other" family would otherwise sort first.
func sortModelsByUsage(models []config.Model, familyCounts, modelCounts map[string]usage.UsageCounts) {
	firstSeen := make(map[string]int, len(models))
	for i, m := range models {
		if _, ok := firstSeen[m.Family]; !ok {
			firstSeen[m.Family] = i
		}
	}
	sort.SliceStable(models, func(i, j int) bool {
		fi := usage.CompositeScore(familyCounts[models[i].Family])
		fj := usage.CompositeScore(familyCounts[models[j].Family])
		if fi != fj {
			return fi > fj
		}
		if models[i].Family != models[j].Family {
			return firstSeen[models[i].Family] < firstSeen[models[j].Family]
		}
		return usage.CompositeScore(modelCounts[models[i].ID]) >
			usage.CompositeScore(modelCounts[models[j].ID])
	})
}

// withFamilyDividers returns list items for the (already usage-sorted) models,
// inserting a dividerItem before each distinct family group. Each model row
// carries its own 1d/7d/30d counts plus its family's 30-day count for display.
func withFamilyDividers(models []config.Model, modelCounts, familyCounts map[string]usage.UsageCounts) []list.Item {
	items := make([]list.Item, 0, len(models)*2)
	prevFamily := ""
	havePrev := false
	for _, m := range models {
		fam := m.Family
		if !havePrev || fam != prevFamily {
			items = append(items, dividerItem{label: familyDividerLabel(fam, familyCounts[fam].ThirtyDay)})
			prevFamily = fam
			havePrev = true
		}
		items = append(items, modelItem{
			model:       m,
			counts:      modelCounts[m.ID],
			familyCount: familyCounts[fam].ThirtyDay,
		})
	}
	return items
}

// buildModelListWithFamilies builds the usage-sorted, family-grouped model
// picker list. It sorts eligible models by family-then-model usage, inserts
// family divider headers, and returns the list plus a modelID→list-index map
// so callers can position the cursor (bypassing divider rows). familyOf maps
// the FULL catalog's model IDs to their families for family-count
// aggregation, so a family's total usage is accurate even when a tag/family
// filter narrows the eligible set.
func buildModelListWithFamilies(models []config.Model, familyOf map[string]string, theme themes.Theme, width, height int) (list.Model, map[string]int) {
	store := newUsageStore()
	modelCounts := store.Counts(modelIDs(models))
	familyCounts := store.FamilyCounts(familyOf)

	sortModelsByUsage(models, familyCounts, modelCounts)
	items := withFamilyDividers(models, modelCounts, familyCounts)

	delegate := modelListDelegate{
		DefaultDelegate: ThemedListDelegate(theme),
		headerStyle: lipgloss.NewStyle().
			Foreground(theme.Token(themes.TokenHeader)).
			Bold(true),
	}
	ml := list.New(items, delegate, width, height)
	ml.Title = "Models"
	ml.SetShowStatusBar(false)

	idIndex := make(map[string]int, len(models))
	for i, it := range items {
		if mi, ok := it.(modelItem); ok {
			idIndex[mi.model.ID] = i
		}
	}
	return ml, idIndex
}

// firstModelIndex returns the index of the first model row in items (skipping
// leading family dividers), or -1 if there are no model rows.
func firstModelIndex(items []list.Item) int {
	for i, it := range items {
		if _, ok := it.(modelItem); ok {
			return i
		}
	}
	return -1
}

// lastModelIndex returns the index of the last model row in items, or -1.
func lastModelIndex(items []list.Item) int {
	for i := len(items) - 1; i >= 0; i-- {
		if _, ok := items[i].(modelItem); ok {
			return i
		}
	}
	return -1
}

// snapSelectionOnModel keeps the model-picker cursor on a model row, never on
// a family divider. prev is the cursor index before the navigation step that
// produced the current cursor; the snap resumes in the direction of travel so
// moving up onto a divider continues up into the previous group (rather than
// snapping back onto the row just left). When prev is <0 (no meaningful
// direction, e.g. an initial land), it prefers the next model row.
func (m *model) snapSelectionOnModel(prev int) {
	items := m.models.Items()
	idx := m.models.Index()
	if idx < 0 || idx >= len(items) {
		return
	}
	if _, ok := items[idx].(dividerItem); !ok {
		return
	}
	if prev >= 0 && idx < prev {
		// Moved backward (e.g. up arrow): continue backward.
		for i := idx - 1; i >= 0; i-- {
			if _, ok := items[i].(modelItem); ok {
				m.models.Select(i)
				return
			}
		}
	}
	// Forward (or unknown direction).
	for i := idx + 1; i < len(items); i++ {
		if _, ok := items[i].(modelItem); ok {
			m.models.Select(i)
			return
		}
	}
	// Defensive: a divider is normally followed by a model row, but cover the edge where none exists.
	for i := idx - 1; i >= 0; i-- {
		if _, ok := items[i].(modelItem); ok {
			m.models.Select(i)
			return
		}
	}
}

// phaseModelView renders the model picker screen: the list of
// agent+tag-compatible models, an agent/tag header, and a footer
// describing the keybinds. The picker IS the agent+model screen —
// there is no separate browser.
func (m *model) phaseModelView() string {
	pad := lipgloss.NewStyle().Padding(1, 2)
	headerStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader))
	dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	header := headerStyle.Render(fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag))
	footer := dimStyle.Render("\n[↑/↓] navigate   [enter] launch   [q] quit")
	body := header + m.models.View() + footer
	// A launch/config/session/ollama error set on the model phase must be
	// visible; phaseModelView previously dropped m.status, making a failed
	// launch look like "nothing happens" when Enter was pressed.
	if m.status != "" {
		body = ErrorStyle(m.theme).Render(m.status) + "\n\n" + body
	}
	return pad.Render(body)
}
