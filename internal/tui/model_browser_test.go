package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
)

// browserTestConfig returns a small config with two curated code models
// and one design model — enough to exercise tag filtering, source
// filtering, and pick-via-Enter.
func browserTestConfig() *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "ollama/a:9b", ProviderID: "ollama", Tags: []string{"code"}, Source: config.SourceCurated},
			{ID: "ollama/a:14b", ProviderID: "ollama", Tags: []string{"code"}, Source: config.SourceCurated},
			{ID: "ollama/a:design", ProviderID: "ollama", Tags: []string{"design"}, Source: config.SourceCurated},
		},
		Agents: []config.Agent{{Name: "claude"}},
	}
}

// modelItem.FilterValue must return the model ID so the list widget's
// built-in fuzzy filter narrows by ID. A regression here would mean
// typing in the browser does nothing — users would think the browser is
// broken.
func TestModelItemFilterValueIsID(t *testing.T) {
	it := modelItem{model: config.Model{ID: "ollama/a:9b"}}
	if got := it.FilterValue(); got != "ollama/a:9b" {
		t.Errorf("FilterValue = %q, want ollama/a:9b", got)
	}
}

// modelItem.Description must surface all four metadata columns
// (provider, location, source, tags). Without this, the browser is just
// a list of IDs and the user can't tell models apart.
func TestModelItemDescriptionHasProviderLocationSourceTags(t *testing.T) {
	it := modelItem{model: config.Model{
		ID:         "ollama/a:9b",
		ProviderID: "ollama",
		Location:   config.LocationLocal,
		Source:     config.SourceCurated,
		Tags:       []string{"code"},
	}}
	d := it.Description()
	for _, want := range []string{"ollama", "local", "curated", "code"} {
		if !strings.Contains(d, want) {
			t.Errorf("Description = %q, missing %q", d, want)
		}
	}
}

// modelItem.Description must render "-" when there are no tags so the
// columns stay aligned. Otherwise an empty-tag model would visibly shift
// the layout.
func TestModelItemDescriptionNoTags(t *testing.T) {
	it := modelItem{model: config.Model{
		ID:         "ollama/bare",
		ProviderID: "ollama",
		Location:   config.LocationLocal,
		Source:     config.SourceDiscovered,
	}}
	d := it.Description()
	if !strings.Contains(d, "-") {
		t.Errorf("Description = %q, expected \"-\" placeholder for empty tags", d)
	}
}

// TestRefreshBrowserFiltersByTag asserts refreshBrowser, given a seeded
// cache and a tag filter, builds the list from only the matching models.
// Without the filter the browser would show every model regardless of
// `f`.
func TestRefreshBrowserFiltersByTag(t *testing.T) {
	m := model{cfg: browserTestConfig(), width: 80, height: 24, tag: "code"}
	m.browserCache = []config.Model{
		{ID: "code-only", Tags: []string{"code"}},
		{ID: "design-only", Tags: []string{"design"}},
		{ID: "both", Tags: []string{"code", "design"}},
	}
	m.browserTag = "design"
	m.refreshBrowser()

	items := m.browser.Items()
	if len(items) != 2 {
		t.Fatalf("browser items = %d, want 2", len(items))
	}
	for _, it := range items {
		mi, ok := it.(modelItem)
		if !ok {
			continue
		}
		if !mi.model.HasTag("design") {
			t.Errorf("item %q in browser does not have design tag", mi.model.ID)
		}
	}
}

// TestRefreshBrowserEmptyTagShowsAll asserts that with browserTag="" the
// browser shows every cached model. A regression here would silently
// empty the browser on first open.
func TestRefreshBrowserEmptyTagShowsAll(t *testing.T) {
	m := model{cfg: browserTestConfig(), width: 80, height: 24, tag: "code"}
	m.browserCache = []config.Model{
		{ID: "a", Tags: []string{"code"}},
		{ID: "b", Tags: []string{"design"}},
	}
	m.browserTag = ""
	m.refreshBrowser()

	if got := len(m.browser.Items()); got != 2 {
		t.Errorf("browser items = %d, want 2", got)
	}
}

