package cloudsync

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

var applyNow = time.Date(2026, 10, 7, 14, 30, 5, 987, time.FixedZone("PDT", -7*3600))

const applyStamp = "2026-10-07T21:30:05+00:00"

// scratchRegistry writes content as this test's registry and returns its
// path.
func scratchRegistry(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_REGISTRY", path)
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// modelRows decodes the registry at path into its model rows by id.
func modelRows(t *testing.T, path string) map[string]*tomlw.Table {
	t.Helper()
	root, err := tomlw.Decode([]byte(readFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := root.Get("models")
	rows := map[string]*tomlw.Table{}
	for _, item := range v.([]any) {
		row := item.(*tomlw.Table)
		rows[str(row, "id")] = row
	}
	return rows
}

// userTimePrice is a cost.time_prices row a user wrote: a label that is not
// the ollama flow's, a key wt does not model, integer prices, and keys in an
// order no schema gives. Neither flow owns it, so it must come through every
// write byte for byte. (It is in the layout the registry writer emits; a row
// written as an inline table is laid out again by any write, with the same
// keys in the same order.)
const userTimePrice = `[[models.cost.time_prices]]
timezone = "America/Los_Angeles"
note = "mine"
label = "weekend"
output_price_per_million = 1
input_price_per_million = 0.5

[[models.cost.time_prices.windows]]
start = "00:00"
days = [
    "sat",
    "sun",
]
end = "24:00"
`

// The registry the Apply tests start from, in the form registries on disk
// have (tomli-w's, which wt's writer reproduces): an entry
// to update (integer prices, a key in its cost table wt does not model, a
// hand-added key on the row, a time_prices row of the user's), one to
// re-tag, one off the page, and a local model that must come through
// untouched.
const catalogRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"

[[models]]
context_length = 131072
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
source = "curated"
tags = [
    "code",
]

[models.cost]
vendor_note = "kept"
input_price_per_million = 9
subscription_price = 100
subscription_period = "month"

` + userTimePrice + `
[[models]]
catalog_name = "mistral-large-3"
id = "ollama/mistral-large-3:cloud"
family = "mistral"
provider_id = "ollama"
model_name = "mistral-large-3:cloud"
location = "cloud"
source = "curated"
tags = [
    "design",
]

[models.cost]
input_price_per_million = 9.0
subscription_price = 100
subscription_period = "month"

[models.model_info]
supports_function_calling = true

[[models]]
id = "ollama/retired:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired:cloud"
location = "cloud"
source = "curated"
tags = []

[models.cost]
subscription_price = 100
subscription_period = "month"

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "local"
source = "curated"
tags = []
`

// ollamaProviderRow is the provider row a registry needs before a model row
// that names ollama can be written: a write validates that a changed row's
// provider_id names a provider.
const ollamaProviderRow = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"

`

func applyCatalog(t *testing.T, catalog Catalog, pulled []string, resolved map[string]string) (CatalogApplied, bool) {
	t.Helper()
	var done CatalogApplied
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		var err error
		done, err = PlanCatalog(Entries(d.Models()), catalog, pulled, resolved).Apply(d, pulled, applyNow)
		return err
	})
	if err != nil {
		t.Fatalf("UpdateRegistry: %v", err)
	}
	return done, changed
}

// TestStamp pins the pricing_updated_at format: UTC, whole seconds, a numeric
// +00:00 offset. Registries on disk already hold stamps in this spelling and
// config.Model.PricingUpdated reads it back for the stale-price notice, so a
// second spelling would be a second format to read forever.
func TestStamp(t *testing.T) {
	if got := Stamp(applyNow); got != applyStamp {
		t.Errorf("Stamp = %q, want %q", got, applyStamp)
	}
}

// TestCatalogApplyWritesWhatThePlanSays runs one sync with an update, a new
// model, a re-tag and a removal through the real registry writer and reads
// the file back. It pins what a user finds in registry.toml afterwards:
// prices and stamps on the rows the page lists, keys wt does not model still
// there, an integer the sync did not change still an integer, a time_prices
// row the user wrote untouched beside the off-peak row the ollama flow adds, a
// re-tagged entry's tags and model_info on its replacement, and the local
// model's row exactly as it was.
func TestCatalogApplyWritesWhatThePlanSays(t *testing.T) {
	path := scratchRegistry(t, catalogRegistry)
	catalog := catalogOf(withOffpeak(cm("deepseek-v4-pro", 1.32, 0.044, 3.96), f(0.66), f(0.022), f(1.98)),
		CatalogModel{Name: "mistral-large-3", Prices: PriceTriple{Input: f(0.5), Output: f(1.5)}}, cm("glm-5.3", 1.4, 0.26, 4.4))
	resolved := map[string]string{"deepseek-v4-pro": "deepseek-v4-pro:cloud", "mistral-large-3": "mistral-large-3:675b-cloud", "glm-5.3": "glm-5.3:cloud"}
	pulled := []string{"deepseek-v4-pro:cloud", "mistral-large-3:cloud", "stray:cloud", "qwen3:8b"}

	done, changed := applyCatalog(t, catalog, pulled, resolved)
	if !changed {
		t.Fatal("the registry was not written")
	}
	want := CatalogApplied{Updated: 1, Added: 2, Removed: 2,
		Pulls: []string{"mistral-large-3:675b-cloud", "glm-5.3:cloud"},
		// retired:cloud was never pulled, so there is nothing to rm for it.
		Removes: []string{"mistral-large-3:cloud", "stray:cloud"}}
	if !reflect.DeepEqual(done, want) {
		t.Errorf("applied = %+v\nwant      %+v", done, want)
	}

	text := readFile(t, path)
	for _, snippet := range []string{
		// The updated row: page prices in, everything else as it was.
		"context_length = 131072\ncatalog_name = \"deepseek-v4-pro\"\nid = \"ollama/deepseek-v4-pro:cloud\"",
		"vendor_note = \"kept\"\ninput_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.96\nsubscription_price = 100\nsubscription_period = \"month\"\n",
		// The user's time_prices row byte for byte, and the off-peak row the
		// ollama flow owns added after it.
		"subscription_period = \"month\"\n\n" + userTimePrice + "\n[[models.cost.time_prices]]\nlabel = \"off-peak\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.66\ncache_price_per_million = 0.022\noutput_price_per_million = 1.98\n",
		// The new row, whole, its keys in schema order.
		"[[models]]\ncatalog_name = \"glm-5.3\"\nid = \"ollama/glm-5.3:cloud\"\nfamily = \"glm-5.3\"\nprovider_id = \"ollama\"\nmodel_name = \"glm-5.3:cloud\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at = \"" + applyStamp + "\"\n\n" +
			"[models.cost]\ninput_price_per_million = 1.4\ncache_price_per_million = 0.26\noutput_price_per_million = 4.4\nsubscription_price = 100.0\nsubscription_period = \"month\"\n",
		// The local model, byte for byte.
		"[[models]]\nid = \"ollama/qwen3:8b\"\nfamily = \"qwen3\"\nprovider_id = \"ollama\"\nmodel_name = \"qwen3:8b\"\nlocation = \"local\"\nsource = \"curated\"\ntags = []\n",
	} {
		if !strings.Contains(text, snippet) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", snippet, text)
		}
	}

	rows := modelRows(t, path)
	for _, gone := range []string{"ollama/retired:cloud", "ollama/mistral-large-3:cloud"} {
		if rows[gone] != nil {
			t.Errorf("%s is still in the registry", gone)
		}
	}
	retag := rows["ollama/mistral-large-3:675b-cloud"]
	if retag == nil {
		t.Fatalf("the re-tagged entry was not added; ids: %v", reflect.ValueOf(rows).MapKeys())
	}
	if str(retag, "model_name") != "mistral-large-3:675b-cloud" || str(retag, "family") != "mistral" || str(retag, "pricing_updated_at") != applyStamp {
		t.Errorf("re-tagged row keys: %v", retag.Keys())
	}
	if tags, _ := retag.Get("tags"); !reflect.DeepEqual(tags, []any{"design"}) {
		t.Errorf("re-tagged row tags = %v, want the old entry's [design]", tags)
	}
	if info, _ := retag.Get("model_info"); info == nil || !info.(*tomlw.Table).Has("supports_function_calling") {
		t.Error("re-tagged row lost the old entry's model_info")
	}
	cost, _ := retag.Get("cost")
	if c := costOf(cost.(*tomlw.Table)); !samePrice(c.Input, f(0.5)) || c.Cache != nil || !samePrice(c.Output, f(1.5)) || !samePrice(c.SubscriptionPrice, f(100)) {
		t.Errorf("re-tagged cost = %+v, want the page's 0.5/-/1.5 and the old subscription", c)
	}
	if CatalogNameKey != "catalog_name" || OffpeakLabel != "off-peak" {
		t.Fatalf("CatalogNameKey = %q, OffpeakLabel = %q: both are spelled in registries already on disk", CatalogNameKey, OffpeakLabel)
	}
	if str(rows["ollama/deepseek-v4-pro:cloud"], CatalogNameKey) != "deepseek-v4-pro" || str(rows["ollama/deepseek-v4-pro:cloud"], "pricing_updated_at") != applyStamp {
		t.Error("the updated row was not given its catalog name and stamp")
	}
	if rows["ollama/qwen3:8b"].Has("pricing_updated_at") {
		t.Error("the local model was stamped")
	}

	// The same page again: nothing to do, and the file is not touched.
	now := append(pulled, "mistral-large-3:675b-cloud", "glm-5.3:cloud")
	again, changed := applyCatalog(t, catalog, now, resolved)
	if changed || again.Changed() || readFile(t, path) != text {
		t.Errorf("a second sync of the same page wrote the registry again: %+v", again)
	}
}

