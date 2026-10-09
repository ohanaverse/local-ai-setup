package configeditor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// tabRegistry is the registry the Models tab tests edit: an omlx and an
// openrouter provider, a long-id cloud model, an omlx model that is on disk
// and one that is not.
const tabRegistry = `[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
id = "openrouter/anthropic--claude-sonnet-4.5-thinking"
family = "sonnet"
provider_id = "openrouter"
model_name = "anthropic/claude-sonnet-4.5-thinking"
tags = [
    "code",
]

[models.cost]
input_price_per_million = 3
output_price_per_million = 15

[[models]]
id = "omlx/Qwen3.8-27B-Instruct-MLX-6bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Qwen3.8-27B-Instruct-MLX-6bit"
tags = []

[[models]]
id = "omlx/Gone-4bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Gone-4bit"
tags = []
`

// tabMachine is a throwaway machine for the Models tab: a registry file wt
// really reads and writes, and a canned probe. probes counts inventory
// rounds.
type tabMachine struct {
	registry string
	probes   int
	// longPath, when set, is where the probe finds tabLongID's weights; the
	// probe reports that model missing otherwise.
	longPath string
}

// tabWeights is where the probe finds the omlx model; tabWeightsShown is how
// the tab writes it (TestMain makes /Users/dev the home directory).
const (
	tabWeights      = "/Users/dev/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit"
	tabWeightsShown = "~/.omlx/models/Qwen3.8-27B-Instruct-MLX-6bit"
)

// tabLongID is a 72-column id, wider than the MODEL column can be at 80
// columns: the name of a locally quantized model.
const tabLongID = "omlx/Qwen3.8-35B-A3B-Instruct-abliterated-heretic-MLX-dynamic-quant-6bit"

// tabLongRegistry is tabRegistry with the missing omlx model under tabLongID.
var tabLongRegistry = strings.ReplaceAll(tabRegistry, "Gone-4bit", strings.TrimPrefix(tabLongID, "omlx/"))

// tabLongWeights is where omlx keeps tabLongID's weights, an 82-column path
// as the tab writes it; tabLongerWeights is the same directory somewhere
// deeper, 183 columns as written: five lines of a 40-column terminal.
var (
	tabLongWeights   = "/Users/dev/.omlx/models/" + strings.TrimPrefix(tabLongID, "omlx/")
	tabLongerWeights = "/Users/dev/quantized/" + strings.Repeat("dynamic-quant-experiments/", 4) + strings.TrimPrefix(tabLongID, "omlx/")
)

// tabMalformedRegistry is tabRegistry whose last row, the omlx model that is
// not on disk, has a fetch that is a string: a local_path written where the
// table belongs. The loader reads it as absent (config.Model.Malformed).
// tabMalformedLongRegistry is the worst case for the detail block: the same
// row under tabLongID, with a draft that is not a table either.
var (
	tabMalformedRegistry     = tabRegistry + "fetch = \"~/models/gone\"\n"
	tabMalformedLongRegistry = strings.ReplaceAll(tabMalformedRegistry, "Gone-4bit", strings.TrimPrefix(tabLongID, "omlx/")) + "draft = 3\n"
)

// tabDuplicatedRegistry is tabRegistry with a second row under the id
// omlx/Gone-4bit, an openrouter one: a registry every launch refuses, which
// the tab still lists so that it can be repaired.
const tabDuplicatedRegistry = tabRegistry + `
[[models]]
id = "omlx/Gone-4bit"
family = "qwen3.8"
provider_id = "openrouter"
model_name = "qwen/gone"
tags = []
`

func newTabMachine(t *testing.T, content string) *tabMachine {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	path := filepath.Join(home, "local-ai", "registry.toml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &tabMachine{registry: path}
}

func (tm *tabMachine) text(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(tm.registry)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// deps are the tab's ways out, pointed at this machine: the real config
// loader over the scratch registry, and a probe that finds the omlx model on
// disk and running, the other one missing, and one ollama model nobody
// registered.
func (tm *tabMachine) deps() ModelsDeps {
	return ModelsDeps{
		Load: func() (*config.Config, error) {
			cfg, err := config.Load()
			if errors.Is(err, config.ErrRegistryMissing) {
				err = nil
			}
			return cfg, err
		},
		Probe: func(cfg *config.Config) localmodels.Snapshot {
			tm.probes++
			snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK}}
			for _, m := range cfg.Models {
				switch m.ID {
				case "omlx/Qwen3.8-27B-Instruct-MLX-6bit":
					snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: m.ID, ModelName: m.ModelName, Artifact: m.ModelName,
						Registered: true, ArtifactKnown: true, Running: true, Path: tabWeights})
				case tabLongID:
					if tm.longPath != "" {
						snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: m.ID, ModelName: m.ModelName, Artifact: m.ModelName,
							Registered: true, ArtifactKnown: true, Path: tm.longPath})
						continue
					}
					fallthrough
				case "omlx/Gone-4bit":
					snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "omlx", ModelID: m.ID, ModelName: m.ModelName, Registered: true, ArtifactKnown: true})
				}
			}
			snap.Entries = append(snap.Entries, localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/qwen3:8b", ModelName: "qwen3:8b", Artifact: "qwen3:8b", ArtifactKnown: true, Size: 5_200_000_000})
			return snap
		},
		SeedEnv:      func() config.SeedEnv { return config.SeedEnv{} },
		Capabilities: func(*config.Config, string) (map[string]any, error) { return nil, errors.New("not stubbed") },
	}
}

// send delivers msg and then runs every command it leads to, the way the
// Bubble Tea runtime would, until none is left. A quit ends the walk.
func send(t *testing.T, m *model, msg tea.Msg) *model {
	t.Helper()
	next, cmd := m.Update(msg)
	m = next.(*model)
	for cmd != nil {
		out := cmd()
		cmd = nil
		switch out := out.(type) {
		case nil, tea.QuitMsg:
		case tea.BatchMsg:
			for _, c := range out {
				if c != nil {
					m = send(t, m, c())
				}
			}
		default:
			next, cmd = m.Update(out)
			m = next.(*model)
		}
	}
	return m
}

func keys(t *testing.T, m *model, ks ...string) *model {
	t.Helper()
	for _, k := range ks {
		m = sendKey(t, m, keyMsg(k))
	}
	return m
}

// keyMsg is the key message for a key's name, or for one typed character.
func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// sendKey delivers one key. Inside the model form only ctrl+s starts a
// command worth running: the others a text input returns are cursor blinks,
// each of which sleeps for half a second.
func sendKey(t *testing.T, m *model, msg tea.KeyMsg) *model {
	t.Helper()
	if m.models.phase == modelsForm && msg.Type != tea.KeyCtrlS {
		next, _ := m.Update(msg)
		return next.(*model)
	}
	return send(t, m, msg)
}

// modelsEditor is the editor in a terminal of the given size, opened on the
// Models tab with its first probe delivered.
func modelsEditor(t *testing.T, tm *tabMachine, width, height int) *model {
	t.Helper()
	cfg, _ := tm.deps().Load()
	m := newModel(testTheme(), cfg, nil)
	m.opts = Options{StartTab: TabModels, Models: tm.deps()}
	m.tab = TabModels
	m = send(t, m, tea.WindowSizeMsg{Width: width, Height: height})
	next, _ := m.Update(loadedMsg{cfg: cfg})
	m = next.(*model)
	return send(t, m, m.probeCmd()())
}

