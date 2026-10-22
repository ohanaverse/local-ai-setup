package config

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// docRegistry is a small registry in tomli-w form with keys wt does not model
// at the model, cost and fetch levels, an integer price, and a model with no
// tags key at all.
const docRegistry = `families = [
    { name = "fam", display_name = "Fam" },
]

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[models]]
catalog_name = "alpha"
id = "ollama/alpha"
family = "fam"
provider_id = "ollama"
model_name = "alpha"
tags = [
    "code",
]

[models.cost]
x_cost_note = "kept"
input_price_per_million = 3
output_price_per_million = 15

[models.model_info]
supports_vision = false
max_input_tokens = 131072

[models.fetch]
x_fetch_note = "kept"
repo = "org/alpha"
local_path = "/models/alpha"

[[models]]
id = "ollama/beta"
family = "fam"
provider_id = "ollama"
model_name = "beta"
`

func parseDoc(t *testing.T, text string) *RegistryDoc {
	t.Helper()
	root, err := tomlw.Decode([]byte(text))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	doc, err := newRegistryDoc(root)
	if err != nil {
		t.Fatalf("newRegistryDoc: %v", err)
	}
	return doc
}

func docText(t *testing.T, doc *RegistryDoc) string {
	t.Helper()
	out, err := tomlw.Encode(doc.root)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(out)
}

