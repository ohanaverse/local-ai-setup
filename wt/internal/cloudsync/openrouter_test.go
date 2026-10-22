package cloudsync

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

func orEntry(name string, cost *Cost) Entry {
	return Entry{ID: "openrouter/" + name, Family: "x", ProviderID: "openrouter", ModelName: "vendor/" + name, Location: "cloud", Cost: cost}
}

func apiOf(t *testing.T, body string) map[string]APIPrice {
	t.Helper()
	api, err := ParseOpenRouter([]byte(body))
	if err != nil {
		t.Fatalf("ParseOpenRouter: %v", err)
	}
	return api
}

// TestParseOpenRouter pins how the API's per-token prices become prices per
// million tokens: decimal strings (what OpenRouter sends) and JSON numbers
// alike, a missing field as no price, and OpenRouter's -1 ("varies") marked
// as not a price instead of written to the registry as minus one million. A
// price sent as text that is not a number at all ("soon", an empty string, a
// number too large to hold) is marked the same way: read as "not reported"
// it would clear the registry's price for that field with nothing said.
func TestParseOpenRouter(t *testing.T) {
	api := apiOf(t, `{"data": [
		{"id": "a/strings", "pricing": {"prompt": "0.0000025", "completion": "0.0000100", "input_cache_read": "0.0000010"}},
		{"id": "a/numbers", "pricing": {"prompt": 0.000001, "completion": 2e-6}},
		{"id": "a/top-level", "prompt": "0.000003"},
		{"id": "a/varies", "pricing": {"prompt": "-1", "completion": "-1"}},
		{"id": "a/junk", "pricing": {"prompt": "soon", "completion": null, "input_cache_read": true}},
		{"id": "a/no-table", "pricing": "n/a"},
		{"id": "a/text", "pricing": {"prompt": "", "completion": "1e999", "input_cache_read": "varies"}},
		{"id": "a/huge", "pricing": {"prompt": 1e999, "completion": "0.000002"}},
		{"pricing": {"prompt": "1"}},
		"not an object"
	]}`)
	if len(api) != 8 {
		t.Fatalf("ids = %d, want 8 (the two items with no id skipped)", len(api))
	}
	wantTriple(t, "strings", PriceTriple{Input: api["a/strings"].Input, Cache: api["a/strings"].Cache, Output: api["a/strings"].Output}, f(0.0000025*1e6), f(0.0000010*1e6), f(0.0000100*1e6))
	wantTriple(t, "numbers", PriceTriple{Input: api["a/numbers"].Input, Cache: api["a/numbers"].Cache, Output: api["a/numbers"].Output}, f(0.000001*1e6), nil, f(2e-6*1e6))
	if !samePrice(api["a/top-level"].Input, f(0.000003*1e6)) {
		t.Errorf("an item with no pricing table: input = %s, want it read from the item itself", num(api["a/top-level"].Input))
	}
	if v := api["a/varies"]; v.Input != nil || v.Output != nil || !reflect.DeepEqual(v.Skipped, []string{"prompt", "completion"}) {
		t.Errorf("a/varies = %+v, want no prices and both fields skipped", v)
	}
	// Text that is not a number is skipped; null and a boolean are fields the
	// API did not report.
	if v := api["a/junk"]; v.Input != nil || v.Output != nil || v.Cache != nil || !reflect.DeepEqual(v.Skipped, []string{"prompt"}) {
		t.Errorf("a/junk = %+v, want no prices and only prompt skipped", v)
	}
	if v := api["a/text"]; v.Input != nil || v.Output != nil || v.Cache != nil || !reflect.DeepEqual(v.Skipped, []string{"prompt", "completion", "input_cache_read"}) {
		t.Errorf("a/text = %+v, want no prices and all three fields skipped", v)
	}
	if v := api["a/huge"]; v.Input != nil || !samePrice(v.Output, f(0.000002*1e6)) || !reflect.DeepEqual(v.Skipped, []string{"prompt"}) {
		t.Errorf("a/huge = %+v, want prompt skipped and the completion price read", v)
	}
	if v := api["a/no-table"]; v.Input != nil || v.Output != nil || v.Cache != nil || v.Skipped != nil {
		t.Errorf("a/no-table = %+v, want no prices", v)
	}
}

