package tui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// bareRepoPicker loads the picker from a linked worktree of a bare
// repository (newRmRepo's origin.git, which holds main): the bare
// repository's own entry is the primary row, with no working tree.
func bareRepoPicker(t *testing.T) model {
	t.Helper()
	r := newRmRepo(t)
	bare := filepath.Join(filepath.Dir(r.dir), "origin.git")
	linked := filepath.Join(filepath.Dir(r.dir), "linked")
	rmGit(t, bare, "worktree", "add", "-b", "topic", linked, "main")
	m, _ := pickerLoad(t, linked, nil)
	if !m.ready {
		t.Fatalf("not ready: %q", m.status)
	}
	return m
}

// bareRowIndex is the item index of the picker's bare-repository row.
func bareRowIndex(t *testing.T, m model) int {
	t.Helper()
	for i, it := range m.rows.items {
		if it.heading == "" && !it.newWorktree && it.row.Bare {
			return i
		}
	}
	var names []string
	for _, it := range m.rows.items {
		names = append(names, cleanup.DisplayName(it.row))
	}
	t.Fatalf("no bare row among %v", names)
	return -1
}

// TestPickerBareRepoRowIsTaggedAndNotLaunchable: the old picker skipped a
// bare repository's entry; the cleanup rows list it (it is the primary). It
// carries a bare tag, and Enter on it launches nothing (there is no working
// tree to run an agent in) and says why on the status line.
func TestPickerBareRepoRowIsTaggedAndNotLaunchable(t *testing.T) {
	m := bareRepoPicker(t)
	i := bareRowIndex(t, m)
	row := m.rows.items[i].row
	if !row.Primary || row.HardStop != cleanup.BlockPrimary {
		t.Errorf("bare row primary=%v hard stop %q, want the primary hard stop (never removable)", row.Primary, row.HardStop)
	}
	tags := []string{}
	for _, tg := range cleanup.RowTags(row) {
		tags = append(tags, tg.Text)
	}
	if !slices.Contains(tags, "bare") {
		t.Errorf("tags %v, want bare", tags)
	}
	m.rows.cursor = i
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	for n := 0; cmd != nil && n < 5; n++ {
		msg := cmd()
		if _, ok := msg.(selectedEntryMsg); ok {
			t.Fatal("enter on the bare repository's row emitted selectedEntryMsg")
		}
		if msg == nil {
			break
		}
		m, cmd = updateMsg(m, msg)
	}
	if !strings.Contains(m.View(), "a bare repository has no working tree to launch in") {
		t.Errorf("status line missing; view:\n%s", m.View())
	}
}
