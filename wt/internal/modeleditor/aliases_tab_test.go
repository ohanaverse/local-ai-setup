package modeleditor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// aliasTabRegistry is tabRegistry plus a native provider and model, three
// openrouter models (a 262144 window, a 131072 one, none) and two aliases.
const aliasTabRegistry = tabRegistry + `
[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "claude/native-one"
family = "sonnet"
provider_id = "claude"
model_name = "native-one"
tags = []

[[models]]
id = "openrouter/big"
family = "f"
provider_id = "openrouter"
model_name = "org/big"
tags = []
context_window = 262144

[[models]]
id = "openrouter/small"
family = "f"
provider_id = "openrouter"
model_name = "org/small"
tags = []
context_window = 131072

[[models]]
id = "openrouter/unknown"
family = "f"
provider_id = "openrouter"
model_name = "org/unknown"
tags = []

[[aliases]]
id = "alias/main"
targets = [
    { model = "openrouter/big", order = 1 },
]

[[aliases]]
id = "alias/pair"
targets = [
    { model = "openrouter/big", order = 1 },
    { model = "openrouter/small", order = 2 },
]
`

// aliasMachine is the Aliases tab's throwaway machine: canned routes and
// sessions, and a count of the reads.
type aliasMachine struct {
	routed    map[string]bool
	routeWarn string
	sessions  map[string]int
	sessErr   error
	reads     int
}

func (am *aliasMachine) deps() AliasDeps {
	return AliasDeps{
		Routed: func(context.Context, *config.Config) (map[string]bool, string) {
			am.reads++
			return am.routed, am.routeWarn
		},
		Sessions: func(context.Context) (map[string]int, error) { return am.sessions, am.sessErr },
		Index:    func() tiers.Index { return tiers.Index{} },
	}
}

// aliasEditor is the editor in a terminal of the given size, opened on the
// Aliases tab with its first read delivered.
func aliasEditor(t *testing.T, tm *tabMachine, am *aliasMachine, width, height int) *model {
	t.Helper()
	cfg, _ := tm.deps().Load()
	m := newModel(testTheme(), cfg, nil)
	m.home = testUserHome
	m.opts = Options{StartTab: TabAliases, Models: tm.deps(), Aliases: am.deps()}
	m.tab = TabAliases
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	m = runCmd(t, m, m.Init())
	return m
}

func defaultAliasMachine() *aliasMachine {
	return &aliasMachine{routed: map[string]bool{"alias/main": true}, sessions: map[string]int{"alias/main": 2}}
}

// press delivers keys: a command is run only for the keys that start one
// (a typed character in a form returns a cursor blink that sleeps).
func press(t *testing.T, m *model, ks ...string) *model {
	t.Helper()
	for _, k := range ks {
		inForm := m.tab == TabAliases && m.aliases.phase == aliasesForm
		if inForm && k != "ctrl+s" && k != "enter" {
			next, _ := m.Update(keyMsg(k))
			m = next.(*model)
			continue
		}
		if inForm && k == "enter" && m.aliases.form.cursor == afID {
			next, _ := m.Update(keyMsg(k))
			m = next.(*model)
			continue
		}
		m = send(t, m, keyMsg(k))
	}
	return m
}

// typeText types s into the focused form field.
func typeAlias(t *testing.T, m *model, s string) *model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(*model)
	}
	return m
}

func selectAlias(t *testing.T, m *model, id string) *model {
	t.Helper()
	m = press(t, m, "up", "up", "up", "up", "up", "up")
	for i := 0; i < 10; i++ {
		if v, ok := m.selectedAlias(); ok && v.ID == id {
			return m
		}
		m = press(t, m, "down")
	}
	t.Fatalf("alias %s not in the table:\n%s", id, m.View())
	return m
}