// TestParseOpenRouterRejectsAnotherShape pins that an answer that is not the
// model list (an error page, a changed API) is an error, so the openrouter flow
// fails as a step instead of reporting "no OpenRouter match" for every model.
func TestParseOpenRouterRejectsAnotherShape(t *testing.T) {
	for body, want := range map[string]string{
		`<html>rate limited</html>`: "not JSON",
		`[1, 2]`:                    "not a JSON object",
		`{"error": "nope"}`:         "missing 'data' list",
		`{"data": {"a": 1}}`:        "missing 'data' list",
	} {
		if _, err := ParseOpenRouter([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseOpenRouter(%s) err = %v, want one naming %q", body, err, want)
		}
	}
}

// TestOpenRouterPricedMatchesTheContract holds this package's row-based
// predicate to docs/contracts/catalog-predicates. The same fixture is read by
// internal/config's TestCatalogPredicatesFixture, which pins the other
// implementation of the rule (config.Config.OpenRouterPriced, over the typed
// registry); if the two drifted, the openrouter flow would refresh one set of
// models while the stale-price notice watched another.
func TestOpenRouterPricedMatchesTheContract(t *testing.T) {
	const dir = "../../../docs/contracts/"
	data, err := os.ReadFile(dir + "catalog-predicates.sample.toml")
	if err != nil {
		t.Fatal(err)
	}
	root, err := tomlw.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	rows := func(key string) []*tomlw.Table {
		v, _ := root.Get(key)
		var out []*tomlw.Table
		for _, item := range v.([]any) {
			out = append(out, item.(*tomlw.Table))
		}
		return out
	}
	raw, err := os.ReadFile(dir + "catalog-predicates.expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		OpenRouterPriced []string `json:"openrouter_priced"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range Entries(rows("models")) {
		if OpenRouterPriced(e) {
			got = append(got, e.ID)
		}
	}
	if !reflect.DeepEqual(got, want.OpenRouterPriced) {
		t.Errorf("openrouter_priced = %v, want %v", got, want.OpenRouterPriced)
	}
}

// TestPlanPricesMergeRules pins what a refresh may and may not overwrite.
// Input and output are what it measures and always take the API's value. The
// cache price is replaced only when the API reports one, and the subscription
// and the time_prices rows are never touched, so a refresh cannot erase
// pricing the user entered by hand.
func TestPlanPricesMergeRules(t *testing.T) {
	mine := timeRow("mine", "sat")
	manual := &Cost{Input: f(2.5), Cache: f(1), Output: f(10), SubscriptionPrice: f(20), SubscriptionPeriod: s("month"), TimePrices: []*tomlw.Table{mine}}
	entries := []Entry{
		orEntry("manual", manual),
		orEntry("cache-reported", &Cost{Cache: f(9.9)}),
		orEntry("new", nil),
	}
	api := apiOf(t, `{"data": [
		{"id": "vendor/manual", "pricing": {"prompt": "0.000003", "completion": "0.000012"}},
		{"id": "vendor/cache-reported", "pricing": {"prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000005"}},
		{"id": "vendor/new", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}
	]}`)
	plan := PlanPrices(entries, api)
	if plan.Candidates != 3 || len(plan.Matched) != 3 || len(plan.Warnings) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	m := plan.Matched[0].After
	if !samePrice(m.Input, f(0.000003*1e6)) || !samePrice(m.Output, f(0.000012*1e6)) || !samePrice(m.Cache, f(1)) ||
		!samePrice(m.SubscriptionPrice, f(20)) || *m.SubscriptionPeriod != "month" || len(m.TimePrices) != 1 || m.TimePrices[0] != mine {
		t.Errorf("manual after = %+v, want new input and output, everything else kept", m)
	}
	if got := plan.Matched[1].After.Cache; !samePrice(got, f(0.0000005*1e6)) {
		t.Errorf("cache-reported cache = %s, want the API's", num(got))
	}
	if got := plan.Matched[2]; got.Before != nil || got.After.Cache != nil || !got.Changed {
		t.Errorf("new = %+v, want a changed entry with no cache price", got)
	}
}

// TestPlanPricesCandidatesAndWarnings pins who is refreshed and what is said
// about the rest. A candidate is a model whose provider_id is "openrouter",
// and nothing else: not another cloud provider's model whose name happens to
// be an OpenRouter id, not an ollama cloud model (priced by ollama.com), not
// a native agent provider's model. None of those is fetched for, changed or
// warned about. A candidate OpenRouter does not list, lists with no price,
// prices at -1 or prices with text that is not a number is a warning and is
// left exactly as it is (a price nobody could read must not delete the one
// in the registry).
func TestPlanPricesCandidatesAndWarnings(t *testing.T) {
	entries := []Entry{
		orEntry("listed", nil),
		{ID: "acme/model-a", ProviderID: "acme", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "ollama/glm:cloud", ProviderID: "ollama", ModelName: "vendor/listed", Location: "cloud", Cost: &Cost{Input: f(1)}},
		{ID: "claude/native", ProviderID: "claude", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "dangling/x", ProviderID: "nowhere", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "corp/claude-3", ProviderID: "corp", ModelName: "vendor/listed", Location: "cloud"},
		{ID: "gateway/y", ProviderID: "gateway", ModelName: "vendor/listed", Location: "local"},
		orEntry("missing", &Cost{Input: f(7)}),
		orEntry("unpriced", nil),
		orEntry("varies", nil),
		orEntry("soon", &Cost{Input: f(3), Output: f(3)}),
	}
	api := apiOf(t, `{"data": [
		{"id": "vendor/listed", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/unpriced", "pricing": {}},
		{"id": "vendor/varies", "pricing": {"prompt": "-1", "completion": "0.000002"}},
		{"id": "vendor/soon", "pricing": {"prompt": "soon", "completion": "0.00001"}}
	]}`)
	plan := PlanPrices(entries, api)
	var matched []string
	for _, m := range plan.Matched {
		matched = append(matched, m.ModelID)
	}
	wantIDs(t, "matched", matched, "openrouter/listed")
	if plan.Candidates != 5 {
		t.Errorf("candidates = %d, want 5", plan.Candidates)
	}
	want := []string{
		"No OpenRouter match for openrouter/missing (vendor/missing)",
		"No pricing data for openrouter/unpriced",
		"Could not use OpenRouter's pricing for openrouter/varies: its prompt price is negative or not a number",
		"Could not use OpenRouter's pricing for openrouter/soon: its prompt price is negative or not a number",
	}
	if !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q\nwant %q", plan.Warnings, want)
	}
	if text := plan.Format(); strings.Contains(text, "  openrouter/soon:") {
		t.Errorf("the plan changes a model whose price could not be read:\n%s", text)
	}
}

