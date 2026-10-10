package configeditor

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// typeText sends s to the focused field one character at a time.
func typeText(t *testing.T, m *model, s string) *model {
	t.Helper()
	for _, r := range s {
		m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// moveTo moves the form's cursor down to field.
func moveTo(t *testing.T, m *model, field int) *model {
	t.Helper()
	for i := 0; i < mfCount && m.models.form.cursor != field; i++ {
		m = keys(t, m, "down")
	}
	if m.models.form.cursor != field {
		t.Fatalf("the cursor cannot reach field %d (%s)", field, modelFormLabels[field])
	}
	return m
}

// formMarker is a "↑ N more" or "↓ N more" row of the form.
var formMarker = regexp.MustCompile(`[↑↓] \d+ more`)

// assertCutFieldsAreCounted fails when the form draws only some of its fields
// and no marker row says so. A form squeezed to one or two fields with nothing
// beside them reads as the whole form: the user does not know the other
// fields are there. A view in which the error has the screen to itself draws
// no fields and is not measured.
func assertCutFieldsAreCounted(t *testing.T, name string, m *model, view string) {
	t.Helper()
	if m.modelFormLayout().errorAlone {
		return
	}
	drawn := 0
	for _, label := range modelFormLabels {
		if strings.Contains(view, " "+label+": ") {
			drawn++
		}
	}
	if drawn < mfCount && !formMarker.MatchString(view) {
		t.Errorf("%s at %dx%d: %d of the %d fields are drawn and no marker counts the rest:\n%s", name, m.width, m.height, drawn, mfCount, view)
	}
}

// TestModelFormFitsTheTerminal measures the form at every size wt supports,
// in each of its three modes, with the cursor on every field it can reach
// and with an error showing. The view must stay inside the terminal with the
// tab bar and the focused field on screen: on a 12-line terminal the ten
// fields do not all fit, and a form that let Bubble Tea cut its top would
// hide the field being typed into. Fields that are off the screen are counted
// by a marker row. The edit form of a 72-column id is measured too while its
// save is out, with and without a quit held for it: those titles are the
// longest the form has, four lines of a 40-column terminal.
func TestModelFormFitsTheTerminal(t *testing.T) {
	modes := []struct {
		name string
		open func(t *testing.T, m *model) *model
	}{
		{"add", func(t *testing.T, m *model) *model { return keys(t, m, "n") }},
		{"edit", func(t *testing.T, m *model) *model {
			return keys(t, selectModel(t, m, "openrouter/anthropic--claude-sonnet-4.5-thinking"), "enter")
		}},
		{"register", func(t *testing.T, m *model) *model { return keys(t, selectModel(t, m, "ollama/qwen3:8b"), "enter") }},
	}
	const longError = `input-price must be a number, got "a price that somebody pasted a whole sentence into by mistake"`
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			for _, mode := range modes {
				tm := newTabMachine(t, tabRegistry)
				m := mode.open(t, modelsEditor(t, tm, width, height))
				if m.models.phase != modelsForm {
					t.Fatalf("%s: the form did not open", mode.name)
				}
				for field := 0; field < mfCount; field++ {
					if !m.models.form.editable(field) {
						continue
					}
					m = moveTo(t, m, field)
					for _, withError := range []bool{false, true} {
						m.models.form.err = ""
						if withError {
							m.models.form.err = longError
						}
						name := mode.name + " form on " + modelFormLabels[field]
						view := m.View()
						assertFits(t, name, view, width, height)
						assertCutFieldsAreCounted(t, name, m, view)
						if !strings.Contains(view, "[Models]") {
							t.Errorf("%s at %dx%d: the tab bar is not on screen:\n%s", name, width, height, view)
						}
						if !strings.Contains(view, "> "+modelFormLabels[field]+":") {
							t.Errorf("%s at %dx%d: the focused field is not on screen:\n%s", name, width, height, view)
						}
						if withError && !strings.Contains(view, "input-price must be a number") {
							t.Errorf("%s at %dx%d: the error is not on screen:\n%s", name, width, height, view)
						}
					}
				}
			}
			tm := newTabMachine(t, tabLongRegistry)
			m := keys(t, selectModel(t, modelsEditor(t, tm, width, height), tabLongID), "enter")
			if m.models.phase != modelsForm {
				t.Fatal("the edit form of the long id did not open")
			}
			for field := 0; field < mfCount; field++ {
				if !m.models.form.editable(field) {
					continue
				}
				m = moveTo(t, m, field)
				for _, c := range []struct {
					title string
					quit  bool
				}{{"(saving...)", false}, {"(saving, then quitting...)", true}} {
					m.models.form.saving, m.quitPending = true, c.quit
					name := "edit form of the long id " + c.title + " on " + modelFormLabels[field]
					view := m.View()
					assertFits(t, name, view, width, height)
					assertCutFieldsAreCounted(t, name, m, view)
					if !strings.Contains(view, "[Models]") || !strings.Contains(flat(view), flat("Edit "+tabLongID+"  "+c.title)) {
						t.Errorf("%s at %dx%d: the tab bar and the whole title should be on screen:\n%s", name, width, height, view)
					}
					if !strings.Contains(view, "> "+modelFormLabels[field]+":") {
						t.Errorf("%s at %dx%d: the focused field is not on screen:\n%s", name, width, height, view)
					}
				}
				m.models.form.saving, m.quitPending = false, false
			}
		}
	}
}