// TestAliasesTabListsTheAliasesWithTheirColumns pins the table: ALIAS,
// TARGETS, CONTEXT, ROUTED and SESSIONS, with routed yes/no and the sessions
// count, "?" for routes not read and "-" for no server.
func TestAliasesTabListsTheAliasesWithTheirColumns(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 100, 24)
	view := ansi.Strip(m.View())
	for _, want := range []string{"[Aliases]", "ALIAS", "TARGETS", "CONTEXT", "ROUTED", "SESSIONS", "alias/main", "1:openrouter/big", "alias/pair", "1:openrouter/big,2:openrouter/small", "262144", "131072"} {
		if !strings.Contains(view, want) {
			t.Errorf("the table lacks %q:\n%s", want, view)
		}
	}
	line := func(id string) string {
		for _, l := range strings.Split(view, "\n") {
			if strings.Contains(l, id) && strings.Contains(l, "1:") {
				return l
			}
		}
		return ""
	}
	if got := strings.Fields(line("alias/main")); got[len(got)-2] != "yes" || got[len(got)-1] != "2" {
		t.Errorf("alias/main row = %v, want routed yes and 2 sessions", got)
	}
	if got := strings.Fields(line("alias/pair")); got[len(got)-2] != "no" || got[len(got)-1] != "0" {
		t.Errorf("alias/pair row = %v, want routed no and 0 sessions", got)
	}
	// Unknown routes and no server read as ? and -.
	m = aliasEditor(t, tm, &aliasMachine{routeWarn: "routes not read (down)"}, 100, 24)
	view = ansi.Strip(m.View())
	if got := strings.Fields(line("alias/main")); got[len(got)-2] != "?" || got[len(got)-1] != "-" {
		t.Errorf("unread row = %v:\n%s", got, view)
	}
	if !strings.Contains(view, "routes not read (down)") {
		t.Errorf("the routes warning should be the footer:\n%s", view)
	}
}

// TestAliasesTabFitsTheTerminal checks the table, the form, an error and the
// prompts at 80x24 and 40x12 (and two more): nothing taller or wider than the
// terminal, the three columns that are never dropped always there, the
// dropped ones in order.
func TestAliasesTabFitsTheTerminal(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	for _, sz := range [][2]int{{80, 24}, {40, 12}, {60, 16}, {120, 40}} {
		w, h := sz[0], sz[1]
		m := aliasEditor(t, tm, defaultAliasMachine(), w, h)
		assertFits(t, "table", m.View(), w, h)
		view := ansi.Strip(m.View())
		for _, col := range []string{"ALIAS", "TARGETS", "CONTEXT"} {
			if !strings.Contains(view, col) {
				t.Errorf("%dx%d: %s is never dropped:\n%s", w, h, col, view)
			}
		}
		if w == 40 && (strings.Contains(view, "SESSIONS") || strings.Contains(view, "ROUTED")) {
			t.Errorf("40x12 should have dropped SESSIONS and ROUTED:\n%s", view)
		}
		if w == 80 && !strings.Contains(view, "SESSIONS") {
			t.Errorf("80x24 should keep every column:\n%s", view)
		}
		m = selectAlias(t, m, "alias/pair")
		assertFits(t, "table, pair selected", m.View(), w, h)
		// The form, with an error on it.
		m = press(t, m, "n")
		assertFits(t, "add form", m.View(), w, h)
		m = press(t, m, "enter")
		m = typeAlias(t, m, "openrouter/nope, openrouter/big:0, claude/native-one, "+strings.Repeat("x", 60))
		m = press(t, m, "ctrl+s")
		assertFits(t, "form with an error", m.View(), w, h)
		m = press(t, m, "esc")
		// The prompts.
		m = selectAlias(t, m, "alias/main")
		m = press(t, m, "enter", "ctrl+u")
		m = typeAlias(t, m, "openrouter/small")
		m = press(t, m, "ctrl+s")
		if m.aliases.phase != aliasesConfirm {
			t.Fatalf("%dx%d: want the shrink question, view:\n%s", w, h, m.View())
		}
		assertFits(t, "shrink question", m.View(), w, h)
		m = press(t, m, "n", "esc")
		m = selectAlias(t, m, "alias/pair")
		m = press(t, m, "d")
		assertFits(t, "remove prompt", m.View(), w, h)
	}
}

// TestAliasesTabShowsWhyAnAliasIsUnusable verifies the detail block under the
// table gives the whole targets and config.AliasFault's text for an alias the
// registry cannot use.
func TestAliasesTabShowsWhyAnAliasIsUnusable(t *testing.T) {
	reg := aliasTabRegistry + "\n[[aliases]]\nid = \"alias/bad\"\ntargets = [\n    { model = \"claude/native-one\", order = 1 },\n]\n"
	tm := newTabMachine(t, reg)
	m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
	m = selectAlias(t, m, "alias/bad")
	view := flatView(m.View())
	for _, want := range []string{"unusable:", "is a native model", "1:claude/native-one"} {
		if !strings.Contains(view, want) {
			t.Errorf("the detail lacks %q:\n%s", want, m.View())
		}
	}
}