func selectModel(t *testing.T, m *model, id string) *model {
	t.Helper()
	for i, it := range m.models.list.VisibleItems() {
		if it.(modelRow).row.ID == id {
			m.models.list.Select(i)
			// Through Update, so the frame is fitted to the new selection.
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
	}
	t.Fatalf("no row %q on the Models tab", id)
	return m
}

// tabSizes are the terminal sizes wt supports, widths 40/80/120 by heights
// 12/24/50, for a test that measures one screen at each.
var tabSizes = func() (sizes [][2]int) {
	for _, w := range []int{40, 80, 120} {
		for _, h := range []int{12, 24, 50} {
			sizes = append(sizes, [2]int{w, h})
		}
	}
	return sizes
}()

// assertFits fails when view is taller or wider than the terminal. Bubble Tea
// drops a too-tall view's top lines (the tab bar and the status) and cuts a
// too-wide line at the edge.
func assertFits(t *testing.T, name, view string, width, height int) {
	t.Helper()
	if h := lipgloss.Height(view); h > height {
		t.Errorf("%s at %dx%d: the view is %d lines tall:\n%s", name, width, height, h, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("%s at %dx%d: a line is %d columns wide: %q", name, width, height, w, line)
		}
	}
}

// flat is a view with its line breaks and padding taken out, to look for text
// the view wraps at the terminal's width.
func flat(view string) string { return strings.Join(strings.Fields(view), "") }

// stillCursors stops the cursors of the two lists' filter inputs blinking. A
// blink is a command that sleeps for half a second, and send runs every
// command a key leads to; call it before typing a filter.
func stillCursors(m *model) *model {
	m.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	m.models.list.FilterInput.Cursor.SetMode(cursor.CursorStatic)
	return m
}

// visibleIDs are the ids of the table's rows as filtered, in order.
func visibleIDs(m *model) []string {
	var ids []string
	for _, it := range m.models.list.VisibleItems() {
		ids = append(ids, it.(modelRow).row.ID)
	}
	return ids
}

// TestTabKeySwitchesTabsAndProbesOnce verifies tab moves between the Agents
// and Models tabs, that the Agents tab's help names the key, that the first
// visit to Models probes the providers in a command, that tab goes back while
// that probe is still out — a hung provider must not hold the user on the
// tab — and that coming back does not probe again (r does). A probe on every
// tab press would dial every local server each time.
func TestTabKeySwitchesTabsAndProbesOnce(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	cfg, _ := tm.deps().Load()
	m := newModel(testTheme(), cfg, nil)
	m.opts = Options{Models: tm.deps()}
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: cfg})
	if !strings.HasPrefix(m.View(), "[Agents]   Models") || tm.probes != 0 {
		t.Fatalf("the editor should open on the Agents tab without probing; probes = %d, view:\n%s", tm.probes, m.View())
	}
	if !strings.Contains(m.View(), "tab models") || !strings.Contains(m.View(), "q quit") {
		t.Errorf("the Agents tab's help should name tab and q:\n%s", m.View())
	}
	// The key alone: the probe is a command, not something Update waits for.
	next, probe := m.Update(keyMsg("tab"))
	m = next.(*model)
	if m.tab != TabModels || probe == nil || tm.probes != 0 || !strings.Contains(m.View(), "Probing providers...") {
		t.Fatalf("tab should switch at once and hand the probe to a command; tab = %d, probes = %d, view:\n%s", m.tab, tm.probes, m.View())
	}
	// Back and forth before the probe has answered: no waiting, no second probe.
	next, cmd := m.Update(keyMsg("tab"))
	if m = next.(*model); m.tab != TabAgents || cmd != nil {
		t.Fatalf("tab while the first probe is out should go back to Agents; tab = %d", m.tab)
	}
	next, cmd = m.Update(keyMsg("tab"))
	if m = next.(*model); m.tab != TabModels || cmd != nil {
		t.Fatalf("back on Models with the probe still out: tab = %d, a second probe = %v", m.tab, cmd != nil)
	}
	m = send(t, m, probe())
	if tm.probes != 1 || !strings.Contains(m.View(), " Agents   [Models]") || !strings.Contains(m.View(), "omlx/Gone-4bit") {
		t.Fatalf("after the probe: probes = %d, view:\n%s", tm.probes, m.View())
	}
	m = keys(t, m, "tab")
	if m.tab != TabAgents {
		t.Fatal("tab on the Models tab should go back to Agents")
	}
	m = keys(t, m, "tab")
	if tm.probes != 1 {
		t.Errorf("probes = %d after returning to the Models tab, want still 1", tm.probes)
	}
	m = keys(t, m, "r")
	if tm.probes != 2 {
		t.Errorf("probes = %d after r, want 2", tm.probes)
	}
}

