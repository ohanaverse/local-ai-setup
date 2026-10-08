package configeditor

import (
	"strings"
	"testing"
)

// pairingRegistry is tabRegistry plus an mlx_lm_server provider and one
// target+draft pairing, as `wt model add mlx_lm_server … --draft …` writes it
// (provider rows before model rows, the order wt and modelman write).
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
// any model's with fetch and draft left alone, and that the add form does
// not offer mlx_lm_server: a pairing needs two artifacts, which the command
// line takes and the form does not.
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
	if f := m.models.form; f == nil || f.mode != modelFormEdit || f.value(mfProvider) != "mlx_lm_server" {
		t.Fatal("enter on a pairing should open the edit form")
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
}
