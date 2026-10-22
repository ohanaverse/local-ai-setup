package configeditor

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// pairingRegistry is tabRegistry plus an mlx_lm_server provider and one
// target+draft pairing, as `wt model add mlx_lm_server … --draft …` writes it
// (provider rows before model rows, the order wt writes).
var pairingRegistry = strings.Replace(tabRegistry, "[[models]]", `[[providers]]
id = "mlx_lm_server"
name = "mlx-lm server (target+draft)"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8001/v1"

[[models]]`, 1) + `
[[models]]
id = "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"
family = "qwen3.8"
provider_id = "mlx_lm_server"
model_name = "Qwen3.8-27B+draft-Qwen3.8-4B"
tags = []

[models.fetch]
repo = "mlx-community/Qwen3.8-27B"

[models.draft]
local_path = "~/models/Qwen3.8-4B"
`

// TestModelsTabShowsAndEditsAPairing verifies an mlx_lm_server pairing is a
// row of the Models tab (status "-": it cannot be enumerated), that its
// target and draft are on the detail line, that its metadata is edited like
// any model's — provider and name fixed, fetch and draft left alone — and
// that the add form does not offer mlx_lm_server: a pairing needs two
// artifacts, which the command line takes and the form does not. An edit
// that rewrote fetch or draft would change what llmbench starts.
func TestModelsTabShowsAndEditsAPairing(t *testing.T) {
	const id = "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"
	tm := newTabMachine(t, pairingRegistry)
	m := selectModel(t, modelsEditor(t, tm, 120, 24), id)
	r, _ := m.selectedModel()
	if !r.Pairing() || string(r.Status) != "-" {
		t.Fatalf("the pairing row = %+v, want a pairing with status -", r)
	}
	if view := m.View(); !strings.Contains(view, "target mlx-community/Qwen3.8-27B · draft ~/models/Qwen3.8-4B") {
		t.Errorf("the detail line should name the target and the draft:\n%s", view)
	}

	m = keys(t, m, "enter")
	f := m.models.form
	if f == nil || f.mode != modelFormEdit || f.value(mfProvider) != "mlx_lm_server" || f.value(mfName) != "Qwen3.8-27B+draft-Qwen3.8-4B" {
		t.Fatal("enter on a pairing should open the edit form on its provider and its name")
	}
	if f.editable(mfProvider) || f.editable(mfName) {
		t.Error("a pairing's provider and name are fixed in the form: its two sides are what it is")
	}
	m = keys(t, typeText(t, moveTo(t, m, mfTags), "code"), "ctrl+s")
	want := strings.Replace(pairingRegistry,
		"model_name = \"Qwen3.8-27B+draft-Qwen3.8-4B\"\ntags = []\n",
		"model_name = \"Qwen3.8-27B+draft-Qwen3.8-4B\"\ntags = [\n    \"code\",\n]\n", 1)
	if got := tm.text(t); got != want {
		t.Errorf("registry after editing the pairing's tags =\n%s\nwant only the tags changed", got)
	}

	m = keys(t, m, "n")
	if got := strings.Join(m.models.form.providers, ","); strings.Contains(got, "mlx_lm_server") {
		t.Errorf("the add form offers %s; mlx_lm_server must not be among them", got)
	}
	// The form says where the missing provider is added, while the Provider
	// choice is the field in hand, and stops saying it on the next field.
	const hint = "a pairing: wt model add mlx_lm_server <target> --draft <draft>"
	if view := m.View(); m.models.form.cursor != mfProvider || !strings.Contains(view, hint) {
		t.Errorf("the add form opens on Provider and should name the command that adds a pairing:\n%s", view)
	}
	if view := keys(t, m, "down").View(); strings.Contains(view, hint) {
		t.Errorf("the pairing line should go when the cursor leaves Provider:\n%s", view)
	}
}

// TestAddFormPairingLineFitsTheTerminal verifies the line that names `wt
// model add mlx_lm_server` costs the add form nothing it needs: at every
// layout size the form is inside the terminal with the line on it — the
// short spelling at 40 columns — and on a terminal so short that the line
// would leave the fields fewer than three rows it is the line that goes, not
// the fields or the markers that count the ones off screen.
func TestAddFormPairingLineFitsTheTerminal(t *testing.T) {
	tm := newTabMachine(t, pairingRegistry)
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			m := keys(t, modelsEditor(t, tm, width, height), "n")
			view := m.View()
			if lipgloss.Height(view) > height || lipgloss.Width(view) > width {
				t.Errorf("%dx%d: the add form is %dx%d", width, height, lipgloss.Width(view), lipgloss.Height(view))
			}
			want := modelFormPairingHints[0]
			if width == 40 {
				want = modelFormPairingHints[1]
			}
			if !strings.Contains(view, want) || !strings.Contains(view, "Provider") {
				t.Errorf("%dx%d: want %q under the Provider field:\n%s", width, height, want, view)
			}
		}
	}
	m := keys(t, modelsEditor(t, tm, 80, 6), "n")
	view := m.View()
	if strings.Contains(view, "wt model add mlx_lm_server") || !strings.Contains(view, "Provider") || !strings.Contains(view, "more") || lipgloss.Height(view) > 6 {
		t.Errorf("80x6: want the fields and their marker, without the pairing line:\n%s", view)
	}
}