// TestRefreshBrowserUsesCachedDiscovery asserts that when browserCache is
// already populated, refreshBrowser does not re-populate it. Without this
// guarantee, every `f` press would shell out to `ollama list` and hit
// OpenRouter's API, which is wasteful and slow.
//
// We prove it indirectly: seed the cache with a sentinel ID, refresh
// twice, and verify the cache still points at the sentinel (i.e. Discover
// was not called and merged its results in).
func TestRefreshBrowserUsesCachedDiscovery(t *testing.T) {
	sentinel := []config.Model{{ID: "cached", Tags: []string{"code"}}}
	m := model{cfg: browserTestConfig(), width: 80, height: 24, tag: "code"}
	m.browserCache = sentinel
	m.refreshBrowser()
	m.refreshBrowser()

	if len(m.browserCache) != 1 || m.browserCache[0].ID != "cached" {
		t.Errorf("browserCache = %v, want sentinel [cached]", m.browserCache)
	}
}

// TestRefreshBrowserPopulatesCacheFromDiscover asserts that the very first
// refresh (with browserCache==nil) calls Discover and stores the result.
// A regression here would mean the cache is never populated, and we'd
// re-discover on every refresh. We verify by checking that after a
// refresh with a nil cache, browserCache is non-nil.
func TestRefreshBrowserPopulatesCacheFromDiscover(t *testing.T) {
	m := model{cfg: browserTestConfig(), width: 80, height: 24, tag: "code"}
	if m.browserCache != nil {
		t.Fatalf("setup: browserCache should start nil")
	}
	m.refreshBrowser()
	if m.browserCache == nil {
		t.Fatalf("browserCache still nil after refresh; cache must be populated")
	}
}

// TestBrowserKeyOpensBrowser asserts pressing 'm' on the agent+model
// screen transitions to phaseBrowser instead of writing the lesson-14
// placeholder. This is the primary entry point to the browser.
func TestBrowserKeyOpensBrowser(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, tag: "code"}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	gotModel := got.(model)
	if gotModel.phase != phaseBrowser {
		t.Errorf("phase = %v, want phaseBrowser", gotModel.phase)
	}
	if gotModel.status != "" {
		t.Errorf("status mutated by m key: %q (lesson-14 placeholder should be gone)", gotModel.status)
	}
}

// TestBrowserKeyIgnoredInListPhase asserts 'm' does nothing while still
// on the worktree list, matching how 'r' and 'd' are gated. The browser
// only makes sense after a worktree has been chosen.
func TestBrowserKeyIgnoredInListPhase(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseList, tag: "code"}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	gotModel := got.(model)
	if gotModel.phase != phaseList {
		t.Errorf("phase = %v, want phaseList (m must be ignored in list phase)", gotModel.phase)
	}
}

// TestBrowserEscReturnsToModelPhase asserts esc from phaseBrowser returns
// to phaseModel and does NOT quit. From phaseModel, esc still quits —
// that's the universal exit affordance.
func TestBrowserEscReturnsToModelPhase(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseBrowser, tag: "code"}
	got, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel", gotModel.phase)
	}
	if cmd != nil {
		t.Errorf("esc from browser returned non-nil cmd; expected no quit")
	}
}

// TestModelEscStillQuits asserts esc from phaseModel still issues
// tea.Quit. Without this, the user could get stuck in the alternate
// screen with no way back to the shell.
func TestModelEscStillQuits(t *testing.T) {
	m := model{cfg: testConfig(), phase: phaseModel, tag: "code"}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Errorf("esc from phaseModel returned nil cmd; expected tea.Quit")
	}
}

