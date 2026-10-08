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

func newTabMachine(t *testing.T, content string) *tabMachine {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
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
				case "omlx/Gone-4bit", tabLongID:
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
		m = send(t, m, keyMsg(k))
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
// (a status that wraps, and the routes-pending note), one key later, and the
// remove prompt. The view must stay inside the terminal with the tab bar and
// the table's header row on screen; STATUS and RUNNING are on every row from
// 80 columns up however long an id is; and the selected row's id is on screen
// whole, with its status, even where the table had to cut it.
func TestModelsTabFitsTheTerminal(t *testing.T) {
	const removed = "removed omlx/Qwen3.8-27B-Instruct-MLX-6bit; weights are still at\n" + tabWeightsShown
	states := []struct {
		name     string
		registry string
		set      func(t *testing.T, m *model) *model
	}{
		{"list", tabRegistry, func(t *testing.T, m *model) *model { return m }},
		{"list with a long id", tabLongRegistry, func(t *testing.T, m *model) *model { return m }},
		{"long id selected", tabLongRegistry, func(t *testing.T, m *model) *model { return selectModel(t, m, tabLongID) }},
		{"local model selected", tabRegistry, func(t *testing.T, m *model) *model {
			return selectModel(t, m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit")
		}},
		{"just after a removal", tabRegistry, func(t *testing.T, m *model) *model {
			m.models.status, m.registryChanged = removed, true
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}},
		{"one key after a removal", tabRegistry, func(t *testing.T, m *model) *model {
			m.models.status, m.registryChanged = removed, true
			return keys(t, m, "down")
		}},
		{"remove prompt", tabRegistry, func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "omlx/Qwen3.8-27B-Instruct-MLX-6bit"), "d")
		}},
	}
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			for _, st := range states {
				tm := newTabMachine(t, st.registry)
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
				if st.name == "remove prompt" {
					// The prompt wraps at the terminal's width: compare with
					// the line breaks and the padding taken out.
					if !strings.Contains(flat(view), "Removeomlx/Qwen3.8-27B-Instruct-MLX-6bitfromtheregistry?[y/N]") || !strings.Contains(flat(view), tabWeightsShown) {
						at("the prompt or the weights path is not on screen")
					}
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
				if st.name == "local model selected" && width >= 80 && !strings.Contains(view, tabWeightsShown) {
					at("the selected local model's path is not whole on one line")
				}
			}
		}
	}
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
// on the status line with the routes not marked pending.
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
	m = keys(t, m, "y")
	if m.registryChanged || !strings.Contains(m.View(), `not removed: model not found: "omlx/Gone-4bit"`) {
		t.Errorf("a refused removal should be reported and leave the routes alone:\n%s", m.View())
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
// and "d" must not open the remove prompt.
func TestModelsTabFilterTakesTheTabsKeys(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := stillCursors(modelsEditor(t, tm, 80, 24))
	probes := tm.probes
	m = keys(t, m, "/", "q", "d", "r")
	if m.models.phase != modelsList || tm.probes != probes || m.models.list.FilterValue() != "qdr" {
		t.Errorf("phase = %d, probes = %d (was %d), filter = %q; want the keys typed into the filter", m.models.phase, tm.probes, probes, m.models.list.FilterValue())
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
	if !strings.Contains(broken.View(), "registry: ") {
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
// then skipped the route sync: the removed model kept its LiteLLM route.
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
			t.Error("the held quit should happen when the write is in")
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
		if cmd != nil || m.quitPending || m.registryChanged || !strings.Contains(m.View(), "not removed: ") {
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
// blank screen or a crash.
func TestModelsTabShowsARegistryThatDoesNotLoad(t *testing.T) {
	tm := newTabMachine(t, "this is not = = toml\n")
	m := modelsEditor(t, tm, 80, 24)
	view := m.View()
	assertFits(t, "broken registry", view, 80, 24)
	if !strings.Contains(view, "registry: ") || !strings.Contains(view, "[Models]") {
		t.Errorf("the tab should say why the registry did not load:\n%s", view)
	}
}
