package sessioneditor

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// stubSaveResult swaps the saveCmd seam for one that reports err (nil for a
// save that succeeds) without touching disk, until the test ends.
func stubSaveResult(t *testing.T, err error) {
	t.Helper()
	old := saveCmd
	saveCmd = func(*config.Config) tea.Cmd {
		return func() tea.Msg { return saveMsg{err: err} }
	}
	t.Cleanup(func() { saveCmd = old })
}

// TestSave_AtomicWrite verifies that Ctrl-S writes the in-memory config
// to disk atomically and that no temporary file is left behind.
func TestSave_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := newModel(testTheme(), &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Agents:     []config.Agent{{Name: "claude"}},
	}, nil)
	m.dirty = true

	// Simulate handleSave dispatch.
	_, cmd := m.handleSave()
	if cmd == nil {
		t.Fatal("expected save command, got nil")
	}
	msg := cmd()
	save, ok := msg.(saveMsg)
	if !ok {
		t.Fatalf("expected saveMsg, got %T", msg)
	}
	if save.err != nil {
		t.Fatalf("save failed: %v", save.err)
	}

	// Process the save message.
	got, _ := m.Update(save)
	m2 := got.(*model)
	if m2.dirty {
		t.Error("expected dirty=false after successful save")
	}

	// Verify file on disk.
	path := config.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config.toml not found: %v", err)
	}
	if !strings.Contains(string(data), "claude") {
		t.Errorf("config.toml does not contain agent:\n%s", string(data))
	}

	// Verify no temp file remains.
	_, err = os.Stat(path + ".tmp")
	if !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up")
	}
}

// TestSave_ValidationFails_BlocksWrite verifies that attempting to save
// an invalid config does not write to disk and surfaces the error.
func TestSave_ValidationFails_BlocksWrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// A session rule (no registry is read here): two agents with one name.
	m := newModel(testTheme(), &config.Config{
		DefaultTag: "code",
		Agents: []config.Agent{
			{Name: "a"},
			{Name: "a"},
		},
	}, nil)
	m.dirty = true

	got, cmd := m.handleSave()
	if cmd != nil {
		t.Fatal("expected nil command when validation fails")
	}
	m2 := got.(*model)
	if m2.status == "" {
		t.Fatal("expected validation error in status")
	}

	path := config.Path()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("config.toml should not exist when validation blocks save")
	}
}

// TestSave_DirtyFlag_TogglesOn verifies that a successful save resets
// dirty to false, and that making an edit sets it to true.
func TestSave_DirtyFlag_TogglesOn(t *testing.T) {
	m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
	if m.dirty {
		t.Fatal("expected dirty=false at start")
	}

	// Make an edit.
	m.cfg.Providers = append(m.cfg.Providers, config.Provider{ID: "new"})
	m.dirty = true

	// Stub saveCmd to succeed without touching disk.
	stubSaveResult(t, nil)

	_, cmd := m.handleSave()
	msg := cmd()
	got, _ := m.Update(msg)
	m2 := got.(*model)
	if m2.dirty {
		t.Error("expected dirty=false after successful save")
	}
}

// TestSave_SavingFlagResetsAfterError verifies that the saving guard is
// cleared when a save fails, so the user can retry. A stuck flag would
// permanently block subsequent saves.
func TestSave_SavingFlagResetsAfterError(t *testing.T) {
	m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
	m.dirty = true

	stubSaveResult(t, os.ErrPermission)

	_, cmd := m.handleSave()
	msg := cmd()
	got, _ := m.Update(msg)
	m2 := got.(*model)
	if m2.saving {
		t.Error("expected saving=false after failed save")
	}
}

// TestSave_ConcurrentRequests_Deduplicated verifies that pressing Ctrl-S
// twice in the same tick dispatches only one save command. Two concurrent
// writes would race on the same .tmp path and could corrupt the file.
func TestSave_ConcurrentRequests_Deduplicated(t *testing.T) {
	m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
	m.dirty = true

	got, cmd := m.handleSave()
	if cmd == nil {
		t.Fatal("expected first save command")
	}
	m2 := got.(*model)
	if !m2.saving {
		t.Fatal("expected saving=true after dispatching save")
	}

	got2, cmd2 := m2.handleSave()
	if cmd2 != nil {
		t.Fatal("expected second save request to be ignored")
	}
	m3 := got2.(*model)
	if !m3.saving {
		t.Fatal("expected saving to remain true")
	}
}