// TestPatchModelChangesOnlyTheNamedKeys is the core promise of the writer: a
// patch sets and deletes the keys it names and every other line of the file,
// including keys wt does not model, comes back exactly as it was. A writer
// that rebuilt the row from wt's typed struct would drop catalog_name, fetch
// and the x_ keys here.
func TestPatchModelChangesOnlyTheNamedKeys(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.PatchModel("ollama/alpha",
		map[string]any{"family": "renamed", "cost.output_price_per_million": 12.5},
		[]string{"fetch.local_path", "model_info.supports_vision", "no_such_key", "draft.repo"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer(
		"family = \"fam\"\nprovider_id = \"ollama\"\nmodel_name = \"alpha\"", "family = \"renamed\"\nprovider_id = \"ollama\"\nmodel_name = \"alpha\"",
		"output_price_per_million = 15", "output_price_per_million = 12.5",
		"supports_vision = false\n", "",
		"local_path = \"/models/alpha\"\n", "",
	).Replace(docRegistry)
	if got := docText(t, doc); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestPatchModelPutsNewKeysAtTheirSchemaPosition pins where a key that was
// not there lands: at its schema position, with a table created on the way,
// and an unmodelled key ahead of the schema keys. The rows of a registry on
// disk already have their keys in this order, so a key placed anywhere else
// leaves the edited row laid out unlike its neighbours, and the same edit
// made on two machines could write two different files.
func TestPatchModelPutsNewKeysAtTheirSchemaPosition(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.PatchModel("ollama/beta", map[string]any{
		"tags":                          []string{"design"},
		"location":                      "local",
		"cost.output_price_per_million": 2,
		"cost.input_price_per_million":  1,
		"pricing_updated_at":            "2026-10-07T00:00:00+00:00",
		"catalog_name":                  "beta",
		"draft":                         map[string]any{"local_path": "/d", "repo": "org/d", "x_note": "n"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[models]]
catalog_name = "beta"
id = "ollama/beta"
family = "fam"
provider_id = "ollama"
model_name = "beta"
location = "local"
tags = [
    "design",
]
pricing_updated_at = "2026-10-07T00:00:00+00:00"

[models.cost]
input_price_per_million = 1
output_price_per_million = 2

[models.draft]
x_note = "n"
repo = "org/d"
local_path = "/d"
`
	got := docText(t, doc)
	if !strings.HasSuffix(got, "\n"+wantRow) {
		t.Errorf("the patched row should be:\n%s\ngot document:\n%s", wantRow, got)
	}
}

// TestPatchModelLeavesAnEqualValueAlone pins "a price key is written only
// when its value changes": asking for the value a key already has — 3.0 for
// an integer 3, the same tags, the same model_info — changes nothing and
// marks nothing as touched. Otherwise every edit of a model would rewrite
// `3` as `3.0` and reorder model_info.
func TestPatchModelLeavesAnEqualValueAlone(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.PatchModel("ollama/alpha", map[string]any{
		"cost.input_price_per_million":  3.0,
		"cost.output_price_per_million": 15.0,
		"tags":                          []string{"code"},
		"family":                        "fam",
		"model_info":                    map[string]any{"max_input_tokens": 131072, "supports_vision": false},
	}, []string{"cost.cache_price_per_million"})
	if err != nil {
		t.Fatal(err)
	}
	// A row with no tags key and no cost table stays that way: a writer that
	// went through the typed struct would add `tags = []` here.
	if err := doc.PatchModel("ollama/beta", map[string]any{"family": "fam", "model_name": "beta"}, []string{"tags", "cost.input_price_per_million"}); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("an equal-valued patch changed the document:\n%s", got)
	}
	if len(doc.touchedModels) != 0 {
		t.Errorf("an equal-valued patch touched %d row(s), want 0", len(doc.touchedModels))
	}
}

// TestPatchModelRefusals pins the errors a caller can act on: an id that is
// not there is ErrModelNotFound (the CLI's "unknown id" exit), the id itself
// cannot be patched (history, rotation and profiles key on it), and a dotted
// key cannot go through a value that is not a table.
func TestPatchModelRefusals(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	if err := doc.PatchModel("ollama/nope", map[string]any{"family": "x"}, nil); !errors.Is(err, ErrModelNotFound) || !strings.Contains(err.Error(), `"ollama/nope"`) {
		t.Errorf("unknown id: err = %v, want ErrModelNotFound naming the id", err)
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"id": "ollama/other"}, nil); err == nil || !strings.Contains(err.Error(), "id cannot be patched") {
		t.Errorf("setting the id: err = %v", err)
	}
	if err := doc.PatchModel("ollama/alpha", nil, []string{"id"}); err == nil {
		t.Error("unsetting the id should be refused")
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"family.name": "x"}, nil); err == nil || !strings.Contains(err.Error(), "family is not a table") {
		t.Errorf("a path through a string: err = %v", err)
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"cost..x": 1}, nil); err == nil {
		t.Error("an empty path component should be refused")
	}
	if err := doc.PatchModel("ollama/alpha", map[string]any{"family": uint8(1)}, nil); err == nil || !strings.Contains(err.Error(), "family") {
		t.Errorf("a value TOML cannot hold: err = %v", err)
	}
	// A patch is all or nothing: the first key (sorted order) is valid and
	// the second fails, and neither is in the document afterwards.
	if err := doc.PatchModel("ollama/beta", map[string]any{"cost.input_price_per_million": -5, "family.name": "x"}, nil); err == nil || !strings.Contains(err.Error(), "family is not a table") {
		t.Errorf("a two-key patch whose second key fails: err = %v", err)
	}
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("a refused patch changed the document:\n%s", got)
	}
}

// TestAddModelAppendsARowInSchemaOrder pins AddModel: the row goes last, its
// keys in schema order whatever order the Go map had, and a second row
// with the same id is ErrModelExists rather than a silent duplicate (which
// wt's own Validate then refuses to load).
func TestAddModelAppendsARowInSchemaOrder(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row := map[string]any{
		"tags":        []string{},
		"model_name":  "org/gamma",
		"provider_id": "ollama",
		"family":      "fam",
		"id":          "ollama/gamma",
		"source":      "discovered",
		"fetch":       map[string]any{"local_path": "/models/gamma", "repo": "org/gamma"},
		"cost": map[string]any{"time_prices": []map[string]any{{
			"windows":  []map[string]any{{"end": "24:00", "start": "00:00", "days": []string{"sat"}}},
			"timezone": "UTC", "label": "off-peak", "input_price_per_million": 0.5,
		}}},
	}
	if err := doc.AddModel(row); err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[models]]
id = "ollama/gamma"
family = "fam"
provider_id = "ollama"
model_name = "org/gamma"
source = "discovered"
tags = []

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.5

[[models.cost.time_prices.windows]]
days = [
    "sat",
]
start = "00:00"
end = "24:00"

[models.fetch]
repo = "org/gamma"
local_path = "/models/gamma"
`
	if got := docText(t, doc); !strings.HasSuffix(got, "\n"+wantRow) || !strings.HasPrefix(got, docRegistry) {
		t.Errorf("the new row should be appended as:\n%s\ngot document:\n%s", wantRow, got)
	}
	if err := doc.AddModel(row); !errors.Is(err, ErrModelExists) {
		t.Errorf("adding the id again: err = %v, want ErrModelExists", err)
	}
	for _, bad := range []map[string]any{{"family": "fam"}, {"id": ""}, {"id": 7}} {
		if err := doc.AddModel(bad); err == nil {
			t.Errorf("AddModel(%v) should be refused: a row needs a string id", bad)
		}
	}
}

// TestCloneModelCopiesEveryKey pins CloneModel, which the catalog mirror uses
// to re-tag a model: the copy carries every key of the source — the ones wt
// does not model too — with only the overrides changed, and the source row is
// left exactly as it was.
func TestCloneModelCopiesEveryKey(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	err := doc.CloneModel("ollama/alpha", map[string]any{
		"id": "ollama/alpha:cloud", "model_name": "alpha:cloud", "cost.input_price_per_million": 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[models]]
catalog_name = "alpha"
id = "ollama/alpha:cloud"
family = "fam"
provider_id = "ollama"
model_name = "alpha:cloud"
tags = [
    "code",
]

[models.cost]
x_cost_note = "kept"
input_price_per_million = 4
output_price_per_million = 15

[models.model_info]
supports_vision = false
max_input_tokens = 131072

[models.fetch]
x_fetch_note = "kept"
repo = "org/alpha"
local_path = "/models/alpha"
`
	if got := docText(t, doc); !strings.HasSuffix(got, "\n"+wantRow) || !strings.HasPrefix(got, docRegistry) {
		t.Errorf("the clone should be appended as:\n%s\ngot document:\n%s", wantRow, got)
	}
	if err := doc.CloneModel("ollama/alpha", map[string]any{"model_name": "x"}); !errors.Is(err, ErrModelExists) {
		t.Errorf("a clone that keeps the source's id: err = %v, want ErrModelExists", err)
	}
	if err := doc.CloneModel("ollama/nope", map[string]any{"id": "x"}); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("a clone of an unknown id: err = %v, want ErrModelNotFound", err)
	}
}

// TestRemoveModelReturnsTheRow pins RemoveModel: the row is gone from the
// document and handed back whole, because `wt model rm` never deletes weights
// and has to print where they are from the row it just removed. It also pins
// what a removal or an unset leaves behind: `models = []` after the last row
// goes, and the empty table after a table's last key is unset. That is
// tomli-w's form for an emptied list and an emptied table, the form
// registries on disk already hold after such an edit; a writer that dropped
// the key instead would give the same registry a second spelling.
func TestRemoveModelReturnsTheRow(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row, err := doc.RemoveModel("ollama/alpha")
	if err != nil {
		t.Fatal(err)
	}
	fetch, _ := row.Get("fetch")
	if path, _ := fetch.(*tomlw.Table).Get("local_path"); path != "/models/alpha" {
		t.Errorf("the removed row's fetch.local_path = %v, want /models/alpha", path)
	}
	got := docText(t, doc)
	if strings.Contains(got, "alpha") || !strings.Contains(got, `id = "ollama/beta"`) {
		t.Errorf("only ollama/alpha should be gone:\n%s", got)
	}
	if _, err := doc.RemoveModel("ollama/alpha"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("removing it again: err = %v, want ErrModelNotFound", err)
	}
	if _, err := doc.RemoveModel("ollama/beta"); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, doc); !strings.Contains(got, "models = []\n") {
		t.Errorf("removing the last model should leave `models = []`:\n%s", got)
	}
	// A table whose last key is unset stays, empty.
	emptied := parseDoc(t, docRegistry)
	if err := emptied.PatchModel("ollama/alpha", nil, []string{"model_info.supports_vision", "model_info.max_input_tokens"}); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, emptied); !strings.Contains(got, "[models.model_info]\n\n[models.fetch]") {
		t.Errorf("unsetting a table's last key should leave the empty table:\n%s", got)
	}
}

// TestPatchModelCopiesTableRowsItIsGiven pins that a patch value of table
// rows is copied in: the rows a write is validated with must be the rows that
// are written, so a caller changing its own row afterwards must not reach the
// document, and a nil row is refused instead of panicking in a later Clone.
func TestPatchModelCopiesTableRowsItIsGiven(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	mine := tomlw.NewTable()
	mine.Set("label", "off-peak")
	if err := doc.PatchModel("ollama/beta", map[string]any{"cost.time_prices": []*tomlw.Table{mine}}, nil); err != nil {
		t.Fatal(err)
	}
	mine.Set("label", "changed by the caller")
	if got := docText(t, doc); !strings.Contains(got, `label = "off-peak"`) || strings.Contains(got, "changed by the caller") {
		t.Errorf("the document should hold its own copy of the row:\n%s", got)
	}
	err := doc.PatchModel("ollama/beta", map[string]any{"cost.time_prices": []*tomlw.Table{mine, nil}}, nil)
	if err == nil || !strings.Contains(err.Error(), "nil table") {
		t.Errorf("a nil row: err = %v, want a nil table error", err)
	}
}

// TestSetTimePricesReplacesTheRows pins the one operation on
// cost.time_prices: the rows are replaced as a set (the catalog mirror
// rewrites its off-peak row), rows equal to the ones there are left alone,
// and no rows deletes the key.
func TestSetTimePricesReplacesTheRows(t *testing.T) {
	rows := []map[string]any{{
		"label": "off-peak", "timezone": "UTC", "input_price_per_million": 1.5,
		"windows": []map[string]any{{"days": []string{"sat", "sun"}, "start": "00:00", "end": "24:00"}},
	}}
	doc := parseDoc(t, docRegistry)
	if err := doc.SetTimePrices("ollama/alpha", rows); err != nil {
		t.Fatal(err)
	}
	const wantCost = `[models.cost]
x_cost_note = "kept"
input_price_per_million = 3
output_price_per_million = 15

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 1.5

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`
	withRows := docText(t, doc)
	if !strings.Contains(withRows, wantCost) {
		t.Errorf("cost should read:\n%s\ngot document:\n%s", wantCost, withRows)
	}

	again := parseDoc(t, withRows)
	if err := again.SetTimePrices("ollama/alpha", rows); err != nil {
		t.Fatal(err)
	}
	if len(again.touchedModels) != 0 || docText(t, again) != withRows {
		t.Error("setting the same rows again should change nothing")
	}
	if err := again.SetTimePrices("ollama/alpha", nil); err != nil {
		t.Fatal(err)
	}
	if got := docText(t, again); got != docRegistry {
		t.Errorf("setting no rows should delete time_prices and nothing else:\n%s", got)
	}
	if err := again.SetTimePrices("ollama/nope", rows); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("unknown id: err = %v, want ErrModelNotFound", err)
	}
}

// TestAddProviderAppendsARow pins the seeding operation: a provider row in
// schema key order, appended, with a duplicate id refused.
func TestAddProviderAppendsARow(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	row := map[string]any{
		"auth": map[string]any{"base_url": "http://localhost:8003/v1", "type": "none"},
		"name": "MTPLX", "model_dir": "~/.mtplx/models", "id": "mtplx", "location": "local",
	}
	if err := doc.AddProvider(row); err != nil {
		t.Fatal(err)
	}
	const wantRow = `[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"

[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"
`
	if got := docText(t, doc); !strings.Contains(got, wantRow) {
		t.Errorf("the provider should be written as:\n%s\ngot document:\n%s", wantRow, got)
	}
	if err := doc.AddProvider(row); !errors.Is(err, ErrProviderExists) {
		t.Errorf("adding the id again: err = %v, want ErrProviderExists", err)
	}
	if err := doc.AddProvider(map[string]any{"name": "x"}); err == nil {
		t.Error("a provider with no id should be refused")
	}
}

// TestOperationsCreateOnlyTheKnownTopLevelKeys pins "wt never adds a
// top-level key" from the other side: on an empty registry the operations
// create `providers` and `models`, at their places, and nothing else. A new
// top-level key is a file wt's own writer refuses to touch again
// (ErrRegistryTopLevel, #247): every later registry write (`wt model
// init|add|edit|rm`, `wt cloud-sync`) would stop on the registry wt just
// wrote, while the typed reader went on loading it without a word.
func TestOperationsCreateOnlyTheKnownTopLevelKeys(t *testing.T) {
	doc := parseDoc(t, "")
	if err := doc.AddModel(map[string]any{"id": "ollama/a", "family": "f", "provider_id": "ollama", "model_name": "a"}); err != nil {
		t.Fatal(err)
	}
	if err := doc.AddProvider(map[string]any{"id": "ollama", "name": "Ollama", "auth": map[string]any{"type": "none"}}); err != nil {
		t.Fatal(err)
	}
	if got, want := doc.root.Keys(), []string{"providers", "models"}; !slices.Equal(got, want) {
		t.Errorf("top-level keys = %v, want %v", got, want)
	}
}

// TestModelsAndProvidersAreCopies pins the read views planners use: they see
// every row in file order, and changing what they were handed changes nothing
// in the document — a planner that edited a row in place would bypass the
// touched-row validation.
func TestModelsAndProvidersAreCopies(t *testing.T) {
	doc := parseDoc(t, docRegistry)
	models := doc.Models()
	if len(models) != 2 || rowID(models[0]) != "ollama/alpha" || rowID(models[1]) != "ollama/beta" {
		t.Fatalf("Models() = %d rows, want alpha then beta", len(models))
	}
	if name, _ := models[0].Get("catalog_name"); name != "alpha" {
		t.Errorf("a view should carry the keys wt does not model, got catalog_name = %v", name)
	}
	models[0].Set("family", "changed")
	cost, _ := models[0].Get("cost")
	cost.(*tomlw.Table).Set("input_price_per_million", int64(99))
	providers := doc.Providers()
	if len(providers) != 1 || rowID(providers[0]) != "ollama" {
		t.Fatalf("Providers() = %d rows, want ollama", len(providers))
	}
	providers[0].Delete("auth")
	if got := docText(t, doc); got != docRegistry {
		t.Errorf("changing a view changed the document:\n%s", got)
	}
}

// TestNewRegistryDocRefusesAnUnexpectedTopLevel pins the #247 rule on the
// write path: a top-level key that is not providers/families/models (the
// classic is [[model]] for [[models]], which parses and reads as no models)
// refuses the write and names the key, and so does one of the three that is
// not an array of tables.
func TestNewRegistryDocRefusesAnUnexpectedTopLevel(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"a misspelled section", "[[model]]\nid = \"a\"\n", "`model`"},
		{"two unknown keys, sorted", "zeta = 1\nalpha = 2\n[[models]]\nid = \"a\"\n", "`alpha`, `zeta`"},
		{"models is not an array of tables", "models = \"none\"\n", "`models` is not an array of tables"},
		{"providers is a table", "[providers]\nid = \"a\"\n", "`providers` is not an array of tables"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, err := tomlw.Decode([]byte(c.text))
			if err != nil {
				t.Fatal(err)
			}
			_, err = newRegistryDoc(root)
			if !errors.Is(err, ErrRegistryTopLevel) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want ErrRegistryTopLevel naming %s", err, c.want)
			}
		})
	}
	for _, ok := range []string{"", "providers = []\nfamilies = []\nmodels = []\n"} {
		root, _ := tomlw.Decode([]byte(ok))
		if _, err := newRegistryDoc(root); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
}

// tomlKeys lists the TOML key of every field of a struct type, skipping "-".
func tomlKeys(typ reflect.Type) []string {
	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("toml"), ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// TestAWriteNeverTouchesAKeyItWasNotAskedTo pins the property that makes the
// writer safe to grow beside: what a write touches is decided by the keys the
// caller names, never by the fields of config.Model. The row here holds every
// key config.Model and config.ModelCost decode — read from the struct tags, so
// a field added later is covered without editing this test — each with the
// zero value an `omitempty` struct round trip would drop. A patch of one
// unrelated key must leave all of them in the file.
func TestAWriteNeverTouchesAKeyItWasNotAskedTo(t *testing.T) {
	row := tomlw.NewTable()
	for _, k := range tomlKeys(reflect.TypeOf(Model{})) {
		row.Set(k, "")
	}
	row.Set("id", "ollama/zero")
	cost := tomlw.NewTable()
	for _, k := range tomlKeys(reflect.TypeOf(ModelCost{})) {
		cost.Set(k, "")
	}
	row.Set("cost", cost)
	root := tomlw.NewTable()
	root.Set("models", []any{row})
	doc, err := newRegistryDoc(root)
	if err != nil {
		t.Fatal(err)
	}
	before := docText(t, doc)

	if err := doc.PatchModel("ollama/zero", map[string]any{"x_unrelated": true}, nil); err != nil {
		t.Fatal(err)
	}
	after := docText(t, doc)
	if want := strings.Replace(before, "[[models]]\n", "[[models]]\nx_unrelated = true\n", 1); after != want {
		t.Errorf("a patch of one key changed others.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, k := range append(tomlKeys(reflect.TypeOf(Model{})), tomlKeys(reflect.TypeOf(ModelCost{}))...) {
		if !strings.Contains(after, "\n"+k+" = ") && !strings.Contains(after, "[models."+k+"]") {
			t.Errorf("key %q, which config.Model decodes, is gone after an unrelated patch", k)
		}
	}
}