// TestPricePlanFormat pins the dry run's text: each changed model as
// `id: old -> new`, unchanged ones as a count, then the warnings.
func TestPricePlanFormat(t *testing.T) {
	entries := []Entry{
		orEntry("moved", &Cost{Input: f(2.5), Output: f(10)}),
		orEntry("same", &Cost{Input: f(1), Output: f(2)}),
		orEntry("new", nil),
		orEntry("missing", nil),
	}
	api := apiOf(t, `{"data": [
		{"id": "vendor/moved", "pricing": {"prompt": "0.000003", "completion": "0.000015", "input_cache_read": "0.0000003"}},
		{"id": "vendor/same", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/new", "pricing": {"prompt": "0.00000068", "completion": "0.00000209"}}
	]}`)
	plan := PlanPrices(entries, api)
	want := strings.Join([]string{
		"openrouter.ai: 4 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)",
		"Price updates (2):",
		"  openrouter/moved: 2.5/-/10 -> 3/0.3/15",
		"  openrouter/new: no cost -> 0.68/-/2.09",
		"Unchanged prices: 1",
		"warning: No OpenRouter match for openrouter/missing (vendor/missing)",
	}, "\n")
	if got := plan.Format(); got != want {
		t.Errorf("Format() =\n%s\n\nwant\n%s", got, want)
	}
	if !plan.HasWork() || (&PricePlan{Candidates: 1}).HasWork() {
		t.Error("HasWork: want true with a matched model, false with none")
	}
}