// TestModelFormAddWritesAtOnce verifies n opens an empty form, and ctrl+s
// writes the model to the registry there and then — in a command, not in
// Update — goes back to the table with the cursor on the new row, and marks
// the routes pending. There is no second "save" for models: a user who quits
// after the form closes has not lost the model.
func TestModelFormAddWritesAtOnce(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 80, 24), "n")
	f := m.models.form
	if f.mode != modelFormAdd || f.value(mfProvider) != "ollama" || f.cursor != mfProvider {
		t.Fatalf("an add should open on the provider, set to ollama; got %q, cursor %d", f.value(mfProvider), f.cursor)
	}
	// ollama, omlx, openrouter and the seedable mtplx; right twice is
	// openrouter.
	if got := strings.Join(f.providers, ","); got != "mtplx,ollama,omlx,openrouter" {
		t.Errorf("provider choices = %s", got)
	}
	m = keys(t, m, "right", "right")
	if got := m.models.form.value(mfProvider); got != "openrouter" {
		t.Fatalf("after right, right the provider is %q, want openrouter", got)
	}
	m = typeText(t, keys(t, m, "down"), "qwen/qwen3.8-27b")
	m = typeText(t, keys(t, m, "down"), "qwen-next")
	m = typeText(t, keys(t, m, "down"), "code, design")
	m = typeText(t, moveTo(t, m, mfInput), "0.5")
	m = typeText(t, moveTo(t, m, mfOutput), "2")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = next.(*model)
	if cmd == nil || tm.text(t) != tabRegistry || !strings.Contains(m.View(), "(saving...)") {
		t.Fatal("ctrl+s must hand the write to a command and say it is saving; nothing is written by Update itself")
	}
	m = send(t, m, cmd())
	want := tabRegistry + `
[[models]]
id = "openrouter/qwen--qwen3.8-27b"
family = "qwen-next"
provider_id = "openrouter"
model_name = "qwen/qwen3.8-27b"
tags = [
    "code",
    "design",
]

[models.cost]
input_price_per_million = 0.5
output_price_per_million = 2.0
`
	if got := tm.text(t); got != want {
		t.Errorf("registry after the add =\n%s\nwant\n%s", got, want)
	}
	if m.models.phase != modelsList || !m.registryChanged {
		t.Errorf("phase = %d, registryChanged = %v; want the table and the change recorded", m.models.phase, m.registryChanged)
	}
	if r, _ := m.selectedModel(); r.ID != "openrouter/qwen--qwen3.8-27b" {
		t.Errorf("the cursor is on %q, want the model just added", r.ID)
	}
	if view := strings.Join(strings.Fields(m.View()), " "); !strings.Contains(view, "added openrouter/qwen--qwen3.8-27b") || !strings.Contains(view, "LiteLLM routes pending") {
		t.Errorf("the status should name the model and the pending routes:\n%s", m.View())
	}
}

// TestModelFormEditSendsOnlyWhatChanged verifies an edit opens with the
// row's own values, that saving it untouched writes nothing at all, and that
// changing one field changes that key alone. A form that wrote back every
// field would add `tags = []` to rows that had none and turn integer prices
// into floats on every save.
func TestModelFormEditSendsOnlyWhatChanged(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "openrouter/anthropic--claude-sonnet-4.5-thinking"), "enter")
	f := m.models.form
	if f.mode != modelFormEdit || f.cursor != mfFamily {
		t.Fatalf("an edit should open on Family (provider and name are fixed); cursor = %d", f.cursor)
	}
	for field, want := range map[int]string{mfProvider: "openrouter", mfName: "anthropic/claude-sonnet-4.5-thinking", mfFamily: "sonnet", mfTags: "code",
		mfLocation: "", mfInput: "3", mfCache: "", mfOutput: "15", mfSubPrice: "", mfSubPeriod: ""} {
		if got := f.value(field); got != want {
			t.Errorf("%s opens as %q, want %q", modelFormLabels[field], got, want)
		}
	}
	view := m.View()
	if !strings.Contains(view, "Edit openrouter/anthropic--claude-sonnet-4.5-thinking") || !strings.Contains(view, "  Provider: openrouter") {
		t.Errorf("the edit form should show the id and the fixed provider:\n%s", view)
	}

	m = keys(t, m, "ctrl+s")
	if tm.text(t) != tabRegistry || m.registryChanged || !strings.Contains(m.View(), "no change to openrouter/anthropic--claude-sonnet-4.5-thinking") {
		t.Fatalf("saving an untouched form must write nothing and say so:\n%s", m.View())
	}

	m = keys(t, m, "enter")
	m = typeText(t, moveTo(t, m, mfTags), ",design")
	m = keys(t, moveTo(t, m, mfLocation), "right", "right") // (the provider's) -> local -> cloud
	m = keys(t, m, "ctrl+s")
	want := strings.Replace(tabRegistry,
		"model_name = \"anthropic/claude-sonnet-4.5-thinking\"\ntags = [\n    \"code\",\n]\n",
		"model_name = \"anthropic/claude-sonnet-4.5-thinking\"\nlocation = \"cloud\"\ntags = [\n    \"code\",\n    \"design\",\n]\n", 1)
	if got := tm.text(t); got != want {
		t.Errorf("registry after the edit =\n%s\nwant only tags and location changed", got)
	}
	if !m.registryChanged || !strings.Contains(m.View(), "saved openrouter/anthropic--claude-sonnet-4.5-thinking") {
		t.Errorf("the edit should be recorded and reported:\n%s", m.View())
	}
}

// TestModelFormRegistersADiscoveredModelUnderItsOwnId verifies enter on a
// `new` row opens the form with the provider and the name fixed, and that
// saving registers the model under the id it already had as a discovered
// row — the id its routes, usage history and rotation already use — with the
// capabilities ollama reports.
func TestModelFormRegistersADiscoveredModelUnderItsOwnId(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	deps := tm.deps()
	asked := ""
	deps.Capabilities = func(_ *config.Config, name string) (map[string]any, error) {
		asked = name
		return map[string]any{"supports_function_calling": true}, nil
	}
	m := modelsEditor(t, tm, 80, 24)
	m.opts.Models = deps
	m = keys(t, selectModel(t, m, "ollama/qwen3:8b"), "enter")
	f := m.models.form
	if f.mode != modelFormRegister || f.editable(mfProvider) || f.editable(mfName) || f.cursor != mfFamily {
		t.Fatalf("registering should fix the provider and the name and open on Family; cursor = %d", f.cursor)
	}
	if !strings.Contains(m.View(), "Register ollama/qwen3:8b") {
		t.Errorf("the form should name the id it will register:\n%s", m.View())
	}
	m = keys(t, typeText(t, m, "qwen3"), "ctrl+s")
	got := tm.text(t)
	for _, want := range []string{"[[providers]]\nid = \"ollama\"", "id = \"ollama/qwen3:8b\"\nfamily = \"qwen3\"\nprovider_id = \"ollama\"\nmodel_name = \"qwen3:8b\"", "[models.model_info]\nsupports_function_calling = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("the registry should now hold %q:\n%s", want, got)
		}
	}
	if asked != "qwen3:8b" {
		t.Errorf("ollama was asked about %q, want qwen3:8b", asked)
	}
	if !strings.Contains(m.View(), "added ollama/qwen3:8b\nadded provider ollama\n") {
		t.Errorf("the status should name the model and the provider row seeded with it:\n%s", m.View())
	}
	if r, _ := findRow(m, "ollama/qwen3:8b"); !r.row.Registered {
		t.Error("after the re-probe the row should be a registry row")
	}
}

