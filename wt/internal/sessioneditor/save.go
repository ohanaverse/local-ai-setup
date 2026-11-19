package sessioneditor

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// saveMsg is emitted after a config save completes.
type saveMsg struct {
	err error
}

// saveCmd persists the editor's own fields (Agents, DefaultTag) and returns
// a saveMsg. It is a package-level var so tests can override it to inject
// failures.
//
// It goes through PatchSave rather than a whole-file Save: cfg is the
// snapshot the editor loaded when it opened, which may be stale by the time
// the user saves (a Load migration or `wt-model model add` run in the meantime —
// issue #143). PatchSave re-reads config.toml fresh under the lock and only
// overwrites the fields the editor owns, so a concurrent change to any other
// field survives.
var saveCmd = func(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		err := cfg.PatchSave(func(fresh *config.Config) {
			fresh.Agents = cfg.Agents
			fresh.DefaultTag = cfg.DefaultTag
		})
		return saveMsg{err: err}
	}
}

// handleSave validates the in-memory config with the session rules
// (config.ValidateSession) and dispatches an async save.
// It ignores additional save requests while one is already in flight to
// prevent concurrent writes to the same temporary file.
func (m *model) handleSave() (tea.Model, tea.Cmd) {
	if !m.dirty {
		return m, nil
	}
	if m.saving {
		return m, nil
	}
	// The session rules only: no registry is read here, so provider ids are
	// not checked, and the agents LoadSession synthesized are skipped (the
	// save never writes them: writeConfigFile drops them).
	if err := m.cfg.ValidateSession(); err != nil {
		m.status = "validation: " + err.Error()
		m.showQuitSaveFailure()
		return m, nil
	}
	m.saving = true
	return m, saveCmd(m.cfg)
}
