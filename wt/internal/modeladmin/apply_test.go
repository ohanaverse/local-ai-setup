package modeladmin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// baseRegistry is a registry in the form wt and modelman write it: an ollama
// and an openrouter provider, and one model with keys wt does not model.
const baseRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
catalog_name = "kept"
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = [
    "code",
]

[models.cost]
input_price_per_million = 3
output_price_per_million = 15

[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 1

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

[models.model_info]
supports_function_calling = true

[models.fetch]
repo = "org/gemma4"
`

// scratchRegistry points wt at a registry file under a temp directory,
// holding content ("" for no file), and returns its path.
func scratchRegistry(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-ai", "registry.toml")
	t.Setenv("WT_REGISTRY", path)
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ptr(s string) *string { return &s }

// TestDeriveID pins the ids `wt model add` gives: a local model gets the id
// wt already routes and keeps history under for that artifact (so
// registering a model that is on disk does not rename it), and a cloud model
// gets modelman's spelling, "/" in the name as "--", so ids agree across the
// two tools while both exist. The last case is the one place the two differ:
// modelman's form kept a native provider's model name as it was, and wt
// writes "--" there as for any cloud provider, so a derived cloud id always
// has one "/".
func TestDeriveID(t *testing.T) {
	cases := []struct{ provider, name, want string }{
		{"ollama", "gemma4:9b", "ollama/gemma4:9b"},
		{"omlx", "Qwen3.8-27B-4bit", "omlx/Qwen3.8-27B-4bit"},
		{"omlx-6bit", "Qwen3.8-27B-6bit", "omlx/Qwen3.8-27B-6bit"},
		{"mtplx", "Youssofal/Qwen3.8-MTPLX", "mtplx/Youssofal/Qwen3.8-MTPLX"},
		{"openrouter", "qwen/qwen3.8-27b", "openrouter/qwen--qwen3.8-27b"},
		{"claude", "opus", "claude/opus"},
		{"claude", "org/model", "claude/org--model"},
	}
	for _, c := range cases {
		if got := DeriveID(c.provider, c.name); got != c.want {
			t.Errorf("DeriveID(%q, %q) = %q, want %q", c.provider, c.name, got, c.want)
		}
	}
	// A registered local model keeps the id of the discovered row it was.
	if got, want := DeriveID("omlx-6bit", "X"), config.DiscoveredModelID("omlx-6bit", "X"); got != want {
		t.Errorf("DeriveID = %q, config.DiscoveredModelID = %q; they must agree", got, want)
	}
}

// TestAddWritesOneRowInModelmansLayout verifies an add appends exactly the
// row asked for, with its keys where modelman writes them, and leaves every
// other byte of the file alone. A row laid out differently would be moved by
// modelman's next save, and a rewritten neighbour is a lost hand edit.
func TestAddWritesOneRowInModelmansLayout(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	res, err := Add(AddRequest{
		ProviderID: "openrouter", ModelName: "qwen/qwen3.8-27b",
		Fields: Fields{
			Family: ptr("qwen3.8"), Tags: ptr("code, design"), InputPrice: ptr("0.5"), OutputPrice: ptr("2"),
			SubscriptionPrice: ptr("20"), SubscriptionPeriod: ptr("month"),
		},
	}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "openrouter/qwen--qwen3.8-27b" || len(res.ProvidersAdded) != 0 || len(res.Warnings) != 0 {
		t.Errorf("result = %+v", res)
	}
	want := baseRegistry + `
[[models]]
id = "openrouter/qwen--qwen3.8-27b"
family = "qwen3.8"
provider_id = "openrouter"
model_name = "qwen/qwen3.8-27b"
tags = [
    "code",
    "design",
]

[models.cost]
input_price_per_million = 0.5
output_price_per_million = 2.0
subscription_price = 20.0
subscription_period = "month"
`
	if got := read(t, path); got != want {
		t.Errorf("registry after the add =\n%s\nwant\n%s", got, want)
	}
}

// TestAddSeedsTheProviderRowInTheSameWrite verifies the first add on a
// machine with no registry creates the file with the model and the default
// row of the provider it names — one write, and a registry wt can load. An
// add that left the provider out would be refused by its own validation.
func TestAddSeedsTheProviderRowInTheSameWrite(t *testing.T) {
	path := scratchRegistry(t, "")
	res, err := Add(AddRequest{
		ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit", Fields: Fields{Family: ptr("qwen3.8")},
		ModelInfo: map[string]any{"supports_function_calling": true},
	}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "omlx/Qwen3.8-27B-4bit" || strings.Join(res.ProvidersAdded, ",") != "omlx" {
		t.Errorf("result = %+v, want the discovered id and the omlx row seeded", res)
	}
	want := `[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:8000"

[[models]]
id = "omlx/Qwen3.8-27B-4bit"
family = "qwen3.8"
provider_id = "omlx"
model_name = "Qwen3.8-27B-4bit"
tags = []

[models.model_info]
supports_function_calling = true
`
	if got := read(t, path); got != want {
		t.Errorf("new registry =\n%s\nwant\n%s", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := config.Load(); err != nil {
		t.Errorf("wt cannot load the registry it just wrote: %v", err)
	}

	// openrouter is the one cloud provider wt has a default row for, and an
	// add that names it gets the row too: `wt model add openrouter …` is the
	// first thing a user with a key runs.
	t.Run("openrouter", func(t *testing.T) {
		path := scratchRegistry(t, "")
		res, err := Add(AddRequest{ProviderID: "openrouter", ModelName: "qwen/qwen3.8-27b", Fields: Fields{Family: ptr("qwen3.8")}}, config.SeedEnv{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ID != "openrouter/qwen--qwen3.8-27b" || strings.Join(res.ProvidersAdded, ",") != "openrouter" || len(res.Warnings) != 0 {
			t.Errorf("result = %+v, want the openrouter row seeded and no warning", res)
		}
		if got := read(t, path); !strings.Contains(got, "id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\n\n[providers.auth]\ntype = \"api_key\"\nsecret_ref = \"OPENROUTER_API_KEY\"\n") {
			t.Errorf("the seeded row should name the key's environment variable, never a key:\n%s", got)
		}
	})
}

// TestAddRefusals verifies each thing an add refuses, that the refusal names
// the field at fault (the form puts its cursor there), and that a refused add
// writes nothing.
func TestAddRefusals(t *testing.T) {
	ok := func() AddRequest {
		return AddRequest{ProviderID: "openrouter", ModelName: "a/b", Fields: Fields{Family: ptr("f")}}
	}
	cases := []struct {
		name   string
		change func(*AddRequest)
		field  string
		want   string
	}{
		{"no provider", func(r *AddRequest) { r.ProviderID = " " }, FieldProvider, "a provider is required"},
		{"no name", func(r *AddRequest) { r.ModelName = "" }, FieldName, "a model name is required"},
		{"no family", func(r *AddRequest) { r.Family = nil }, FieldFamily, "family is required"},
		{"a blank family", func(r *AddRequest) { r.Family = ptr("  ") }, FieldFamily, "family is required"},
		{"a location that is not one", func(r *AddRequest) { r.Location = ptr("mars") }, FieldLocation, `location must be local or cloud, got "mars"`},
		{"a price that is not a number", func(r *AddRequest) { r.InputPrice = ptr("cheap") }, FieldInputPrice, `input-price must be a number, got "cheap"`},
		{"a negative price", func(r *AddRequest) { r.OutputPrice = ptr("-1") }, FieldOutputPrice, "output-price must not be negative"},
		{"an infinite price", func(r *AddRequest) { r.CachePrice = ptr("inf") }, FieldCachePrice, "cache-price must be finite"},
		{"a price too large to hold", func(r *AddRequest) { r.CachePrice = ptr("1e999") }, FieldCachePrice, "cache-price must be finite"},
		{"NaN", func(r *AddRequest) { r.SubscriptionPrice = ptr("nan") }, FieldSubscriptionPrice, "subscription-price must be finite"},
		{"a subscription with no period", func(r *AddRequest) { r.SubscriptionPrice = ptr("20") }, FieldSubscriptionPeriod, "a subscription price needs a subscription period (month or year)"},
		{"a period that is not one", func(r *AddRequest) { r.SubscriptionPeriod = ptr("week") }, FieldSubscriptionPeriod, `subscription period must be month or year, got "week"`},
		{"a pairing", func(r *AddRequest) { r.ProviderID = "mlx_lm_server" }, FieldProvider, "target+draft pairing"},
		{"an id with no slash", func(r *AddRequest) { r.ID = "noslash" }, FieldID, `an id is <provider>/<name> with no spaces, got "noslash"`},
		{"an id with a space", func(r *AddRequest) { r.ID = "openrouter/has space" }, FieldID, `an id is <provider>/<name> with no spaces, got "openrouter/has space"`},
		{"an id with nothing after the slash", func(r *AddRequest) { r.ID = "openrouter/" }, FieldID, "an id is <provider>/<name>"},
		{"an id with a control character", func(r *AddRequest) { r.ID = "openrouter/a\tb" }, FieldID, "an id is <provider>/<name>"},
		{"an artifact that is already registered", func(r *AddRequest) { r.ProviderID, r.ModelName, r.ID = "ollama", "gemma4:9b", "ollama/again" }, FieldName,
			`model "ollama/gemma4:9b" already registers gemma4:9b on ollama`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, baseRegistry)
			req := ok()
			c.change(&req)
			_, err := Add(req, config.SeedEnv{})
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != c.field || !strings.Contains(fe.Msg, c.want) {
				t.Fatalf("err = %v, want a FieldError on %q containing %q", err, c.field, c.want)
			}
			if got := read(t, path); got != baseRegistry {
				t.Error("a refused add changed the registry")
			}
			// CheckAdd refuses the same request without the registry, except
			// the one refusal that needs it.
			if check := CheckAdd(req); (check == nil) != (c.name == "an artifact that is already registered") {
				t.Errorf("CheckAdd = %v; it must refuse what Add refuses without reading the registry, and nothing else", check)
			}
		})
	}
	if err := CheckAdd(ok()); err != nil {
		t.Errorf("CheckAdd of a good request = %v", err)
	}
	t.Run("an id that is taken", func(t *testing.T) {
		path := scratchRegistry(t, baseRegistry)
		req := ok()
		req.ID = "ollama/gemma4:9b"
		if _, err := Add(req, config.SeedEnv{}); !errors.Is(err, config.ErrModelExists) {
			t.Fatalf("err = %v, want config.ErrModelExists", err)
		}
		if got := read(t, path); got != baseRegistry {
			t.Error("a refused add changed the registry")
		}
	})
	t.Run("a provider wt has no row for", func(t *testing.T) {
		path := scratchRegistry(t, baseRegistry)
		req := ok()
		req.ProviderID = "corp-gateway"
		_, err := Add(req, config.SeedEnv{})
		if !errors.Is(err, config.ErrRegistryInvalid) || !strings.Contains(err.Error(), `provider_id "corp-gateway" names no provider row`) ||
			!strings.Contains(err.Error(), `add a [[providers]] block for "corp-gateway" to `+path+" and run this again") {
			t.Fatalf("err = %v, want the refusal and what to do about it", err)
		}
		if got := read(t, path); got != baseRegistry {
			t.Error("a refused add changed the registry")
		}
	})
}

// TestAddWarnsAboutAKeylessProvider verifies an add under an api_key provider
// row with no secret_ref succeeds and says the route will carry an empty key.
// The model is in the registry either way; without the warning its first
// request fails with a 401 nobody can trace to the registry.
func TestAddWarnsAboutAKeylessProvider(t *testing.T) {
	path := scratchRegistry(t, strings.Replace(baseRegistry, "secret_ref = \"OPENROUTER_API_KEY\"\n", "", 1))
	res, err := Add(AddRequest{ProviderID: "openrouter", ModelName: "a/b", Fields: Fields{Family: ptr("f")}}, config.SeedEnv{})
	if err != nil {
		t.Fatal(err)
	}
	want := `provider "openrouter" has no auth.secret_ref, so its LiteLLM routes carry an empty api_key: set it in ` + path
	if len(res.Warnings) != 1 || res.Warnings[0] != want {
		t.Errorf("warnings = %q, want [%q]", res.Warnings, want)
	}
}

// TestEditPatchesOnlyWhatWasGiven verifies an edit changes the named keys and
// nothing else in the row or the file: time_prices, model_info, fetch and a
// key wt does not model all survive, an integer price that was not edited
// stays an integer, and pricing_updated_at is not stamped (that date is `wt
// cloud-sync`'s). An edit that rewrote the row would drop the off-peak prices
// the catalog mirror wrote.
func TestEditPatchesOnlyWhatWasGiven(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	changed, err := Edit("ollama/gemma4:9b", Fields{Family: ptr("gemma"), Tags: ptr(""), Location: ptr("cloud"), OutputPrice: ptr("12.5"), CachePrice: ptr("0.3")})
	if err != nil || !changed {
		t.Fatalf("Edit = (%v, %v), want (true, nil)", changed, err)
	}
	want := baseRegistry
	for _, r := range [][2]string{
		{"family = \"gemma4\"", "family = \"gemma\""},
		{"model_name = \"gemma4:9b\"\ntags = [\n    \"code\",\n]\n", "model_name = \"gemma4:9b\"\nlocation = \"cloud\"\ntags = []\n"},
		{"input_price_per_million = 3\noutput_price_per_million = 15\n", "input_price_per_million = 3\ncache_price_per_million = 0.3\noutput_price_per_million = 12.5\n"},
	} {
		if !strings.Contains(want, r[0]) {
			t.Fatalf("fixture error: %q is not in the registry", r[0])
		}
		want = strings.Replace(want, r[0], r[1], 1)
	}
	if got := read(t, path); got != want {
		t.Errorf("registry after the edit =\n%s\nwant\n%s", got, want)
	}

	// The same edit again changes nothing and writes nothing.
	changed, err = Edit("ollama/gemma4:9b", Fields{Family: ptr("gemma"), OutputPrice: ptr("12.5")})
	if err != nil || changed {
		t.Errorf("a repeated edit = (%v, %v), want (false, nil)", changed, err)
	}
	// An empty value clears the key; an empty location inherits the provider's.
	if _, err := Edit("ollama/gemma4:9b", Fields{Location: ptr(""), InputPrice: ptr(" ")}); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if !strings.Contains(got, "model_name = \"gemma4:9b\"\ntags = []\n") || !strings.Contains(got, "[models.cost]\ncache_price_per_million = 0.3\n") {
		t.Errorf("an empty value did not clear its key:\n%s", got)
	}
}

// TestEditClearingTheLastPriceDropsTheCostTable verifies that clearing every
// price of a model leaves no empty [models.cost] header behind, so the row
// reads like one that never had a price — and that an edit which does not
// touch a price leaves an empty table that was already there alone.
func TestEditClearingTheLastPriceDropsTheCostTable(t *testing.T) {
	const model = `[[models]]
id = "openrouter/a--b"
family = "f"
provider_id = "openrouter"
model_name = "a/b"
tags = []
`
	providers := baseRegistry[:strings.Index(baseRegistry, "[[models]]")]
	path := scratchRegistry(t, providers+model+"\n[models.cost]\ninput_price_per_million = 1\noutput_price_per_million = 2\n")
	if _, err := Edit("openrouter/a--b", Fields{InputPrice: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.Contains(got, "[models.cost]\noutput_price_per_million = 2\n") {
		t.Fatalf("one price cleared, one left: the table stays\n%s", got)
	}
	if _, err := Edit("openrouter/a--b", Fields{OutputPrice: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != providers+model {
		t.Errorf("the last price cleared should take the table with it:\n%s", got)
	}

	path = scratchRegistry(t, providers+model+"\n[models.cost]\n")
	if _, err := Edit("openrouter/a--b", Fields{Family: ptr("g"), InputPrice: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); !strings.Contains(got, "family = \"g\"") || !strings.HasSuffix(got, "\n[models.cost]\n") {
		t.Errorf("an empty cost table that was there before the edit is not this edit's to remove:\n%s", got)
	}
}

// TestEditMovesALegacyCostTable verifies an edit of a price on a row still in
// modelman's old cost layout (cost.kind) moves the whole table to the current
// one: modelman reads a table that has `kind` by its old keys only, so a
// current key written beside them is a price wt shows and modelman ignores.
// An edit that touches no price leaves the old layout exactly as it was.
func TestEditMovesALegacyCostTable(t *testing.T) {
	providers := baseRegistry[:strings.Index(baseRegistry, "[[models]]")]
	row := func(cost string) string {
		return providers + "[[models]]\nid = \"openrouter/a--b\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"a/b\"\ntags = []\n" + cost
	}
	const perToken = "\n[models.cost]\nkind = \"per_token\"\nprice_per_million_tokens = 2.5\n"
	cases := []struct {
		name   string
		before string
		edit   Fields
		after  string
	}{
		{"a per-token price is the input and the output price; the edit sets one", perToken, Fields{InputPrice: ptr("9")},
			"\n[models.cost]\ninput_price_per_million = 9.0\noutput_price_per_million = 2.5\n"},
		{"clearing one side keeps the other", perToken, Fields{OutputPrice: ptr("")},
			"\n[models.cost]\ninput_price_per_million = 2.5\n"},
		{"a subscription keeps its price and period under their new names", "\n[models.cost]\nkind = \"subscription\"\nprice_per_period = 20\nperiod = \"month\"\n", Fields{SubscriptionPeriod: ptr("year")},
			"\n[models.cost]\nsubscription_price = 20\nsubscription_period = \"year\"\n"},
		{"a free model gets its first price", "\n[models.cost]\nkind = \"free\"\n", Fields{CachePrice: ptr("0.1")},
			"\n[models.cost]\ncache_price_per_million = 0.1\n"},
		{"an edit of the family leaves the old layout alone", perToken, Fields{Family: ptr("f")}, perToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, row(c.before))
			if _, err := Edit("openrouter/a--b", c.edit); err != nil {
				t.Fatal(err)
			}
			if got := read(t, path); got != row(c.after) {
				t.Errorf("registry after the edit =\n%s\nwant\n%s", got, row(c.after))
			}
		})
	}
	// The subscription rule is judged over the carried values too.
	scratchRegistry(t, row("\n[models.cost]\nkind = \"subscription\"\nprice_per_period = 20\nperiod = \"month\"\n"))
	var fe *FieldError
	if _, err := Edit("openrouter/a--b", Fields{SubscriptionPeriod: ptr("")}); !errors.As(err, &fe) || fe.Field != FieldSubscriptionPeriod {
		t.Errorf("clearing the period under a carried price: err = %v, want a FieldError on the period", err)
	}
}

// TestEditRefusals verifies an edit of a model that is not in the registry,
// a value that cannot be used, and a subscription price left without a
// period are each refused with nothing written.
func TestEditRefusals(t *testing.T) {
	path := scratchRegistry(t, baseRegistry)
	if _, err := Edit("ollama/nope", Fields{Family: ptr("x")}); !errors.Is(err, config.ErrModelNotFound) {
		t.Errorf("unknown id: err = %v, want config.ErrModelNotFound", err)
	}
	var fe *FieldError
	if _, err := Edit("ollama/gemma4:9b", Fields{Family: ptr("")}); !errors.As(err, &fe) || fe.Field != FieldFamily {
		t.Errorf("empty family: err = %v, want a FieldError on family", err)
	}
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPrice: ptr("20")}); !errors.As(err, &fe) || fe.Field != FieldSubscriptionPeriod {
		t.Errorf("subscription with no period: err = %v, want a FieldError on the period", err)
	}
	if got := read(t, path); got != baseRegistry {
		t.Error("a refused edit changed the registry")
	}
	// With the period given in the same edit, or already in the row, it is fine.
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPrice: ptr("20"), SubscriptionPeriod: ptr("year")}); err != nil {
		t.Fatal(err)
	}
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPrice: ptr("25")}); err != nil {
		t.Errorf("a new price beside the period already there: %v", err)
	}
	// And the period cannot then be cleared from under the price.
	if _, err := Edit("ollama/gemma4:9b", Fields{SubscriptionPeriod: ptr("")}); !errors.As(err, &fe) || fe.Field != FieldSubscriptionPeriod {
		t.Errorf("clearing the period under a price: err = %v, want a FieldError on the period", err)
	}
}

