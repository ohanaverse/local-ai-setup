package configeditor

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// saveMsg is emitted after a config save completes.
type saveMsg struct {
	err error
}

// saveCmd writes cfg to disk and returns a saveMsg. It is a package-level
// var so tests can override it to inject failures.
var saveCmd = func(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		return saveMsg{err: config.Save(cfg)}
	}
}

// handleSave validates the in-memory config and dispatches an async save.
// It ignores additional save requests while one is already in flight to
// prevent concurrent writes to the same temporary file.
func (m model) handleSave() (tea.Model, tea.Cmd) {
	if !m.dirty {
		return m, nil
	}
	if m.saving {
		return m, nil
	}
	if err := m.cfg.ValidateAll(); err != nil {
		m.status = "validation: " + err.Error()
		return m, nil
	}
	m.saving = true
	return m, saveCmd(m.cfg)
}
