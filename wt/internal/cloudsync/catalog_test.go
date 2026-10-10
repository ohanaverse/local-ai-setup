package cloudsync

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// cloudEntry is an ollama cloud entry under tag, as an earlier sync or a
// hand edit would have left it.
func cloudEntry(tag string, edit ...func(*Entry)) Entry {
	e := Entry{ID: "ollama/" + tag, Family: "fam", ProviderID: "ollama", ModelName: tag, Location: "cloud"}
	for _, fn := range edit {
		fn(&e)
	}
	return e
}

func withCost(c Cost) func(*Entry)        { return func(e *Entry) { e.Cost = &c } }
func withName(name string) func(*Entry)   { return func(e *Entry) { e.CatalogName = name } }
func withFamily(name string) func(*Entry) { return func(e *Entry) { e.Family = name } }
func local(e *Entry)                      { e.Location = "local" }

// cm is a page model at 1.0/0.1/2.0 unless prices are given.
func cm(name string, prices ...float64) CatalogModel {
	p := []float64{1.0, 0.1, 2.0}
	copy(p, prices)
	return CatalogModel{Name: name, Prices: PriceTriple{Input: f(p[0]), Cache: f(p[1]), Output: f(p[2])}}
}

func withOffpeak(m CatalogModel, in, cache, out *float64) CatalogModel {
	m.Offpeak = &PriceTriple{Input: in, Cache: cache, Output: out}
	return m
}

func catalogOf(models ...CatalogModel) Catalog { return Catalog{Models: models} }

// timeRow is a time_prices row with one window, for rows the user wrote.
func timeRow(label, day string) *tomlw.Table {
	w := tomlw.NewTable()
	w.Set("days", []any{day})
	w.Set("start", "00:00")
	w.Set("end", "24:00")
	row := tomlw.NewTable()
	row.Set("label", label)
	row.Set("timezone", "UTC")
	row.Set("windows", []any{w})
	return row
}

func updateIDs(p *CatalogPlan) []string {
	var ids []string
	for _, u := range p.Updates {
		ids = append(ids, u.ModelID)
	}
	return ids
}

func additionIDs(p *CatalogPlan) []string {
	var ids []string
	for _, a := range p.Additions {
		ids = append(ids, a.ID)
	}
	return ids
}

func pullIDs(p *CatalogPlan) []string {
	var ids []string
	for _, pull := range p.Pulls {
		ids = append(ids, pull.ID)
	}
	return ids
}

func labels(rows []*tomlw.Table) []string {
	var out []string
	for _, row := range rows {
		out = append(out, str(row, "label"))
	}
	return out
}

func wantIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestPlanUpdatesExistingAndRecordsTheCatalogName pins the everyday case: an
// entry under the page's tag takes the page's prices, keeps its subscription,
// and is recorded as that page model. Losing the subscription here would
// erase the plan price from every ollama cloud entry on each sync.
func TestPlanUpdatesExistingAndRecordsTheCatalogName(t *testing.T) {
	old := Cost{Input: f(9), SubscriptionPrice: f(100), SubscriptionPeriod: s("month")}
	plan := PlanCatalog([]Entry{cloudEntry("glm-5.3:cloud", withCost(old))}, catalogOf(cm("glm-5.3", 1.4, 0.26, 4.4)), nil, nil)
	if len(plan.Updates) != 1 || len(plan.Additions) != 0 {
		t.Fatalf("updates = %v, additions = %v; want one update", updateIDs(plan), additionIDs(plan))
	}
	u := plan.Updates[0]
	if u.ModelID != "ollama/glm-5.3:cloud" || u.CatalogName != "glm-5.3" || !samePrice(u.After.Input, f(1.4)) ||
		!samePrice(u.After.SubscriptionPrice, f(100)) {
		t.Errorf("update = %+v, want glm-5.3 at 1.4 with the subscription untouched", u)
	}
}