// TestModelFormRefusalStaysOnTheFieldAtFault verifies a save the rules
// refuse writes nothing, keeps the form open with what was typed, shows why,
// and puts the cursor on the field to fix. A failed ollama lookup, by
// contrast, does not refuse the add: it is noted on the status line.
func TestModelFormRefusalStaysOnTheFieldAtFault(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 80, 24), "n")
	m = typeText(t, moveTo(t, m, mfName), "tiny:1b")
	m = typeText(t, moveTo(t, m, mfFamily), "tiny")
	m = typeText(t, moveTo(t, m, mfCache), "cheap")
	m = keys(t, moveTo(t, m, mfSubPeriod), "ctrl+s")
	f := m.models.form
	if m.models.phase != modelsForm || f.cursor != mfCache || !strings.Contains(m.View(), `cache-price must be a number, got "cheap"`) {
		t.Fatalf("a bad price should keep the form open on Cache $/M with the reason; cursor = %d\n%s", f.cursor, m.View())
	}
	if tm.text(t) != tabRegistry || m.registryChanged || f.value(mfName) != "tiny:1b" {
		t.Error("a refused save must write nothing and keep what was typed")
	}

	// A missing name, with the cursor elsewhere: the cursor goes to the name.
	m = keys(t, m, "esc", "n", "down", "down")
	m = keys(t, typeText(t, m, "f"), "ctrl+s")
	if m.models.form.cursor != mfName || !strings.Contains(m.View(), "a model name is required") {
		t.Errorf("a missing name should move the cursor to Model name:\n%s", m.View())
	}

	// The lookup fails (tabMachine's Capabilities is not stubbed): the model
	// is still added, and the status says what is missing.
	m = keys(t, typeText(t, moveTo(t, m, mfName), "tiny:1b"), "ctrl+s")
	if !strings.Contains(tm.text(t), `id = "ollama/tiny:1b"`) || strings.Contains(tm.text(t), "model_info") {
		t.Errorf("a failed lookup must still add the model, without model_info:\n%s", tm.text(t))
	}
	if !strings.Contains(m.View(), "added ollama/tiny:1b\nadded without tool/vision capabilities: not stubbed\nadded provider ollama\n") {
		t.Errorf("the status should say the capabilities were not recorded:\n%s", m.View())
	}

	// The usual reason, ollama not installed, is said in a few words: the
	// whole error is three lines of a 40-column terminal.
	m.opts.Models.Capabilities = func(*config.Config, string) (map[string]any, error) {
		return nil, fmt.Errorf("`ollama show x` failed: %w", exec.ErrNotFound)
	}
	m = keys(t, m, "n")
	m = typeText(t, moveTo(t, m, mfName), "other:1b")
	m = keys(t, typeText(t, moveTo(t, m, mfFamily), "f"), "ctrl+s")
	if !strings.Contains(m.View(), "added without tool/vision capabilities: ollama is not installed\n") {
		t.Errorf("want the short note for a missing ollama:\n%s", m.View())
	}
}

// TestModelFormRefusesBeforeItAsksOllama verifies a save that is going to be
// refused for a value does not run the ollama lookup first: with the daemon
// down the lookup takes its whole timeout, and the user would wait ten
// seconds to be told a price is not a number.
func TestModelFormRefusesBeforeItAsksOllama(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := modelsEditor(t, tm, 80, 24)
	asked := 0
	m.opts.Models.Capabilities = func(*config.Config, string) (map[string]any, error) { asked++; return nil, nil }
	m = keys(t, m, "n")
	m = typeText(t, moveTo(t, m, mfName), "tiny:1b")
	m = typeText(t, moveTo(t, m, mfFamily), "tiny")
	m = keys(t, typeText(t, moveTo(t, m, mfInput), "cheap"), "ctrl+s")
	if asked != 0 || m.models.phase != modelsForm || !strings.Contains(m.View(), "input-price must be a number") {
		t.Errorf("lookups = %d, want the price refused with none run:\n%s", asked, m.View())
	}
}

