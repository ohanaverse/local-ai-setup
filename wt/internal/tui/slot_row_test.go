// Tests for a slot profile's row in the model picker: its cells, where it
// sorts, the slot line under the table, and the fit of the screen around it.
// The fixtures here are shared with the cursor and launch-flow tests.
package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// slotTestConfig is modelTestConfig (pin_model_test.go) plus an "openrouter"
// cloud provider with two models, both tagged "code" and supported by the
// "claude" agent: "openrouter/a" (family "a", provider-side name "a", priced
// 0.40 / 0.04 / 1.60 per million) and "openrouter/b" (name "b", no price).
// modelTestConfig already sets the [litellm] url and key.
func slotTestConfig() *config.Config {
	cfg := modelTestConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{
		ID: "openrouter", Location: config.LocationCloud,
		Protocols: []config.Protocol{config.ProtocolOpenAIChat},
		Auth:      config.AuthConfig{Type: "api_key", BaseURL: "https://openrouter.ai/api"},
	})
	cfg.Models = append(cfg.Models,
		config.Model{ID: "openrouter/a", ProviderID: "openrouter", ModelName: "a", Family: "a", Tags: []string{"code"}, Cost: config.ModelCost{
			InputPricePerMillion: f64(0.40), CachePricePerMillion: f64(0.04), OutputPricePerMillion: f64(1.60)}},
		config.Model{ID: "openrouter/b", ProviderID: "openrouter", ModelName: "b", Family: "b", Tags: []string{"code"}},
	)
	cfg.Agents[0].SupportedProviders = append(cfg.Agents[0].SupportedProviders, "openrouter")
	return cfg
}

// slotCheckFor is a profile that passed static validation: usable, for agent
// "claude", with Claude's four slots declared. index is its position in the
// file, which is the order profile rows sort in.
func slotCheckFor(index int, name, def string, slots map[string]string) profiles.SlotCheck {
	return profiles.SlotCheck{
		Index:    index,
		Profile:  profiles.SlotProfile{Name: name, Agent: "claude", Default: def, Slots: slots},
		Usable:   true,
		Declared: []string{"fable", "opus", "sonnet", "haiku"},
	}
}

// slotPicker enters the model phase for "claude" on cfg with checks as the
// picker's slot profiles, in a terminal of width x height. It isolates the
// state directory and the usage store (flowEnter). The
// inventory is whatever the test stubbed before the call; with no stub it is
// TestMain's: every local model on disk and none running.
func slotPicker(t *testing.T, cfg *config.Config, checks []profiles.SlotCheck, width, height int) model {
	t.Helper()
	m := flowEnter(t, model{cfg: cfg, agent: "claude", selectedPath: t.TempDir(), slotChecks: checks, width: width, height: height}, "claude")
	return resized(t, m, width, height)
}

// slotRowChecks are the three profiles the row tests draw: "cloud" (every
// slot a cloud model, a launch row), "mixed" (a cloud default and the
// fixture's local model) and "broken" (one slot names a model the registry
// does not have, a blocked row).
func slotRowChecks() []profiles.SlotCheck {
	return []profiles.SlotCheck{
		slotCheckFor(0, "cloud", "opus", map[string]string{"opus": "openrouter/a", "haiku": "openrouter/b"}),
		slotCheckFor(1, "mixed", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "omlx/qwen3.8"}),
		slotCheckFor(2, "broken", "opus", map[string]string{"opus": "openrouter/a", "haiku": "openrouter/gone"}),
	}
}