// TestModelsTabFitsTheTerminal measures the Models tab at every size wt
// supports, in each state that changes its height or its width: plain, with a
// 72-column id in the table, with that row selected, right after a removal
// (a status that wraps, and the routes-pending note), one key later, the
// remove prompt (for a short id, for a 72-column one, and for that one with a
// path of five lines), a filter being typed, applied and matching nothing,
// and each status the tab puts over the table: a probe that is out, a row
// that is not the registry's to remove, a quit waiting for a write; and the
// form in each of its phases — the add form empty, filled with values longer
// than their rows, with a family suggestion offered and accepted, with a
// price refused, on its last field; the edit form of a 72-column id; the
// register form; an edit refused because the registry holds the id twice
// (a refusal of several lines, which at 40x12 takes the screen from the
// fields); and the table a save comes back to, with the routes pending. The view
// must stay inside the terminal with the tab bar and the table's header row
// on screen; STATUS and RUNNING are on every row from 80 columns up however
// long an id is; the selected row's id is on screen whole, with its status,
// even where the table had to cut it; and what a state is there to say (its
// want) is on screen whole. A phase that is not measured here is one a later
// change can push off a 40x12 terminal unseen.
func TestModelsTabFitsTheTerminal(t *testing.T) {
	const removed = "removed omlx/Qwen3.8-27B-Instruct-MLX-6bit; weights are still at\n" + tabWeightsShown
	const closing = "wt removes the registry entry only; it never deletes weights."
	same := func(t *testing.T, m *model) *model { return m }
	// typing opens the filter and types into it.
	typing := func(text ...string) func(t *testing.T, m *model) *model {
		return func(t *testing.T, m *model) *model {
			return keys(t, stillCursors(m), append([]string{"/"}, text...)...)
		}
	}
	longPrompt := func(t *testing.T, m *model) *model { return keys(t, selectModel(t, m, tabLongID), "d") }
	// filled opens the add form and types values longer than a 40-column
	// row; then moves to field and types more there.
	filled := func(field int, more string) func(t *testing.T, m *model) *model {
		return func(t *testing.T, m *model) *model {
			m = typeText(t, moveTo(t, keys(t, m, "n"), mfName), "Qwen3.8-35B-A3B-Instruct-abliterated-heretic-MLX-dynamic-quant-6bit")
			m = typeText(t, moveTo(t, m, mfTags), "code,design,reasoning,vision,long-context,cheap")
			return typeText(t, moveTo(t, m, field), more)
		}
	}
	// editDuplicated opens the edit form on the second row of a duplicated
	// id, changes its tags and saves: the writer refuses.
	editDuplicated := func(t *testing.T, m *model) *model {
		for i, it := range m.models.list.VisibleItems() {
			if r := it.(modelRow).row; strings.HasPrefix(r.ID, "omlx/") && r.ProviderID == "openrouter" {
				m.models.list.Select(i)
			}
		}
		return keys(t, typeText(t, moveTo(t, keys(t, m, "enter"), mfTags), "code"), "ctrl+s")
	}
	states := []struct {
		name     string
		registry string
		set      func(t *testing.T, m *model) *model
		// want is text the state must show whole. The tab wraps it at the
		// terminal's width, so it is looked for with the line breaks and the
		// padding taken out (flat).
		want []string
		// bare is a state with no table header and no detail block to look
		// for: a prompt, or a list bubbles draws its filter input over.
		bare bool
		// longPath is where the probe finds tabLongID's weights.
		longPath string
	}{
		{name: "list", registry: tabRegistry, set: same},
		{name: "list with a long id", registry: tabLongRegistry, set: same},
		{name: "long id selected", registry: tabLongRegistry, set: func(t *testing.T, m *model) *model { return selectModel(t, m, tabLongID) }},
		{name: "local model selected", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			return selectModel(t, m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit")
		}},
		{name: "malformed fetch selected", registry: tabMalformedRegistry, set: func(t *testing.T, m *model) *model {
			return selectModel(t, m, "omlx/Gone-4bit")
		}},
		{name: "malformed fetch and draft under a long id", registry: tabMalformedLongRegistry, set: func(t *testing.T, m *model) *model {
			return selectModel(t, m, tabLongID)
		}},
		{name: "just after a removal", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			m.models.status, m.registryChanged = removed, true
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}},
		{name: "one key after a removal", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			m.models.status, m.registryChanged = removed, true
			return keys(t, m, "down")
		}},
		{name: "remove prompt", registry: tabRegistry, bare: true, set: func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit"), "d")
		}, want: []string{"Remove omlx/Qwen3.8-27B-Instruct-MLX-6bit from the registry? [y/N]", "weights are still at", tabWeightsShown, closing}},
		// 13 lines at 40 columns as the prompt is written; the blank lines
		// are what a 12-line terminal gives up.
		{name: "remove prompt for a long id", registry: tabLongRegistry, bare: true, longPath: tabLongWeights, set: longPrompt,
			want: []string{"Remove " + tabLongID + " from the registry? [y/N]", "weights are still at", tildePath(tabLongWeights), closing}},
		// Two lines more: at 40x12 the closing sentence goes too, never a
		// line of the path (TestRemovePromptGivesUpLinesBeforeThePath).
		{name: "remove prompt for a long id and a long path", registry: tabLongRegistry, bare: true, longPath: tabLongerWeights, set: longPrompt,
			want: []string{"Remove " + tabLongID + " from the registry? [y/N]", "weights are still at", tildePath(tabLongerWeights)}},
		// bubbles draws the filter's input where the table's header was.
		{name: "filter being typed", registry: tabRegistry, bare: true, set: typing("q", "w"), want: []string{"qw"}},
		{name: "filter applied", registry: tabRegistry, set: typing("q", "w", "enter")},
		{name: "filter with no match", registry: tabRegistry, bare: true, set: typing("z", "z", "z"), want: []string{"zzz"}},
		{name: "probing", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			m.models.status, m.models.busy = modelsProbing, true
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}, want: []string{modelsProbing}},
		{name: "nothing to remove", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "ollama/qwen3:8b"), "d")
		}, want: []string{"ollama/qwen3:8b is not in the registry: there is nothing to remove"}},
		{name: "add form", registry: tabRegistry, bare: true, set: func(t *testing.T, m *model) *model { return keys(t, m, "n") },
			want: []string{"Add a model", "> Provider: < ollama >"}},
		{name: "add form with long values", registry: tabRegistry, bare: true, set: filled(mfTags, ""),
			want: []string{"Add a model", "> Tags:"}},
		{name: "family suggestion offered", registry: tabRegistry, bare: true, set: filled(mfFamily, "qw"),
			want: []string{"> Family: qwen3.8"}},
		{name: "family suggestion accepted", registry: tabRegistry, bare: true, set: func(t *testing.T, m *model) *model {
			return keys(t, filled(mfFamily, "QW")(t, m), "right")
		}, want: []string{"> Family: qwen3.8"}},
		{name: "price refused", registry: tabRegistry, bare: true, set: func(t *testing.T, m *model) *model {
			return keys(t, typeText(t, filled(mfFamily, "qwen3.8")(t, m), ""), "down", "down", "down", "c", "h", "e", "a", "p", "ctrl+s")
		}, want: []string{`input-price must be a number, got "cheap"`, "> Input $/M: cheap"}},
		{name: "add form on its last field", registry: tabRegistry, bare: true, set: filled(mfSubPeriod, ""),
			want: []string{"> Subscription period: < (none) >"}},
		{name: "edit form of a long id", registry: tabLongRegistry, bare: true, set: func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, tabLongID), "enter")
		}, want: []string{"Edit " + tabLongID, "> Family: qwen3.8"}},
		{name: "register form", registry: tabRegistry, bare: true, set: func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "ollama/qwen3:8b"), "enter")
		}, want: []string{"Register ollama/qwen3:8b", "Model name: qwen3:8b", "> Family:"}},
		{name: "edit of a duplicated id refused", registry: tabDuplicatedRegistry, bare: true, set: editDuplicated,
			want: []string{`model "omlx/Gone-4bit" is in the registry twice (providers omlx, openrouter); wt cannot tell which one you mean`, "registry.toml"}},
		// The same refusal for a 72-column id: fourteen lines at 40 columns.
		// Whatever is given up, it is not the tab bar or the refusal's start.
		{name: "edit of a duplicated long id refused", registry: strings.ReplaceAll(tabDuplicatedRegistry, "Gone-4bit", strings.TrimPrefix(tabLongID, "omlx/")),
			bare: true, set: editDuplicated, want: []string{`model "` + tabLongID + `" is in the registry twice`}},
		{name: "just after a save", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			m = keys(t, m, "n", "right", "right") // ollama -> omlx -> openrouter
			m = typeText(t, moveTo(t, m, mfName), "qwen/qwen3.8-27b")
			return keys(t, typeText(t, moveTo(t, m, mfFamily), "qwen3.8"), "ctrl+s")
		}, want: []string{"added openrouter/qwen--qwen3.8-27b", routesPending}},
		// Four lines of status and the note: at 40x12 they leave the table no
		// row, and have the screen (modelsView).
		{name: "just after a save with notes", registry: tabRegistry, bare: true, set: func(t *testing.T, m *model) *model {
			m = typeText(t, moveTo(t, keys(t, m, "n"), mfName), "tiny:1b")
			return keys(t, typeText(t, moveTo(t, m, mfFamily), "tiny"), "ctrl+s")
		}, want: []string{"added ollama/tiny:1b", "added without tool/vision capabilities: not stubbed", "added provider ollama", routesPending}},
		{name: "quit waiting for a write", registry: tabRegistry, set: func(t *testing.T, m *model) *model {
			m.models.status, m.models.busy, m.models.writing, m.quitPending = "removing omlx/Gone-4bit...", true, true, true
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}, want: []string{"removing omlx/Gone-4bit...", "quitting when the registry write is done..."}},
	}
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			for _, st := range states {
				tm := newTabMachine(t, st.registry)
				tm.longPath = st.longPath
				m := st.set(t, modelsEditor(t, tm, width, height))
				view := m.View()
				assertFits(t, st.name, view, width, height)
				at := func(format string, args ...any) {
					t.Helper()
					t.Errorf("%s at %dx%d: %s:\n%s", st.name, width, height, fmt.Sprintf(format, args...), view)
				}
				if !strings.Contains(view, "[Models]") {
					at("the tab bar is not on screen")
				}
				for _, want := range st.want {
					if !strings.Contains(flat(view), flat(want)) {
						at("%q is not whole on screen", want)
					}
				}
				if st.bare {
					continue
				}
				// The header row is the table's first line: it is what a
				// too-tall view loses. MODEL is always there; STATUS and
				// RUNNING are never dropped, and from 80 columns up there is
				// room for both beside any id.
				for _, head := range []string{"MODEL", "STATUS", "RUNNING"} {
					if !strings.Contains(view, head) && (head == "MODEL" || width >= 80) {
						at("header %s is not on screen", head)
					}
				}
				if st.name == "just after a save" {
					// As after a removal: the status has the room, and the
					// cursor is on the model just added.
					if sel, _ := m.selectedModel(); sel.ID != "openrouter/qwen--qwen3.8-27b" {
						at("the cursor is on %q, want the model just saved", sel.ID)
					}
					continue
				}
				if st.name == "just after a removal" {
					// The status has the room: the path it ends with matters
					// more than the detail of whichever row is selected.
					if !strings.Contains(flat(view), flat(removed)) || !strings.Contains(view, routesPending) {
						at("the removal's status or the pending note is not whole")
					}
					continue
				}
				// The detail block, under the table: the id, whole, then the
				// status (the separator between them is dropped where the
				// line breaks).
				sel, _ := m.selectedModel()
				text := flat(view)
				i := strings.LastIndex(text, sel.ID)
				if i < 0 || !strings.HasPrefix(strings.TrimPrefix(text[i+len(sel.ID):], "·"), string(sel.Status)) {
					at("the selected row's id and status (%s, %s) are not whole on screen", sel.ID, sel.Status)
				}
				if st.name == "one key after a removal" {
					// The removal's status went with the key, and its lines
					// are the table's again.
					if strings.Contains(view, "removed omlx/") || !strings.Contains(view, routesPending) || !strings.Contains(view, "q quit") {
						at("want the pending note and the hints, and the status gone")
					}
					if rows := strings.Count(view, "ok ") + strings.Count(view, "missing ") + strings.Count(view, "new "); rows < 3 {
						at("%d table rows are on screen, want at least 3", rows)
					}
				}
				// What the loader tolerated in the row is said under its id
				// at every size: it is why the row reads "missing".
				for name, want := range map[string]string{
					"malformed fetch selected":                  "fetch is not a table; read as absent",
					"malformed fetch and draft under a long id": "fetch is not a table, draft is not a table; read as absent",
				} {
					if st.name == name && !strings.Contains(text, flat(want)) {
						at("the detail block does not say %q", want)
					}
				}
				if st.name == "local model selected" && width >= 80 && !strings.Contains(view, tabWeightsShown) {
					at("the selected local model's path is not whole on one line")
				}
			}
		}
	}
}

