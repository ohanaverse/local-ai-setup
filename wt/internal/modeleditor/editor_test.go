package modeleditor

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

func testTheme() themes.Theme {
	t, _ := themes.Get("default")
	return t
}

// runCmd runs a command the way the Bubble Tea runtime would, a batch's
// commands each in turn, and delivers what they produce.
func runCmd(t *testing.T, m *model, cmd tea.Cmd) *model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch out := cmd().(type) {
	case nil, tea.QuitMsg:
		return m
	case tea.BatchMsg:
		for _, c := range out {
			m = runCmd(t, m, c)
		}
		return m
	default:
		return send(t, m, out)
	}
}

// TestRunOpensOnTheModelsTabAndProbesAtOnce verifies the Models screen probes
// the providers as soon as it opens (Init hands the probe to a command next
// to the loaded config) and draws the Models tab first: there is no other tab
// to start on, and a screen that waited for a key before probing would open
// empty.
func TestRunOpensOnTheModelsTabAndProbesAtOnce(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := newModel(testTheme(), nil, nil)
	m.opts = Options{Models: tm.deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init returned no command")
	}
	m = runCmd(t, m, cmd)
	if tm.probes != 1 {
		t.Errorf("probes = %d after Init, want 1", tm.probes)
	}
	if view := m.View(); !strings.HasPrefix(view, "[Models]") {
		t.Errorf("the view should start with the Models tab:\n%s", view)
	}
}

// TestTabBarShowsOnlyModels pins the Models screen's tab bar: the one tab it
// has, current. Agents and Model server stay in `wt config`.
func TestTabBarShowsOnlyModels(t *testing.T) {
	got := tabBar(testTheme(), TabModels)
	if !strings.Contains(got, "[Models]") || strings.Contains(got, "Agents") || strings.Contains(got, "Model server") {
		t.Errorf("tab bar = %q, want [Models] alone", got)
	}
}

// TestQuitLeavesAtOnceWithNoAgentPrompt verifies q on the loaded table ends
// the program at once: the screen edits no agents and has no save key, so
// there is no unsaved-changes prompt to go through.
func TestQuitLeavesAtOnceWithNoAgentPrompt(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	next, cmd := m.Update(keyMsg("q"))
	if !isQuit(cmd) {
		t.Fatalf("q should quit at once; view:\n%s", next.(*model).View())
	}
}

// TestLoadedMsgLocationErrorNamesTheRegistry pins #209 on the Models screen.
// A mistyped location is in a registry.toml row, and the repair is an edit of
// that file: the load banner must carry config.RegistryFixHint's words, and
// any other config error keeps its wording with no hint. The banner must be
// drawn, not only kept: the probe's config.Load does not validate, so after
// it lands the banner is the one place the screen says the registry needs a
// repair.
func TestLoadedMsgLocationErrorNamesTheRegistry(t *testing.T) {
	t.Setenv("WT_REGISTRY", "/tmp/somewhere/registry.toml")
	cfg := &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "omlx", Location: "Local"}},
		Models:     []config.Model{{ID: "omlx/m", ProviderID: "omlx", ModelName: "m"}},
	}
	locErr := cfg.Validate()
	if locErr == nil {
		t.Fatal("fixture validated; want a location error")
	}
	got, _ := newModel(themes.Theme{}, cfg, locErr).Update(loadedMsg{cfg: cfg, err: locErr})
	want := `config load/validation error: provider "omlx" has location "Local"; expected "local" or "cloud" (` + config.RegistryFixHint(locErr) + `)`
	if status := got.(*model).status; status != want || !strings.Contains(status, "fix the entry in /tmp/somewhere/registry.toml") {
		t.Errorf("status = %q, want %q", status, want)
	}

	tm := newTabMachine(t, tabRegistry)
	m := newModel(testTheme(), cfg, locErr)
	m.opts = Options{Models: tm.deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: cfg, err: locErr})
	m = send(t, m, m.probeCmd()())
	if view := flatView(m.View()); !strings.Contains(view, flatView(want)) {
		t.Errorf("the load banner should be on screen after the probe:\n%s", m.View())
	}

	got, _ = newModel(themes.Theme{}, cfg, nil).Update(loadedMsg{cfg: cfg, err: fmt.Errorf("default_tag must not be empty")})
	if status := got.(*model).status; status != "config load/validation error: default_tag must not be empty" {
		t.Errorf("another config error's status = %q, want it unchanged", status)
	}
}

// flatView is a view's text with its ANSI styling removed and every run of
// whitespace, the wrap's line breaks included, read as one space: a status
// wrapped to the terminal's width still contains its sentence.
func flatView(s string) string {
	return strings.Join(strings.Fields(ansi.Strip(s)), " ")
}

// TestLoadBannerGivesWayToTheProbesLoadError verifies a config that does not
// load is reported once on the Models screen. The host hands its load error
// to the screen as the banner, and the probe's own config.Load fails on the
// same file: the probe's reason, which a later probe refreshes, is the one
// drawn, so the user does not read the same failure twice.
func TestLoadBannerGivesWayToTheProbesLoadError(t *testing.T) {
	tm := newTabMachine(t, "this is not = = toml\n")
	cfg, loadErr := tm.deps().Load()
	if loadErr == nil {
		t.Fatal("fixture loaded; want a parse error")
	}
	m := newModel(testTheme(), cfg, loadErr)
	m.opts = Options{Models: tm.deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = send(t, m, loadedMsg{cfg: cfg, err: loadErr})
	m = send(t, m, m.probeCmd()())
	view := flatView(m.View())
	if !strings.Contains(view, "config load error: ") {
		t.Errorf("the probe's load error should be on screen:\n%s", m.View())
	}
	if strings.Contains(view, "config load/validation error: ") {
		t.Errorf("the banner should give way to the probe's load error:\n%s", m.View())
	}
}
