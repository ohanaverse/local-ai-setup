package config

import (
	"errors"
	"testing"
)

// dupRow is a second row under docRegistry's id "ollama/alpha", held by
// another provider: the registry Config.Validate refuses with "duplicate
// model id", which `wt model edit` and `wt model rm` are still let into so
// that it can be repaired.
const dupRow = `
[[models]]
id = "ollama/alpha"
family = "fam"
provider_id = "omlx"
model_name = "Alpha-4bit"
`

// tripleRow is a third row under the same id, with no provider_id at all.
const tripleRow = `
[[models]]
id = "ollama/alpha"
family = "fam"
model_name = "alpha-three"
`

// ambiguousOps is every RegistryDoc operation that finds a model row by its
// id, each called on the id "ollama/alpha".
var ambiguousOps = []struct {
	name string
	run  func(d *RegistryDoc) error
}{
	{"PatchModel", func(d *RegistryDoc) error {
		return d.PatchModel("ollama/alpha", map[string]any{"family": "renamed"}, []string{"tags"})
	}},
	{"RemoveModel", func(d *RegistryDoc) error {
		_, err := d.RemoveModel("ollama/alpha")
		return err
	}},
	{"CloneModel", func(d *RegistryDoc) error {
		return d.CloneModel("ollama/alpha", map[string]any{"id": "ollama/alpha:cloud"})
	}},
	{"SetTimePrices", func(d *RegistryDoc) error {
		return d.SetTimePrices("ollama/alpha", []map[string]any{{"label": "night", "timezone": "UTC"}})
	}},
	{"SetTimePrices with no rows", func(d *RegistryDoc) error {
		return d.SetTimePrices("ollama/alpha", nil)
	}},
	{"Model", func(d *RegistryDoc) error {
		_, err := d.Model("ollama/alpha")
		return err
	}},
}

// TestOperationsRefuseAnIDTheRegistryHoldsTwice verifies every operation that
// addresses a model row by its id refuses an id two rows carry, changes
// nothing, and says which providers hold the rows, in file order. Before
// this each of them took the first row: on the one registry where the user
// most needs to choose — two rows under one id is what `wt model edit` and
// `wt model rm` are let in to repair — an edit or a removal landed on
// whichever row came first, and the row of the other provider could not be
// reached at all.
func TestOperationsRefuseAnIDTheRegistryHoldsTwice(t *testing.T) {
	t.Setenv("WT_REGISTRY", "/scratch/registry.toml")
	for _, tc := range []struct {
		name, text, want string
	}{
		{
			"two rows", docRegistry + dupRow,
			`model "ollama/alpha" is in the registry twice (providers ollama, omlx); wt cannot tell which one you mean — fix the entry in /scratch/registry.toml`,
		},
		{
			"three rows", docRegistry + dupRow + tripleRow,
			`model "ollama/alpha" is in the registry 3 times (providers ollama, omlx, (none)); wt cannot tell which one you mean — fix the entry in /scratch/registry.toml`,
		},
	} {
		for _, op := range ambiguousOps {
			t.Run(tc.name+"/"+op.name, func(t *testing.T) {
				doc := parseDoc(t, tc.text)
				err := op.run(doc)
				if !errors.Is(err, ErrModelAmbiguous) {
					t.Fatalf("err = %v, want ErrModelAmbiguous", err)
				}
				if errors.Is(err, ErrModelNotFound) || errors.Is(err, ErrModelExists) {
					t.Errorf("err = %v also matches another registry error", err)
				}
				if err.Error() != tc.want {
					t.Errorf("message:\n got %s\nwant %s", err, tc.want)
				}
				if got := docText(t, doc); got != tc.text {
					t.Errorf("a refused operation changed the document:\n%s", got)
				}
				if len(doc.touchedModels) != 0 {
					t.Errorf("a refused operation marked %d row(s) as touched", len(doc.touchedModels))
				}
			})
		}
	}
}

// TestADuplicatedIDLeavesTheOtherRowsReachable verifies the refusal is about
// the duplicated id alone: a row whose id is its own is still patched and
// removed on the same document, and a clone whose NEW id is the duplicated
// one, like an add of it, is still "already exists". The commands that repair
// a registry must keep working on the rows that are not the problem, and an
// add must not answer "wt cannot tell which one you mean" about a row it is
// not looking for.
func TestADuplicatedIDLeavesTheOtherRowsReachable(t *testing.T) {
	doc := parseDoc(t, docRegistry+dupRow)
	if err := doc.AddModel(map[string]any{"id": "ollama/alpha", "provider_id": "ollama", "model_name": "x"}); !errors.Is(err, ErrModelExists) || errors.Is(err, ErrModelAmbiguous) {
		t.Errorf("adding the duplicated id: err = %v, want ErrModelExists", err)
	}
	if err := doc.CloneModel("ollama/beta", map[string]any{"id": "ollama/alpha"}); !errors.Is(err, ErrModelExists) || errors.Is(err, ErrModelAmbiguous) {
		t.Errorf("cloning onto the duplicated id: err = %v, want ErrModelExists", err)
	}
	if row, err := doc.Model("ollama/beta"); err != nil || rowID(row) != "ollama/beta" {
		t.Errorf("Model of a row with its own id = %v, %v", row, err)
	}
	if _, err := doc.Model("ollama/nope"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("Model of an unknown id: err = %v, want ErrModelNotFound", err)
	}
	if err := doc.PatchModel("ollama/beta", map[string]any{"family": "other"}, nil); err != nil {
		t.Errorf("patching a row with its own id: %v", err)
	}
	if _, err := doc.RemoveModel("ollama/beta"); err != nil {
		t.Errorf("removing a row with its own id: %v", err)
	}
}

// TestModelReturnsACopy verifies the row Model hands back is a copy: a
// caller reads it to decide a patch (modeladmin.Edit reads the cost table),
// and a change made through it must not reach the document unvalidated.
func TestModelReturnsACopy(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row, err := doc.Model("ollama/alpha")
	if err != nil {
		t.Fatal(err)
	}
	row.Set("family", "changed")
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("a change to the returned row reached the document:\n%s", got)
	}
}

// TestCheckModelRow verifies the same three answers from a loaded Config:
// nil for an id one row has, ErrModelNotFound for none, and ErrModelAmbiguous
// — in the writer's own words — for two. `wt model rm` asks it before its
// confirmation question, so a user is not asked "remove these?" about an id
// the write is going to refuse.
func TestCheckModelRow(t *testing.T) {
	t.Setenv("WT_REGISTRY", "/scratch/registry.toml")
	cfg := &Config{Models: []Model{
		{ID: "dup/x", ProviderID: "omlx-6bit"},
		{ID: "one/x", ProviderID: "ollama"},
		{ID: "dup/x", ProviderID: "omlx"},
	}}
	if err := cfg.CheckModelRow("one/x"); err != nil {
		t.Errorf("an id one row has: %v", err)
	}
	if err := cfg.CheckModelRow("no/x"); !errors.Is(err, ErrModelNotFound) || err.Error() != `model not found: "no/x"` {
		t.Errorf("an unknown id: err = %v", err)
	}
	err := cfg.CheckModelRow("dup/x")
	want := `model "dup/x" is in the registry twice (providers omlx-6bit, omlx); wt cannot tell which one you mean — fix the entry in /scratch/registry.toml`
	if !errors.Is(err, ErrModelAmbiguous) || err.Error() != want {
		t.Errorf("a duplicated id:\n got %v\nwant %s", err, want)
	}
}
