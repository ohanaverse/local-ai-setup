package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// newUsageStore is a seam for tests: production uses realNewUsageStore (the
// default config dir); tests swap it to isolate from the real usage.jsonl.
var newUsageStore = realNewUsageStore

// realNewUsageStore is the production implementation of the newUsageStore
// seam: a Store rooted at the default config dir.
func realNewUsageStore() usage.Store { return usage.NewStore() }

// newRefcountStore is a seam for tests: production uses
// realNewRefcountStore (the default config dir); tests swap it to isolate
// from the real refcount.jsonl.
var newRefcountStore = realNewRefcountStore

// realNewRefcountStore is the production implementation of the
// newRefcountStore seam: a Store rooted at the default config dir.
func realNewRefcountStore() refcount.Store { return refcount.NewStore() }

// modelItem adapts a config.Model to a list.Item for the model picker.
// The compact representation is baked onto .line; marked records the
// rotation's last-launched row and ref is the live "in use" session count
// (issue #73) — both are composed into the rendered prefix by Title()
// rather than baked into .line, so FilterValue (fuzzy matching) and every
// .line consumer see the unprefixed format. exception, when non-empty,
// appends a per-row deviation note in Title() (e.g. "(via proxy)" for a
// row protocol negotiation forces through LiteLLM, "(unavailable)" for a
// row whose route cannot resolve at all).
type modelItem struct {
	model     config.Model
	line      string
	marked    bool
	ref       int
	exception string
}

// markerMarked is the last-launched row's 2-rune prefix; markerBlank keeps
// every other row aligned. Plain ASCII is deliberate: Unicode geometric
// shapes (e.g. U+25B6 ▶) are East Asian Ambiguous width and render 2 cells
// wide on CJK-configured terminals, which would shift the marked row's
// columns relative to every other row.
const (
	markerMarked = "> "
	markerBlank  = "  "
)

// refClamp is the highest digit the ref column ever renders — a single
// glyph keeps the column width fixed regardless of how many concurrent
// sessions are actually using a model.
const refClamp = 9

// refColumn renders the ref count's 2-rune prefix: "<digit> " when ref > 0
// (clamped at refClamp), or two blank spaces when the model is unused.
// Kept the same width as markerMarked/markerBlank so every row's columns
// line up regardless of ref/marked state.
func refColumn(ref int) string {
	if ref <= 0 {
		return "  "
	}
	if ref > refClamp {
		ref = refClamp
	}
	return fmt.Sprintf("%d ", ref)
}

// FilterValue returns the full line so the list's built-in fuzzy filter
// narrows by both family and ID. Deliberately excludes the marker prefix:
// the user never typed it, so it would only pollute match ranking.
func (m modelItem) FilterValue() string { return m.line }

// Title renders the compact one-line model representation with the ref
// column (issue #73's "in use" count) prepended before the last-launched
// marker prefix, and any exception note appended after the line.
func (m modelItem) Title() string {
	prefix := refColumn(m.ref)
	line := m.line
	if m.exception != "" {
		line += " " + m.exception
	}
	if m.marked {
		return prefix + markerMarked + line
	}
	return prefix + markerBlank + line
}

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