// TestCatalogApplyNeverRemovesATagARemainingEntryNames pins the rule that
// protects a tag two rows share: when one row is removed and another still
// names its tag, the tag is not handed to `ollama rm`. Removing it would
// leave a registered model with nothing behind it.
func TestCatalogApplyNeverRemovesATagARemainingEntryNames(t *testing.T) {
	scratchRegistry(t, ollamaProviderRow+`[[models]]
id = "ollama/x:cloud"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"

[[models]]
id = "ollama/x-second"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"
`)
	pulled := []string{"x:cloud"}
	done, _ := applyCatalog(t, catalogOf(cm("x")), pulled, map[string]string{"x": "x:cloud"})
	if done.Removed != 1 || len(done.Removes) != 0 {
		t.Errorf("applied = %+v, want the second row removed from the registry and no tag to rm", done)
	}
}

// TestCatalogApplyNeverRemovesALocalTag pins the flow's promise that local
// models are never touched, for the one row that could break it: an entry
// marked location = "cloud" whose model_name is a local tag (a mislabelled
// hand edit). The page does not list it, so the entry leaves the registry,
// as the plan said; but its tag is real weights, and `ollama rm` of it would
// delete gigabytes nobody asked to lose. Only a cloud tag is ever handed to
// `ollama rm`.
func TestCatalogApplyNeverRemovesALocalTag(t *testing.T) {
	path := scratchRegistry(t, ollamaProviderRow+`[[models]]
id = "ollama/keep:cloud"
family = "keep"
provider_id = "ollama"
model_name = "keep:cloud"
location = "cloud"

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "cloud"

[[models]]
id = "ollama/gone:cloud"
family = "gone"
provider_id = "ollama"
model_name = "gone:cloud"
location = "cloud"
`)
	pulled := []string{"keep:cloud", "qwen3:8b", "gone:cloud"}
	done, _ := applyCatalog(t, catalogOf(cm("keep")), pulled, map[string]string{"keep": "keep:cloud"})
	if done.Removed != 2 || !reflect.DeepEqual(done.Removes, []string{"gone:cloud"}) {
		t.Errorf("applied = %+v, want both entries removed from the registry and only gone:cloud to rm", done)
	}
	if rows := modelRows(t, path); rows["ollama/qwen3:8b"] != nil || rows["ollama/gone:cloud"] != nil || rows["ollama/keep:cloud"] == nil {
		t.Errorf("registry rows after the sync: %v", reflect.ValueOf(rows).MapKeys())
	}
}