// TestPlanUnchangedWhenPricesMatchAndNameRecorded pins that a second sync of
// the same page plans nothing. An entry that looked "updated" on every run
// would rewrite the registry and restart the LiteLLM proxy each time.
func TestPlanUnchangedWhenPricesMatchAndNameRecorded(t *testing.T) {
	cost := Cost{Input: f(1), Cache: f(0.1), Output: f(2)}
	plan := PlanCatalog([]Entry{cloudEntry("glm-5.3:cloud", withCost(cost), withName("glm-5.3"))}, catalogOf(cm("glm-5.3")), []string{"glm-5.3:cloud"}, nil)
	wantIDs(t, "updates", updateIDs(plan))
	wantIDs(t, "unchanged", plan.Unchanged, "ollama/glm-5.3:cloud")
	if plan.HasWork() {
		t.Error("a plan with nothing to change reports work")
	}

	// An entry with no cost table against a page row with no prices: nothing
	// to write, so nothing changed. A missing cost table and an empty one
	// are the same here; telling them apart would list this entry as an
	// update on every run and never write anything for it.
	bare := PlanCatalog([]Entry{cloudEntry("free:cloud", withName("free"))}, catalogOf(CatalogModel{Name: "free"}), []string{"free:cloud"}, nil)
	wantIDs(t, "updates (no cost, no page prices)", updateIDs(bare))
	wantIDs(t, "unchanged (no cost, no page prices)", bare.Unchanged, "ollama/free:cloud")
	if bare.HasWork() {
		t.Error("an entry with no cost against a page row with no prices reports work")
	}
}