// TestQuitWaitsForAFormSaveInFlight is TestQuitWaitsForARegistryWriteInFlight
// for the form, where the window is wider: an add of an ollama model runs
// `ollama show` before it writes. ctrl+c while the save is out must not end
// the program before the save has reported; a refused save calls the quit
// off and stays on the form.
func TestQuitWaitsForAFormSaveInFlight(t *testing.T) {
	// saving fills the add form and presses ctrl+s, handing back the save,
	// not run.
	saving := func(t *testing.T, price string) (*tabMachine, *model, tea.Cmd) {
		tm := newTabMachine(t, tabRegistry)
		m := keys(t, modelsEditor(t, tm, 80, 24), "n", "right", "right") // ollama -> omlx -> openrouter
		m = typeText(t, moveTo(t, m, mfName), "qwen/qwen3.8-27b")
		m = typeText(t, moveTo(t, m, mfFamily), "qwen3.8")
		m = typeText(t, moveTo(t, m, mfInput), price)
		next, save := m.Update(keyMsg("ctrl+s"))
		if save == nil {
			t.Fatal("ctrl+s should hand the save to a command")
		}
		return tm, next.(*model), save
	}
	tm, m, save := saving(t, "0.5")
	next, cmd := m.Update(keyMsg("ctrl+c"))
	m = next.(*model)
	if cmd != nil || !m.quitPending {
		t.Fatalf("ctrl+c with the save in flight: a command = %v, quitPending = %v; want the quit held back", cmd != nil, m.quitPending)
	}
	// The form says the quit was taken: an `ollama show` can hold the save
	// for seconds, and "(saving...)" alone reads as a ctrl+c that was lost.
	if !strings.Contains(flat(m.View()), flat("(saving, then quitting...)")) {
		t.Errorf("the form should say it quits once the save is in:\n%s", m.View())
	}
	assertFits(t, "form, quit held", m.View(), 80, 24)
	next, cmd = m.Update(save())
	m = next.(*model)
	if cmd == nil || !m.registryChanged || !strings.Contains(tm.text(t), `id = "openrouter/qwen--qwen3.8-27b"`) {
		t.Fatalf("once the save is in: a command = %v, registryChanged = %v; want the quit, with the change recorded", cmd != nil, m.registryChanged)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("the command after a held quit should be the quit, not a re-probe")
	}

	tm, m, save = saving(t, "cheap")
	next, _ = m.Update(keyMsg("ctrl+c"))
	next, cmd = next.(*model).Update(save())
	m = next.(*model)
	if cmd != nil || m.quitPending || m.models.phase != modelsForm || !strings.Contains(m.View(), "input-price must be a number") || tm.text(t) != tabRegistry {
		t.Errorf("a refused save has to be read: want no quit and the form still open with the reason:\n%s", m.View())
	}
}

// TestModelFormShowsWhatDoesNotFit verifies nothing in the form is cut
// without a sign of it on a 40-column, 12-line terminal: the fields that have
// scrolled off are counted above and below, the title wraps, a fixed value
// keeps both its ends, a field that is not being edited shows the start of
// its value with an ellipsis, and the key hints are the short ones, whole.
func TestModelFormShowsWhatDoesNotFit(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 40, 12), "n")
	view := m.View()
	// Three, not two: while the cursor is on Provider the line that says
	// where a pairing is added has one of the rows.
	if !strings.Contains(view, "> Provider: < ollama >") || !strings.Contains(view, "  ↓ 3 more") || strings.Contains(view, "Subscription") {
		t.Errorf("at the top of the add form the three fields below should be counted:\n%s", view)
	}
	if below := keys(t, m, "down").View(); !strings.Contains(below, "  ↓ 2 more") {
		t.Errorf("off Provider the pairing line gives its row back to the fields:\n%s", below)
	}
	if !strings.Contains(view, "^s save · esc · ↑/↓ · ←/→ change") {
		t.Errorf("the short key hints should be whole at 40 columns:\n%s", view)
	}
	m = typeText(t, moveTo(t, m, mfName), "Qwen3.8-35B-A3B-Instruct-abliterated-heretic-MLX-6bit")
	m = moveTo(t, m, mfSubPeriod)
	view = m.View()
	if !strings.Contains(view, "  ↑ 2 more") || !strings.Contains(view, "> Subscription period: < (none) >") || strings.Contains(view, "Provider") {
		t.Errorf("at the bottom the two fields above should be counted:\n%s", view)
	}
	m = moveTo(t, m, mfFamily)
	if view = m.View(); !strings.Contains(view, "  Model name: Qwen3.8-35B-A3B-Instruct-…") {
		t.Errorf("a field that is not being edited should show the start of its value and an ellipsis:\n%s", view)
	}

	m = keys(t, m, "esc")
	m = keys(t, selectModel(t, m, "openrouter/anthropic--claude-sonnet-4.5-thinking"), "enter")
	view = m.View()
	if !strings.Contains(flat(view), "Editopenrouter/anthropic--claude-sonnet-4.5-thinking") {
		t.Errorf("the title should wrap, not lose the end of the id:\n%s", view)
	}
	if !strings.Contains(view, "  Model name: anthropic/claude-…thinking") {
		t.Errorf("the fixed model name should keep both its ends:\n%s", view)
	}
	assertFits(t, "edit form", view, 40, 12)

	for _, c := range []struct{ cursor, n, room, first, count int }{
		{0, 10, 10, 0, 10}, {0, 10, 9, 0, 8}, {8, 10, 9, 2, 8}, {5, 10, 5, 3, 3}, {9, 10, 2, 8, 2}, {4, 10, 1, 4, 1},
	} {
		first, count, _, _ := formWindow(c.cursor, c.n, c.room)
		if first != c.first || count != c.count {
			t.Errorf("formWindow(%d, %d, %d) = fields %d..%d, want %d..%d", c.cursor, c.n, c.room, first, first+count-1, c.first, c.first+c.count-1)
		}
	}
}

// TestModelFormFamilySuggestions verifies the Family field offers the
// registry's families: right at the end of the text accepts the suggestion,
// right in the middle of the text only moves the cursor, a name that matches
// nothing is kept as typed, and an accepted suggestion replaces what was
// typed, case and all. bubbles accepts a suggestion wherever the cursor is,
// which would rewrite a family the user went back to correct, and it only
// appends the rest of the suggestion, which made "QW" into "QWen3.8": a new
// family that does not group with qwen3.8.
func TestModelFormFamilySuggestions(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := moveTo(t, keys(t, modelsEditor(t, tm, 80, 24), "n"), mfFamily)

	m = keys(t, typeText(t, m, "qw"), "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8" {
		t.Fatalf("right at the end of %q should accept the suggestion; got %q", "qw", got)
	}

	// Back to "s|o" with the cursor in the middle: right must not complete it
	// to "sonnet".
	for range len("qwen3.8") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "so"), "left", "right")
	if got := m.models.form.value(mfFamily); got != "so" {
		t.Errorf("right in the middle of the text changed it to %q", got)
	}
	m = keys(t, m, "right")
	if got := m.models.form.value(mfFamily); got != "sonnet" {
		t.Errorf("right at the end should now accept; got %q", got)
	}

	for range len("sonnet") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "brand-new"), "right")
	if got := m.models.form.value(mfFamily); got != "brand-new" {
		t.Errorf("a family that matches no suggestion should be kept as typed; got %q", got)
	}

	for range len("brand-new") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "QW"), "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8" {
		t.Errorf("an accepted suggestion should replace what was typed, case and all; got %q", got)
	}

	// Two families match "qw": ctrl+n moves to the next one, since up and
	// down are "previous field" and "next field" here.
	two := strings.Replace(tabRegistry, "id = \"omlx/Gone-4bit\"\nfamily = \"qwen3.8\"", "id = \"omlx/Gone-4bit\"\nfamily = \"qwen3\"", 1)
	m = moveTo(t, keys(t, modelsEditor(t, newTabMachine(t, two), 80, 24), "n"), mfFamily)
	m = sendKey(t, typeText(t, m, "qw"), tea.KeyMsg{Type: tea.KeyCtrlN})
	if got := keys(t, m, "right").models.form.value(mfFamily); got != "qwen3.8" {
		t.Errorf("ctrl+n then right should accept the second match, qwen3.8; got %q", got)
	}
}

