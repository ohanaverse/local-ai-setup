// Tests for the picker's question about other providers' models (#412): the
// dialog a start opens when the model server reports models of other provider
// families loaded beside the target, how it fits the terminal, and --others
// for the pinned row.
package tui

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// choiceTitles lists the dialog's choices by title.
func choiceTitles(m model) []string {
	var titles []string
	for _, it := range m.replace.choices.Items() {
		titles = append(titles, it.(choiceItem).title)
	}
	return titles
}

// chooseTitled moves the dialog's cursor to the choice titled title and
// presses Enter.
func chooseTitled(t *testing.T, m model, title string) model {
	t.Helper()
	for i, it := range m.replace.choices.Items() {
		if it.(choiceItem).title == title {
			m.replace.choices.Select(i)
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
			if next.start != nil && next.start.cancel != nil {
				t.Cleanup(next.start.cancel)
			}
			return next
		}
	}
	t.Fatalf("no choice %q in %v", title, choiceTitles(m))
	return m
}

// TestRunArgsReachModelOthers pins the Run(...) → model hop for --others:
// the value lands on the model, where the pinned row's start reads it.
// Without it the flag would be parsed and silently dropped by every picker
// launch.
func TestRunArgsReachModelOthers(t *testing.T) {
	for _, mode := range []string{"", "keep", "stop"} {
		m := newRunModel(false, false, mode, "", "omlx/qwen3.8", "", "", nil, themes.Theme{}, "", nil, &config.Config{}, nil)
		if m.othersMode != mode {
			t.Errorf("--others=%q: model.othersMode = %q", mode, m.othersMode)
		}
	}
}

// longOthersDialog is the others dialog as the model server's refusal opens
// it for long ids at width x height: a same-family occupant, three other
// providers' models wt can stop and a family the server cannot read. The
// first start is refused; every later one ends with a failure the picker
// reports (the tests read the retry's request). It returns the fake, whose
// start requests are what each attempt carried.
func longOthersDialog(t *testing.T, width, height int) (model, *fakeModels) {
	t.Helper()
	cat := modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: []modelapi.Row{
		wireRow("omlx/Qwen3.6-35B-A3B-4bit", "qwen", func(r *modelapi.Row) {
			r.Location, r.State, r.Action, r.Order = "local", modelapi.StateStopped, modelapi.ActionStart, 1
		}),
	}}
	f := &fakeModels{cat: cat}
	f.startFn = func(_ context.Context, n int, _ modelapi.StartRequest, _ modelclient.Progress) (modelapi.JobStatus, error) {
		if n > 1 {
			return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeFailed, Message: "load failed"}}
		}
		return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{
			Code: modelapi.CodeOccupied, Message: "omlx is serving omlx/Ornith-1.5-35B-A3B-6bit",
			Occupants: []string{"omlx/Ornith-1.5-35B-A3B-6bit"},
			Others: []modelapi.Resident{
				{ID: "mtplx/mlx-community/Qwen3.8-35B-A3B-8bit", Label: "mtplx/mlx-community/Qwen3.8-35B-A3B-8bit", Stoppable: true, Figure: "memory use unknown"},
				{ID: "ollama/gemma4:27b-it-qat", Label: "ollama/gemma4:27b-it-qat", Stoppable: true, Figure: "18.2 GB loaded"},
				{ID: "ollama/qwen3-coder:30b-a3b-q4_K_M", Label: "ollama/qwen3-coder:30b-a3b-q4_K_M", Stoppable: true, Figure: "19.0 GB loaded"},
			},
			OthersUnknown: []string{"mlx_lm_server"},
		}}
	}
	m := remoteTable(t, f)
	m.width, m.height = width, height
	next, _ := enterRemote(t, m, "omlx/Qwen3.6-35B-A3B-4bit")
	next, _ = updateMsg(next, recvStart(t, next))
	if next.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v (status %q), want the others dialog", next.phase, next.status)
	}
	return resized(t, next, width, height), f
}