// TestAliasesTabEmptyRegistry says how to add the first alias.
func TestAliasesTabEmptyRegistry(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	for _, w := range []int{80, 40} {
		m := aliasEditor(t, tm, defaultAliasMachine(), w, 12)
		assertFits(t, "empty", m.View(), w, 12)
		if !strings.Contains(m.View(), "no aliases: n adds one") {
			t.Errorf("empty table should say how to add:\n%s", m.View())
		}
	}
}

// TestAliasesTabAddWritesTheAlias adds an alias with the prefix left out and
// checks the registry, the routes-pending note and Result.RegistryChanged.
func TestAliasesTabAddWritesTheAlias(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
	m = press(t, m, "n")
	m = typeAlias(t, m, "fresh")
	m = press(t, m, "enter")
	m = typeAlias(t, m, "openrouter/small, openrouter/big:5")
	m = press(t, m, "enter")
	if !m.registryChanged || m.aliases.phase != aliasesList {
		t.Fatalf("registryChanged = %v, phase = %d, view:\n%s", m.registryChanged, m.aliases.phase, m.View())
	}
	text := tm.text(t)
	if !strings.Contains(text, `id = "alias/fresh"`) || !strings.Contains(text, `{ model = "openrouter/small" }`) || !strings.Contains(text, `{ model = "openrouter/big", order = 5 }`) {
		t.Errorf("registry lacks the new alias:\n%s", text)
	}
	view := m.View()
	if !strings.Contains(view, "alias/fresh") || !strings.Contains(view, "set alias/fresh") || !strings.Contains(view, routesPending) {
		t.Errorf("the table should show the new alias, the status and the pending note:\n%s", view)
	}
}

// TestAliasesTabEditTargets edits alias/pair down to one target (a bigger
// window, so no question) and checks the form was prefilled from the config.
func TestAliasesTabEditTargets(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
	m = selectAlias(t, m, "alias/pair")
	m = press(t, m, "enter")
	if got := m.aliases.form.text[afTargets].Value(); got != "openrouter/big:1, openrouter/small:2" {
		t.Fatalf("prefill = %q", got)
	}
	m = press(t, m, "ctrl+u")
	m = typeAlias(t, m, "openrouter/big:1")
	m = press(t, m, "ctrl+s")
	if m.aliases.phase != aliasesList || !m.registryChanged {
		t.Fatalf("an enlarging edit should write at once; view:\n%s", m.View())
	}
	text := tm.text(t)
	i := strings.Index(text, `id = "alias/pair"`)
	if i < 0 || strings.Contains(text[i:], "openrouter/small") {
		t.Errorf("alias/pair should hold one target now:\n%s", text[max(i, 0):])
	}
}

// TestAliasesTabRefusesWhatTheCommandRefuses pins the live validation: the
// same refusals as `wt-model alias set`, shown on the form, nothing written.
func TestAliasesTabRefusesWhatTheCommandRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ targets, want string }{
		"an alias":       {"alias/main", "an alias cannot target an alias"},
		"a native model": {"claude/native-one", "is a native model"},
		"two local":      {"omlx/Qwen3.8-27B-Instruct-MLX-6bit, omlx/Gone-4bit", "at most one local target"},
		"an unknown":     {"openrouter/nope", "names no model"},
		"a bad order":    {"openrouter/big:0", "order must be a whole number from 1"},
		"twice":          {"openrouter/big, openrouter/big:2", "more than once"},
		"nothing":        {" , ", "no targets"},
	} {
		tm := newTabMachine(t, aliasTabRegistry)
		m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
		m = press(t, m, "n")
		m = typeAlias(t, m, "newone")
		m = press(t, m, "enter")
		m = typeAlias(t, m, tc.targets)
		m = press(t, m, "ctrl+s")
		if m.aliases.phase != aliasesForm || m.registryChanged || tm.text(t) != aliasTabRegistry {
			t.Errorf("%s: the form should stay open with nothing written", name)
		}
		if err := m.aliases.form.err; !strings.Contains(err, tc.want) || strings.Contains(err, "--target") {
			t.Errorf("%s: error = %q, want %q (and no flag name)", name, err, tc.want)
		}
		if view := flatView(m.View()); !strings.Contains(view, flatView(tc.want)) {
			t.Errorf("%s: the error should be on the form:\n%s", name, m.View())
		}
	}
	// An id that exists, and an empty id.
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
	m = press(t, m, "n", "ctrl+s")
	if !strings.Contains(m.aliases.form.err, "needs an id") {
		t.Errorf("empty id: %q", m.aliases.form.err)
	}
	m = typeAlias(t, m, "alias/main")
	m = press(t, m, "enter")
	m = typeAlias(t, m, "openrouter/big")
	m = press(t, m, "ctrl+s")
	if !strings.Contains(m.aliases.form.err, "already an alias") {
		t.Errorf("existing id: %q", m.aliases.form.err)
	}
}