// TestModelFormPrefilledFamilyDoesNotPanic is the regression test for a
// bubbles v1.0.0 fault: the accept key panics (slice bounds out of range)
// when a value was set after the suggestions matched. The edit form prefills
// Family with a value that is itself a suggestion, then the user presses
// right, shortens it and presses right again.
func TestModelFormPrefilledFamilyDoesNotPanic(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "enter")
	if m.models.form.value(mfFamily) != "qwen3.8" {
		t.Fatalf("fixture: the edit should open with the row's family, got %q", m.models.form.value(mfFamily))
	}
	m = keys(t, m, "right", "backspace", "backspace", "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8" {
		t.Errorf("after shortening and accepting, Family = %q, want qwen3.8", got)
	}
	m = keys(t, typeText(t, m, "-instruct"), "right")
	if got := m.models.form.value(mfFamily); got != "qwen3.8-instruct" {
		t.Errorf("Family = %q, want what was typed", got)
	}
}

// TestModelFormPrevSuggestionWithNoMatchDoesNotPanic is the regression test
// for a second bubbles v1.0.0 fault: ctrl+p with no family matching sets the
// suggestion index to -1, and asking for the current suggestion then indexes
// the empty match list with it. Ctrl+P in an empty Family field followed by
// right — both keys the form documents — killed the editor, and with it any
// unsaved agent edits. The index stays -1 while nothing matches, so a family
// typed after the ctrl+p must survive right too.
func TestModelFormPrevSuggestionWithNoMatchDoesNotPanic(t *testing.T) {
	prev := tea.KeyMsg{Type: tea.KeyCtrlP}
	tm := newTabMachine(t, tabRegistry)
	m := moveTo(t, keys(t, modelsEditor(t, tm, 80, 24), "n"), mfFamily)
	m = keys(t, sendKey(t, m, prev), "right")
	if got := m.models.form.value(mfFamily); got != "" {
		t.Errorf("ctrl+p then right in an empty Family field should leave it empty; got %q", got)
	}
	m = keys(t, typeText(t, m, "brand-new"), "right")
	if got := m.models.form.value(mfFamily); got != "brand-new" {
		t.Errorf("a family that matches nothing should be kept as typed; got %q", got)
	}
	// With text that matches nothing when ctrl+p is pressed.
	m = keys(t, sendKey(t, m, prev), "right")
	if got := m.models.form.value(mfFamily); got != "brand-new" {
		t.Errorf("ctrl+p then right with no match should change nothing; got %q", got)
	}

	// The edit form, its prefilled family cleared.
	m = keys(t, selectModel(t, modelsEditor(t, newTabMachine(t, tabRegistry), 80, 24), "omlx/Gone-4bit"), "enter")
	for range len("qwen3.8") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, sendKey(t, m, prev), "right")
	if got := m.models.form.value(mfFamily); got != "" {
		t.Errorf("edit form: ctrl+p then right in a cleared Family field should leave it empty; got %q", got)
	}
	m = keys(t, typeText(t, m, "brand-new"), "right")
	if got := m.models.form.value(mfFamily); got != "brand-new" {
		t.Errorf("edit form: a family that matches nothing should be kept as typed; got %q", got)
	}
	// A match is still accepted afterwards.
	for range len("brand-new") {
		m = keys(t, m, "backspace")
	}
	m = keys(t, typeText(t, m, "so"), "right")
	if got := m.models.form.value(mfFamily); got != "sonnet" {
		t.Errorf("edit form: a suggestion should still be accepted after the ctrl+p; got %q", got)
	}
}

// TestModelFormKeysAreTextNotCommands verifies that inside the form q, d, n
// and r are characters, esc closes the form without writing, and tab moves
// between fields instead of switching tabs. A form that quit on q could not
// take a model named "qwen".
func TestModelFormKeysAreTextNotCommands(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := moveTo(t, keys(t, modelsEditor(t, tm, 80, 24), "n"), mfName)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = next.(*model)
	if cmd != nil && isQuit(cmd) {
		t.Fatal("q in a text field quit the editor")
	}
	m = typeText(t, m, "dnr")
	if got := m.models.form.value(mfName); got != "qdnr" {
		t.Errorf("Model name = %q, want the four characters typed", got)
	}
	m = keys(t, m, "tab")
	if m.tab != TabModels || m.models.form.cursor != mfFamily {
		t.Errorf("tab should move to the next field; tab = %d, cursor = %d", m.tab, m.models.form.cursor)
	}
	// Up from the first field wraps to the last, and down from there back.
	m = keys(t, moveTo(t, m, mfProvider), "up")
	if m.models.form.cursor != mfSubPeriod {
		t.Errorf("up from the first field should wrap to the last; cursor = %d", m.models.form.cursor)
	}
	m = keys(t, m, "esc")
	if m.models.phase != modelsList || m.models.form != nil || tm.text(t) != tabRegistry || m.registryChanged {
		t.Error("esc must close the form and write nothing")
	}
}