// formatPerToken returns per-token costs as "$0.1234 0.2345 0.3456".
// Each value has exactly 4 decimal places; missing values render as "-0000".
// When all three prices are absent it returns a hyphen.
func formatPerToken(cost config.ModelCost) string {
	format4dec := func(p *float64) string {
		if p == nil {
			return "-0000"
		}
		return fmt.Sprintf("%.4f", *p)
	}
	in := format4dec(cost.InputPricePerMillion)
	cache := format4dec(cost.CachePricePerMillion)
	out := format4dec(cost.OutputPricePerMillion)
	if in == "-0000" && cache == "-0000" && out == "-0000" {
		return "-"
	}
	return fmt.Sprintf("$%s %s %s", in, cache, out)
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
// when tags or families narrow the eligible slice. refStore supplies the
// live "in use" session count (issue #73) rendered as Title()'s leading
// ref column; it is queried over the same full-catalog IDs as the usage
// counts, in the same pass. cfg and agent drive the per-row exception
// marker: a row whose agent×provider protocol intersection is empty is
// forced through LiteLLM ("(via proxy)"), and a row whose route cannot
// resolve at all is marked "(unavailable)" — both resolved with the same
// ResolveRoute call BuildLaunchCmd uses, so the picker never disagrees
// with what a launch would actually do. A nil cfg skips resolution
// (exception stays empty), which keeps picker-only tests cheap.
func buildModelItems(cfg *config.Config, agent string, models []config.Model, familyOf map[string]string, s usage.Store, refStore refcount.Store, lastID string, stats map[string]survey.Stats) []*modelItem {
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

	// One Counts pass over the same full-catalog IDs used for usage, so a
	// filtered (-T/-F) picker still shows accurate "in use" counts for
	// every row it renders.
	refCounts := refStore.Counts(catalogIDs)

	// Sort the models in place.
	sortModelsByUsage(models, familyCounts, modelCounts)

	// Compute max widths for alignment.
	// Widths are measured in runes so single-byte characters such as the
	// hyphen used for absent prices do not throw off fmt.Sprintf padding.
	// The marker prefix is composed in Title() outside this measurement —
	// see markerMarked for why it must stay plain ASCII.
	famWidth := 0
	idWidth := 0
	ptWidth := 0
	perToken := make([]string, len(models))
	for i, m := range models {
		if w := utf8.RuneCountInString(m.Family); w > famWidth {
			famWidth = w
		}
		if w := utf8.RuneCountInString(m.ID); w > idWidth {
			idWidth = w
		}
		perToken[i] = formatPerToken(m.Cost)
		if w := utf8.RuneCountInString(perToken[i]); w > ptWidth {
			ptWidth = w
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

		line := fmt.Sprintf("%-*s  %3d  %-*s  %-5s  %-*s  %-*s",
			famWidth, famDisp, fam30d, idWidth, m.ID, string(m.Location), 11, countsStr,
			ptWidth, perToken[i])

		if len(m.Tags) > 0 {
			line += fmt.Sprintf(" [%s]", strings.Join(m.Tags, ","))
		}
		// Survey segment always appended last so it never shifts any
		// existing column; omitted entirely when there's nothing answered
		// (FormatPickerSegment returns "" in that case).
		if st, ok := stats[m.ID]; ok {
			if seg := survey.FormatPickerSegment(st); seg != "" {
				line += " " + seg
			}
		}

		item := &modelItem{
			model:  m,
			line:   line,
			marked: lastID != "" && m.ID == lastID,
			ref:    refCounts[m.ID],
		}
		if cfg != nil {
			route, err := cfg.ResolveRoute(m, agents.ProtocolsFor(agent))
			switch {
			case err != nil:
				item.exception = "(unavailable)"
			case route.Forced:
				item.exception = "(via proxy)"
			}
		}
		items = append(items, item)
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
	// Mode footer: the picker is where an off/on surprise surfaces (a row
	// marked "(via proxy)" under direct mode), so name the current mode on
	// the same screen. Falls back to direct when cfg is nil (view-only
	// tests).
	mode := "LiteLLM: off (direct)"
	if m.cfg != nil && m.cfg.Gateway.IsLitellm() {
		mode = "LiteLLM: on"
	}
	footer := dimStyle.Render(fmt.Sprintf("\n%s\n[↑/↓] navigate   [enter] launch   [q] quit", mode))
	body := header + m.models.View() + footer
	// A launch/config/session/ollama error set on the model phase must be
	// visible; phaseModelView previously dropped m.status, making a failed
	// launch look like "nothing happens" when Enter was pressed.
	if m.status != "" {
		body = ErrorStyle(m.theme).Render(m.status) + "\n\n" + body
	}
	return pad.Render(body)
}