// TestTheTwoFlowsNeverShareAModel pins what lets each flow write
// cost.time_prices rows without looking at the other's: no registry row is
// in both flows' scope. The openrouter flow takes the rows whose provider_id
// is "openrouter"; the ollama flow addresses only rows whose provider_id is
// "ollama". If a row could be in both, one run would have two plans made
// from the same reading of the registry, and the flow applied second would
// write back the rows it read, dropping the first flow's.
func TestTheTwoFlowsNeverShareAModel(t *testing.T) {
	var entries []Entry
	for _, provider := range []string{"openrouter", "ollama", "acme", ""} {
		for _, location := range []string{"cloud", "local", ""} {
			// A cloud tag, an OpenRouter id, and one name that is both a page
			// name's tag and listed by OpenRouter below.
			for _, name := range []string{"a:cloud", "vendor/a", "a"} {
				e := Entry{ID: provider + "/" + location + "/" + name, Family: "x", ProviderID: provider, ModelName: name, Location: location, CatalogName: "a", Cost: &Cost{Input: f(9)}}
				if isOllamaCloud(e) && OpenRouterPriced(e) {
					t.Errorf("%s is in both flows' scope", e.ID)
				}
				entries = append(entries, e)
			}
		}
	}
	api := apiOf(t, `{"data": [
		{"id": "a:cloud", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "vendor/a", "pricing": {"prompt": "0.000001", "completion": "0.000002"}},
		{"id": "a", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}
	]}`)
	prices := PlanPrices(entries, api)
	catalog := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.5), nil, f(1))), []string{"a:cloud"}, nil)
	touched := map[string]string{}
	for _, m := range prices.Matched {
		touched[m.ModelID] = "openrouter"
	}
	if len(touched) != 9 {
		t.Fatalf("the openrouter plan matched %d rows, want the 9 openrouter ones:\n%s", len(touched), prices.Format())
	}
	var catalogIDs []string
	for _, u := range catalog.Updates {
		catalogIDs = append(catalogIDs, u.ModelID)
	}
	catalogIDs = append(catalogIDs, catalog.Removals...)
	for _, a := range catalog.Additions {
		catalogIDs = append(catalogIDs, a.ID)
		if a.CloneOf != "" {
			catalogIDs = append(catalogIDs, a.CloneOf)
		}
	}
	if len(catalogIDs) == 0 {
		t.Fatalf("the ollama plan addresses no row; the fixture proves nothing:\n%s", catalog.Format())
	}
	for _, id := range catalogIDs {
		if touched[id] != "" {
			t.Errorf("%s is changed by both plans:\n%s\n\n%s", id, prices.Format(), catalog.Format())
		}
		if !strings.HasPrefix(id, "ollama/") {
			t.Errorf("the ollama plan addresses %s, a row of another provider", id)
		}
	}
}

// healthyList is an OpenRouter list long enough that its absences are read
// as removals, plus the given ids.
func healthyList(ids ...string) map[string]APIPrice {
	api := map[string]APIPrice{}
	for i := 0; i < MinListedForRemovals; i++ {
		api["pad/model-"+string(rune('a'+i))] = APIPrice{Input: f(1), Output: f(2)}
	}
	for _, id := range ids {
		api[id] = APIPrice{Input: f(1), Output: f(2)}
	}
	return api
}

func manualOR(name string) Entry {
	e := orEntry(name, nil)
	e.Source = "manual"
	return e
}