func isQuit(cmd tea.Cmd) bool {
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestModelFormOnAnEmptyRegistry verifies the form works before any
// registry exists: the provider choices are the ones wt can seed, and the
// first save creates the file with the model and its provider row.
func TestModelFormOnAnEmptyRegistry(t *testing.T) {
	tm := newTabMachine(t, "")
	deps := tm.deps()
	deps.Capabilities = func(*config.Config, string) (map[string]any, error) { return nil, errors.New("ollama is not running") }
	m := modelsEditor(t, tm, 80, 24)
	m.opts.Models = deps
	m = keys(t, m, "n")
	if got := strings.Join(m.models.form.providers, ","); got != "mtplx,ollama,omlx,openrouter" {
		t.Fatalf("provider choices on an empty registry = %s", got)
	}
	m = keys(t, m, "right") // ollama -> omlx
	m = typeText(t, moveTo(t, m, mfName), "Qwen3.8-4B-4bit")
	m = keys(t, typeText(t, moveTo(t, m, mfFamily), "qwen3.8"), "ctrl+s")
	got := tm.text(t)
	if !strings.Contains(got, "[[providers]]\nid = \"omlx\"") || !strings.Contains(got, `id = "omlx/Qwen3.8-4B-4bit"`) {
		t.Errorf("the first save should create the registry with the provider row and the model:\n%s", got)
	}
	if m.models.phase != modelsList || !m.registryChanged {
		t.Error("the form should close and the change be recorded")
	}
}

// TestKeysDuringTheProbeAfterASaveSayWhy verifies n and enter typed while
// the probe that follows a save is still out are answered with "probing
// providers..." under the save's own line, once, and that the line goes when
// the probe lands while "added ..." stays. The form must not open over rows
// that are about to be replaced, but a key that silently does nothing right
// after a save reads as a tab that has hung.
func TestKeysDuringTheProbeAfterASaveSayWhy(t *testing.T) {
	tm := newTabMachine(t, tabRegistry)
	m := keys(t, modelsEditor(t, tm, 80, 24), "n", "right", "right")
	m = typeText(t, moveTo(t, m, mfName), "qwen/qwen3.8-27b")
	m = typeText(t, moveTo(t, m, mfFamily), "qwen3.8")
	next, save := m.Update(keyMsg("ctrl+s"))
	next, probe := next.(*model).Update(save())
	m = next.(*model)
	if probe == nil || !m.models.busy || m.models.phase != modelsList {
		t.Fatalf("a save should go back to the table with a probe out: busy = %v, phase = %d", m.models.busy, m.models.phase)
	}
	for _, k := range []string{"n", "enter", "n"} {
		next, cmd := m.Update(keyMsg(k))
		m = next.(*model)
		if cmd != nil || m.models.phase != modelsList || m.models.form != nil {
			t.Fatalf("%s while the probe is out should not open the form", k)
		}
	}
	view := m.View()
	if !strings.Contains(view, "added openrouter/qwen--qwen3.8-27b") || strings.Count(view, modelsProbing) != 1 {
		t.Errorf("want the save's line and one probing line:\n%s", view)
	}
	assertFits(t, "probing after a save", view, 80, 24)
	m = send(t, m, probe())
	view = m.View()
	if strings.Contains(view, modelsProbing) || !strings.Contains(view, "added openrouter/qwen--qwen3.8-27b") {
		t.Errorf("once the probe is in, its line goes and the save's stays:\n%s", view)
	}
	if m = keys(t, m, "n"); m.models.form == nil {
		t.Error("n after the probe should open the form")
	}
}

// selectRowOf puts the cursor on the row with id under provider: the way to
// name one of two rows that share an id.
func selectRowOf(t *testing.T, m *model, id, provider string) *model {
	t.Helper()
	for i, it := range m.models.list.VisibleItems() {
		if r := it.(modelRow).row; r.ID == id && r.ProviderID == provider {
			m.models.list.Select(i)
			return send(t, m, tea.WindowSizeMsg{Width: m.width, Height: m.height})
		}
	}
	t.Fatalf("no row %q under provider %q", id, provider)
	return m
}

// TestModelFormRefusesAnEditOfADuplicatedID verifies what the form does with
// a row whose id the registry holds twice, for each of the two rows and at
// every size wt supports: it opens with the values of the row under the
// cursor, not the first row's; a save is refused with the registry writer's
// own message, unchanged — both providers and the file to fix; the registry
// is byte-identical and no route sync is owed; and the form is still open
// with what was typed, the cursor where it was. Patching "the first row with
// the id" would change a model the user was not looking at. Where the refusal
// is too long to share a short terminal with the fields it has the screen to
// itself, and the next key brings the form back without typing into it.
func TestModelFormRefusesAnEditOfADuplicatedID(t *testing.T) {
	// The short id, and a 72-column one: its refusal is the one too long for
	// a 12-line terminal.
	type row struct{ id, provider string }
	cases := map[row]string{}
	for _, name := range []string{"Gone-4bit", strings.TrimPrefix(tabLongID, "omlx/")} {
		cases[row{"omlx/" + name, "omlx"}] = name
		cases[row{"omlx/" + name, "openrouter"}] = "qwen/gone"
	}
	alone := 0
	for _, size := range tabSizes {
		for c, name := range cases {
			id, provider := c.id, c.provider
			registry := strings.ReplaceAll(tabDuplicatedRegistry, "Gone-4bit", strings.TrimPrefix(id, "omlx/"))
			tm := newTabMachine(t, registry)
			m := keys(t, selectRowOf(t, modelsEditor(t, tm, size[0], size[1]), id, provider), "enter")
			at := func(format string, args ...any) {
				t.Helper()
				t.Errorf("the %s row of %s at %dx%d: %s:\n%s", provider, id, size[0], size[1], fmt.Sprintf(format, args...), m.View())
			}
			f := m.models.form
			if f == nil || f.mode != modelFormEdit || f.value(mfProvider) != provider || f.value(mfName) != name {
				t.Fatalf("the %s row of %s at %dx%d: enter should open the edit form of that row:\n%s", provider, id, size[0], size[1], m.View())
			}
			m = keys(t, typeText(t, moveTo(t, m, mfTags), "code"), "ctrl+s")
			refusal := `model "` + id + `" is in the registry twice (providers omlx, openrouter); wt cannot tell which one you mean — fix the entry in ` + tm.registry
			if f.err != refusal {
				at("the form's error = %q, want the writer's refusal unchanged: %q", f.err, refusal)
			}
			view := m.View()
			assertMessageShown(t, fmt.Sprintf("refused edit of the %s row of %s", provider, id), view, refusal, size[0], size[1])
			assertCutFieldsAreCounted(t, "refused edit of a duplicated id", m, view)
			if got := tm.text(t); got != registry {
				at("the registry changed:\n%s", got)
			}
			if m.registryChanged || m.models.writing || m.models.phase != modelsForm || f.cursor != mfTags || f.value(mfTags) != "code" {
				at("registryChanged = %v, writing = %v, phase = %d, cursor = %d, tags = %q; want the form open as it was and no sync owed",
					m.registryChanged, m.models.writing, m.models.phase, f.cursor, f.value(mfTags))
			}
			if m.modelFormLayout().errorAlone {
				alone++
				if !strings.Contains(view, modelFormErrorHint) {
					at("an error with the screen to itself should say how to get the form back")
				}
				// The key is not typed into the field nobody can see.
				m = keys(t, m, "x")
				if f.err != "" || f.value(mfTags) != "code" || !strings.Contains(m.View(), "> Tags:") {
					at("a key should bring the form back and do nothing else; tags = %q", f.value(mfTags))
				}
				assertFits(t, "the form after the refusal", m.View(), size[0], size[1])
				assertCutFieldsAreCounted(t, "the form after the refusal", m, m.View())
			} else if !strings.Contains(view, "> Tags:") {
				at("the field being edited should still be on screen beside the refusal")
			}
			m = keys(t, m, "esc")
			if view := m.View(); m.models.phase != modelsList || strings.Contains(view, routesPending) || !strings.Contains(view, "MODEL") {
				at("esc should go back to the table with no routes pending")
			}
		}
	}
	if alone == 0 {
		t.Error("fixture: no case had a refusal too long for its terminal, so the way back from one was not tested")
	}
}

// TestModelFormRefusesAnEditOfARowWithAMalformedFetch verifies an edit of a
// row whose fetch is not a table is refused by the registry writer, which
// names the key, and that the form shows it as the save's error: the cursor
// stays on the field the user was editing (Tags, which is not what is wrong),
// nothing is written and no route sync is owed. wt reads such a fetch as
// absent, so the form opens; writing the row back would bless a file
// llmbench's loader crashes on, and an error pinned on the Tags field would
// send the user to fix the wrong thing. The repair is in the file.
func TestModelFormRefusesAnEditOfARowWithAMalformedFetch(t *testing.T) {
	for _, size := range tabSizes {
		tm := newTabMachine(t, tabMalformedRegistry)
		m := keys(t, selectModel(t, modelsEditor(t, tm, size[0], size[1]), "omlx/Gone-4bit"), "enter")
		m = keys(t, typeText(t, moveTo(t, m, mfTags), "code"), "ctrl+s")
		f := m.models.form
		view := m.View()
		at := func(format string, args ...any) {
			t.Helper()
			t.Errorf("at %dx%d: %s:\n%s", size[0], size[1], fmt.Sprintf(format, args...), view)
		}
		if m.models.phase != modelsForm || f == nil {
			t.Fatalf("at %dx%d: a refused save should leave the form open:\n%s", size[0], size[1], view)
		}
		want := `invalid registry entry: model "omlx/Gone-4bit": fetch must be a table (fix the entry in ` + tm.registry + ")"
		if f.err != want {
			at("the form's error = %q, want the writer's refusal, which names the row and the key, and the file to fix: %q", f.err, want)
		}
		// Whole where it fits; where the registry's path makes it taller
		// than the terminal, cut in the middle behind a marker (#318).
		assertMessageShown(t, "refused edit of a malformed row", view, want, size[0], size[1])
		assertCutFieldsAreCounted(t, "refused edit of a malformed row", m, view)
		if m.modelFormLayout().errorAlone {
			// Too long to share a short terminal with at least three rows of
			// the form: it has the screen, and a key brings the form back.
			m = keys(t, m, "x")
			view = m.View()
		}
		assertFits(t, "the form of a malformed row", view, size[0], size[1])
		assertCutFieldsAreCounted(t, "the form of a malformed row", m, view)
		if f.cursor != mfTags || f.value(mfTags) != "code" || !strings.Contains(view, "> Tags:") {
			at("cursor = %d (%s), tags = %q; a refusal that names no form field moves nothing", f.cursor, modelFormLabels[f.cursor], f.value(mfTags))
		}
		if tm.text(t) != tabMalformedRegistry || m.registryChanged || strings.Contains(view, routesPending) {
			at("registryChanged = %v; a refused edit writes nothing and owes no sync", m.registryChanged)
		}
	}
}

// TestModelFormRefusalTheFormCanRepairNamesNoFile verifies a registry
// refusal the form itself can repair is shown as it is, without "(fix the
// entry in <registry>)": a row written by hand with no family is refused when
// its tags are edited, and typing a family in the same form saves it. The
// hint would send the user to a hand edit of the file for something the field
// two rows up fixes.
func TestModelFormRefusalTheFormCanRepairNamesNoFile(t *testing.T) {
	registry := strings.Replace(tabRegistry, "id = \"omlx/Gone-4bit\"\nfamily = \"qwen3.8\"\n", "id = \"omlx/Gone-4bit\"\n", 1)
	if registry == tabRegistry {
		t.Fatal("fixture: the family key was not removed")
	}
	tm := newTabMachine(t, registry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "omlx/Gone-4bit"), "enter")
	m = keys(t, typeText(t, moveTo(t, m, mfTags), "code"), "ctrl+s")
	f := m.models.form
	if m.models.phase != modelsForm || f == nil {
		t.Fatalf("a refused save should leave the form open:\n%s", m.View())
	}
	if want := `invalid registry entry: model "omlx/Gone-4bit": family is required`; f.err != want {
		t.Errorf("the form's error = %q, want the refusal with no file to fix: %q", f.err, want)
	}
	if tm.text(t) != registry || m.registryChanged {
		t.Error("a refused save must write nothing")
	}
	m = keys(t, typeText(t, moveTo(t, m, mfFamily), "qwen3.8"), "ctrl+s")
	if m.models.phase != modelsList || !strings.Contains(tm.text(t), "id = \"omlx/Gone-4bit\"\nfamily = \"qwen3.8\"\n") || !strings.Contains(m.View(), "saved omlx/Gone-4bit") {
		t.Errorf("a family typed in the same form should save the row:\n%s\n%s", tm.text(t), m.View())
	}
}