// TestSortRowsPutsProfilesBetweenNativeAndTheRest pins the picker's bands
// with real slot-profile rows: native rows, then profile rows alphabetically
// by id (not in file order), then every other row. The sort order is also the
// default selection, so a profile sorted above a native row would become what
// a bare Enter picks.
func TestSortRowsPutsProfilesBetweenNativeAndTheRest(t *testing.T) {
	profile := func(index int, name string, verdict catalog.Action) tableRow {
		c := slotCheckFor(index, name, "opus", map[string]string{"opus": "openrouter/a"})
		return tableRow{Row: catalog.Row{Model: config.Model{ID: c.Profile.ID()}, Status: catalog.StatusOK,
			Profile: &catalog.ProfileRow{Check: c, Verdict: verdict}}}
	}
	rows := []tableRow{
		{Row: catalog.Row{Model: config.Model{ID: "omlx/idle"}, Location: config.LocationLocal}},
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/dear", Cost: config.ModelCost{OutputPricePerMillion: f64(9)}}, Location: config.LocationCloud}},
		profile(2, "zeta", catalog.ActionLaunch),
		{Row: catalog.Row{Model: config.Model{ID: "openrouter/cheap", Cost: config.ModelCost{OutputPricePerMillion: f64(1)}}, Location: config.LocationCloud}},
		profile(0, "start", catalog.ActionStart),
		{Row: catalog.Row{Model: config.Model{ID: "claude/native", Native: true}, Location: config.LocationCloud}},
		profile(1, "alpha", catalog.ActionBlock),
		sortAlias("alias/x", config.LocationCloud, false, 5),
		sortLocal("omlx/run", true, false, 90),
	}

	sortRows(rows)

	var got []string
	for _, r := range rows {
		got = append(got, r.Model.ID)
	}
	want := "claude/native,profile/alpha,profile/start,profile/zeta,alias/x,omlx/run,omlx/idle,openrouter/cheap,openrouter/dear"
	if strings.Join(got, ",") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, ","), want)
	}
}

// slotLineRow is a four-slot profile with names of the length real ones
// have: the default is opus, so it is listed first, ahead of fable.
func slotLineRow() *catalog.ProfileRow {
	c := slotCheckFor(0, "ollama-cloud", "opus", map[string]string{
		"fable": "ollama/kimi-k3:cloud", "opus": "ollama/glm-5.2:cloud",
		"sonnet": "ollama/glm-5.3-flash:cloud", "haiku": "omlx/qwen3.8",
	})
	models := map[string]config.Model{}
	for slot, id := range c.Profile.Slots {
		_, name, _ := strings.Cut(id, "/")
		models[slot] = config.Model{ID: id, ModelName: name}
	}
	return &catalog.ProfileRow{Check: c, Models: models, Verdict: catalog.ActionLaunch, Loc: "mixed"}
}

