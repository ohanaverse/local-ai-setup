package editorkit

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// No status line may be wider than the terminal, and continuation lines must
// sit under the text, not under the label.
func TestRenderLineHangsUnderTheLabel(t *testing.T) {
	l := Line{Label: "token", Text: "aaa bbb ccc ddd eee fff ggg"}
	for _, c := range []struct {
		width int
		want  []string
	}{
		{26, []string{"token     aaa bbb ccc ddd", "          eee fff ggg"}},
		// Too narrow for the column: the whole entry flows.
		{25, []string{"token aaa bbb ccc ddd eee", "fff ggg"}},
		{0, []string{"token     aaa bbb ccc ddd eee fff ggg"}},
	} {
		if got := RenderLine(l, c.width); !reflect.DeepEqual(got, c.want) {
			t.Errorf("RenderLine at %d = %q, want %q", c.width, got, c.want)
		}
	}
}

// A row and its continuation are dropped whole, so an unlabelled line belongs
// to the group of the row before it.
func TestGroupsFoldsUnlabelledLinesIntoTheRowBefore(t *testing.T) {
	g := Groups([]Line{{Label: "mode", Text: "x"}, {Text: "y"}, {Label: "url", Text: "z"}}, 80, map[Tone]lipgloss.Style{ToneNone: lipgloss.NewStyle()})
	if len(g) != 2 || g[0].Label != "mode" || g[1].Label != "url" || len(g[0].Lines) != 2 {
		t.Fatalf("Groups = %+v, want two groups mode (2 lines) and url", g)
	}
}

// The mode choice must survive every terminal height, and the marker must
// count what went.
func TestFitBodyDropsRowsInOrderBehindOneMarker(t *testing.T) {
	b := Body{Groups: []StatusGroup{{"a", []string{"A"}}, {"b", []string{"B"}}, {"c", []string{"C"}}}, Choice: []string{"CH"}}
	plain := lipgloss.NewStyle()
	order := []string{"b", "a"}
	for _, c := range []struct {
		name  string
		room  int
		order []string
		want  []string
	}{
		{"fits", 5, order, []string{"A", "B", "C", "", "CH"}},
		{"drops b then a", 4, order, []string{"C", "... 2 status rows not shown ...", "", "CH"}},
		{"blank lines go last", 3, order, []string{"C", "... 2 status rows not shown ...", "CH"}},
		// The order is exhausted; nothing else is dropped by choice, so the
		// blank lines go instead.
		{"order exhausted", 4, []string{"b"}, []string{"A", "C", "... 1 status row not shown ...", "CH"}},
	} {
		if got := FitBody(b, c.room, c.order, plain); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: FitBody(room %d) = %q, want %q", c.name, c.room, got, c.want)
		}
	}
}

func TestChoiceLinesFlowsUnderTheLabel(t *testing.T) {
	opts := []string{"( ) a", "(x) b"}
	for _, c := range []struct {
		width int
		want  []string
	}{
		{0, []string{"choose    ( ) a  (x) b"}},
		{80, []string{"choose    ( ) a  (x) b"}},
		// Too narrow for the label column: the label is not drawn.
		{20, []string{"( ) a  (x) b"}},
	} {
		if got := ChoiceLines("choose", opts, c.width); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ChoiceLines at %d = %q, want %q", c.width, got, c.want)
		}
	}
}

// A status ends with what matters (#209) and must not be cut at the edge.
func TestStatusBlockWrapsToTheWidth(t *testing.T) {
	if got := StatusBlock("", 80); got != "" {
		t.Errorf("StatusBlock of no status = %q, want empty", got)
	}
	if got := StatusBlock("a b", 0); got != "a b" {
		t.Errorf("StatusBlock with no width = %q, want it as given", got)
	}
	if got := lipgloss.Height(StatusBlock("aaa bbb ccc", 4)); got != 3 {
		t.Errorf("StatusBlock at 4 is %d lines, want 3", got)
	}
}