// TestAHeldQuitAfterAFormSaveIsAnOrdinaryQuit verifies the two things a quit
// held for a form's save owes the Agents tab, as a quit held for a removal
// does. Agent edits made before the save and not yet asked about get the
// unsaved-changes prompt when the save lands — with the model saved, the
// change recorded and the table read again under the prompt — instead of
// being thrown away unasked. And when the user answered "discard and quit"
// and the save is then refused, the quit is off and those edits are unsaved
// again: the next quit asks.
func TestAHeldQuitAfterAFormSaveIsAnOrdinaryQuit(t *testing.T) {
	saving := func(t *testing.T, price string) (*tabMachine, *model, tea.Cmd) {
		tm := newTabMachine(t, tabRegistry)
		m := keys(t, modelsEditor(t, tm, 80, 24), "n", "right", "right")
		m = typeText(t, moveTo(t, m, mfName), "qwen/qwen3.8-27b")
		m = typeText(t, moveTo(t, m, mfFamily), "qwen3.8")
		m = typeText(t, moveTo(t, m, mfInput), price)
		next, save := m.Update(keyMsg("ctrl+s"))
		if save == nil {
			t.Fatal("ctrl+s should hand the save to a command")
		}
		return tm, next.(*model), save
	}
	t.Run("agent edits not yet asked about", func(t *testing.T) {
		tm, m, save := saving(t, "0.5")
		next, _ := m.Update(keyMsg("ctrl+c"))
		m = next.(*model)
		// An edit the quit did not know of when it was taken.
		m.dirty = true
		next, cmd := m.Update(save())
		m = next.(*model)
		if m.phase != phaseQuit || m.quitPending || !m.registryChanged || !strings.Contains(m.View(), "unsaved agent changes") {
			t.Fatalf("phase = %d, quitPending = %v, registryChanged = %v; want the unsaved-changes prompt and the change recorded:\n%s", m.phase, m.quitPending, m.registryChanged, m.View())
		}
		if cmd == nil || isQuit(cmd) {
			t.Fatal("the command should be the re-probe, for a user who cancels the prompt; not a quit")
		}
		m = keys(t, send(t, m, cmd()), "c")
		if r, ok := m.selectedModel(); !ok || r.ID != "openrouter/qwen--qwen3.8-27b" || m.models.phase != modelsList || !strings.Contains(tm.text(t), `id = "openrouter/qwen--qwen3.8-27b"`) {
			t.Errorf("after c the table should be back with the cursor on the saved model; on %q", r.ID)
		}
	})
	t.Run("a refused save keeps the edits a held quit was to discard", func(t *testing.T) {
		tm, m, save := saving(t, "cheap")
		m.dirty = true
		m = keys(t, m, "ctrl+c")
		if m.phase != phaseQuit {
			t.Fatalf("phase = %d, want the unsaved-changes prompt", m.phase)
		}
		next, cmd := m.Update(keyMsg("n"))
		if m = next.(*model); cmd != nil || !m.quitPending || !m.discardHeld {
			t.Fatal("n on the prompt with the save in flight should hold the quit back")
		}
		next, cmd = m.Update(save())
		m = next.(*model)
		if cmd != nil || m.quitPending || m.discardHeld || !m.dirty || m.tab != TabModels || m.models.phase != modelsForm {
			t.Fatalf("quitPending = %v, discardHeld = %v, dirty = %v; the quit is off, the form is back and the edits are unsaved again", m.quitPending, m.discardHeld, m.dirty)
		}
		if !strings.Contains(m.View(), "input-price must be a number") || tm.text(t) != tabRegistry {
			t.Errorf("the refusal should be on the form, and nothing written:\n%s", m.View())
		}
		if m = keys(t, m, "ctrl+c"); m.phase != phaseQuit {
			t.Errorf("phase = %d, want the next quit to ask about the edits", m.phase)
		}
	})
}

