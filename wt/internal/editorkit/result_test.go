package editorkit

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// An apply with nothing to read by hand goes straight back to the tab.
func TestNewResultScreenIsNilWithNothingToShow(t *testing.T) {
	if r := NewResultScreen("", nil, "done"); r != nil {
		t.Fatalf("NewResultScreen with no notes and no recovery = %+v, want nil", r)
	}
}

// Nothing an editor shows may hold a credential.
func TestNewResultScreenScrubsRecoveryAndNotes(t *testing.T) {
	r := NewResultScreen("curl https://u:pw@h/x", []string{"key sk-" + strings.Repeat("ab12", 5)}, "text")
	if r == nil {
		t.Fatal("NewResultScreen = nil, want a screen")
	}
	if r.Recovery != "curl https://h/x" {
		t.Errorf("Recovery = %q, want %q", r.Recovery, "curl https://h/x")
	}
	if len(r.Notes) != 1 || r.Notes[0] != "key sk-<hidden>" {
		t.Errorf("Notes = %q, want [\"key sk-<hidden>\"]", r.Notes)
	}
	if r.Text != "text" {
		t.Errorf("Text = %q, want %q", r.Text, "text")
	}
}

// The recovery is the part that must be read, so it comes first.
func TestResultScreenLinesPutTheRecoveryFirst(t *testing.T) {
	plain := lipgloss.NewStyle()
	var got []string
	for _, l := range NewResultScreen("cp a b", []string{"n1"}, "done").Lines(80, plain, plain) {
		got = append(got, ansi.Strip(l))
	}
	want := []string{"to put things right by hand:", "cp a b", "", "note: n1", "", "done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines = %q, want %q", got, want)
	}
}

func thirtyNotes() *ResultScreen {
	var notes []string
	for i := range 30 {
		notes = append(notes, fmt.Sprintf("n%02d", i))
	}
	return NewResultScreen("", notes, "")
}

// c and r must not replace the screen while a recovery is unread; up and
// down scroll within the lines, esc and enter leave, q, ctrl+c and tab pass.
func TestResultScreenKeyScrollsAndLeaves(t *testing.T) {
	plain := lipgloss.NewStyle()
	r := thirtyNotes()
	k := func(s string) (bool, bool) { return r.Key(s, 80, 10, 1, plain, plain) }
	if h, d := k("down"); !h || d || r.Top != 1 {
		t.Fatalf("down = (%v, %v), Top %d; want (true, false), Top 1", h, d, r.Top)
	}
	k("up")
	k("up")
	if r.Top != 0 {
		t.Fatalf("after up, up Top = %d, want 0", r.Top)
	}
	for range 40 {
		k("down")
	}
	if r.Top != 23 {
		t.Fatalf("after 40 downs Top = %d, want 23", r.Top)
	}
	if h, d := k("c"); !h || d || r.Top != 23 {
		t.Fatalf("c = (%v, %v), Top %d; want (true, false), Top 23", h, d, r.Top)
	}
	for _, s := range []string{"q", "ctrl+c", "tab"} {
		if h, _ := k(s); h {
			t.Errorf("%s handled = true, want false", s)
		}
	}
	for _, s := range []string{"esc", "enter"} {
		if h, d := k(s); !h || !d {
			t.Errorf("%s = (%v, %v), want (true, true)", s, h, d)
		}
	}
}

// The scroll marker counts the lines shown, and the screen fits the terminal.
func TestResultScreenViewCountsTheLinesShown(t *testing.T) {
	plain := lipgloss.NewStyle()
	r := thirtyNotes()
	var asked []bool
	hints := func(s bool) []string { asked = append(asked, s); return []string{"esc done"} }
	v := ansi.Strip(r.View("BAR", hints, 80, 10, plain, plain))
	if !strings.Contains(v, "... lines 1-7 of 30; up/down scroll ...") {
		t.Errorf("View has no scroll marker for lines 1-7:\n%s", v)
	}
	if lines := strings.Split(v, "\n"); lines[len(lines)-1] != "esc done" {
		t.Errorf("View's last line = %q, want %q", lines[len(lines)-1], "esc done")
	}
	if h := lipgloss.Height(v); h > 10 {
		t.Errorf("View height = %d, want <= 10", h)
	}
	if !reflect.DeepEqual(asked, []bool{true}) {
		t.Errorf("hints asked with %v, want [true]", asked)
	}
}