// TestBrowserEnterPicksModel asserts Enter on a browser item sets
// m.current to the picked model and returns to phaseModel. The pick must
// NOT advance the rotation state file — picking is a deliberate
// selection, distinct from `r` which advances rotation.
func TestBrowserEnterPicksModel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-wt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(dir))

	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code",
		current: config.Model{ID: "previous"}, width: 80, height: 24}
	m.browserCache = []config.Model{
		{ID: "ollama/a:9b", ProviderID: "ollama", Tags: []string{"code"}},
		{ID: "ollama/a:14b", ProviderID: "ollama", Tags: []string{"code"}},
	}
	m.refreshBrowser()

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.phase != phaseModel {
		t.Errorf("phase = %v, want phaseModel after Enter", gotModel.phase)
	}
	if gotModel.current.ID != "ollama/a:9b" {
		t.Errorf("current = %q, want ollama/a:9b (first browser item)", gotModel.current.ID)
	}

	// Rotation state file must not be touched.
	stateFile := rotation.StateFile(dir, "code")
	if _, err := os.Stat(stateFile); err == nil {
		t.Errorf("rotation state file was created; Enter must not advance rotation")
	}
}

// TestBrowserEnterIgnoresNonModelItem is defensive: if the list's
// SelectedItem somehow returns a non-modelItem, Enter is a no-op. A
// regression that panicked here would crash the TUI on edge cases.
//
// We force this by filtering the cache to zero items — the list still
// exists but has no visible rows, so SelectedItem returns nil.
func TestBrowserEnterIgnoresNonModelItem(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code",
		current: config.Model{ID: "untouched"}, width: 80, height: 24}
	m.browserCache = []config.Model{
		{ID: "code-only", Tags: []string{"code"}},
	}
	// Set a tag filter that matches nothing in the cache, so the list
	// is built but contains zero items.
	m.browserTag = "design"
	m.refreshBrowser()
	if items := len(m.browser.Items()); items != 0 {
		t.Fatalf("setup: filtered list has %d items, want 0", items)
	}

	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	gotModel := got.(model)
	if gotModel.phase != phaseBrowser {
		t.Errorf("phase = %v, want phaseBrowser (Enter on empty browser must be a no-op)", gotModel.phase)
	}
	if gotModel.current.ID != "untouched" {
		t.Errorf("current mutated: %q, want untouched", gotModel.current.ID)
	}
}

// TestBrowserTagFilterToggle asserts `f` cycles browserTag between ""
// and m.tag, and that the browser list shrinks/grows accordingly. This
// is the primary "show me design models" affordance.
func TestBrowserTagFilterToggle(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code"}
	m.browserCache = []config.Model{
		{ID: "code", Tags: []string{"code"}},
		{ID: "design", Tags: []string{"design"}},
	}
	m.width = 80
	m.height = 24

	// First refresh: no filter.
	m.refreshBrowser()
	if got := len(m.browser.Items()); got != 2 {
		t.Fatalf("unfiltered items = %d, want 2", got)
	}

	// Press 'f': browserTag becomes m.tag ("code"), list shrinks.
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	gotModel := got.(model)
	if gotModel.browserTag != "code" {
		t.Errorf("after first f: browserTag = %q, want code", gotModel.browserTag)
	}
	if got := len(gotModel.browser.Items()); got != 1 {
		t.Errorf("after first f: items = %d, want 1 (code only)", got)
	}

	// Press 'f' again: browserTag becomes "", list grows back.
	got, _ = gotModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	gotModel = got.(model)
	if gotModel.browserTag != "" {
		t.Errorf("after second f: browserTag = %q, want empty", gotModel.browserTag)
	}
	if got := len(gotModel.browser.Items()); got != 2 {
		t.Errorf("after second f: items = %d, want 2", got)
	}
}

// TestBrowserSourceFilterCycle is the optional challenge: `c` cycles
// sourceCycle 0=all, 1=curated, 2=discovered. A regression here would
// leave the source filter stuck on one value.
func TestBrowserSourceFilterCycle(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code"}
	m.browserCache = []config.Model{
		{ID: "cur", Source: config.SourceCurated},
		{ID: "disc", Source: config.SourceDiscovered},
	}
	m.width = 80
	m.height = 24

	// 0 = all (no source filter).
	m.refreshBrowser()
	if got := len(m.browser.Items()); got != 2 {
		t.Fatalf("cycle 0 items = %d, want 2", got)
	}

	// c → 1 (curated only).
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	gotModel := got.(model)
	if got := len(gotModel.browser.Items()); got != 1 {
		t.Errorf("cycle 1 items = %d, want 1 (curated only)", got)
	}

	// c → 2 (discovered only).
	got, _ = gotModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	gotModel = got.(model)
	if got := len(gotModel.browser.Items()); got != 1 {
		t.Errorf("cycle 2 items = %d, want 1 (discovered only)", got)
	}

	// c → 0 (back to all).
	got, _ = gotModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	gotModel = got.(model)
	if got := len(gotModel.browser.Items()); got != 2 {
		t.Errorf("cycle 3 (back to 0) items = %d, want 2", got)
	}
}