// TestOffpeakRowIsOllamasPublishedWindow pins the one row the ollama flow
// writes into cost.time_prices, as it lands in registry.toml: ollama's
// off-peak window (outside 12:00 to 18:00 UTC on weekdays, all day at
// weekends), keys in schema order, and a price the page
// does not list left out. The window is not parsed from the page, so this is
// where a change to it shows.
func TestOffpeakRowIsOllamasPublishedWindow(t *testing.T) {
	doc := tomlw.NewTable()
	doc.Set("time_prices", []any{offpeakRow(f(0.66), nil, f(1.98))})
	got, err := tomlw.Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[[time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.66
output_price_per_million = 1.98

[[time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "00:00"
end = "12:00"

[[time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "18:00"
end = "24:00"

[[time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`
	if string(got) != want {
		t.Errorf("off-peak row =\n%s\nwant\n%s", got, want)
	}
}

// TestPlanOffpeakSetReplaceRemoveKeepsOtherRows pins that the ollama flow
// owns exactly one time_prices row, the one labelled off-peak: it is set when
// the page has off-peak prices and removed when it does not, and a row the
// user wrote under another label survives both.
func TestPlanOffpeakSetReplaceRemoveKeepsOtherRows(t *testing.T) {
	cost := Cost{Input: f(1), TimePrices: []*tomlw.Table{timeRow("mine", "sun"), timeRow("off-peak", "sat")}}
	entries := []Entry{cloudEntry("a:cloud", withCost(cost)), cloudEntry("b:cloud", withCost(cost))}
	plan := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.5), nil, f(1)), cm("b")), nil, nil)
	after := map[string]Cost{}
	for _, u := range plan.Updates {
		after[u.ModelID] = u.After
	}
	a := after["ollama/a:cloud"].TimePrices
	wantIDs(t, "a's rows", labels(a), "mine", "off-peak")
	if !samePrice(number(a[1], "input_price_per_million"), f(0.5)) || a[1].Has("cache_price_per_million") {
		t.Errorf("a's off-peak row has the wrong prices: keys %v", a[1].Keys())
	}
	windows, _ := a[1].Get("windows")
	if n := len(windows.([]any)); n != 3 {
		t.Errorf("a's off-peak row has %d windows, want ollama's 3 (the stale Saturday-only row replaced)", n)
	}
	wantIDs(t, "b's rows", labels(after["ollama/b:cloud"].TimePrices), "mine")
}

// TestPlanOffpeakReplacedInPlaceAndUnchangedWhenSame pins two things about
// the off-peak row: it stays where it stands among the other rows (the first
// matching row wins, so moving it changes which price applies), and a row
// that already says what the page says is not an update.
func TestPlanOffpeakReplacedInPlaceAndUnchangedWhenSame(t *testing.T) {
	cost := Cost{Input: f(1), Cache: f(0.1), Output: f(2),
		TimePrices: []*tomlw.Table{offpeakRow(f(0.5), f(0.05), f(1)), timeRow("holiday", "sun")}}
	entries := []Entry{cloudEntry("a:cloud", withCost(cost), withName("a"))}

	same := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.5), f(0.05), f(1))), nil, nil)
	wantIDs(t, "updates when the row matches", updateIDs(same))
	wantIDs(t, "unchanged", same.Unchanged, "ollama/a:cloud")

	changed := PlanCatalog(entries, catalogOf(withOffpeak(cm("a"), f(0.4), f(0.04), f(0.8))), nil, nil)
	if len(changed.Updates) != 1 {
		t.Fatalf("updates = %v, want one", updateIDs(changed))
	}
	rows := changed.Updates[0].After.TimePrices
	wantIDs(t, "rows", labels(rows), "off-peak", "holiday")
	if !samePrice(number(rows[0], "input_price_per_million"), f(0.4)) {
		t.Error("the off-peak row was not given the page's new price")
	}
}

// TestPlanMatchesByCatalogNameAfterRename pins that an entry whose tag the
// user renamed is still that page model: it is updated, and neither it nor
// its pulled tag is treated as off the page and removed.
func TestPlanMatchesByCatalogNameAfterRename(t *testing.T) {
	entries := []Entry{cloudEntry("glm-5.3-renamed:cloud", withName("glm-5.3"))}
	plan := PlanCatalog(entries, catalogOf(cm("glm-5.3")), []string{"glm-5.3-renamed:cloud"}, nil)
	wantIDs(t, "updates", updateIDs(plan), "ollama/glm-5.3-renamed:cloud")
	wantIDs(t, "additions", additionIDs(plan))
	wantIDs(t, "pulls", pullIDs(plan))
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanCatalogNameMatchKeepsTheCanonicalTagListed pins the other half of
// a rename: with both the renamed tag and the page's own tag pulled, neither
// is a stray to `ollama rm`.
func TestPlanCatalogNameMatchKeepsTheCanonicalTagListed(t *testing.T) {
	entries := []Entry{cloudEntry("gpt-oss:120b-cloud-custom", withName("gpt-oss:120b"))}
	plan := PlanCatalog(entries, catalogOf(cm("gpt-oss:120b")), []string{"gpt-oss:120b-cloud", "gpt-oss:120b-cloud-custom"}, nil)
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanAdditionsFamilyAndSubscription pins what a new entry is given: the
// family of a namesake already in the registry (so the cloud and local sizes
// of one model group together in the picker), else the name's stem; and the
// subscription every existing ollama cloud entry shares.
func TestPlanAdditionsFamilyAndSubscription(t *testing.T) {
	sub := Cost{SubscriptionPrice: f(100), SubscriptionPeriod: s("month")}
	entries := []Entry{
		cloudEntry("gpt-oss:20b", withFamily("gpt-oss"), local),
		cloudEntry("glm-5.2:cloud", withFamily("glm"), withCost(sub)),
	}
	plan := PlanCatalog(entries, catalogOf(cm("glm-5.2"), cm("gpt-oss:120b"), cm("kimi-k3")), nil, nil)
	added := map[string]Addition{}
	for _, a := range plan.Additions {
		added[a.ID] = a
	}
	gpt := added["ollama/gpt-oss:120b-cloud"]
	if gpt.Family != "gpt-oss" || gpt.ModelName != "gpt-oss:120b-cloud" || gpt.CatalogName != "gpt-oss:120b" || gpt.CloneOf != "" {
		t.Errorf("gpt-oss:120b addition = %+v", gpt)
	}
	if !samePrice(gpt.Cost.SubscriptionPrice, f(100)) || gpt.Cost.SubscriptionPeriod == nil || *gpt.Cost.SubscriptionPeriod != "month" {
		t.Errorf("the new entry did not inherit the shared subscription: %+v", gpt.Cost)
	}
	if got := added["ollama/kimi-k3:cloud"].Family; got != "kimi-k3" {
		t.Errorf("kimi-k3 family = %q, want its own name", got)
	}
}

// TestPlanSubscriptionDisagreementWarns pins that when the existing entries
// disagree on the subscription, a new entry gets none and the plan says so:
// guessing one of two prices would show a wrong monthly cost in the picker.
func TestPlanSubscriptionDisagreementWarns(t *testing.T) {
	entries := []Entry{
		cloudEntry("a:cloud", withCost(Cost{SubscriptionPrice: f(100), SubscriptionPeriod: s("month")})),
		cloudEntry("b:cloud", withCost(Cost{SubscriptionPrice: f(20), SubscriptionPeriod: s("month")})),
	}
	plan := PlanCatalog(entries, catalogOf(cm("a"), cm("b"), cm("new")), nil, nil)
	if len(plan.Additions) != 1 || plan.Additions[0].Cost.SubscriptionPrice != nil {
		t.Fatalf("additions = %+v, want one with no subscription", plan.Additions)
	}
	if want := []string{"ollama cloud entries disagree on subscription pricing; new entries get none"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}
}

// TestPlanDoesNotTouchALocalNamesake pins the rule the whole flow rests on:
// a real local model is never updated, removed or `ollama rm`'d, even when
// the page lists a cloud model of the same name and size.
func TestPlanDoesNotTouchALocalNamesake(t *testing.T) {
	entries := []Entry{cloudEntry("gpt-oss:20b", withFamily("gpt-oss"), local)}
	plan := PlanCatalog(entries, catalogOf(cm("gpt-oss:20b")), []string{"gpt-oss:20b"}, nil)
	wantIDs(t, "updates", updateIDs(plan))
	wantIDs(t, "additions", additionIDs(plan), "ollama/gpt-oss:20b-cloud")
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanPullsPageModelsNotInOllamaList pins the pull list: every page
// model whose tag `ollama list` lacks, existing entry or new, and none that
// is already pulled.
func TestPlanPullsPageModelsNotInOllamaList(t *testing.T) {
	plan := PlanCatalog([]Entry{cloudEntry("a:cloud"), cloudEntry("b:cloud")}, catalogOf(cm("a"), cm("b"), cm("c")), []string{"a:cloud"}, nil)
	want := []Pull{{ID: "ollama/b:cloud", Tag: "b:cloud"}, {ID: "ollama/c:cloud", Tag: "c:cloud"}}
	if !reflect.DeepEqual(plan.Pulls, want) {
		t.Errorf("pulls = %v, want %v", plan.Pulls, want)
	}
}

// TestPlanRemovesOffPageCloudEntriesAndStrayStubs pins the mirror's other
// direction: a cloud entry the page no longer lists goes whether or not it is
// pulled, a pulled cloud stub with no entry is a stray to remove, and a local
// model is in neither list.
func TestPlanRemovesOffPageCloudEntriesAndStrayStubs(t *testing.T) {
	entries := []Entry{cloudEntry("old:cloud"), cloudEntry("gone:cloud"), cloudEntry("keep:cloud"), cloudEntry("ornith-1.5:35b", local)}
	plan := PlanCatalog(entries, catalogOf(cm("keep")), []string{"old:cloud", "stray:cloud", "ornith-1.5:35b", "keep:cloud"}, nil)
	wantIDs(t, "removals", plan.Removals, "ollama/old:cloud", "ollama/gone:cloud")
	wantIDs(t, "stray tags", plan.StrayTags, "stray:cloud")
	wantIDs(t, "pulls", pullIDs(plan))
}

// TestPlanWarnsWhenARemovedEntrysTagIsNotACloudTag pins what the plan says
// about an entry marked location = "cloud" whose model_name is a local tag
// (a mislabelled hand edit). The page does not list it, so the entry goes
// from the registry, but its tag is real weights, not a cloud stub: the plan
// must say, before anyone approves it, that the tag stays in ollama. The
// removal digest is unchanged by the warning: it covers what is deleted, and
// a digest approved before the warning existed still approves the same plan.
func TestPlanWarnsWhenARemovedEntrysTagIsNotACloudTag(t *testing.T) {
	entries := []Entry{cloudEntry("keep:cloud"), cloudEntry("qwen3:8b"), cloudEntry("gone:cloud")}
	plan := PlanCatalog(entries, catalogOf(cm("keep")), []string{"keep:cloud", "qwen3:8b", "gone:cloud"}, nil)
	wantIDs(t, "removals", plan.Removals, "ollama/qwen3:8b", "ollama/gone:cloud")
	want := []string{`ollama/qwen3:8b: its tag "qwen3:8b" is not a cloud tag; the entry is removed from the registry and the tag is left in ollama`}
	if !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q\nwant       %q", plan.Warnings, want)
	}
	if !strings.Contains(plan.Format(), "warning: "+want[0]) {
		t.Errorf("the printed plan lacks the warning:\n%s", plan.Format())
	}
	silent := &CatalogPlan{Removals: plan.Removals}
	if plan.RemovalDigest() != silent.RemovalDigest() {
		t.Error("the warning changed the removal digest")
	}
}

// TestPlanMassRemovalGuard pins the second half of the safety net: a plan
// that would remove more than half the ollama cloud entries is flagged, and
// exactly half is not. Such a plan is far more likely a page that parsed
// wrong than a catalog that shrank, and the command refuses it without
// --force.
func TestPlanMassRemovalGuard(t *testing.T) {
	entries := []Entry{cloudEntry("a:cloud"), cloudEntry("b:cloud"), cloudEntry("c:cloud"), cloudEntry("d:cloud")}
	if PlanCatalog(entries, catalogOf(cm("a"), cm("b")), nil, nil).MassRemoval() {
		t.Error("removing 2 of 4 entries was flagged; the guard is for more than half")
	}
	if !PlanCatalog(entries, catalogOf(cm("a")), nil, nil).MassRemoval() {
		t.Error("removing 3 of 4 entries was not flagged")
	}
}

// TestPlanIDCollisionWarns pins that an id the page would add but another
// row already holds is left alone with a warning that says who holds it. The
// alternative, a duplicate id, is a registry wt refuses to load. The owner's
// tag is printed in single quotes ('foo:cloud-old'), the fixed text of this
// warning; the user reads it to find the row that holds the id.
func TestPlanIDCollisionWarns(t *testing.T) {
	squatter := Entry{ID: "ollama/x:cloud", Family: "f", ProviderID: "other", ModelName: "zzz"}
	plan := PlanCatalog([]Entry{squatter}, catalogOf(cm("x")), nil, nil)
	wantIDs(t, "additions", additionIDs(plan))
	if want := []string{"ollama/x:cloud already exists on another provider; not adding"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}

	renamed := Entry{ID: "ollama/foo:cloud", Family: "f", ProviderID: "ollama", ModelName: "foo:cloud-old"}
	plan = PlanCatalog([]Entry{renamed}, catalogOf(cm("foo")), nil, nil)
	wantIDs(t, "additions", additionIDs(plan))
	if want := []string{"ollama/foo:cloud already exists with model_name 'foo:cloud-old'; not adding"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}
}

// TestPlanNeverAddsTheSameIDTwice pins the guard for two page rows that
// resolve to one tag: the second is a warning, not a second row with the
// same id.
func TestPlanNeverAddsTheSameIDTwice(t *testing.T) {
	plan := PlanCatalog(nil, catalogOf(cm("x"), cm("y")), nil, map[string]string{"x": "same:cloud", "y": "same:cloud"})
	wantIDs(t, "additions", additionIDs(plan), "ollama/same:cloud")
	if want := []string{"ollama/same:cloud already exists as an earlier addition from this page; not adding"}; !reflect.DeepEqual(plan.Warnings, want) {
		t.Errorf("warnings = %q, want %q", plan.Warnings, want)
	}
}

// TestPlanUnrecognizedCellKeepsTheExistingPrice pins the planner's half of
// the unknown-cell rule: one column changing format (below the whole-page
// threshold) must not overwrite a known price with nothing. Only a real "-"
// cell clears a price.
func TestPlanUnrecognizedCellKeepsTheExistingPrice(t *testing.T) {
	catalog, err := ParsePricing(page(pageHead,
		pageRow("zz", "$1.00", "$0.10 / 1M", "$2.00"),
		pageRow("yy", "$1.00", "-", "$2.00"),
		pageRow("ww", "$1.00", "$0.10", "$2.00"),
		pageRow("ww (Off-Peak)", "$0.50", "$0.05*", "$1.00"),
	))
	if err != nil {
		t.Fatal(err)
	}
	old := Cost{Input: f(9), Cache: f(0.5), Output: f(9)}
	withOld := Cost{Input: f(1), TimePrices: []*tomlw.Table{offpeakRow(f(0.4), f(0.04), f(0.8))}}
	entries := []Entry{cloudEntry("zz:cloud", withCost(old)), cloudEntry("yy:cloud", withCost(old)), cloudEntry("ww:cloud", withCost(withOld))}
	after := map[string]Cost{}
	for _, u := range PlanCatalog(entries, catalog, nil, nil).Updates {
		after[u.ModelID] = u.After
	}
	if zz := after["ollama/zz:cloud"]; !samePrice(zz.Cache, f(0.5)) || !samePrice(zz.Input, f(1)) {
		t.Errorf("zz = %s/%s, want the page's input and the kept cache price 0.5", num(zz.Input), num(zz.Cache))
	}
	if yy := after["ollama/yy:cloud"]; yy.Cache != nil {
		t.Errorf("yy cache = %s, want it cleared by the \"-\" cell", num(yy.Cache))
	}
	row := after["ollama/ww:cloud"].TimePrices[0]
	if !samePrice(number(row, "input_price_per_million"), f(0.5)) || !samePrice(number(row, "cache_price_per_million"), f(0.04)) {
		t.Errorf("ww off-peak row keys %v: want the page's input 0.5 and the kept cache 0.04", row.Keys())
	}
}

// TestPlanUsesTheResolvedTag pins that additions and pulls use the tag the
// library lookup found, not the :cloud guess.
func TestPlanUsesTheResolvedTag(t *testing.T) {
	plan := PlanCatalog(nil, catalogOf(cm("mistral-large-3")), nil, map[string]string{"mistral-large-3": "mistral-large-3:675b-cloud"})
	if len(plan.Additions) != 1 || plan.Additions[0].ID != "ollama/mistral-large-3:675b-cloud" || plan.Additions[0].ModelName != "mistral-large-3:675b-cloud" {
		t.Errorf("additions = %+v", plan.Additions)
	}
	if want := []Pull{{ID: "ollama/mistral-large-3:675b-cloud", Tag: "mistral-large-3:675b-cloud"}}; !reflect.DeepEqual(plan.Pulls, want) {
		t.Errorf("pulls = %v, want %v", plan.Pulls, want)
	}
}

// TestPlanRetagsAnEntryUnderAGuessedTag pins the re-tag: an entry an earlier
// sync filed under a tag ollama does not publish is removed and re-added
// under the real one, as a copy (CloneOf) that keeps its family and its
// subscription and takes the page's prices. It is one model changing id, so
// it must not count toward the mass-removal guard.
func TestPlanRetagsAnEntryUnderAGuessedTag(t *testing.T) {
	old := cloudEntry("mistral-large-3:cloud", withFamily("mistral"), withName("mistral-large-3"),
		withCost(Cost{Input: f(9), SubscriptionPrice: f(7), SubscriptionPeriod: s("month")}))
	plan := PlanCatalog([]Entry{old}, catalogOf(cm("mistral-large-3")), nil, map[string]string{"mistral-large-3": "mistral-large-3:675b-cloud"})
	wantIDs(t, "removals", plan.Removals, "ollama/mistral-large-3:cloud")
	wantIDs(t, "updates", updateIDs(plan))
	if len(plan.Additions) != 1 {
		t.Fatalf("additions = %+v, want one", plan.Additions)
	}
	add := plan.Additions[0]
	if add.ID != "ollama/mistral-large-3:675b-cloud" || add.CloneOf != "ollama/mistral-large-3:cloud" || add.Family != "mistral" {
		t.Errorf("addition = %+v", add)
	}
	if !samePrice(add.Cost.Input, f(1)) || !samePrice(add.Cost.SubscriptionPrice, f(7)) {
		t.Errorf("addition cost = %+v, want the page's price and the old entry's subscription", add.Cost)
	}
	if want := map[string]string{"ollama/mistral-large-3:cloud": "ollama/mistral-large-3:675b-cloud"}; !reflect.DeepEqual(plan.Replaced, want) {
		t.Errorf("replaced = %v, want %v", plan.Replaced, want)
	}
	if plan.MassRemoval() {
		t.Error("a re-tag of the only entry was flagged as a mass removal")
	}
}

// TestPlanRetagOfAnEntryWithNoCostTakesTheSharedSubscription pins the one
// case where a re-tagged entry has nothing to carry over: it gets what a new
// entry would.
func TestPlanRetagOfAnEntryWithNoCostTakesTheSharedSubscription(t *testing.T) {
	entries := []Entry{
		cloudEntry("x:cloud", withName("x")),
		cloudEntry("y:cloud", withName("y"), withCost(Cost{SubscriptionPrice: f(100), SubscriptionPeriod: s("month")})),
	}
	plan := PlanCatalog(entries, catalogOf(cm("x"), cm("y")), nil, map[string]string{"x": "x:675b-cloud", "y": "y:cloud"})
	wantIDs(t, "removals", plan.Removals, "ollama/x:cloud")
	if len(plan.Additions) != 1 || plan.Additions[0].CloneOf != "ollama/x:cloud" || plan.Additions[0].Cost.SubscriptionPrice != nil {
		t.Errorf("additions = %+v, want x re-tagged with no subscription (the entries disagree: one has none)", plan.Additions)
	}
}

// TestPlanUnresolvedModelKeepsItsEntryButSkipsPullAndAdd pins what "tag
// unknown" means: the model is still on the page, so its entry keeps getting
// prices and nothing with its name is removed, but nothing is added or pulled
// under a tag nobody verified.
func TestPlanUnresolvedModelKeepsItsEntryButSkipsPullAndAdd(t *testing.T) {
	entries := []Entry{cloudEntry("x:cloud", withName("x"))}
	plan := PlanCatalog(entries, catalogOf(cm("x", 5), cm("y")), nil, map[string]string{"x": "", "y": ""})
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "additions", additionIDs(plan))
	wantIDs(t, "pulls", pullIDs(plan))
	wantIDs(t, "updates", updateIDs(plan), "ollama/x:cloud")
}

// TestPlanPrefersTheEntryAlreadyUnderTheResolvedTag pins that a guessed-tag
// entry carrying the catalog name does not shadow the entry already under the
// real tag: only the guessed one goes, and the real one is updated.
func TestPlanPrefersTheEntryAlreadyUnderTheResolvedTag(t *testing.T) {
	entries := []Entry{cloudEntry("ml3:cloud", withName("ml3")), cloudEntry("ml3:675b-cloud")}
	plan := PlanCatalog(entries, catalogOf(cm("ml3")), []string{"ml3:675b-cloud"}, map[string]string{"ml3": "ml3:675b-cloud"})
	wantIDs(t, "removals", plan.Removals, "ollama/ml3:cloud")
	wantIDs(t, "additions", additionIDs(plan))
	wantIDs(t, "updates", updateIDs(plan), "ollama/ml3:675b-cloud")
	wantIDs(t, "stray tags", plan.StrayTags)
}

// TestPlanUnresolvedModelProtectsItsPulledStubAndSizedEntry pins the
// protection around a failed lookup: neither a pulled stub with the model's
// name nor an entry under a sized tag may be removed, because either may be
// the model whose tag could not be read. A tag with another name still goes.
func TestPlanUnresolvedModelProtectsItsPulledStubAndSizedEntry(t *testing.T) {
	entries := []Entry{cloudEntry("foo:1t-cloud")}
	pulled := []string{"glm-5.3:cloud", "foo:1t-cloud", "other:cloud"}
	plan := PlanCatalog(entries, catalogOf(cm("foo"), cm("glm-5.3")), pulled, map[string]string{"foo": "", "glm-5.3": ""})
	wantIDs(t, "removals", plan.Removals)
	wantIDs(t, "stray tags", plan.StrayTags, "other:cloud")
	wantIDs(t, "updates", updateIDs(plan), "ollama/foo:1t-cloud")
}

// TestRemovalDigestIsAFixedFormat pins the digest on fixed vectors: the
// first 12 hex digits of the SHA-256 of the sorted removals and the sorted
// "rm <tag>" lines. A digest printed by one run's dry run is what the user
// passes to a later run's --approve-removals, across wt versions too, so a
// change to the format would refuse an approval of the very plan it was
// printed for; the order the plan lists its removals in must not matter.
func TestRemovalDigestIsAFixedFormat(t *testing.T) {
	cases := []struct {
		removals, strays []string
		want             string
	}{
		{nil, nil, ""},
		{[]string{"ollama/b:8b-cloud", "ollama/a:cloud"}, []string{"z:cloud"}, "a4335c3c2fb0"},
		{[]string{"ollama/a:cloud", "ollama/b:8b-cloud"}, []string{"z:cloud"}, "a4335c3c2fb0"},
		{[]string{"ollama/gone:cloud"}, nil, "0bcc560b956e"},
		{nil, []string{"stray:cloud"}, "cedb1eab545d"},
		{[]string{"ollama/é:cloud", "ollama/Z:cloud", "ollama/a:cloud"}, nil, "ea46b1389797"},
	}
	for _, tc := range cases {
		plan := &CatalogPlan{Removals: tc.removals, StrayTags: tc.strays}
		if got := plan.RemovalDigest(); got != tc.want {
			t.Errorf("RemovalDigest(%v, %v) = %q, want %q", tc.removals, tc.strays, got, tc.want)
		}
	}
}

// TestFormatPriceMatchesPythonsG pins the price format to Python's `{v:g}`
// on values computed there. The plan's text is what the user approves and
// what the command compares before applying, so a price that printed
// differently from one run to the next would read as a changed plan.
func TestFormatPriceMatchesPythonsG(t *testing.T) {
	cases := map[float64]string{
		1.32: "1.32", 0.044: "0.044", 15: "15", 0: "0", 0.015: "0.015", 100000: "100000",
		1000000: "1e+06", 123456789: "1.23457e+08", 1234567: "1.23457e+06", 999999.5: "1e+06",
		0.0001: "0.0001", 0.00001: "1e-05", 1e16: "1e+16",
		2.4999999999999996: "2.5", 0.09999999999999999: "0.1",
	}
	for v, want := range cases {
		if got := formatPrice(&v); got != want {
			t.Errorf("formatPrice(%v) = %q, want %q", v, got, want)
		}
	}
	if got := formatPrice(nil); got != "-" {
		t.Errorf("formatPrice(nil) = %q, want -", got)
	}
}

// TestCatalogPlanFormat pins the whole printed plan for a run with one of
// everything. A user approves this text and the cloud-sync skill reads the
// digest line out of it, so a reworded line is a changed contract with both.
func TestCatalogPlanFormat(t *testing.T) {
	entries := []Entry{
		cloudEntry("a:cloud", withCost(Cost{Input: f(9), TimePrices: []*tomlw.Table{offpeakRow(f(0.4), nil, nil)}})),
		cloudEntry("gone:cloud"),
		cloudEntry("m:cloud", withName("m")),
		cloudEntry("same:cloud", withName("same"), withCost(Cost{Input: f(1), Cache: f(0.1), Output: f(2)})),
	}
	catalog := catalogOf(withOffpeak(cm("a", 1.32, 0.044, 3.96), f(0.66), f(0.022), f(1.98)), cm("b"), cm("m"), cm("same"), cm("lost"))
	catalog.Warnings = []string{"from the page"}
	resolved := map[string]string{"a": "a:cloud", "b": "b:cloud", "m": "m:675b-cloud", "same": "same:cloud", "lost": ""}
	plan := PlanCatalog(entries, catalog, []string{"gone:cloud", "stray:cloud", "same:cloud"}, resolved)
	want := strings.Join([]string{
		"ollama.com/pricing: 5 models (prices are input/cached/output per million tokens)",
		"Price updates (1):",
		"  ollama/a:cloud: 9/-/- (off-peak 0.4/-/-) -> 1.32/0.044/3.96 (off-peak 0.66/0.022/1.98)",
		"Registry additions (2):",
		"  ollama/b:cloud [family b]: 1/0.1/2",
		"  ollama/m:675b-cloud [family fam]: 1/0.1/2",
		"Unchanged prices: 1",
		"ollama pull (3):",
		"  ollama/a:cloud",
		"  ollama/b:cloud",
		"  ollama/m:675b-cloud",
		"Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; `ollama rm` if pulled (2):",
		"  ollama/gone:cloud",
		"  ollama/m:cloud (re-tagged as ollama/m:675b-cloud)",
		"ollama rm — pulled, unregistered, off the page (1):",
		"  stray:cloud",
		"Removal digest: " + plan.RemovalDigest() + " (apply non-interactively with `--yes --approve-removals " + plan.RemovalDigest() + "`)",
		"warning: from the page",
	}, "\n")
	if got := plan.Format(); got != want {
		t.Errorf("Format() =\n%s\n\nwant\n%s", got, want)
	}
	if len(plan.RemovalDigest()) != 12 {
		t.Errorf("digest = %q, want 12 hex digits", plan.RemovalDigest())
	}
}

// TestPlanCatalogIsDeterministic pins what the apply step relies on: the
// same inputs give the same printed plan every time. The command re-plans
// under the registry lock and refuses when the text differs from what was
// printed, so a plan that varied with map order would refuse at random.
func TestPlanCatalogIsDeterministic(t *testing.T) {
	entries := []Entry{cloudEntry("a:cloud"), cloudEntry("b:cloud"), cloudEntry("c:cloud", withName("c")), cloudEntry("d:cloud")}
	catalog := catalogOf(cm("c"), cm("e"), cm("f"), cm("g"))
	resolved := map[string]string{"c": "c:9b-cloud", "e": "e:cloud", "f": "f:cloud", "g": ""}
	pulled := []string{"x:cloud", "y:cloud", "a:cloud"}
	first := PlanCatalog(entries, catalog, pulled, resolved).Format()
	for range 50 {
		if got := PlanCatalog(entries, catalog, pulled, resolved).Format(); got != first {
			t.Fatalf("the plan changed between two runs on the same inputs:\n%s\n\nvs\n%s", first, got)
		}
	}
}