// TestPlanPricesRemovesAModelOpenRouterNoLongerLists pins the new removal: a
// registry model whose id is absent from a healthy OpenRouter list is a
// removal, not just the "No OpenRouter match" warning it used to get.
func TestPlanPricesRemovesAModelOpenRouterNoLongerLists(t *testing.T) {
	plan := PlanPrices([]Entry{orEntry("gone", nil), orEntry("here", nil)}, healthyList("vendor/here"))
	wantIDs(t, "removals", plan.Removals, "openrouter/gone")
	for _, w := range plan.Warnings {
		if strings.Contains(w, "No OpenRouter match") {
			t.Errorf("warning %q for a removal", w)
		}
	}
	if plan.RemovableCandidates != 2 || !plan.HasWork() {
		t.Errorf("RemovableCandidates = %d, HasWork = %v", plan.RemovableCandidates, plan.HasWork())
	}
}

// TestPlanPricesNeverRemovesAListedModelWithoutAUsablePrice pins that a model
// OpenRouter lists but whose price cannot be used (-1 "varies", no pricing,
// time-of-day it cannot read) still exists: it keeps its warning and is
// never a removal. Only absence from the list removes.
func TestPlanPricesNeverRemovesAListedModelWithoutAUsablePrice(t *testing.T) {
	api := apiOf(t, `{"data": [
		{"id": "vendor/varies", "pricing": {"prompt": "-1", "completion": "-1"}},
		{"id": "vendor/nopricing", "pricing": {}}
	]}`)
	for id, p := range healthyList() {
		api[id] = p
	}
	plan := PlanPrices([]Entry{orEntry("varies", nil), orEntry("nopricing", nil)}, api)
	wantIDs(t, "removals", plan.Removals)
	if len(plan.Warnings) != 2 {
		t.Errorf("warnings = %v, want one per unusable model", plan.Warnings)
	}
}

// TestPlanPricesWithholdsRemovalsOnATinyList pins the outage rule: an empty
// list, or one shorter than MinListedForRemovals, plans no removals and says
// why, and the old "No OpenRouter match" warning remains. A truncated or
// failed response must never read as "every model is gone".
func TestPlanPricesWithholdsRemovalsOnATinyList(t *testing.T) {
	for _, api := range []map[string]APIPrice{{}, {"vendor/other": {Input: f(1)}}} {
		plan := PlanPrices([]Entry{orEntry("gone", nil)}, api)
		wantIDs(t, "removals", plan.Removals)
		if plan.Withheld == "" || !strings.Contains(plan.Withheld, "no removals") {
			t.Errorf("Withheld = %q, want a reason", plan.Withheld)
		}
		if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "No OpenRouter match for openrouter/gone") {
			t.Errorf("warnings = %v, want the old no-match warning", plan.Warnings)
		}
	}
	short := healthyList()
	delete(short, "pad/model-a")
	if plan := PlanPrices([]Entry{orEntry("gone", nil)}, short); len(plan.Removals) != 0 || plan.Withheld == "" {
		t.Errorf("a list one short of the floor planned removals %v (Withheld %q)", plan.Removals, plan.Withheld)
	}
}

// TestPlanPricesKeepsAManualModelOffTheList pins the protection of a
// hand-added OpenRouter model: absent from the list it is kept and reported,
// not removed, and it is not a removable candidate for the mass-removal
// count.
func TestPlanPricesKeepsAManualModelOffTheList(t *testing.T) {
	plan := PlanPrices([]Entry{manualOR("mine"), orEntry("gone", nil)}, healthyList())
	wantIDs(t, "removals", plan.Removals, "openrouter/gone")
	wantIDs(t, "kept", plan.Kept, "openrouter/mine")
	if plan.RemovableCandidates != 1 {
		t.Errorf("RemovableCandidates = %d, want 1", plan.RemovableCandidates)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w, "openrouter/mine") {
			t.Errorf("warning %q for a kept manual model", w)
		}
	}
}