// TestCatalogApplyRefusesARowThatWouldNotLoad pins what happens when a row
// the sync must change is already broken (here, a hand edit removed its
// family): the write is refused as a whole, the error names the row, and the
// file is untouched. The command reports that as a failed step; writing the
// rest would leave the registry half synced with nothing saying which half.
func TestCatalogApplyRefusesARowThatWouldNotLoad(t *testing.T) {
	const broken = `[[models]]
id = "ollama/x:cloud"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"
`
	path := scratchRegistry(t, broken)
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		_, err := PlanCatalog(Entries(d.Models()), catalogOf(cm("x"), cm("y")), nil, map[string]string{"x": "x:cloud", "y": "y:cloud"}).Apply(d, nil, applyNow)
		return err
	})
	if !errors.Is(err, config.ErrRegistryInvalid) || !strings.Contains(err.Error(), `"ollama/x:cloud"`) {
		t.Errorf("err = %v, want ErrRegistryInvalid naming ollama/x:cloud", err)
	}
	if got := readFile(t, path); got != broken {
		t.Errorf("a refused write changed the file:\n%s", got)
	}
}

// TestPricesApplyStampsEveryMatchedModel runs a refresh through the real
// registry writer. It pins that every matched model is stamped, including
// one whose price did not move: a run that stamped only the changed models
// would leave the check it just made looking older than it is. It also pins
// that a price that did not change is not rewritten (an integer stays an
// integer),
// that a cost.time_prices row the user wrote comes through byte for byte
// whether or not a price beside it moved (the openrouter flow owns no such row),
// and that an unmatched model is not stamped.
func TestPricesApplyStampsEveryMatchedModel(t *testing.T) {
	path := scratchRegistry(t, `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[models]]
id = "openrouter/same"
family = "x"
provider_id = "openrouter"
model_name = "vendor/same"
location = "cloud"
pricing_updated_at = "2026-09-01T00:00:00+00:00"

[models.cost]
note = "kept"
input_price_per_million = 3
cache_price_per_million = 0.5
output_price_per_million = 15
subscription_price = 20
subscription_period = "month"

`+userTimePrice+`
[[models]]
id = "openrouter/moved"
family = "x"
provider_id = "openrouter"
model_name = "vendor/moved"
location = "cloud"

[[models]]
id = "openrouter/repriced"
family = "x"
provider_id = "openrouter"
model_name = "vendor/repriced"
location = "cloud"

[models.cost]
input_price_per_million = 7
output_price_per_million = 15

`+userTimePrice+`
[[models]]
id = "openrouter/missing"
family = "x"
provider_id = "openrouter"
model_name = "vendor/missing"
location = "cloud"
`)
	api := apiOf(t, `{"data": [
		{"id": "vendor/same", "pricing": {"prompt": "0.000003", "completion": "0.000015"}},
		{"id": "vendor/moved", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/repriced", "pricing": {"prompt": "0.000003", "completion": "0.000015"}}
	]}`)
	var done PricesApplied
	changed, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		var err error
		done, err = PlanPrices(Entries(d.Models()), Providers(d.Providers()), api).Apply(d, applyNow)
		return err
	})
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry: changed %v, err %v", changed, err)
	}
	if done != (PricesApplied{Stamped: 3, Changed: 2}) {
		t.Errorf("applied = %+v, want 3 stamped, 2 changed", done)
	}
	text := readFile(t, path)
	for _, snippet := range []string{
		"model_name = \"vendor/same\"\nlocation = \"cloud\"\npricing_updated_at = \"" + applyStamp + "\"\n\n[models.cost]\nnote = \"kept\"\ninput_price_per_million = 3\ncache_price_per_million = 0.5\noutput_price_per_million = 15\nsubscription_price = 20\nsubscription_period = \"month\"\n\n" + userTimePrice + "\n[[models]]\nid = \"openrouter/moved\"",
		// A cost block whose input price moved: that one value is rewritten,
		// the price that did not move is still an integer, and the user's
		// time_prices row is byte for byte what it was.
		"model_name = \"vendor/repriced\"\nlocation = \"cloud\"\npricing_updated_at = \"" + applyStamp + "\"\n\n[models.cost]\ninput_price_per_million = 3.0\noutput_price_per_million = 15\n\n" + userTimePrice + "\n[[models]]\nid = \"openrouter/missing\"",
		"model_name = \"vendor/moved\"\nlocation = \"cloud\"\npricing_updated_at = \"" + applyStamp + "\"\n\n[models.cost]\ninput_price_per_million = 1.0\noutput_price_per_million = 2.0\n",
		"id = \"openrouter/missing\"\nfamily = \"x\"\nprovider_id = \"openrouter\"\nmodel_name = \"vendor/missing\"\nlocation = \"cloud\"\n",
	} {
		if !strings.Contains(text, snippet) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", snippet, text)
		}
	}
	if strings.Count(text, "pricing_updated_at") != 3 {
		t.Errorf("want exactly the three matched models stamped:\n%s", text)
	}
}