// TestSlotLine pins the line under the table for a highlighted profile row:
// the filled slots, the default first and marked, each with its model's
// provider-side name; whole entries only, with the ones that do not fit
// counted as "+N more"; and a blocked row's reason in its place. The footer
// is clipped at the terminal's edge with no sign of the cut, so an entry cut
// there would read as another model's name.
func TestSlotLine(t *testing.T) {
	const (
		opus   = "opus* glm-5.2:cloud"
		fable  = "fable kimi-k3:cloud"
		sonnet = "sonnet glm-5.3-flash:cloud"
		haiku  = "haiku qwen3.8"
	)
	all := opus + " · " + fable + " · " + sonnet + " · " + haiku
	if lipgloss.Width(all) != 86 {
		t.Fatalf("the fixture's full line is %d columns, want 86: the widths below are counted from it", lipgloss.Width(all))
	}
	for _, c := range []struct {
		width int
		want  string
	}{
		{0, all},
		{116, all},
		{86, all},
		{85, opus + " · " + fable + " · " + sonnet + " · +1 more"},
		{80, opus + " · " + fable + " · " + sonnet + " · +1 more"},
		{79, opus + " · " + fable + " · +2 more"},
		{76, opus + " · " + fable + " · +2 more"},
		{51, opus + " · " + fable + " · +2 more"},
		{50, opus + " · +3 more"},
		{36, opus + " · +3 more"},
		{29, opus + " · +3 more"},
		{28, "+4 more"},
		{7, "+4 more"},
		{6, ""},
	} {
		got := slotLine(slotLineRow(), nil, c.width, "")
		if got != c.want {
			t.Errorf("slotLine(width %d) = %q, want %q", c.width, got, c.want)
		}
		if c.width > 0 && lipgloss.Width(got) > c.width {
			t.Errorf("slotLine(width %d) is %d columns wide", c.width, lipgloss.Width(got))
		}
	}

	// Only the slots the profile fills are listed; the others fall back to
	// the default's model and would repeat it.
	two := slotLineRow()
	two.Check.Profile.Default = "sonnet"
	two.Check.Profile.Slots = map[string]string{"sonnet": "ollama/glm-5.3-flash:cloud", "opus": "ollama/glm-5.2:cloud"}
	if got, want := slotLine(two, nil, 76, ""), "sonnet* glm-5.3-flash:cloud · opus glm-5.2:cloud"; got != want {
		t.Errorf("two filled slots: slotLine = %q, want %q", got, want)
	}

	// A blocked row shows its reason, and says so when it is cut.
	blocked := &catalog.ProfileRow{Check: two.Check, Verdict: catalog.ActionBlock, Reason: "slot haiku: ollama/gemma4:31b-cloud is not in the registry"}
	if got := slotLine(blocked, nil, 76, ""); got != blocked.Reason {
		t.Errorf("blocked row: slotLine = %q, want the reason", got)
	}
	if got, want := slotLine(blocked, nil, 36, ""), "slot haiku: ollama/gemma4:31b-cloud…"; got != want {
		t.Errorf("blocked row at 36 columns: slotLine = %q, want %q", got, want)
	}
	// A short form (#699) is said only where the whole reason is too wide:
	// the whole one wins whenever it fits.
	const short = "slot haiku: claude-haiku-5-5 uses your Claude subscription; passthrough off"
	if got := slotLine(blocked, nil, 76, short); got != blocked.Reason {
		t.Errorf("blocked row with a short form at 76 columns: slotLine = %q, want the whole reason", got)
	}
	wide := &catalog.ProfileRow{Check: two.Check, Verdict: catalog.ActionBlock, Reason: strings.Repeat("long ", 30)}
	if got := slotLine(wide, nil, 76, short); got != short {
		t.Errorf("blocked row too wide for its reason: slotLine = %q, want the short form", got)
	}
	if got := slotLine(nil, nil, 76, ""); got != "" {
		t.Errorf("slotLine(nil) = %q, want nothing", got)
	}
}

// TestSlotLineOpusplan pins the line for a profile whose default is claude's
// opusplan alias: the alias's name first, then the slot the session works on
// (sonnet) marked, then the rest. Marking the opus slot, or no slot, would
// say the session runs on a model it only plans with; leaving out the name
// would hide that the session switches models with plan mode.
func TestSlotLineOpusplan(t *testing.T) {
	row := slotLineRow()
	row.Check.Profile.Default = "opusplan"
	row.Check.DefaultSlot = "sonnet"
	row.Check.Alias = profiles.SlotAlias{Requires: []string{"opus", "sonnet"}, Rest: "sonnet", Doc: "doc"}
	all := "opusplan · sonnet* glm-5.3-flash:cloud · fable kimi-k3:cloud · opus glm-5.2:cloud · haiku qwen3.8"
	if got := slotLine(row, nil, 0, ""); got != all {
		t.Errorf("slotLine = %q, want %q", got, all)
	}
	if got, want := slotLine(row, nil, 50, ""), "opusplan · sonnet* glm-5.3-flash:cloud · +3 more"; got != want {
		t.Errorf("slotLine(width 50) = %q, want %q (the alias kept with the slot it rests on)", got, want)
	}
}

// footerLine is the line of view that sits just above the picker's key
// hints: the mode line, or the slot line when a profile row is highlighted.
func footerLine(t *testing.T, view string) string {
	t.Helper()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.Contains(line, "[↑/↓] navigate") && i > 0 {
			return strings.TrimSpace(lines[i-1])
		}
	}
	t.Fatalf("the view has no key hints:\n%s", view)
	return ""
}