// TestPlanPricesGraduatesAManualModelOnTheList pins that presence on the
// list, whatever the price, graduates a manual model: a free model (price 0)
// or one OpenRouter cannot price is on the list, and the user's rule is that
// a model never goes back once listed.
func TestPlanPricesGraduatesAManualModelOnTheList(t *testing.T) {
	api := healthyList("vendor/free")
	api["vendor/free"] = APIPrice{Input: f(0), Output: f(0)}
	api["vendor/odd"] = APIPrice{Skipped: []string{"prompt"}}
	plan := PlanPrices([]Entry{manualOR("free"), manualOR("odd")}, api)
	wantIDs(t, "graduations", plan.Graduations, "openrouter/free", "openrouter/odd")
	wantIDs(t, "kept", plan.Kept)
	if len(plan.Matched) != 1 {
		t.Errorf("matched = %d, want only the free model re-priced", len(plan.Matched))
	}
}

// TestPricePlanMassRemoval pins the guard's threshold: more than half of the
// removable OpenRouter models going is refused, half is not.
func TestPricePlanMassRemoval(t *testing.T) {
	two := &PricePlan{Removals: []string{"a", "b"}, RemovableCandidates: 3}
	one := &PricePlan{Removals: []string{"a"}, RemovableCandidates: 3}
	if !two.MassRemoval() || two.EntriesGone() != 2 {
		t.Errorf("2 of 3: MassRemoval = %v, EntriesGone = %d", two.MassRemoval(), two.EntriesGone())
	}
	if one.MassRemoval() {
		t.Error("1 of 3 flagged as a mass removal")
	}
}

// TestPricePlanRemovalDigestIsStable pins the digest as the shared format
// over the sorted removals: the same ids in any order give one value, and a
// plan with none gives "". A digest approved on a dry run must approve the
// same removals later.
func TestPricePlanRemovalDigestIsStable(t *testing.T) {
	a := &PricePlan{Removals: []string{"openrouter/b", "openrouter/a"}}
	b := &PricePlan{Removals: []string{"openrouter/a", "openrouter/b"}}
	if a.RemovalDigest() != b.RemovalDigest() || len(a.RemovalDigest()) != 12 {
		t.Errorf("digests %q %q", a.RemovalDigest(), b.RemovalDigest())
	}
	if want := digestOf([]string{"openrouter/a", "openrouter/b"}); a.RemovalDigest() != want {
		t.Errorf("digest = %q, want digestOf(sorted ids) = %q", a.RemovalDigest(), want)
	}
	if (&PricePlan{}).RemovalDigest() != "" {
		t.Error("an empty plan has a digest")
	}
}

// TestPricePlanFormatRemovalsSection pins the plan's text for removals,
// graduations and kept models, and that none of the sections appears when
// empty (TestPricePlanFormat holds the unchanged text).
func TestPricePlanFormatRemovalsSection(t *testing.T) {
	plan := PlanPrices([]Entry{orEntry("gone", nil), manualOR("mine"), manualOR("now")}, healthyList("vendor/now"))
	got := plan.Format()
	d := plan.RemovalDigest()
	for _, want := range []string{
		"Registry removals — not on openrouter.ai/api/v1/models (1):\n  openrouter/gone",
		"Removal digest: " + d + " (apply non-interactively with `--yes --approve-removals " + d + "`)",
		"Graduated (manual -> curated) (1):\n  openrouter/now",
		"Kept (manual, not on openrouter.ai) (1):\n  openrouter/mine",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Format() lacks %q:\n%s", want, got)
		}
	}
	plain := PlanPrices([]Entry{orEntry("here", nil)}, healthyList("vendor/here")).Format()
	for _, no := range []string{"Registry removals", "Graduated", "Kept", "Removal digest"} {
		if strings.Contains(plain, no) {
			t.Errorf("a plan with none prints %q:\n%s", no, plain)
		}
	}
}

// TestPlanPricesKeepsAVariantOfAListedModel pins that a routing suffix the
// list does not carry as an id (`:nitro`) is not read as the model vanishing:
// the bare id is listed, so the row is kept with a warning.
func TestPlanPricesKeepsAVariantOfAListedModel(t *testing.T) {
	e := orEntry("here:nitro", nil)
	plan := PlanPrices([]Entry{e}, healthyList("vendor/here"))
	wantIDs(t, "removals", plan.Removals)
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "kept") {
		t.Errorf("warnings = %q, want one saying the row is kept", plan.Warnings)
	}
}
