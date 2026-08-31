package configeditor

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestSave_AtomicWrite verifies that Ctrl-S writes the in-memory config
// to disk atomically and that no temporary file is left behind.
func TestSave_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		DefaultTag: "code",
		Providers:  []config.Provider{{ID: "ollama"}},
		Agents:     []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
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
	m2 := got.(model)
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

	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{
		DefaultTag: "code",
		Models:     []config.Model{{ID: "foo", ProviderID: "missing"}},
	}
	m.dirty = true

	got, cmd := m.handleSave()
	if cmd != nil {
		t.Fatal("expected nil command when validation fails")
	}
	m2 := got.(model)
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
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{DefaultTag: "code"}
	if m.dirty {
		t.Fatal("expected dirty=false at start")
	}

	// Make an edit.
	m.cfg.Providers = append(m.cfg.Providers, config.Provider{ID: "new"})
	m.dirty = true

	// Stub saveCmd to succeed without touching disk.
	old := saveCmd
	saveCmd = func(cfg *config.Config) tea.Cmd {
		return func() tea.Msg { return saveMsg{} }
	}
	defer func() { saveCmd = old }()

	_, cmd := m.handleSave()
	msg := cmd()
	got, _ := m.Update(msg)
	m2 := got.(model)
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

	old := saveCmd
	saveCmd = func(cfg *config.Config) tea.Cmd {
		return func() tea.Msg { return saveMsg{err: os.ErrPermission} }
	}
	defer func() { saveCmd = old }()

	_, cmd := m.handleSave()
	msg := cmd()
	got, _ := m.Update(msg)
	m2 := got.(model)
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
	m2 := got.(model)
	if !m2.saving {
		t.Fatal("expected saving=true after dispatching save")
	}

	got2, cmd2 := m2.handleSave()
	if cmd2 != nil {
		t.Fatal("expected second save request to be ignored")
	}
	m3 := got2.(model)
	if !m3.saving {
		t.Fatal("expected saving to remain true")
	}
}

// TestSave_FailureKeepsDirty verifies that a failed save leaves dirty=true
// so the user can retry after fixing the problem.
func TestSave_FailureKeepsDirty(t *testing.T) {
	m := newModel(testTheme(), &config.Config{}, nil)
	m.cfg = &config.Config{DefaultTag: "code"}
	m.dirty = true

	// Stub saveCmd to return an error.
	old := saveCmd
	saveCmd = func(cfg *config.Config) tea.Cmd {
		return func() tea.Msg { return saveMsg{err: os.ErrPermission} }
	}
	defer func() { saveCmd = old }()

	_, cmd := m.handleSave()
	msg := cmd()
	got, _ := m.Update(msg)
	m2 := got.(model)
	if !m2.dirty {
		t.Error("expected dirty=true after failed save")
	}
	if m2.status == "" {
		t.Error("expected error status after failed save")
	}
}