// TestAliasesTabShrinkAsksFirst pins decision 7 in the tab: a save that makes
// the window smaller asks in the command's own sentence, anything but y goes
// back to the form with nothing written, y writes.
func TestAliasesTabShrinkAsksFirst(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 120, 24)
	m = selectAlias(t, m, "alias/main")
	m = press(t, m, "enter", "ctrl+u")
	m = typeAlias(t, m, "openrouter/small")
	m = press(t, m, "ctrl+s")
	want := "alias/main's context window would go from 262144 to 131072 tokens; 2 client session(s) use it and keep the window they launched with. Set it anyway? [y/N]"
	if m.aliases.phase != aliasesConfirm || !strings.Contains(flatView(m.View()), flatView(want)) {
		t.Fatalf("want the question %q, view:\n%s", want, m.View())
	}
	m = press(t, m, "n")
	if m.aliases.phase != aliasesForm || tm.text(t) != aliasTabRegistry || m.registryChanged {
		t.Fatalf("n must go back to the form and write nothing")
	}
	m = press(t, m, "ctrl+s")
	if m.aliases.phase != aliasesConfirm {
		t.Fatalf("ctrl+s should ask again; phase = %d", m.aliases.phase)
	}
	m = press(t, m, "enter")
	if m.aliases.phase != aliasesForm || tm.text(t) != aliasTabRegistry {
		t.Fatal("a key other than y must go back to the form and write nothing")
	}
	m = press(t, m, "ctrl+s", "y")
	if !m.registryChanged || !strings.Contains(tm.text(t), `{ model = "openrouter/small" }`) {
		t.Errorf("y should write; registry:\n%s", tm.text(t))
	}
	// An unknown window is a shrink too.
	m = selectAlias(t, m, "alias/main")
	m = press(t, m, "enter", "ctrl+u")
	m = typeAlias(t, m, "openrouter/unknown")
	m = press(t, m, "ctrl+s")
	if m.aliases.phase != aliasesConfirm || !strings.Contains(flatView(m.View()), "to - tokens") {
		t.Errorf("a window that goes unknown should ask:\n%s", m.View())
	}
	// Sessions that cannot be read add nothing about them.
	am := defaultAliasMachine()
	am.sessErr = errors.New("serve.json unreadable")
	m = aliasEditor(t, newTabMachine(t, aliasTabRegistry), am, 120, 24)
	m = selectAlias(t, m, "alias/main")
	m = press(t, m, "enter", "ctrl+u")
	m = typeAlias(t, m, "openrouter/small")
	m = press(t, m, "ctrl+s")
	if v := flatView(m.View()); !strings.Contains(v, "131072 tokens. Set it anyway?") {
		t.Errorf("unread sessions: %s", v)
	}
}

// TestAliasesTabNoChange verifies a save that changes nothing says so and
// writes nothing.
func TestAliasesTabNoChange(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
	m = press(t, m, "enter", "ctrl+s")
	if m.registryChanged || m.aliases.phase != aliasesList || !strings.Contains(m.View(), "no change: alias/main") || tm.text(t) != aliasTabRegistry {
		t.Errorf("want no change; view:\n%s", m.View())
	}
}

// TestAliasesTabRemove walks the remove prompt: the question, no, then yes.
func TestAliasesTabRemove(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 80, 24)
	m = selectAlias(t, m, "alias/pair")
	m = press(t, m, "d")
	if v := flatView(m.View()); !strings.Contains(v, "Remove alias/pair from the registry? Its LiteLLM routes go with it. [y/N]") {
		t.Fatalf("remove prompt:\n%s", m.View())
	}
	m = press(t, m, "n")
	if m.aliases.phase != aliasesList || tm.text(t) != aliasTabRegistry {
		t.Fatal("n must go back with nothing removed")
	}
	m = press(t, m, "d", "y")
	if !m.registryChanged || strings.Contains(tm.text(t), "alias/pair") || !strings.Contains(m.View(), "removed alias/pair") || strings.Contains(m.View(), "alias/pair  ") {
		t.Errorf("alias/pair should be gone:\n%s\n%s", m.View(), tm.text(t))
	}
	if !strings.Contains(m.View(), routesPending) {
		t.Errorf("the pending note:\n%s", m.View())
	}
}