// TestApplyWritesOnlyWhatThePlanShows pins that an Apply changes nothing in
// a cost table beyond what its plan printed. The planners read a row's cost
// into a few typed fields, and an Apply that wrote those fields back whole
// would delete what the read left out: here an empty `time_prices = []`,
// which a plan calls unchanged. The printed plan is what the user approved,
// so a key it does not mention must come through byte for byte, in the
// openrouter flow (a matched model whose price is current, and one whose input
// price moved) and in the ollama flow (an entry whose input price moved).
func TestApplyWritesOnlyWhatThePlanShows(t *testing.T) {
	const before = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"

[[models]]
id = "openrouter/same"
family = "x"
provider_id = "openrouter"
model_name = "vendor/same"
location = "cloud"
pricing_updated_at = "2026-09-01T00:00:00+00:00"

[models.cost]
input_price_per_million = 3
output_price_per_million = 15
time_prices = []

[[models]]
id = "openrouter/moved"
family = "x"
provider_id = "openrouter"
model_name = "vendor/moved"
location = "cloud"
pricing_updated_at = "2026-09-01T00:00:00+00:00"

[models.cost]
input_price_per_million = 7
output_price_per_million = 15
time_prices = []

[[models]]
id = "ollama/x:cloud"
family = "x"
provider_id = "ollama"
model_name = "x:cloud"
location = "cloud"
pricing_updated_at = "2026-09-01T00:00:00+00:00"
catalog_name = "x"

[models.cost]
input_price_per_million = 9
cache_price_per_million = 0.1
output_price_per_million = 2
time_prices = []
`
	path := scratchRegistry(t, before)
	api := apiOf(t, `{"data": [
		{"id": "vendor/same", "pricing": {"prompt": "0.000003", "completion": "0.000015"}},
		{"id": "vendor/moved", "pricing": {"prompt": "0.000003", "completion": "0.000015"}}
	]}`)
	var planText string
	var done PricesApplied
	if _, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		plan := PlanPrices(Entries(d.Models()), Providers(d.Providers()), api)
		planText = plan.Format()
		var err error
		done, err = plan.Apply(d, applyNow)
		return err
	}); err != nil {
		t.Fatalf("UpdateRegistry (prices): %v", err)
	}
	if !strings.Contains(planText, "Price updates (1):\n  openrouter/moved: 7/-/15 -> 3/-/15\nUnchanged prices: 1") || done != (PricesApplied{Stamped: 2, Changed: 1}) {
		t.Fatalf("plan =\n%s\napplied = %+v; want one price moved and one unchanged", planText, done)
	}
	const oldStamp = `pricing_updated_at = "2026-09-01T00:00:00+00:00"`
	newStamp := `pricing_updated_at = "` + applyStamp + `"`
	want := strings.Replace(before, oldStamp, newStamp, 2)
	want = strings.Replace(want, "input_price_per_million = 7\n", "input_price_per_million = 3.0\n", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("after the price refresh, registry.toml =\n%s\nwant only the two stamps and the one price changed:\n%s", got, want)
	}

	applied, _ := applyCatalog(t, catalogOf(cm("x", 1)), []string{"x:cloud"}, map[string]string{"x": "x:cloud"})
	if applied.Updated != 1 {
		t.Fatalf("catalog applied = %+v, want one update", applied)
	}
	want = strings.Replace(want, oldStamp, newStamp, 1)
	want = strings.Replace(want, "input_price_per_million = 9\n", "input_price_per_million = 1.0\n", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("after the catalog sync, registry.toml =\n%s\nwant only the stamp and the one price changed:\n%s", got, want)
	}
}