// TestProfileRowScreenFitsAndShowsItsSlots pins the picker with profile rows
// on a real-sized screen, through Update and View, at 80x24, 40x12 and
// 120x50. With a profile row highlighted the line under the table is that
// profile's slot line, whole entries only, and names neither the LiteLLM
// mode nor a price; on a blocked row it is the reason. It takes the mode
// line's place, so the screen is no taller than with a model highlighted,
// and moving back to a model row brings the mode line back. A slot line on a
// line of its own would push the header off the top of a short terminal.
func TestProfileRowScreenFitsAndShowsItsSlots(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {40, 12}, {120, 50}} {
		width, height := size[0], size[1]
		name := fmt.Sprintf("%dx%d", width, height)
		stubRunning(t, "omlx/qwen3.8")
		m := slotPicker(t, slotTestConfig(), slotRowChecks(), width, height)
		if got, want := strings.Join(itemIDs(m), ","), "profile/broken,profile/cloud,profile/mixed,"; !strings.HasPrefix(got, want) {
			t.Fatalf("%s: rows = %s, want the three profiles first, alphabetically", name, got)
		}

		m.models.Select(indexOfID(m, "openrouter/a"))
		m = resized(t, m, width, height)
		plain := m.View()
		assertFits(t, name+", model row", plain, width, height)
		if got, want := footerLine(t, plain), "cost 0.4/0.04/1.6"; got != want {
			t.Errorf("%s, model row: the line under the table = %q, want %q", name, got, want)
		}

		for id, want := range map[string]string{
			"profile/cloud":  "opus* a · haiku b",
			"profile/mixed":  "sonnet* a · haiku qwen3.8",
			"profile/broken": cutLines("slot haiku: openrouter/gone is not in the model server's catalog", width-2*pickerPadX),
		} {
			m.models.Select(indexOfID(m, id))
			m = resized(t, m, width, height)
			view := m.View()
			assertFits(t, name+", on "+id, view, width, height)
			if got := footerLine(t, view); got != want {
				t.Errorf("%s, on %s: the line under the table = %q, want %q", name, id, got, want)
			}
			if strings.Contains(view, "LiteLLM:") || strings.Contains(view, "cost ") {
				t.Errorf("%s, on %s: the view names the LiteLLM mode or a price:\n%s", name, id, view)
			}
			if got, want := lipgloss.Height(view), lipgloss.Height(plain); got != want {
				t.Errorf("%s, on %s: the view is %d lines, want the %d it is with a model row highlighted", name, id, got, want)
			}
			if !strings.Contains(view, id) {
				t.Errorf("%s, on %s: the row is not on screen:\n%s", name, id, view)
			}
		}
	}
}

// TestProfileSlotLineIsCutToWholeEntriesOnScreen pins the "+N more" cut on
// the screen itself: a profile whose slot list is wider than the picker shows
// as many whole entries as fit and counts the rest, at every size. Without
// the cut the footer's clip would end the line mid-name at the right edge.
func TestProfileSlotLineIsCutToWholeEntriesOnScreen(t *testing.T) {
	cfg := slotTestConfig()
	for _, name := range []string{"glm-5.2:cloud", "kimi-k3:cloud", "glm-5.3-flash:cloud"} {
		cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/" + name, ProviderID: "openrouter", ModelName: name, Family: "x", Tags: []string{"code"}})
	}
	checks := []profiles.SlotCheck{slotCheckFor(0, "ollama-cloud", "opus", map[string]string{
		"fable": "openrouter/kimi-k3:cloud", "opus": "openrouter/glm-5.2:cloud",
		"sonnet": "openrouter/glm-5.3-flash:cloud", "haiku": "omlx/qwen3.8",
	})}
	const opus, fable, sonnet, haiku = "opus* glm-5.2:cloud", "fable kimi-k3:cloud", "sonnet glm-5.3-flash:cloud", "haiku qwen3.8"
	for _, c := range []struct {
		width, height int
		want          string
	}{
		{80, 24, opus + " · " + fable + " · +2 more"},
		{40, 12, opus + " · +3 more"},
		{120, 50, opus + " · " + fable + " · " + sonnet + " · " + haiku},
	} {
		stubRunning(t, "omlx/qwen3.8")
		m := slotPicker(t, cfg, checks, c.width, c.height)
		m.models.Select(indexOfID(m, "profile/ollama-cloud"))
		m = resized(t, m, c.width, c.height)
		view := m.View()
		assertFits(t, "slot line", view, c.width, c.height)
		if got := footerLine(t, view); got != c.want {
			t.Errorf("%dx%d: the line under the table = %q, want %q", c.width, c.height, got, c.want)
		}
	}
}