// TestRemoveIsAllOrNothing verifies a removal deletes exactly the named rows,
// and that one unknown id removes none of them: `wt model rm a b` must not
// half-apply and leave the user to work out which rows went.
func TestRemoveIsAllOrNothing(t *testing.T) {
	two := baseRegistry + `
[[models]]
id = "openrouter/a--b"
family = "f"
provider_id = "openrouter"
model_name = "a/b"
tags = []
`
	path := scratchRegistry(t, two)
	if err := Remove([]string{"openrouter/a--b", "ollama/nope"}); !errors.Is(err, config.ErrModelNotFound) {
		t.Fatalf("err = %v, want config.ErrModelNotFound", err)
	}
	if got := read(t, path); got != two {
		t.Error("a refused removal changed the registry")
	}
	if err := Remove([]string{"openrouter/a--b"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != baseRegistry {
		t.Errorf("after the removal =\n%s\nwant the registry without the row", got)
	}
}

// TestARowWithAGapCanBeRepairedOrRemoved verifies that a registry holding a
// model whose provider has no row — which makes every other wt command report
// a config error — can still be fixed with an edit of another row, an edit
// that repairs the bad row, or its removal. The commands that repair a
// registry must not be blocked by the damage they are there to repair.
func TestARowWithAGapCanBeRepairedOrRemoved(t *testing.T) {
	gap := baseRegistry + `
[[models]]
id = "ghost/old"
family = "f"
provider_id = "ghost"
model_name = "old"
tags = []
`
	scratchRegistry(t, gap)
	if _, err := Edit("ollama/gemma4:9b", Fields{Family: ptr("gemma")}); err != nil {
		t.Errorf("an edit of a good row beside the bad one: %v", err)
	}
	if _, err := Edit("ghost/old", Fields{Family: ptr("g")}); !errors.Is(err, config.ErrRegistryInvalid) {
		t.Errorf("an edit that leaves the bad row bad: err = %v, want config.ErrRegistryInvalid", err)
	}
	if err := Remove([]string{"ghost/old"}); err != nil {
		t.Errorf("removing the bad row: %v", err)
	}
}