// TestAliasesTabRefusesToRemoveAnAliasInUse pins the guard: no prompt while a
// client session uses the alias or the sessions cannot be read; no session
// and no server removes.
func TestAliasesTabRefusesToRemoveAnAliasInUse(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, defaultAliasMachine(), 120, 24)
	m = press(t, m, "d")
	if m.aliases.phase != aliasesList || !strings.Contains(flatView(m.View()), "not removed: alias/main is used by 2 client session(s); `wt-model alias rm --force-in-use` overrides") {
		t.Errorf("in use:\n%s", m.View())
	}
	am := defaultAliasMachine()
	am.sessErr = errors.New("boom")
	m = aliasEditor(t, tm, am, 120, 24)
	m = press(t, m, "d")
	if m.aliases.phase != aliasesList || !strings.Contains(flatView(m.View()), "client sessions could not be read") || !strings.Contains(m.View(), "SESSIONS") {
		t.Errorf("unreadable:\n%s", m.View())
	}
	// No server: nothing is in use, and the column says -.
	m = aliasEditor(t, tm, &aliasMachine{}, 120, 24)
	m = press(t, m, "d")
	if m.aliases.phase != aliasesRemove {
		t.Errorf("with no server running the prompt should open:\n%s", m.View())
	}
	if tm.text(t) != aliasTabRegistry {
		t.Error("nothing should have been written")
	}
}

// TestAliasesTabWriteFailures verifies a registry that changed since the read
// is refused by the write, on the form and as a status, and the table is read
// again.
func TestAliasesTabWriteFailures(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	am := defaultAliasMachine()
	m := aliasEditor(t, tm, am, 120, 24)
	m = press(t, m, "n")
	m = typeAlias(t, m, "late")
	m = press(t, m, "enter")
	m = typeAlias(t, m, "openrouter/small")
	// Another program removes the model between the read and the save.
	gone := strings.Replace(aliasTabRegistry, `id = "openrouter/small"`, `id = "openrouter/other"`, 1)
	gone = strings.Replace(gone, `{ model = "openrouter/small", order = 2 }`, `{ model = "openrouter/big", order = 2 }`, 1)
	writeFileT(t, tm.registry, gone)
	reads := am.reads
	m = press(t, m, "ctrl+s")
	if m.aliases.phase != aliasesForm || m.registryChanged || !strings.Contains(m.aliases.form.err, "not saved: ") || !strings.Contains(m.aliases.form.err, "names no model") {
		t.Fatalf("the write should be refused on the form: %q", m.aliases.form.err)
	}
	if am.reads != reads+1 {
		t.Errorf("a refused write should read again: reads %d -> %d", reads, am.reads)
	}
	// A removal of an alias another program already removed.
	m = press(t, m, "esc")
	m = selectAlias(t, m, "alias/pair")
	m = press(t, m, "d")
	writeFileT(t, tm.registry, strings.Replace(gone, `id = "alias/pair"`, `id = "alias/gone"`, 1))
	reads = am.reads
	m = press(t, m, "y")
	if m.registryChanged || !strings.Contains(flatView(m.View()), "not removed: ") || am.reads != reads+1 {
		t.Errorf("a refused removal should say so and read again (reads %d -> %d):\n%s", reads, am.reads, m.View())
	}
}

// TestAliasesTabQuitWaitsForTheWrite verifies a quit asked for while a write
// is out is held, and issued when the write has reported, with the change
// recorded for the caller (the route sync it owes).
func TestAliasesTabQuitWaitsForTheWrite(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	m := aliasEditor(t, tm, &aliasMachine{}, 80, 24)
	m = selectAlias(t, m, "alias/pair")
	m = press(t, m, "d")
	next, write := m.Update(keyMsg("y"))
	m = next.(*model)
	if write == nil || !m.aliases.writing {
		t.Fatal("y should start the write")
	}
	next, cmd := m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || !strings.Contains(m.View(), "quitting when the registry write is done") {
		t.Fatalf("the quit should be held; view:\n%s", m.View())
	}
	next, cmd = m.Update(write())
	m = next.(*model)
	if !isQuit(cmd) || !m.registryChanged {
		t.Errorf("the quit should be issued once the write reported, with the change recorded (registryChanged = %v)", m.registryChanged)
	}

	// The same from the form, with ctrl+c.
	tm = newTabMachine(t, aliasTabRegistry)
	m = aliasEditor(t, tm, &aliasMachine{}, 80, 24)
	m = press(t, m, "n")
	m = typeAlias(t, m, "held")
	m = press(t, m, "enter")
	m = typeAlias(t, m, "openrouter/small")
	next, write = m.Update(keyMsg("enter"))
	m = next.(*model)
	if write == nil || !m.aliases.writing {
		t.Fatal("enter on the targets should start the write")
	}
	next, cmd = m.Update(keyMsg("ctrl+c"))
	m = next.(*model)
	if cmd != nil || !m.quitPending || !strings.Contains(m.View(), "saving, then quitting") {
		t.Fatalf("the quit should be held:\n%s", m.View())
	}
	next, cmd = m.Update(write())
	m = next.(*model)
	if !isQuit(cmd) || !m.registryChanged {
		t.Error("the held quit should be issued after the save")
	}
}