// retryStops is the stop list the second start request carried, after the
// dialog's choice ran its start to the end.
func retryStops(t *testing.T, next model, f *fakeModels) []string {
	t.Helper()
	if next.phase == phaseStarting {
		next, _ = updateMsg(next, recvStart(t, next))
	}
	reqs := f.startReqs()
	if len(reqs) < 2 || reqs[1].Others == nil {
		return nil
	}
	return reqs[1].Others.Stop
}

// The long-ids dialog's question line and the stop choice's title.
const (
	longQuestion = "Starting omlx/Qwen3.6-35B-A3B-4bit will stop:"
	longStop     = "Replace and stop the others"
	enlargeNote  = "enlarge the terminal to stop them"
)

// TestOthersDialogOffersStopOnlyWhenEveryModelShows pins the dialog on a
// short terminal (#412): when its question has to be cut ("…"), so some of
// the models a stop would stop are not on the screen, the stop choice is not
// offered — Cancel (first, under the cursor) and keep remain — and the cut
// line says to enlarge the terminal; at 80x24 every model shows and stop is
// offered. Offered under a cut question, one Enter stops models the user
// never saw.
func TestOthersDialogOffersStopOnlyWhenEveryModelShows(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		titles        []string
		note          bool
	}{
		{"80x12, question cut", 80, 12, []string{"Cancel", "Replace, keep the others"}, true},
		{"40x12, question cut", 40, 12, []string{"Cancel", "Replace, keep the others"}, true},
		{"80x24, every model shows", 80, 24, []string{"Cancel", "Replace, keep the others", longStop}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := longOthersDialog(t, c.width, c.height)
			if titles := choiceTitles(got); !reflect.DeepEqual(titles, c.titles) {
				t.Errorf("choices = %v, want %v", titles, c.titles)
			}
			if got.replace.choices.Index() != 0 {
				t.Errorf("cursor on choice %d, want 0 (Cancel)", got.replace.choices.Index())
			}
			view := got.View()
			if has := strings.Contains(strings.Join(strings.Fields(view), " "), enlargeNote); has != c.note {
				t.Errorf("screen says %q: %v, want %v; screen:\n%s", enlargeNote, has, c.note, view)
			}
		})
	}
}

// TestOthersDialogStopFollowsTheTerminalSize pins a resize while the dialog
// is open (#412): shrinking the terminal until the question is cut withdraws
// the stop choice, and with it the cursor if it was on stop (back to Cancel),
// and enlarging it again brings stop back. Without the first, a stop chosen
// at full size stays one Enter away after its models left the screen;
// without the second, the user who enlarged the terminal as told still
// cannot stop them.
func TestOthersDialogStopFollowsTheTerminalSize(t *testing.T) {
	got, f := longOthersDialog(t, 80, 24)
	got.replace.choices.Select(2)
	small := resized(t, got, 80, 12)
	if titles := choiceTitles(small); slices.Contains(titles, longStop) {
		t.Fatalf("choices at 80x12 = %v, want no stop", titles)
	}
	if small.replace.choices.Index() != 0 {
		t.Errorf("cursor on choice %d after stop was withdrawn, want 0 (Cancel)", small.replace.choices.Index())
	}
	large := resized(t, small, 80, 24)
	if titles := choiceTitles(large); !slices.Contains(titles, longStop) {
		t.Fatalf("choices back at 80x24 = %v, want stop again", titles)
	}
	next := chooseTitled(t, large, longStop)
	if next.phase != phaseStarting {
		t.Fatalf("phase = %v after the stop choice, want the start screen", next.phase)
	}
	if stops := retryStops(t, next, f); len(stops) != 3 {
		t.Errorf("retry stops %v, want the three models shown", stops)
	}
}