// TestBrowserViewRendersList asserts the View in phaseBrowser includes
// the title "Model browser". Without this, the user sees a blank screen
// after pressing `m`.
func TestBrowserViewRendersList(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code"}
	m.browserCache = []config.Model{{ID: "ollama/a:9b", Tags: []string{"code"}}}
	m.width = 80
	m.height = 24
	m.refreshBrowser()

	view := m.View()
	if !strings.Contains(view, "Model browser") {
		t.Errorf("View missing 'Model browser' title: %q", view)
	}
}

// TestBrowserViewShowsFilterLine asserts View includes a "filter:" line
// when browserTag is set. This is the empty-list hint that tells the
// user why they see nothing.
func TestBrowserViewShowsFilterLine(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code",
		browserTag: "design"}
	m.browserCache = []config.Model{{ID: "code-only", Tags: []string{"code"}}}
	m.width = 80
	m.height = 24
	m.refreshBrowser()

	view := m.View()
	if !strings.Contains(view, "filter") || !strings.Contains(view, "design") {
		t.Errorf("View missing filter line: %q", view)
	}
}

// TestBrowserIgnoresKeysWhenNotOpen asserts f, enter, and c are no-ops
// in phaseModel and phaseList. They only matter in phaseBrowser.
func TestBrowserIgnoresKeysWhenNotOpen(t *testing.T) {
	for _, phase := range []phase{phaseList, phaseModel} {
		for _, key := range []rune{'f', 'c'} {
			m := model{cfg: browserTestConfig(), phase: phase, tag: "code",
				current: config.Model{ID: "ollama/a:9b"}}
			got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
			gotModel := got.(model)
			if gotModel.browserTag != "" {
				t.Errorf("phase=%v key=%q: browserTag mutated: %q", phase, string(key), gotModel.browserTag)
			}
			if gotModel.sourceCycle != 0 {
				t.Errorf("phase=%v key=%q: sourceCycle mutated: %d", phase, string(key), gotModel.sourceCycle)
			}
		}
	}
}

// TestRefreshBrowserDeferredWhenNoWindowSize asserts that calling
// refreshBrowser before any WindowSizeMsg arrives does not panic and
// leaves the browser field in a renderable-but-empty state. Without
// this guard the browser would crash on the first `m` press on tiny
// terminals.
func TestRefreshBrowserDeferredWhenNoWindowSize(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code"}
	m.browserCache = []config.Model{{ID: "a", Tags: []string{"code"}}}
	// width and height are zero.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("refreshBrowser panicked with zero window size: %v", r)
		}
	}()
	m.refreshBrowser()
}

// TestWindowSizeMsgRebuildsBrowser asserts that a WindowSizeMsg arriving
// while the browser is open triggers refreshBrowser so the list lays
// out at the right size. Otherwise the browser would stay at the size
// it had on first open (or zero size).
func TestWindowSizeMsgRebuildsBrowser(t *testing.T) {
	m := model{cfg: browserTestConfig(), phase: phaseBrowser, tag: "code"}
	m.browserCache = []config.Model{{ID: "a", Tags: []string{"code"}}}

	got, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	gotModel := got.(model)
	if gotModel.width != 100 || gotModel.height != 30 {
		t.Errorf("window size = (%d, %d), want (100, 30)", gotModel.width, gotModel.height)
	}
	// The browser field should have been (re)built by refreshBrowser.
	// Calling View() must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked after WindowSizeMsg: %v", r)
		}
	}()
	_ = gotModel.View()
}