// TestAliasesTabRefreshReadsAgain pins r: one read per press, none while one is
// out.
func TestAliasesTabRefreshReadsAgain(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	am := defaultAliasMachine()
	m := aliasEditor(t, tm, am, 80, 24)
	if am.reads != 1 {
		t.Fatalf("reads after open = %d", am.reads)
	}
	m = press(t, m, "r")
	if am.reads != 2 {
		t.Errorf("reads after r = %d, want 2", am.reads)
	}
}

// TestAliasesTabIsTheMiddleOfThreeTabs cycles Models, Aliases, Setup and
// round: the Aliases tab reads on its first visit only, and a registry change
// there makes the Models tab read again on its next visit.
func TestAliasesTabIsTheMiddleOfThreeTabs(t *testing.T) {
	tm := newTabMachine(t, aliasTabRegistry)
	am := defaultAliasMachine()
	cfg, _ := tm.deps().Load()
	m := newModel(testTheme(), cfg, nil)
	m.home = testUserHome
	m.opts = Options{Models: tm.deps(), Aliases: am.deps(), Setup: (&setupMachine{}).deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = runCmd(t, m, m.Init())
	if am.reads != 0 || !strings.Contains(m.View(), "[Models]") {
		t.Fatalf("the Aliases tab must not read before its first visit (reads = %d)", am.reads)
	}
	m = keys(t, m, "tab")
	if m.tab != TabAliases || am.reads != 1 || !strings.Contains(m.View(), "[Aliases]") {
		t.Fatalf("tab = %d reads = %d", m.tab, am.reads)
	}
	m = keys(t, m, "tab")
	if m.tab != TabSetup {
		t.Fatalf("the second tab should reach Setup, tab = %d", m.tab)
	}
	m = keys(t, m, "tab")
	if m.tab != TabModels {
		t.Fatalf("the third tab should round to Models, tab = %d", m.tab)
	}
	m = keys(t, m, "tab")
	if m.tab != TabAliases || am.reads != 1 {
		t.Errorf("a second visit shows what is there: tab = %d reads = %d", m.tab, am.reads)
	}
	// StartTab opens on the tab and reads at once.
	am2 := defaultAliasMachine()
	m = aliasEditor(t, tm, am2, 80, 24)
	if am2.reads != 1 || !strings.Contains(m.View(), "alias/main") {
		t.Errorf("StartTab = TabAliases: reads = %d\n%s", am2.reads, m.View())
	}
}

// TestAliasesTabRefusesAStaleRedirectEdit says why enter does nothing on a
// redirect: the writer's own sentence.
func TestAliasesTabRefusesToEditARedirect(t *testing.T) {
	reg := strings.Replace(aliasTabRegistry, "[[aliases]]\nid = \"alias/pair\"", "[[aliases]]\nid = \"alias/old\"\nredirect_to = \"alias/main\"\n\n[[aliases]]\nid = \"alias/pair\"", 1)
	tm := newTabMachine(t, reg)
	m := aliasEditor(t, tm, defaultAliasMachine(), 120, 24)
	m = selectAlias(t, m, "alias/old")
	if !strings.Contains(flatView(m.View()), "redirects to alias/main") {
		t.Errorf("the detail should say it is a redirect:\n%s", m.View())
	}
	m = press(t, m, "enter")
	if m.aliases.phase != aliasesList || !strings.Contains(flatView(m.View()), "is a redirect to") {
		t.Errorf("enter on a redirect should refuse:\n%s", m.View())
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
