package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
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
func realNewUsageStore() usage.Store { return usage.NewStore() }

// modelItem adapts a config.Model to a list.Item for the model picker.
// The entire compact representation is baked onto .line; the list delegate
// renders it via Title()/FilterValue(), so there is no separate state to keep
// in sync with the render.
type modelItem struct {
	model config.Model
	line  string
}

// FilterValue returns the full line so the list's built-in fuzzy filter
// narrows by both family and ID.
func (m modelItem) FilterValue() string { return m.line }

// Title renders the compact one-line model representation.
func (m modelItem) Title() string { return m.line }

// Description returns empty because the compact view is one line per item.
// list.DefaultDelegate.Render still calls this method; we keep it to satisfy
// the interface while rendering nothing.
func (m modelItem) Description() string { return "" }

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

// formatPrice returns a price string with a leading "$". It guarantees at
// least two decimal places while preserving fractional cents beyond the
// hundredth. A nil price renders as a hyphen.
func formatPrice(p *float64) string {
	if p == nil {
		return "-"
	}
	return "$" + formatPriceNumber(p)
}

// formatPriceNumber returns the numeric portion of a price string without
// the leading "$". A nil price renders as a hyphen.
func formatPriceNumber(p *float64) string {
	if p == nil {
		return "-"
	}
	s := strconv.FormatFloat(*p, 'f', -1, 64)
	if i := strings.Index(s, "."); i == -1 {
		s += ".00"
	} else {
		frac := s[i+1:]
		if len(frac) < 2 {
			s += strings.Repeat("0", 2-len(frac))
		}
	}
	return s
}

// formatPerToken returns the compact per-token cost as "$in/cache/out".
// When all three prices are absent it returns a hyphen; missing individual
// segments are rendered with a hyphen in their slot.
func formatPerToken(cost config.ModelCost) string {
	in := formatPriceNumber(cost.InputPricePerMillion)
	cache := formatPriceNumber(cost.CachePricePerMillion)
	out := formatPriceNumber(cost.OutputPricePerMillion)
	if in == "-" && cache == "-" && out == "-" {
		return "-"
	}
	return fmt.Sprintf("$%s/%s/%s", in, cache, out)
}

// formatSubscription returns the subscription price as "$amount/mo" or
// "$amount/yr". A nil subscription price renders as a hyphen.
func formatSubscription(cost config.ModelCost) string {
	if cost.SubscriptionPrice == nil {
		return "-"
	}
	period := cost.SubscriptionPeriod
	switch period {
	case "month":
		period = "mo"
	case "year":
		period = "yr"
	}
	return fmt.Sprintf("%s/%s", formatPrice(cost.SubscriptionPrice), period)
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

// buildModelItems returns usage-sorted items for the model picker,
// computing a compact one-line representation for each model that
// includes family context and usage counts. familyOf maps the FULL
// catalog's model IDs to families so family totals are accurate even
// when tags or families narrow the eligible slice.
func buildModelItems(models []config.Model, familyOf map[string]string, s usage.Store) []*modelItem {
	// We need per-model and per-family counts for the line format.
	// Count over the full catalog (familyOf's keys), not just the
	// eligible subset, so a family's 30-day total includes launches of
	// models that are currently filtered out.
	catalogIDs := make([]string, 0, len(familyOf))
	for id := range familyOf {
		catalogIDs = append(catalogIDs, id)
	}
	modelCounts := s.Counts(catalogIDs)
	familyCounts := usage.AggregateByFamily(familyOf, modelCounts)

	// Sort the models in place.
	sortModelsByUsage(models, familyCounts, modelCounts)

	// Compute max widths for alignment, including the new pricing columns.
	// Widths are measured in runes so single-byte characters such as the
	// hyphen used for absent prices do not throw off fmt.Sprintf padding.
	famWidth := 0
	idWidth := 0
	ptWidth := 0
	subWidth := 0
	type pricingStrings struct {
		perToken     string
		subscription string
	}
	pricing := make([]pricingStrings, len(models))
	for i, m := range models {
		if w := utf8.RuneCountInString(m.Family); w > famWidth {
			famWidth = w
		}
		if w := utf8.RuneCountInString(m.ID); w > idWidth {
			idWidth = w
		}
		pricing[i].perToken = formatPerToken(m.Cost)
		pricing[i].subscription = formatSubscription(m.Cost)
		if w := utf8.RuneCountInString(pricing[i].perToken); w > ptWidth {
			ptWidth = w
		}
		if w := utf8.RuneCountInString(pricing[i].subscription); w > subWidth {
			subWidth = w
		}
	}

	items := make([]*modelItem, 0, len(models))
	for i, m := range models {
		fam := m.Family
		famDisp := fam
		if fam == "" {
			famDisp = "-"
		}
		// Always read the aggregate, including for the empty ("other")
		// family — AggregateByFamily sums launches under the "" key, so
		// `fam30d` must match the family's CompositeScore sort key rather
		// than hardcode 0. famDisp alone distinguishes the unnamed bucket.
		fam30d := familyCounts[fam].ThirtyDay

		c := modelCounts[m.ID]
		countsStr := fmt.Sprintf("%d/%d/%d", c.OneDay, c.SevenDay, c.ThirtyDay)

		line := fmt.Sprintf("%-*s  %3d  %-*s  %-5s  %-*s  %-*s  %-*s",
			famWidth, famDisp, fam30d, idWidth, m.ID, string(m.Location), 11, countsStr,
			ptWidth, pricing[i].perToken, subWidth, pricing[i].subscription)

		if len(m.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(m.Tags, ","))
		}

		items = append(items, &modelItem{
			model: m,
			line:  line,
		})
	}
	return items
}

// clampModelSelection guards against bubbles v1.0.0 leaving
// m.Index() outside [0, len(VisibleItems())) after a filter
// narrows the list. With dividers gone there is no
// direction-of-travel walk or divider-skipping — just the clamp.
func clampModelSelection(m *model) tea.Cmd {
	visible := m.models.VisibleItems()
	if len(visible) == 0 {
		return nil
	}
	if i := m.models.Index(); i < 0 || i >= len(visible) {
		m.models.Select(0)
	}
	return nil
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