// TestSave_PreservesConcurrentLitellmChange reproduces issue #143: the
// editor loads a Config with no [litellm], a concurrent writer changes it on
// disk, and then the editor saves its own (agent) edit. A whole-file save of
// the editor's stale snapshot would revert the concurrent [litellm] change;
// this pins that the editor's save preserves it instead — the table is host
// state (`wt-model serve` reads it) no session save path may drop.
func TestSave_PreservesConcurrentLitellmChange(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	registryDir := tmpDir + "/local-ai"
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The agy provider row (native) is kept alongside ollama: Load adds no
	// agent of its own, and the rows are what an agy agent would be offered.
	registryToml := `providers = [{id = "ollama", name = "Ollama"}, {id = "agy", name = "Agy", auth = {type = "native"}}]
models = []
`
	if err := os.WriteFile(registryDir+"/registry.toml", []byte(registryToml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The editor "opens" with a stale, litellm-less snapshot.
	editorCfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(testTheme(), editorCfg, nil)
	m.cfg.Agents = append(m.cfg.Agents, config.Agent{Name: "claude"})
	m.dirty = true

	// Concurrently a host-side write updates the [litellm] table from its own
	// load (the table is host state wt's save paths leave alone).
	writerCfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := writerCfg.PatchSave(func(fresh *config.Config) {
		fresh.LitellmTable = &config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-concurrent"}
	}); err != nil {
		t.Fatal(err)
	}

	// The editor now saves.
	_, cmd := m.handleSave()
	if cmd == nil {
		t.Fatal("expected save command, got nil")
	}
	msg := cmd()
	save, ok := msg.(saveMsg)
	if !ok {
		t.Fatalf("expected saveMsg, got %T", msg)
	}
	if save.err != nil {
		t.Fatalf("save failed: %v", save.err)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	enabled := reloaded.LitellmTable != nil && reloaded.LitellmTable.Enabled
	if !enabled || reloaded.LitellmAPIKey() != "sk-concurrent" {
		t.Fatalf("editor save clobbered concurrent litellm change: enabled=%v", enabled)
	}
	found := false
	for _, a := range reloaded.Agents {
		if a.Name == "claude" {
			found = true
		}
	}
	if !found {
		t.Fatalf("editor's own agent change was lost: %+v", reloaded.Agents)
	}
}

// TestSave_FailureKeepsDirty verifies that a failed save leaves dirty=true
// so the user can retry after fixing the problem.
func TestSave_FailureKeepsDirty(t *testing.T) {
	m := newModel(testTheme(), &config.Config{DefaultTag: "code"}, nil)
	m.dirty = true

	// Stub saveCmd to return an error.
	stubSaveResult(t, os.ErrPermission)

	_, cmd := m.handleSave()
	msg := cmd()
	got, _ := m.Update(msg)
	m2 := got.(*model)
	if !m2.dirty {
		t.Error("expected dirty=true after failed save")
	}
	if m2.status == "" {
		t.Error("expected error status after failed save")
	}
}

// TestSave_ValidatesOnlyTheSessionRules pins the session editor's save rule:
// it validates with config.ValidateSession, so an agent naming a provider no
// registry is loaded for still saves (wt config reads no registry, and every
// provider id would look unknown), while a session rule such as two agents
// with one name refuses the save with a "validation: " status and no registry
// hint.
func TestSave_ValidatesOnlyTheSessionRules(t *testing.T) {
	m := newModel(testTheme(), &config.Config{
		DefaultTag: "code",
		Agents:     []config.Agent{{Name: "a"}},
	}, nil)
	m.dirty = true
	got, cmd := m.handleSave()
	if cmd == nil || !got.(*model).saving {
		t.Fatalf("an agent naming an unloaded provider was not saved: status %q", got.(*model).status)
	}

	m = newModel(testTheme(), &config.Config{
		DefaultTag: "code",
		Agents: []config.Agent{
			{Name: "a"},
			{Name: "a"},
		},
	}, nil)
	m.dirty = true
	got, cmd = m.handleSave()
	if cmd != nil || got.(*model).saving {
		t.Fatal("two agents named \"a\" were saved")
	}
	if status := got.(*model).status; status != `validation: duplicate agent name "a"` {
		t.Errorf("status = %q, want %q", status, `validation: duplicate agent name "a"`)
	}
}

// TestSaveNeverWritesASynthesizedAgent pins the spec's rule for the session
// load: config.LoadSession adds an in-memory agent for each driver with no
// entry (Synthesized), and a save through the real saveCmd must write only
// the file's own agents. A synthesized claude written to config.toml would
// pin claude to whatever providers it was saved with and stop accepting every
// provider.
func TestSaveNeverWritesASynthesizedAgent(t *testing.T) {
	scratchConfigHome(t)
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	const toml = "default_tag = \"code\"\n\n[[agents]]\nname = \"codex\"\n"
	if err := os.WriteFile(config.Path(), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSession("claude")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if !slicesContainsSynth(cfg.Agents, "claude") {
		t.Fatalf("LoadSession did not synthesize claude: %+v", cfg.Agents)
	}
	m := newModel(testTheme(), cfg, nil)
	m = send(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = send(t, m, loadedMsg{cfg: cfg})
	m.dirty = true
	m = keys(t, m, "ctrl+s")
	if m.dirty || m.status != "saved" {
		t.Fatalf("the save did not finish: dirty %v status %q", m.dirty, m.status)
	}
	data, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `name = "codex"`) {
		t.Errorf("config.toml lost the codex agent:\n%s", data)
	}
	if strings.Contains(string(data), `name = "claude"`) {
		t.Errorf("config.toml holds the synthesized claude agent:\n%s", data)
	}
}

// slicesContainsSynth reports whether agents holds a synthesized (Synthesized)
// agent named name.
func slicesContainsSynth(agents []config.Agent, name string) bool {
	for _, a := range agents {
		if a.Name == name && a.Synthesized {
			return true
		}
	}
	return false
}