// TestRemovePromptGivesUpLinesBeforeThePath verifies what the remove prompt
// loses on a terminal too short for it, and in which order: its blank lines,
// then the closing sentence, and only then — cut at the bottom — the path. A
// 72-column id with a path of five lines is 15 lines at 40 columns; clipped as
// written, the 12-line screen asked y/N with the end of the path, the model's
// own directory name, off its bottom edge. With room, the prompt is as it was.
func TestRemovePromptGivesUpLinesBeforeThePath(t *testing.T) {
	const closing = "wt removes the registry entry only; it never deletes weights."
	prompt := func(t *testing.T, path string, width, height int) string {
		tm := newTabMachine(t, tabLongRegistry)
		tm.longPath = path
		view := keys(t, selectModel(t, modelsEditor(t, tm, width, height), tabLongID), "d").View()
		assertFits(t, "remove prompt", view, width, height)
		return view
	}
	question := flat("Remove " + tabLongID + " from the registry? [y/N]")

	// The blank lines go first: everything else is still said.
	view := prompt(t, tabLongWeights, 40, 12)
	for _, want := range []string{question, flat(tildePath(tabLongWeights)), flat(closing)} {
		if !strings.Contains(flat(view), want) {
			t.Errorf("at 40x12 the prompt lost %q:\n%s", want, view)
		}
	}
	if n := blankLines(view); n != 0 {
		t.Errorf("at 40x12 the prompt kept %d blank lines it has no room for:\n%s", n, view)
	}

	// Then the closing sentence, with the path still whole.
	view = prompt(t, tabLongerWeights, 40, 12)
	if !strings.Contains(flat(view), question) || !strings.Contains(flat(view), flat(tildePath(tabLongerWeights))) {
		t.Errorf("at 40x12 the question and the whole path must be on screen:\n%s", view)
	}
	if strings.Contains(flat(view), flat(closing)) {
		t.Errorf("at 40x12 this prompt has no room for the closing sentence, yet it fits?\n%s", view)
	}

	// With the room, nothing is given up.
	view = prompt(t, tabLongerWeights, 40, 24)
	if blankLines(view) != 2 || !strings.HasSuffix(flat(view), flat(closing)) {
		t.Errorf("at 40x24 the prompt should be whole, its two blank lines and the closing sentence included:\n%s", view)
	}
}

// blankLines counts the lines of a view with nothing on them (lipgloss pads
// a line to the view's width, so a blank one is not empty).
func blankLines(view string) (n int) {
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			n++
		}
	}
	return n
}

// TestModelsTabKeepsStatusAndRunning verifies what a narrow terminal loses,
// and in which order: SIZE, LOC and FAMILY, never MODEL, STATUS or RUNNING;
// and that an id too long for what is left is cut in its middle — keeping the
// provider and the end of the name, where two variants of a model differ —
// instead of pushing STATUS and RUNNING off every row of the table.
func TestModelsTabKeepsStatusAndRunning(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	wide := modelsEditor(t, tm, 120, 24).View()
	if !strings.Contains(wide, "FAMILY   MODEL                                             LOC    STATUS   RUNNING  SIZE") {
		t.Errorf("at 120 columns every column should show:\n%s", wide)
	}
	// FAMILY, a 48-column MODEL, STATUS and RUNNING are 75 columns with their
	// gaps; LOC would make it 82.
	at80 := modelsEditor(t, tm, 80, 24).View()
	if !strings.Contains(at80, "sonnet   openrouter/anthropic--claude-sonnet-4.5-thinking  ok") || strings.Contains(at80, "SIZE") || strings.Contains(at80, "LOC") {
		t.Errorf("at 80 columns SIZE and LOC go and the longest id stays whole:\n%s", at80)
	}
	narrow := modelsEditor(t, tm, 40, 24).View()
	for _, want := range []string{"MODEL                STATUS   RUNNING", "omlx/Qwen3.8…X-6bit  ok       run", "omlx/Gone-4bit       missing"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("at 40 columns want %q — FAMILY and LOC gone, a long id cut in the middle, a short one whole:\n%s", want, narrow)
		}
	}

	// A 72-column id at 80 columns: FAMILY goes too, and the id gives up its
	// middle so that STATUS and RUNNING stay.
	long := modelsEditor(t, newTabMachine(t, tabLongRegistry), 80, 24).View()
	for _, want := range []string{
		"MODEL                                                        STATUS   RUNNING",
		"omlx/Qwen3.8-35B-A3B-Instruct-abliterat…-dynamic-quant-6bit  missing",
		"omlx/Qwen3.8-27B-Instruct-MLX-6bit                           ok       run",
	} {
		if !strings.Contains(long, want) {
			t.Errorf("with a 72-column id at 80 columns want %q:\n%s", want, long)
		}
	}
	for _, c := range []struct {
		s    string
		w    int
		want string
	}{{"omlx/abcdefghij", 15, "omlx/abcdefghij"}, {"omlx/abcdefghij", 12, "omlx/abc…hij"}, {"abcdef", 2, "ab"}} {
		if got := middleCut(c.s, c.w); got != c.want {
			t.Errorf("middleCut(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

// TestModelsTabShowsAMalformedFetch verifies the detail block names what the
// loader tolerated in the selected row's fetch and draft, in config's own
// phrases, on a line of its own under the id — and says nothing for a row
// that is well formed. The load reads such a value as absent so that a hand
// edit cannot stop wt; without the line the tab shows a model as "missing"
// with no sign that its local_path was never read.
func TestModelsTabShowsAMalformedFetch(t *testing.T) {
	// The view's lines without the padding lipgloss gives a block.
	lines := func(view string) string {
		ls := strings.Split(view, "\n")
		for i := range ls {
			ls[i] = strings.TrimRight(ls[i], " ")
		}
		return strings.Join(ls, "\n")
	}
	tm := newTabMachine(t, tabMalformedRegistry)
	m := selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Qwen3.8-27B-Instruct-MLX-6bit")
	if view := m.View(); strings.Contains(view, "read as absent") {
		t.Errorf("a well-formed row is selected; the detail block should not mention a malformed one:\n%s", view)
	}
	m = selectModel(t, m, "omlx/Gone-4bit")
	if view := lines(m.View()); !strings.Contains(view, "omlx/Gone-4bit · missing\nfetch is not a table; read as absent\n") {
		t.Errorf("the detail block should name the malformed fetch on the line under the id:\n%s", view)
	}

	// Two problems, at 40 columns: the line wraps between words like the
	// rest of the block, and stays on screen.
	m = selectModel(t, modelsEditor(t, newTabMachine(t, tabMalformedLongRegistry), 40, 12), tabLongID)
	assertFits(t, "malformed fetch and draft", m.View(), 40, 12)
	if view := lines(m.View()); !strings.Contains(view, "fetch is not a table, draft is not a\ntable; read as absent\n") {
		t.Errorf("at 40 columns the line should wrap between words:\n%s", view)
	}
}

// TestModelsTabRemove verifies the remove flow: d shows a y/N prompt with the
// weights path, n leaves the registry alone, y deletes exactly that row at
// once, repeats the path in the status, marks the routes pending and
// re-probes. wt deletes no weights, so the path has to be in front of the
// user before and after — on a line of its own, written from ~.
func TestModelsTabRemove(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Qwen3.8-27B-Instruct-MLX-6bit")

	m = keys(t, m, "d")
	view := m.View()
	for _, want := range []string{"Remove omlx/Qwen3.8-27B-Instruct-MLX-6bit from the registry? [y/N]", "weights are still at", tabWeightsShown, "never deletes weights"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the prompt should say %q:\n%s", want, view)
		}
	}
	m = keys(t, m, "n")
	if m.models.phase != modelsList || tm.text(t) != tabRegistry || m.registryChanged {
		t.Fatal("n must cancel the removal and write nothing")
	}

	probes := tm.probes
	m = keys(t, m, "d", "y")
	if got := tm.text(t); strings.Contains(got, "Qwen3.8-27B-Instruct-MLX-6bit") || !strings.Contains(got, `id = "omlx/Gone-4bit"`) {
		t.Errorf("y should remove exactly that row:\n%s", got)
	}
	if !m.registryChanged || tm.probes != probes+1 {
		t.Errorf("registryChanged = %v, probes = %d (was %d); want the change recorded and one re-probe", m.registryChanged, tm.probes, probes)
	}
	// The path is on a line of its own, so that it can be copied whole.
	for _, want := range []string{"removed omlx/Qwen3.8-27B-Instruct-MLX-6bit; weights are still at\n" + tabWeightsShown + "\n", routesPending} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("after the removal the screen should say %q:\n%s", want, m.View())
		}
	}
	if _, still := findRow(m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit"); still {
		t.Error("the removed row is still listed")
	}
}

