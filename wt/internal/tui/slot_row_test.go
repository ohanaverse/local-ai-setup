// Tests for a slot profile's row in the model picker: its cells, where it
// sorts, the slot line under the table, and the fit of the screen around it.
// The fixtures here are shared with the cursor and launch-flow tests.
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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
// state directory and the usage and refcount stores (flowEnter). The
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

// idleOmlxSnapshot is the fixture's local model on disk and not running,
// which makes its own row, and a profile that names it, a start row.
func idleOmlxSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	}
}

// slotTable builds the picker's table for "claude" over cfg and snap with
// checks opted in, the way tableFor does, with a usage store of its own.
func slotTable(t *testing.T, cfg *config.Config, snap localmodels.Snapshot, checks []profiles.SlotCheck) modelTable {
	t.Helper()
	models, err := cfg.EligibleModels("claude", "", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	store := stubUsageStore(t)
	for i := 0; i < 2; i++ {
		if err := store.RecordFor("claude", "profile/cloud"); err != nil {
			t.Fatalf("seed usage: %v", err)
		}
	}
	return buildTable(tableInput{cfg: cfg, agent: "claude", models: models, inventory: &snap, usage: store, slots: checks}, stubRefcountStore(t), "")
}

// tableItem is the table's item for id.
func tableItem(t *testing.T, tbl modelTable, id string) *modelItem {
	t.Helper()
	for _, it := range tbl.items {
		if it.model.ID == id {
			return it
		}
	}
	t.Fatalf("the table has no row %q", id)
	return nil
}

// TestProfileRowsNeedTheOptIn pins that the table has profile rows only when
// its input carries the slot profiles, and none under -T or -F. `wt start`
// and `wt smoke` build their picker from the same function without them; a
// profile row there would be offered as a model to start.
func TestProfileRowsNeedTheOptIn(t *testing.T) {
	cfg := slotTestConfig()
	models, _ := cfg.EligibleModels("claude", "", "")
	snap := runningOmlxSnapshot()
	count := func(in tableInput) int {
		in.cfg, in.agent, in.models, in.inventory = cfg, "claude", models, &snap
		n := 0
		for _, it := range buildTable(in, nil, "").items {
			if strings.HasPrefix(it.model.ID, profiles.SlotIDPrefix) {
				n++
			}
		}
		return n
	}
	if got := count(tableInput{slots: slotRowChecks()}); got != 3 {
		t.Errorf("with the slot profiles: %d profile rows, want 3", got)
	}
	if got := count(tableInput{}); got != 0 {
		t.Errorf("without the slot profiles: %d profile rows, want none", got)
	}
	if got := count(tableInput{slots: slotRowChecks(), hideDiscovered: true}); got != 0 {
		t.Errorf("under -T or -F: %d profile rows, want none", got)
	}
}

// TestSortRowsPutsProfilesBetweenNativeAndTheRest pins the picker's three
// bands: native rows, then profile rows in file order, then every other row
// in the order it had before. The sort order is also the default selection,
// so a profile sorted by cost or id would move whenever a price changed, and
// one sorted above a native row would become what a bare Enter picks.
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
	}

	sortRows(rows)

	var got []string
	for _, r := range rows {
		got = append(got, r.Model.ID)
	}
	want := "claude/native,profile/start,profile/alpha,profile/zeta,openrouter/cheap,openrouter/dear,omlx/idle"
	if strings.Join(got, ",") != want {
		t.Errorf("order = %s\nwant    %s", strings.Join(got, ","), want)
	}
}

// TestRefreshedTableKeepsProfileRows pins that a table rebuilt after a start
// attempt still has its profile rows. The rebuild runs from a reduced copy of
// the model, and a field left out of that copy is lost on every refresh: the
// profiles would vanish from the picker the first time a start failed.
func TestRefreshedTableKeepsProfileRows(t *testing.T) {
	stubInventory(t, runningOmlxSnapshot())
	m := slotPicker(t, slotTestConfig(), slotRowChecks(), 80, 24)
	before := strings.Join(itemIDs(m), ",")

	m, cmd := m.refreshTable()
	m = drainCmds(t, m, cmd)

	if got := strings.Join(itemIDs(m), ","); got != before || !strings.Contains(got, "profile/mixed") {
		t.Errorf("rows after a refresh = %s, want %s", got, before)
	}
}