// pairingLongRegistry holds one pairing with sides as long as real ones (a
// Hugging Face repo and a directory under the home), and one whose draft a
// hand edit left malformed.
var pairingLongRegistry = strings.Replace(pairingRegistry, `
[[models]]
id = "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"`, `
[[models]]
id = "mlx_lm_server/Qwen3.8-35B-A3B-Instruct-MLX-6bit+draft-Qwen3.8-4B-Instruct-MLX-4bit"
family = "qwen3.8"
provider_id = "mlx_lm_server"
model_name = "Qwen3.8-35B-A3B-Instruct-MLX-6bit+draft-Qwen3.8-4B-Instruct-MLX-4bit"
tags = ["code", "design"]

[models.fetch]
repo = "mlx-community/Qwen3.8-35B-A3B-Instruct-MLX-6bit"

[models.draft]
local_path = "~/models/quantized/Qwen3.8-4B-Instruct-MLX-4bit"

[[models]]
id = "mlx_lm_server/T+draft-D"
family = "qwen3.8"
provider_id = "mlx_lm_server"
model_name = "T+draft-D"
tags = []
draft = 7

[models.fetch]
repo = "org/T"

[[models]]
id = "mlx_lm_server/Qwen3.8-27B+draft-Qwen3.8-4B"`, 1)

// TestPairingRowsFitTheTerminal measures the Models tab with a pairing row
// selected, and the edit form opened on it, at every size wt supports. The
// row's detail must name the id whole, then the target and the draft whole
// (they are in no table column, and the start command is built from them),
// everywhere but at 40x12, where the two sides are what is given up so that
// the id still is; the form must
// stay inside the terminal with the cursor's field on screen. A pairing whose
// draft is malformed is still a row with status "-", and its detail says
// what is malformed. A pairing's id and sides are the longest text the tab
// shows, so this is the row that would push a 40-column screen out first.
func TestPairingRowsFitTheTerminal(t *testing.T) {
	const longID = "mlx_lm_server/Qwen3.8-35B-A3B-Instruct-MLX-6bit+draft-Qwen3.8-4B-Instruct-MLX-4bit"
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{12, 24, 50} {
			tm := newTabMachine(t, pairingLongRegistry)
			m := selectModel(t, modelsEditor(t, tm, width, height), longID)
			r, _ := m.selectedModel()
			if string(r.Status) != "-" || r.Target != "mlx-community/Qwen3.8-35B-A3B-Instruct-MLX-6bit" || r.Draft != "~/models/quantized/Qwen3.8-4B-Instruct-MLX-4bit" {
				t.Fatalf("the pairing row = %+v", r)
			}
			view := m.View()
			assertFits(t, "pairing row", view, width, height)
			if !strings.Contains(flat(view), flat(longID+" · -")) {
				t.Errorf("pairing row at %dx%d: the id and its status should be on screen whole:\n%s", width, height, view)
			}
			for _, side := range []string{"target mlx-community/Qwen3.8-35B-A3B-Instruct-MLX-6bit", "draft ~/models/quantized/Qwen3.8-4B-Instruct-MLX-4bit"} {
				// At 40x12 the two sides alone are four lines: they are what
				// the tab gives up to keep the id and a table row.
				if (width > 40 || height > 12) && !strings.Contains(flat(view), flat(side)) {
					t.Errorf("pairing row at %dx%d: %q should be on screen whole:\n%s", width, height, side, view)
				}
			}

			bad := selectModel(t, m, "mlx_lm_server/T+draft-D")
			r, _ = bad.selectedModel()
			if string(r.Status) != "-" || r.Target != "org/T" || r.Draft != "" {
				t.Fatalf("the pairing with a malformed draft = %+v, want it listed with its target and no draft", r)
			}
			view = bad.View()
			assertFits(t, "pairing with a malformed draft", view, width, height)
			if !strings.Contains(flat(view), flat("target org/T")) || !strings.Contains(flat(view), flat("draft -")) || !strings.Contains(flat(view), flat("draft is not a table; read as absent")) {
				t.Errorf("pairing with a malformed draft at %dx%d: the detail should say the draft is absent and why:\n%s", width, height, view)
			}

			m = keys(t, selectModel(t, m, longID), "enter")
			if m.models.phase != modelsForm || m.models.form.mode != modelFormEdit {
				t.Fatalf("at %dx%d the edit form did not open on the pairing", width, height)
			}
			for field := 0; field < mfCount; field++ {
				if !m.models.form.editable(field) {
					continue
				}
				m = moveTo(t, m, field)
				name := "edit form of a pairing on " + modelFormLabels[field]
				view := m.View()
				assertFits(t, name, view, width, height)
				assertCutFieldsAreCounted(t, name, m, view)
				if !strings.Contains(view, "[Models]") || !strings.Contains(flat(view), flat("Edit "+longID)) {
					t.Errorf("%s at %dx%d: the tab bar and the whole title should be on screen:\n%s", name, width, height, view)
				}
				if !strings.Contains(view, "> "+modelFormLabels[field]+":") {
					t.Errorf("%s at %dx%d: the focused field is not on screen:\n%s", name, width, height, view)
				}
			}
		}
	}
}