// TestModelFormEditsTheFlatPriceOfATimePricedModel pins what the model form
// shows for a model with cost.time_prices rows: the model's own (flat)
// prices, which are the keys the form edits, and never the price in force
// now. The picker shows the price in force (#322); if the form did too, a
// save made during a cheap window would write that window's price over the
// model's own, which is the price LiteLLM's route carries. The row here is
// in force at every instant, so the test does not depend on the clock.
func TestModelFormEditsTheFlatPriceOfATimePricedModel(t *testing.T) {
	registry := tabRegistry + `
[[models]]
id = "openrouter/timed"
family = "timed"
provider_id = "openrouter"
model_name = "vendor/timed"
tags = []

[models.cost]
input_price_per_million = 1.32
cache_price_per_million = 0.044
output_price_per_million = 3.96

[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.98

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`
	tm := newTabMachine(t, registry)
	m := keys(t, selectModel(t, modelsEditor(t, tm, 80, 24), "openrouter/timed"), "enter")
	f := m.models.form
	if f == nil || f.mode != modelFormEdit {
		t.Fatalf("enter did not open the edit form:\n%s", m.View())
	}
	for field, want := range map[int]string{mfInput: "1.32", mfCache: "0.044", mfOutput: "3.96"} {
		if got := f.value(field); got != want {
			t.Errorf("%s opens as %q, want the model's own price %q", modelFormLabels[field], got, want)
		}
	}
	m = keys(t, m, "ctrl+s")
	if tm.text(t) != registry || !strings.Contains(m.View(), "no change to openrouter/timed") {
		t.Errorf("saving the untouched form changed the registry or did not say it wrote nothing:\n%s", m.View())
	}
}