// TestProfileRowCells pins what a slot profile's row shows in each column,
// and that the row is judged by resolved validation alone. FAMILY is
// "profile", LOC says cloud, local or mixed, RUNNING is the local model's,
// COST is the default slot model's price, and the usage columns count
// launches recorded under the profile's own id. A profile row has no route
// of its own, so asking ResolveRoute about it fails with "unknown provider":
// the row would be marked "(unavailable)", and a start profile would be
// turned into a blocked one that Enter refuses.
func TestProfileRowCells(t *testing.T) {
	for _, c := range []struct {
		name, id               string
		snap                   localmodels.Snapshot
		loc, running, cost, d7 string
		start                  bool
		blocked                string
	}{
		{"all cloud", "profile/cloud", runningOmlxSnapshot(), "cloud", "-", " 0.4000  0.0400  1.6000", "2", false, ""},
		{"mixed, local running", "profile/mixed", runningOmlxSnapshot(), "mixed", "run", " 0.4000  0.0400  1.6000", "0", false, ""},
		{"mixed, local idle", "profile/mixed", idleOmlxSnapshot(), "mixed", "-", " 0.4000  0.0400  1.6000", "0", true, ""},
		{"blocked", "profile/broken", runningOmlxSnapshot(), "cloud", "-", " 0.4000  0.0400  1.6000", "0", false,
			"slot haiku: openrouter/gone is not in the registry"},
	} {
		tbl := slotTable(t, slotTestConfig(), c.snap, slotRowChecks())
		it := tableItem(t, tbl, c.id)
		cell := func(col int) string { return strings.TrimRight(it.cells[col], " ") }
		for _, want := range []struct {
			col       int
			name, val string
		}{
			{colFamily, "FAMILY", "profile"}, {colModel, "MODEL", c.id}, {colLoc, "LOC", c.loc},
			{colStatus, "STATUS", "ok"}, {colRunning, "RUNNING", c.running}, {colCost, "COST", c.cost},
			{col7D, "7D", c.d7}, {colSurvey, "SURVEY", ""},
		} {
			if got := cell(want.col); got != want.val {
				t.Errorf("%s: %s = %q, want %q", c.name, want.name, got, want.val)
			}
		}
		if it.profile == nil || it.cost != "" || it.ref != 0 {
			t.Errorf("%s: profile set = %t, cost note = %q, ref = %d; want the profile carried, no price note and no in-use count", c.name, it.profile != nil, it.cost, it.ref)
		}
		if it.exception != "" || it.start != c.start || it.blocked != c.blocked {
			t.Errorf("%s: exception = %q start = %t blocked = %q, want no route note, start = %t and blocked = %q",
				c.name, it.exception, it.start, it.blocked, c.start, c.blocked)
		}
	}

	// A profile whose slots resolve to nothing has no location to name.
	none := []profiles.SlotCheck{slotCheckFor(0, "none", "opus", map[string]string{"opus": "openrouter/gone"})}
	it := tableItem(t, slotTable(t, slotTestConfig(), runningOmlxSnapshot(), none), "profile/none")
	if loc, cost := strings.TrimSpace(it.cells[colLoc]), strings.TrimSpace(it.cells[colCost]); loc != "-" || cost != "-" {
		t.Errorf("unresolved profile: LOC = %q COST = %q, want a dash in each", loc, cost)
	}
}

// TestProfileRowCostFollowsTheDefaultSlotsClock pins that a profile row is
// priced as its default slot's model at the table's own instant, with the
// "~" mark when that model is priced by time of day. Priced from the row's
// own (empty) model it would show a dash for every profile.
func TestProfileRowCostFollowsTheDefaultSlotsClock(t *testing.T) {
	cfg := slotTestConfig()
	cfg.Models[config.IndexModelByID(cfg.Models, "openrouter/a")].Cost = timedTableRows(deepseekDear)[0].Model.Cost
	models, _ := cfg.EligibleModels("claude", "", "")
	snap := runningOmlxSnapshot()
	for _, c := range []struct {
		name string
		in   tableInput
		want string
	}{
		{"dear window", tableInput{now: deepseekDear}, " 1.3200  0.0440  3.9600~"},
		{"cheap window", tableInput{now: ollamaPeak}, " 0.6600  0.0220  1.9800~"},
	} {
		in := c.in
		in.cfg, in.agent, in.models, in.inventory, in.slots = cfg, "claude", models, &snap, slotRowChecks()
		it := tableItem(t, buildTable(in, nil, ""), "profile/cloud")
		if got := strings.TrimRight(it.cells[colCost], " "); got != c.want {
			t.Errorf("%s: COST = %q, want %q", c.name, got, c.want)
		}
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
		got := slotLine(slotLineRow(), c.width)
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
	if got, want := slotLine(two, 76), "sonnet* glm-5.3-flash:cloud · opus glm-5.2:cloud"; got != want {
		t.Errorf("two filled slots: slotLine = %q, want %q", got, want)
	}

	// A blocked row shows its reason, and says so when it is cut.
	blocked := &catalog.ProfileRow{Check: two.Check, Verdict: catalog.ActionBlock, Reason: "slot haiku: ollama/gemma4:31b-cloud is not in the registry"}
	if got := slotLine(blocked, 76); got != blocked.Reason {
		t.Errorf("blocked row: slotLine = %q, want the reason", got)
	}
	if got, want := slotLine(blocked, 36), "slot haiku: ollama/gemma4:31b-cloud…"; got != want {
		t.Errorf("blocked row at 36 columns: slotLine = %q, want %q", got, want)
	}
	if got := slotLine(nil, 76); got != "" {
		t.Errorf("slotLine(nil) = %q, want nothing", got)
	}
}