// TestOthersDialogCutStopCannotBeChosen pins every key path through the
// dialog at a size whose question is cut (#412): moving the cursor as far
// down as it goes (down, j, end, G, a page down) or typing a digit, then
// Enter, never retries with a stop; the cursor's last choice is keep, so a
// retry, when there is one, keeps them. A key that reached a withheld stop
// would stop models the user cannot see.
func TestOthersDialogCutStopCannotBeChosen(t *testing.T) {
	paths := map[string][]tea.KeyMsg{
		"down x5":   {{Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyDown}, {Type: tea.KeyDown}},
		"j x5":      {{Type: tea.KeyRunes, Runes: []rune("j")}, {Type: tea.KeyRunes, Runes: []rune("j")}, {Type: tea.KeyRunes, Runes: []rune("j")}, {Type: tea.KeyRunes, Runes: []rune("j")}, {Type: tea.KeyRunes, Runes: []rune("j")}},
		"end":       {{Type: tea.KeyEnd}},
		"G":         {{Type: tea.KeyRunes, Runes: []rune("G")}},
		"page down": {{Type: tea.KeyPgDown}, {Type: tea.KeyPgDown}},
		"digit 3":   {{Type: tea.KeyRunes, Runes: []rune("3")}},
	}
	for name, keys := range paths {
		t.Run(name, func(t *testing.T) {
			got, f := longOthersDialog(t, 80, 12)
			for _, k := range keys {
				got, _ = updateMsg(got, k)
			}
			next, _ := updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})
			if next.start != nil && next.start.cancel != nil {
				t.Cleanup(next.start.cancel)
			}
			if stops := retryStops(t, next, f); len(stops) != 0 {
				t.Errorf("the retry stops %v at a size that hides them", stops)
			}
		})
	}
}

// TestOthersDialogCutKeepsTheQuestion pins the sparest layout of a cut
// dialog (#412): it keeps the question's first line and the line saying why
// stop is missing, never a lone "…". A header cut to "…" leaves the user
// choosing without knowing what the dialog asks.
func TestOthersDialogCutKeepsTheQuestion(t *testing.T) {
	got, _ := longOthersDialog(t, 80, 12)
	frames := got.replaceFrames()
	sparest := frames[len(frames)-1]("")
	if !strings.Contains(sparest, longQuestion) || !strings.Contains(sparest, enlargeNote) {
		t.Errorf("sparest frame = %q, want the question line and %q", sparest, enlargeNote)
	}
}

// TestOthersDialogShowsFigure pins the others question on the model server:
// an others problem opens the same dialog, one line per resident from the
// problem (its Label and the memory Figure the server worded) and a line per
// family the server could not read; Cancel stays first, stop is offered for
// the stoppable residents, and choosing it posts again with exactly those ids.
func TestOthersDialogShowsFigure(t *testing.T) {
	problem := &modelclient.JobError{Problem: modelapi.Problem{
		Code: modelapi.CodeOthers, Message: "other providers' models are loaded",
		Others: []modelapi.Resident{
			{ID: "ollama/gemma4:12b", Label: "ollama/gemma4:12b", Stoppable: true, Figure: "9.6 GB loaded"},
			{ID: "mlx_lm_server/pair", Label: "mlx_lm_server/pair", Figure: "memory use unknown"},
		},
		OthersUnknown: []string{"sushi"},
	}}
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(_ context.Context, n int, _ modelapi.StartRequest, _ modelclient.Progress) (modelapi.JobStatus, error) {
		if n == 1 {
			return modelapi.JobStatus{}, problem
		}
		return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeFailed, Message: "load failed"}}
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	if m.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v, status %q: want the dialog", m.phase, m.status)
	}
	want := "Also loaded, by other providers:\n" +
		"  ollama/gemma4:12b — 9.6 GB loaded\n" +
		"  mlx_lm_server/pair — memory use unknown\n" +
		"  cannot tell whether sushi is serving a model"
	if m.replace.header != want {
		t.Errorf("header = %q, want %q", m.replace.header, want)
	}
	if got := choiceTitles(m); !reflect.DeepEqual(got, []string{"Cancel", "Start and keep them", "Stop them, then start"}) || m.replace.choices.Index() != 0 {
		t.Errorf("choices = %v at %d, want Cancel first and under the cursor", got, m.replace.choices.Index())
	}
	m = chooseTitled(t, m, "Stop them, then start")
	m, _ = updateMsg(m, recvStart(t, m))
	reqs := f.startReqs()
	if len(reqs) != 2 || reqs[0].Others != nil || reqs[1].Others == nil || !reflect.DeepEqual(reqs[1].Others.Stop, []string{"ollama/gemma4:12b"}) {
		t.Errorf("start requests = %+v, want the retry to stop exactly the stoppable resident", reqs)
	}
}