func findRow(m *model, id string) (modelRow, bool) {
	for _, it := range m.models.list.Items() {
		if r := it.(modelRow); r.row.ID == id {
			return r, true
		}
	}
	return modelRow{}, false
}

// TestModelsTabRemoveRefusals verifies d on a discovered row (nothing to
// remove) only says so, and that a removal the registry refuses is reported
// on the status line with the routes not marked pending and the table read
// again.
func TestModelsTabRemoveRefusals(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "ollama/qwen3:8b"), "d")
	if m.models.phase != modelsList || !strings.Contains(m.View(), "ollama/qwen3:8b is not in the registry") {
		t.Errorf("d on a discovered row should only set the status:\n%s", m.View())
	}

	// Another program removes the row while the prompt is open.
	m = keys(t, selectModel(t, m, "omlx/Gone-4bit"), "d")
	if err := os.WriteFile(tm.registry, []byte(strings.Replace(tabRegistry, `id = "omlx/Gone-4bit"`, `id = "omlx/Other"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	probes := tm.probes
	m = keys(t, m, "y")
	if m.registryChanged || !strings.Contains(m.View(), `not removed: model not found: "omlx/Gone-4bit"`) {
		t.Errorf("a refused removal should be reported and leave the routes alone:\n%s", m.View())
	}
	// The table is read again, so the row the other program took is not
	// left on screen under a message that says it does not exist.
	if _, still := findRow(m, "omlx/Gone-4bit"); still || tm.probes != probes+1 || m.models.busy {
		t.Errorf("after a refusal: the stale row is listed = %v, probes = %d (was %d); want one re-probe and the row gone:\n%s", still, tm.probes, probes, m.View())
	}
	if _, ok := findRow(m, "omlx/Other"); !ok {
		t.Errorf("the row the other program wrote is not listed:\n%s", m.View())
	}
}

// TestModelsTabRefusesToRemoveADuplicatedID verifies d then y on a row whose
// id the registry holds twice removes neither row: the file is left byte for
// byte as it was, the status carries the writer's refusal as the writer words
// it (it names both providers and the file to fix), whole at every size wt
// supports (at 40x12 it has the screen to itself, under the tab bar), and
// no route sync is owed. Taking "the first row with the id" would delete a
// model the user did not point at. The cursor stays on the row it was on —
// the table is read again after a refusal, and the two rows share the id the
// cursor is kept by.
func TestModelsTabRefusesToRemoveADuplicatedID(t *testing.T) {
	for _, size := range tabSizes {
		for _, provider := range []string{"omlx", "openrouter"} {
			tm := newTabMachine(t, tabDuplicatedRegistry)
			m := modelsEditor(t, tm, size[0], size[1])
			for i, it := range m.models.list.VisibleItems() {
				if r := it.(modelRow).row; r.ID == "omlx/Gone-4bit" && r.ProviderID == provider {
					m.models.list.Select(i)
				}
			}
			at := func(format string, args ...any) {
				t.Helper()
				t.Errorf("the %s row at %dx%d: %s:\n%s", provider, size[0], size[1], fmt.Sprintf(format, args...), m.View())
			}
			m = keys(t, m, "d")
			if m.models.phase != modelsRemove || m.models.remove.ProviderID != provider {
				t.Fatalf("the %s row at %dx%d: d should open the remove prompt for it:\n%s", provider, size[0], size[1], m.View())
			}
			m = keys(t, m, "y")
			if got := tm.text(t); got != tabDuplicatedRegistry {
				at("the registry changed:\n%s", got)
			}
			view := m.View()
			assertFits(t, "refused removal of a duplicated id", view, size[0], size[1])
			refusal := `model "omlx/Gone-4bit" is in the registry twice (providers omlx, openrouter); wt cannot tell which one you mean — fix the entry in ` + tm.registry
			if !strings.Contains(view, "[Models]") || !strings.Contains(flat(view), flat("not removed: "+refusal)) {
				at("the status should carry the writer's refusal %q under the tab bar", refusal)
			}
			if m.registryChanged || strings.Contains(view, routesPending) || m.models.busy {
				at("registryChanged = %v, busy = %v; a refused removal owes no route sync", m.registryChanged, m.models.busy)
			}
			if sel, _ := m.selectedModel(); sel.ID != "omlx/Gone-4bit" || sel.ProviderID != provider {
				at("the cursor is on %s (provider %s), want it left where it was", sel.ID, sel.ProviderID)
			}
			// The next key takes the refusal down and the table is whole
			// again.
			m = keys(t, m, "up")
			if view := m.View(); strings.Contains(view, "not removed") || !strings.Contains(view, "MODEL") || !strings.Contains(view, "q quit") {
				at("after a key want the table and the hints back")
			}
			assertFits(t, "one key after a refusal", m.View(), size[0], size[1])
		}
	}
}

// TestModelsTabDropsAStaleProbe verifies the rows of an older probe do not
// replace a newer one's. A slow first probe that lands after a refresh would
// otherwise put back a row the user just removed.
func TestModelsTabDropsAStaleProbe(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	stale := m.probeCmd()
	fresh := m.probeCmd()
	m = send(t, m, fresh())
	rows := len(m.models.rows)
	old := stale().(modelsLoadedMsg)
	old.rows = nil
	m = send(t, m, old)
	if len(m.models.rows) != rows {
		t.Errorf("a stale probe replaced the rows: %d rows, want %d", len(m.models.rows), rows)
	}
}

// TestModelsTabKeepsTheCursorAcrossARefresh verifies r rebuilds the table
// with the cursor on the row it was on. A refresh that jumped to the top
// would make the next d or enter act on a different model.
func TestModelsTabKeepsTheCursorAcrossARefresh(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit")
	m = keys(t, m, "r")
	if r, _ := m.selectedModel(); r.ID != "omlx/Gone-4bit" {
		t.Errorf("after r the cursor is on %q, want omlx/Gone-4bit", r.ID)
	}
}

// TestModelsTabFilterTakesTheTabsKeys verifies that while the filter is being
// typed, d, r, n and q are text, not commands: typing "qwen" must not quit,
// and "d" must not open the remove prompt. ctrl+c is the exception, on both
// tabs: it leaves the editor from every screen (by way of the unsaved-changes
// prompt), and the lists' own force-quit is off, so a filter that swallowed
// it left a user who reached for ctrl+c with nothing happening.
func TestModelsTabFilterTakesTheTabsKeys(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := stillCursors(modelsEditor(t, tm, 80, 24))
	probes := tm.probes
	m = keys(t, m, "/", "q", "d", "r")
	if m.models.phase != modelsList || tm.probes != probes || m.models.list.FilterValue() != "qdr" {
		t.Errorf("phase = %d, probes = %d (was %d), filter = %q; want the keys typed into the filter", m.models.phase, tm.probes, probes, m.models.list.FilterValue())
	}
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}
	if _, cmd := m.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Error("ctrl+c while a Models filter is being typed should quit")
	}
	agents := stillCursors(modelsEditor(t, newTabMachine(t, tabRegistry), 80, 24))
	agents = keys(t, keys(t, agents, "tab"), "/", "q")
	if agents.tab != TabAgents || agents.list.FilterState() != list.Filtering {
		t.Fatalf("tab = %d, filter state = %v; want the Agents filter being typed", agents.tab, agents.list.FilterState())
	}
	if _, cmd := agents.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Error("ctrl+c while an Agents filter is being typed should quit")
	}
	agents.dirty = true
	next, cmd := agents.Update(keyMsg("ctrl+c"))
	if cmd != nil || next.(*model).phase != phaseQuit {
		t.Error("ctrl+c in a filter with unsaved agent edits should ask about them first")
	}
}

// TestModelsTabFilterNarrowsTheTable verifies / filters the rows: the table
// shows only the matches, enter keeps them, d and the detail then act on the
// row the user sees, and esc clears the filter without leaving the editor.
// bubbles ranks the rows in a command and sends the result back as a
// message; a tab that dropped it showed "Filter: qwen" above every row, and d
// removed a model the user was not looking at.
func TestModelsTabFilterNarrowsTheTable(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := stillCursors(modelsEditor(t, tm, 80, 24))
	all := len(visibleIDs(m))
	m = keys(t, m, "/", "q", "w", "e", "n")
	// The matches, best first: the two rows of the qwen3.8 family and the
	// unregistered qwen3:8b.
	shown := visibleIDs(m)
	want := []string{"ollama/qwen3:8b", "omlx/Gone-4bit", "omlx/Qwen3.8-27B-Instruct-MLX-6bit"}
	if got := slices.Sorted(slices.Values(shown)); !slices.Equal(got, want) {
		t.Fatalf("while typing /qwen the table shows %v, want %v", shown, want)
	}
	if view := m.View(); strings.Contains(view, "claude-sonnet") {
		t.Errorf("a row that does not match is still drawn:\n%s", view)
	}
	m = keys(t, m, "enter", "down", "d")
	if m.models.phase != modelsRemove || m.models.remove.ID != shown[1] {
		t.Errorf("d on the second filtered row asks about %q, want %q, the row under the cursor", m.models.remove.ID, shown[1])
	}
	m = keys(t, m, "n")
	// esc clears the filter; it is not the list's quit key here.
	next, cmd := m.Update(keyMsg("esc"))
	m = next.(*model)
	if cmd != nil || m.models.list.FilterState() != list.Unfiltered || len(visibleIDs(m)) != all {
		t.Errorf("esc on a filtered table should clear the filter and nothing else; rows = %d of %d", len(visibleIDs(m)), all)
	}

	// A result for the Agents tab's list, or for a table that has been
	// rebuilt since it was ranked, is not this table's.
	before := visibleIDs(m)
	m = send(t, m, filterMatchesMsg{tab: TabModels, built: m.models.built - 1})
	if got := visibleIDs(m); !slices.Equal(got, before) {
		t.Errorf("a filter result for an older table was applied: %v", got)
	}
}

// TestAgentsFilterNarrowsTheList verifies the same on the Agents tab, whose
// list lost its filter results the same way before the tabs: "/cla" showed
// every agent.
func TestAgentsFilterNarrowsTheList(t *testing.T) {
	cfg := &config.Config{Agents: []config.Agent{{Name: "claude"}, {Name: "pi"}}}
	m := newModel(testTheme(), cfg, nil)
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = stillCursors(send(t, m, loadedMsg{cfg: cfg}))
	all := len(m.list.VisibleItems())
	m = keys(t, m, "/", "c", "l", "a", "u")
	got := m.list.VisibleItems()
	if len(got) != 1 || got[0].(agentItem).agent.Name != "claude" || all < 2 {
		t.Errorf("/clau shows %d of %d agents, want claude alone", len(got), all)
	}
}

// TestEscOnAListDoesNotQuit verifies esc on either tab's list does not end
// the editor. esc is the list's own quit key, and that quit skipped the
// unsaved-changes prompt: esc pressed once too often — it cancels the remove
// prompt — threw away every unsaved agent edit. q still asks.
func TestEscOnAListDoesNotQuit(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	m.dirty = true
	m = keys(t, selectModel(t, m, "omlx/Gone-4bit"), "d", "esc")
	for _, tab := range []Tab{TabModels, TabAgents} {
		m.tab = tab
		next, cmd := m.Update(keyMsg("esc"))
		m = next.(*model)
		if cmd != nil || m.phase != phaseList {
			t.Fatalf("esc on the list of tab %d: a command = %v, phase = %d; want nothing to happen", tab, cmd != nil, m.phase)
		}
	}
	next, cmd := m.Update(keyMsg("q"))
	if cmd != nil || next.(*model).phase != phaseQuit {
		t.Error("q with unsaved agent edits should still reach the prompt")
	}
}

// TestCtrlSOnTheModelsTabDoesNothing verifies ctrl+s is the Agents tab's key
// only. On the Models tab there is nothing to save — each change is written
// when it is made — and a save of the agents from there would report its
// outcome on a tab the user is not looking at.
func TestCtrlSOnTheModelsTabDoesNothing(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	m.dirty = true
	saves := 0
	old := saveCmd
	saveCmd = func(*config.Config) tea.Cmd { saves++; return nil }
	t.Cleanup(func() { saveCmd = old })
	next, cmd := m.Update(keyMsg("ctrl+s"))
	m = next.(*model)
	if cmd != nil || saves != 0 || !m.dirty || m.saving {
		t.Errorf("ctrl+s on the Models tab: saves = %d, dirty = %v; want the agents left unsaved for their own tab", saves, m.dirty)
	}
	m = keys(t, m, "tab")
	if _, cmd = m.Update(keyMsg("ctrl+s")); saves != 1 {
		t.Errorf("ctrl+s on the Agents tab: saves = %d, want 1", saves)
	}
}

// TestModelsTabStatusClearsOnTheNextKey verifies what the last action said
// stays until the user presses a key on the table and then goes, while the
// routes-pending note and a registry that does not load stay. A status that
// never cleared kept its lines from the table for the rest of the session.
func TestModelsTabStatusClearsOnTheNextKey(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "d", "y")
	if !strings.Contains(m.View(), "removed omlx/Gone-4bit; no weights were found on disk") {
		t.Fatalf("the removal's status should be on screen:\n%s", m.View())
	}
	m = keys(t, m, "down")
	if view := m.View(); strings.Contains(view, "removed omlx/Gone-4bit") || !strings.Contains(view, routesPending) {
		t.Errorf("after a key the status should be gone and the pending note still there:\n%s", view)
	}

	broken := modelsEditor(t, newTabMachine(t, "this is not = = toml\n"), 80, 24)
	broken = keys(t, broken, "down")
	if !strings.Contains(broken.View(), "config load error: ") {
		t.Errorf("why the registry did not load must outlast a key:\n%s", broken.View())
	}
}

// TestQuitOnTheModelsTab verifies q leaves at once when only the registry
// changed — every change there is already saved — and that unsaved agent
// edits still get their prompt from the Models tab.
func TestQuitOnTheModelsTab(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	m.registryChanged = true
	if _, cmd := m.Update(keyMsg("q")); cmd == nil {
		t.Fatal("q with only registry changes should quit: there is nothing unsaved")
	}
	m.dirty = true
	next, cmd := m.Update(keyMsg("q"))
	if cmd != nil || next.(*model).phase != phaseQuit || !strings.Contains(next.(*model).View(), "unsaved agent changes") {
		t.Errorf("q with unsaved agent edits should ask about them:\n%s", next.(*model).View())
	}
}

// TestQuitWaitsForARegistryWriteInFlight verifies a quit typed while a
// removal is still being written — d, y, q in one breath — waits for the
// write's result and only then ends the program, with the change recorded.
// The write runs in a command; a program that ended before its message was
// handled reported "nothing changed" for a registry that did change, and wt
// then skipped the route sync: the removed model kept its LiteLLM route. The
// held quit is a quit like any other when its turn comes: agent edits made
// while it waited get the unsaved-changes prompt, not thrown away unasked.
func TestQuitWaitsForARegistryWriteInFlight(t *testing.T) {
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}
	// removing presses d and y on a row and hands back the write, not run.
	removing := func(t *testing.T, dirty bool) (*tabMachine, *model, tea.Cmd) {
		tm := newTabMachine(t, tabRegistry)
		m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "d")
		m.dirty = dirty
		next, write := m.Update(keyMsg("y"))
		if write == nil {
			t.Fatal("y should hand the removal to a command")
		}
		return tm, next.(*model), write
	}

	t.Run("q", func(t *testing.T) {
		tm, m, write := removing(t, false)
		next, cmd := m.Update(keyMsg("q"))
		m = next.(*model)
		if cmd != nil || !m.quitPending || m.registryChanged {
			t.Fatalf("q with the write in flight: a command = %v, quitPending = %v; want the quit held back", cmd != nil, m.quitPending)
		}
		if !strings.Contains(m.View(), "quitting when the registry write is done") {
			t.Errorf("the tab should say why it has not left yet:\n%s", m.View())
		}
		next, cmd = m.Update(write())
		m = next.(*model)
		if !isQuit(cmd) || !m.registryChanged || strings.Contains(tm.text(t), "Gone-4bit") {
			t.Errorf("once the write is in: quit = %v, registryChanged = %v; want both, and the row gone", isQuit(cmd), m.registryChanged)
		}
	})
	t.Run("discarding agent edits from the quit prompt", func(t *testing.T) {
		_, m, write := removing(t, true)
		next, _ := m.Update(keyMsg("q"))
		if m = next.(*model); m.phase != phaseQuit {
			t.Fatalf("phase = %d, want the unsaved-changes prompt first", m.phase)
		}
		next, cmd := m.Update(keyMsg("n"))
		if m = next.(*model); cmd != nil || !m.quitPending {
			t.Fatal("n on the prompt with the write in flight should hold the quit back too")
		}
		if _, cmd = m.Update(write()); !isQuit(cmd) {
			t.Error("the held quit should happen when the write is in: the edits were already given up, and are not asked about twice")
		}
	})
	t.Run("a refused write keeps the edits a held quit was to discard", func(t *testing.T) {
		tm, m, write := removing(t, true)
		if err := os.WriteFile(tm.registry, []byte(strings.Replace(tabRegistry, `id = "omlx/Gone-4bit"`, `id = "omlx/Other"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		m = keys(t, m, "q", "n")
		if !m.quitPending {
			t.Fatal("n on the prompt with the write in flight should hold the quit back")
		}
		next, cmd := m.Update(write())
		m = next.(*model)
		if isQuit(cmd) || m.quitPending || !m.dirty {
			t.Fatalf("quit = %v, quitPending = %v, dirty = %v; the quit is off, so the edits it was to discard are unsaved again", isQuit(cmd), m.quitPending, m.dirty)
		}
		// And the next quit asks about them.
		if m = keys(t, m, "q"); m.phase != phaseQuit {
			t.Errorf("phase = %d, want the unsaved-changes prompt", m.phase)
		}
	})
	t.Run("agent edits made while the quit waited are asked about", func(t *testing.T) {
		tm, m, write := removing(t, false)
		next, _ := m.Update(keyMsg("q"))
		// The tab still takes keys while the quit waits: tab goes to Agents,
		// where a form can be opened and submitted.
		m = keys(t, next.(*model), "tab")
		if m.tab != TabAgents || !m.quitPending {
			t.Fatalf("tab = %d, quitPending = %v; want the Agents tab with the quit still held", m.tab, m.quitPending)
		}
		m.dirty = true
		next, cmd := m.Update(write())
		m = next.(*model)
		if isQuit(cmd) || m.phase != phaseQuit || m.quitPending || !m.registryChanged {
			t.Fatalf("the held quit with unsaved agent edits: quit = %v, phase = %d, quitPending = %v, registryChanged = %v; want the unsaved-changes prompt and the change recorded", isQuit(cmd), m.phase, m.quitPending, m.registryChanged)
		}
		if !strings.Contains(m.View(), "unsaved agent changes") {
			t.Errorf("the prompt should be on screen:\n%s", m.View())
		}
		// Cancelled, the editor goes on, with a table that was read again.
		m = keys(t, send(t, m, cmd()), "c")
		if _, still := findRow(m, "omlx/Gone-4bit"); still || m.phase != phaseList || m.models.busy || strings.Contains(tm.text(t), "Gone-4bit") {
			t.Errorf("after c: the removed row is listed = %v, phase = %d, busy = %v; want the editor back with the table re-read", still, m.phase, m.models.busy)
		}
	})
	t.Run("a refused write calls the quit off", func(t *testing.T) {
		tm, m, write := removing(t, false)
		// Another program takes the row first.
		if err := os.WriteFile(tm.registry, []byte(strings.Replace(tabRegistry, `id = "omlx/Gone-4bit"`, `id = "omlx/Other"`, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		next, _ := m.Update(keyMsg("q"))
		next, cmd := next.(*model).Update(write())
		m = next.(*model)
		// The command is the re-probe a refusal asks for, not the quit.
		if isQuit(cmd) || m.quitPending || m.registryChanged || !strings.Contains(m.View(), "not removed: ") {
			t.Errorf("a refusal has to be read: want no quit and the reason on screen:\n%s", m.View())
		}
	})
	t.Run("a probe in flight holds nothing up", func(t *testing.T) {
		tm := newTabMachine(t, tabRegistry)
		m := modelsEditor(t, tm, 80, 24)
		next, probe := m.Update(keyMsg("r"))
		if probe == nil {
			t.Fatal("r should hand the probe to a command")
		}
		if _, cmd := next.(*model).Update(keyMsg("q")); !isQuit(cmd) {
			t.Error("q while only a probe is out should quit at once")
		}
	})
}

// TestModelsTabShowsARegistryThatDoesNotLoad verifies a registry wt cannot
// read still opens the tab, with the reason on the status line, instead of a
// blank screen or a crash — and that the reason ends with the repair
// config.RegistryFixHint gives every other place that reports this error, so
// the tab that manages the registry is not the one screen that leaves it out.
// At every size wt supports the reason is whole under the tab bar; at 40x12
// it is longer than the table has room beside.
func TestModelsTabShowsARegistryThatDoesNotLoad(t *testing.T) {
	for _, size := range tabSizes {
		tm := newTabMachine(t, "this is not = = toml\n")
		m := modelsEditor(t, tm, size[0], size[1])
		view := m.View()
		assertFits(t, "broken registry", view, size[0], size[1])
		want := "config load error: parse " + tm.registry + ": toml: line 1: expected '.' or '=', but got 'i' instead (fix that file by hand)"
		if !strings.Contains(flat(view), flat(want)) || !strings.Contains(view, "[Models]") {
			t.Errorf("at %dx%d the tab should say %q:\n%s", size[0], size[1], want, view)
		}
	}
}

// TestModelsTabDoesNotBlameTheRegistryForConfigToml verifies a config.toml
// that does not parse is reported on the Models tab as a config load error,
// in config.Load's words, not as a registry problem. The tab re-reads the whole config on every
// probe, and labelled each failure "registry: ": the user was sent to repair
// a registry.toml that was fine.
func TestModelsTabDoesNotBlameTheRegistryForConfigToml(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	path := config.Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not = = toml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := modelsEditor(t, tm, 120, 24)
	if !strings.Contains(m.View(), "config load error: parse config: ") {
		t.Errorf("the tab should report the load error as config.Load words it:\n%s", m.View())
	}
	if strings.Contains(m.models.loadErr, "registry: ") || strings.Contains(m.models.loadErr, "fix that file by hand") {
		t.Errorf("a config.toml error is not the registry's, and has no registry repair: %q", m.models.loadErr)
	}
}

// TestModelsTabSaysAProbeIsOut verifies r puts "probing providers..." on the
// status line until the probe answers, and that a key pressed meanwhile does
// not take it down. The table stays on screen during a refresh and r and d
// are ignored until it lands; without the line a slow provider looks like a
// tab that has stopped taking keys.
func TestModelsTabSaysAProbeIsOut(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	next, probe := m.Update(keyMsg("r"))
	m = next.(*model)
	if probe == nil || !strings.Contains(m.View(), modelsProbing) {
		t.Fatalf("r should start a probe and say so:\n%s", m.View())
	}
	next, cmd := m.Update(keyMsg("d"))
	m = next.(*model)
	if cmd != nil || m.models.phase != modelsList || !strings.Contains(m.View(), modelsProbing) {
		t.Errorf("d while the probe is out should do nothing and leave the reason on screen:\n%s", m.View())
	}
	assertFits(t, "probing", m.View(), 80, 24)
	m = send(t, m, probe())
	if strings.Contains(m.View(), modelsProbing) || m.models.busy {
		t.Errorf("the probe has answered; the line should be gone:\n%s", m.View())
	}
}

// TestAHeldQuitIsShownOnTheModelsTab verifies a quit typed on the Agents
// tab while a removal is being written brings the Models tab forward: the
// line that says the editor is waiting for the write, and a refusal's
// reason, are drawn only there. Left on the Agents tab the user sees an
// editor that ignored q.
func TestAHeldQuitIsShownOnTheModelsTab(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "d")
	if err := os.WriteFile(tm.registry, []byte(strings.Replace(tabRegistry, `id = "omlx/Gone-4bit"`, `id = "omlx/Other"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	next, write := m.Update(keyMsg("y"))
	m = keys(t, next.(*model), "tab")
	if m.tab != TabAgents {
		t.Fatalf("tab = %d, want Agents while the write is out", m.tab)
	}
	next, cmd := m.Update(keyMsg("q"))
	m = next.(*model)
	if cmd != nil || m.tab != TabModels || !strings.Contains(m.View(), "quitting when the registry write is done") {
		t.Fatalf("q from the Agents tab with a write in flight: tab = %d\n%s\nwant the Models tab saying it waits", m.tab, m.View())
	}
	m = keys(t, m, "tab")
	m = send(t, m, write())
	if m.tab != TabModels || !strings.Contains(m.View(), "not removed: ") {
		t.Errorf("the refusal that called the quit off: tab = %d\n%s\nwant it shown on the Models tab", m.tab, m.View())
	}
}

// TestQuitPromptFitsANarrowTerminal verifies the unsaved-changes prompt is
// wrapped to the terminal: its two lines are 54 and 51 columns, and at 40
// the choices past the edge ([c] cancel among them) were cut off with no
// sign of it. At 80 it reads as it always did.
func TestQuitPromptFitsANarrowTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {40, 24}, {80, 24}, {120, 50}} {
		tm := newTabMachine(t, tabRegistry)
		m := modelsEditor(t, tm, size[0], size[1])
		m.dirty = true
		m = keys(t, m, "q")
		if m.phase != phaseQuit {
			t.Fatalf("phase = %d, want the quit prompt", m.phase)
		}
		view := m.View()
		assertFits(t, "quit prompt", view, size[0], size[1])
		for _, want := range []string{"unsaved agent changes", "[y] save and quit", "[n] discard and quit", "[c] cancel"} {
			if !strings.Contains(view, want) {
				t.Errorf("at %dx%d the prompt lost %q:\n%s", size[0], size[1], want, view)
			}
		}
		if size[0] >= 80 && view != "You have unsaved agent changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n" {
			t.Errorf("at %d columns the prompt changed:\n%q", size[0], view)
		}
	}
}

// TestWrapTextWithoutAWidth verifies text is left as written when the width
// is not known yet (a view drawn before the first WindowSizeMsg), and that a
// line that fits keeps its own spacing. Wrapping to a width of 0 put one
// rune on each line.
func TestWrapTextWithoutAWidth(t *testing.T) {
	text := "one two  three\n\n/a/path with spaces"
	if got := wrapText(text, 0); got != text {
		t.Errorf("wrapText at width 0 = %q, want the text unchanged", got)
	}
	if got := wrapText(text, 80); got != text {
		t.Errorf("wrapText at 80 = %q, want the text unchanged", got)
	}
	if got := wrapText("one two  three", 7); got != "one two\nthree" {
		t.Errorf("wrapText at 7 = %q", got)
	}
}

// TestAFailedQuitSaveIsShownOnTheAgentsTab verifies a save-before-quit that
// fails takes the unsaved-changes prompt down and brings the Agents tab
// forward, whichever tab q was typed on: the reason is that tab's status
// line and is drawn nowhere else. Left where it was, the user saw a prompt
// that ignored y and, after c on the Models tab, a table with no sign that
// the edits are still unsaved. Both failures are covered: the write's, and
// the validation that runs before it.
func TestAFailedQuitSaveIsShownOnTheAgentsTab(t *testing.T) {
	old := saveCmd
	saveCmd = func(*config.Config) tea.Cmd {
		return func() tea.Msg { return saveMsg{err: errors.New("disk full")} }
	}
	t.Cleanup(func() { saveCmd = old })

	t.Run("the write fails", func(t *testing.T) {
		m := modelsEditor(t, newTabMachine(t, tabRegistry), 80, 24)
		m.dirty = true
		m = keys(t, m, "q", "y")
		if m.tab != TabAgents || m.phase != phaseList || m.quitting || !m.dirty {
			t.Fatalf("tab = %d, phase = %d, quitting = %v, dirty = %v; want the Agents tab's list, the quit called off and the edits still unsaved", m.tab, m.phase, m.quitting, m.dirty)
		}
		if view := m.View(); !strings.Contains(view, "[Agents]") || !strings.Contains(view, "save failed: disk full") {
			t.Errorf("the failure should be on screen, on the Agents tab:\n%s", view)
		}
		assertFits(t, "failed quit save", m.View(), 80, 24)
	})
	t.Run("validation fails", func(t *testing.T) {
		m := modelsEditor(t, newTabMachine(t, tabRegistry), 80, 24)
		bad := *m.cfg
		bad.DefaultTag = ""
		m.cfg, m.dirty = &bad, true
		if err := m.cfg.ValidateAll(); err == nil {
			t.Fatal("the fixture should not validate")
		}
		m = keys(t, m, "q", "y")
		if m.tab != TabAgents || m.phase != phaseList || m.quitting || !m.dirty {
			t.Fatalf("tab = %d, phase = %d, quitting = %v, dirty = %v; want the Agents tab's list, the quit called off and the edits still unsaved", m.tab, m.phase, m.quitting, m.dirty)
		}
		if view := m.View(); !strings.Contains(view, "[Agents]") || !strings.Contains(view, "validation: ") {
			t.Errorf("the failure should be on screen, on the Agents tab:\n%s", view)
		}
	})
}

// TestAgentsStatusFollowsTheLastSave verifies the Agents tab draws the status
// of the latest save, not the one it first rendered: a failed ctrl+s says
// "save failed: ...", and the save that then succeeds replaces it with
// "saved". The rendered status was cached by the terminal's width alone, so
// at a width that never changed the screen went on showing the first status
// drawn — usually none, and a failed save looked like a save.
func TestAgentsStatusFollowsTheLastSave(t *testing.T) {
	fail := true
	old := saveCmd
	saveCmd = func(*config.Config) tea.Cmd {
		return func() tea.Msg {
			if fail {
				return saveMsg{err: errors.New("disk full")}
			}
			return saveMsg{}
		}
	}
	t.Cleanup(func() { saveCmd = old })

	m := keys(t, modelsEditor(t, newTabMachine(t, tabRegistry), 80, 24), "tab")
	if view := m.View(); m.tab != TabAgents || strings.Contains(view, "save") {
		t.Fatalf("want the Agents tab with no status yet:\n%s", view)
	}
	m.dirty = true
	m = keys(t, m, "ctrl+s")
	if view := m.View(); !strings.Contains(view, "save failed: disk full") {
		t.Fatalf("a failed save should say so:\n%s", view)
	}
	fail = false
	m = keys(t, m, "ctrl+s")
	if view := m.View(); strings.Contains(view, "save failed") || !strings.Contains(view, "saved") {
		t.Errorf("the save that worked should replace the failure:\n%s", view)
	}
	assertFits(t, "agents tab with a status", m.View(), 80, 24)
}
