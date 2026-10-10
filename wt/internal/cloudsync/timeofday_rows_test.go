package cloudsync

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// scheduleRows is the cost.time_prices rows a plan gives the one model of a
// response whose pricing object is pricing.
func scheduleRows(t *testing.T, pricing string) []*tomlw.Table {
	t.Helper()
	plan := PlanPrices([]Entry{orEntry("m", nil)}, apiOf(t, one(pricing)))
	if len(plan.Matched) != 1 {
		t.Fatalf("plan = %s", plan.Format())
	}
	return plan.Matched[0].After.TimePrices
}

// TestScheduleRows pins how a schedule is written as cost.time_prices rows,
// in the registry's own vocabulary, which is not OpenRouter's: UTC, days as
// mon..sun, times as "HH:MM", the end of the day as "24:00", and each day's
// hours under that day, so a window of OpenRouter's that wraps past midnight
// is two. The registry would hold it as one (a window may run past midnight,
// #322), but the rows are read back from the week minute by minute, not
// copied from OpenRouter's list, and one day at a time is the one form every
// list that prices the week the same way comes out as: the same bytes and
// the same plan text whatever order, grouping or wrapping OpenRouter
// publishes. A row that the registry's loader refused would make every
// launch fail until it was removed by hand.
func TestScheduleRows(t *testing.T) {
	// OpenRouter's own example of a wrapping window: 16:30 to 00:30 UTC.
	rows := scheduleRows(t, `{"prompt": "0.00000014", "completion": "0.00000021", "overrides": [
		{"utc_start": 30, "utc_end": 1630, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_start": 1630, "utc_end": 30, "prompt": "0.00000014", "completion": "0.00000021"}
	]}`)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	// Keys in schema order: a table is written in the order its keys were
	// set. No cache key, because no window has a cached-input price.
	if got, want := rows[0].Keys(), []string{"label", "timezone", "input_price_per_million", "output_price_per_million", "windows"}; !reflect.DeepEqual(got, want) {
		t.Errorf("row keys = %v, want %v", got, want)
	}
	if str(rows[0], "label") != "openrouter" || str(rows[0], "timezone") != "UTC" ||
		!samePrice(number(rows[0], "input_price_per_million"), pm("0.00000014")) || !samePrice(number(rows[0], "output_price_per_million"), pm("0.00000021")) {
		t.Errorf("row = %v", rows[0])
	}
	raw, _ := rows[0].Get("windows")
	windows := raw.([]any)
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want the wrapping window as two", len(windows))
	}
	if got, want := windows[0].(*tomlw.Table).Keys(), []string{"days", "start", "end"}; !reflect.DeepEqual(got, want) {
		t.Errorf("window keys = %v, want %v", got, want)
	}
	days, _ := windows[1].(*tomlw.Table).Get("days")
	if want := []any{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}; !reflect.DeepEqual(days, want) {
		t.Errorf("days = %v, want %v", days, want)
	}
	if got, want := formatWindows(rows[0]), "mon-sun 00:00-00:30 16:30-24:00"; got != want {
		t.Errorf("windows = %q, want %q", got, want)
	}

	// Days with different times get their own windows, grouped by the times
	// they share; a day with none of this price is not named.
	rows = scheduleRows(t, `{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_days": ["monday", "wednesday", "friday"], "utc_start": 900, "utc_end": 1700, "prompt": "0.000002", "completion": "0.000004"},
		{"utc_days": ["monday", "wednesday", "friday"], "utc_start": 1700, "utc_end": 900, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_days": ["tuesday", "thursday"], "prompt": "0.000002", "completion": "0.000004"},
		{"utc_days": ["saturday", "sunday"], "prompt": "0.000001", "completion": "0.000002"}
	]}`)
	if len(rows) != 1 || formatWindows(rows[0]) != "mon,wed,fri 00:00-09:00 17:00-24:00, sat-sun 00:00-24:00" {
		t.Errorf("rows = %d, windows %q", len(rows), formatWindows(rows[0]))
	}

	// One row per level below the dearest, dearest first.
	rows = scheduleRows(t, `{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 800, "utc_end": 2000, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 1400, "prompt": "0.000002", "completion": "0.000004"}
	]}`)
	if len(rows) != 2 || formatWindows(rows[0]) != "mon-sun 12:00-14:00" || formatWindows(rows[1]) != "mon-sun 00:00-08:00 20:00-24:00" ||
		!samePrice(number(rows[0], "input_price_per_million"), f(2)) || !samePrice(number(rows[1], "input_price_per_million"), f(1)) {
		t.Errorf("three levels: %d rows", len(rows))
	}

	// Every window at one price, a prompt-size tier, no overrides: no row.
	for _, pricing := range []string{
		`{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}]}`,
		`{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"min_prompt_tokens": 200000, "prompt": "0.000002", "completion": "0.000004"}]}`,
		`{"prompt": "0.000001", "completion": "0.000002"}`,
	} {
		if rows := scheduleRows(t, pricing); rows != nil {
			t.Errorf("%s: %d rows, want none", pricing, len(rows))
		}
	}
}

// TestPlanPricesOwnsOnlyItsOwnRows pins whose cost.time_prices rows are
// whose. The openrouter flow owns the rows labelled openrouter and no other:
// it replaces them where the first one stood (rows resolve first match wins,
// so moving one changes which price is in force) and drops them when the
// model no longer has a schedule. A row the user wrote, under any other
// label, is the very table it was read as, which is what lets the write
// leave its bytes alone. The ollama flow never plans from an openrouter
// model (TestTheTwoFlowsNeverShareAModel); what it does with a row labelled
// openrouter that someone wrote by hand on an ollama entry is keep it, as it
// keeps every row that is not its off-peak one.
func TestPlanPricesOwnsOnlyItsOwnRows(t *testing.T) {
	mine, offpeak := timeRow("mine", "sun"), timeRow("off-peak", "sat")
	rows := func() []*tomlw.Table {
		return []*tomlw.Table{mine, timeRow("openrouter", "mon"), offpeak, timeRow("openrouter", "tue")}
	}
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	entry := func(name string, cost *Cost) Entry {
		return Entry{ID: "openrouter/" + name, ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: cost}
	}

	// A model with a schedule: the two stale rows become the one current row,
	// where the first stood.
	plan := PlanPrices([]Entry{entry("tencent/hy3", &Cost{TimePrices: rows()})}, api)
	if len(plan.Matched) != 1 {
		t.Fatalf("plan = %s", plan.Format())
	}
	after := plan.Matched[0].After.TimePrices
	wantIDs(t, "rows of a model with a schedule", labels(after), "mine", "openrouter", "off-peak")
	if after[0] != mine || after[2] != offpeak {
		t.Error("a row the openrouter flow does not own is not the table it was read as")
	}
	if got := formatWindows(after[1]); got != "mon-sun 16:00-24:00" {
		t.Errorf("the schedule row's windows = %q", got)
	}
	// Planned again from its own result, nothing is left to change.
	if again := PlanPrices([]Entry{entry("tencent/hy3", &plan.Matched[0].After)}, api); again.Matched[0].Changed {
		t.Errorf("a second plan still changes the model: %s", again.Format())
	}

	// A model with no schedule (any more): its openrouter rows go, and the
	// plan shows that they do.
	plan = PlanPrices([]Entry{entry("z-ai/glm-5.2", &Cost{Input: pm("0.00000006"), Cache: pm("0.000000059"), Output: pm("0.000006"), TimePrices: rows()})}, api)
	wantIDs(t, "rows of a model with no schedule", labels(plan.Matched[0].After.TimePrices), "mine", "off-peak")
	if want := "  openrouter/z-ai/glm-5.2: 0.06/0.059/6 (off-peak -/-/-) (openrouter -/-/- mon 00:00-24:00) (openrouter -/-/- tue 00:00-24:00) -> 0.06/0.059/6 (off-peak -/-/-)"; !strings.Contains(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant the line\n%s", plan.Format(), want)
	}

	// A model with neither: the list is the one it had.
	kept := []*tomlw.Table{mine, offpeak}
	plan = PlanPrices([]Entry{entry("z-ai/glm-5.2", &Cost{TimePrices: kept})}, api)
	if got := plan.Matched[0].After.TimePrices; len(got) != 2 || got[0] != mine || got[1] != offpeak {
		t.Errorf("rows of a model with nothing of the flow's = %v", labels(got))
	}

	// A row labelled openrouter on an ollama cloud entry is not the
	// openrouter flow's (that flow never sees the entry): to the ollama flow
	// it is one more row that is not its off-peak one, and is kept.
	cost := Cost{Input: f(1), TimePrices: rows()}
	catalog := PlanCatalog([]Entry{cloudEntry("a:cloud", withCost(cost))}, catalogOf(withOffpeak(cm("a"), f(0.5), nil, f(1))), nil, nil)
	wantIDs(t, "rows after the ollama flow", labels(catalog.Updates[0].After.TimePrices), "mine", "openrouter", "off-peak", "openrouter")
}

// TestPricePlanFormatShowsASchedule pins the plan's text for a model with a
// schedule: the flat price, then each stored level with its windows. The
// windows are printed because they are written: a plan that changed only a
// row's hours would otherwise read `x -> x`, and the rule is that nothing is
// written that the printed plan does not show.
func TestPricePlanFormatShowsASchedule(t *testing.T) {
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	entry := func(id, name string, cost *Cost) Entry {
		return Entry{ID: id, ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: cost}
	}
	const deepseek = "deepseek/deepseek-v4-pro-0813"
	// What a sync before the fix left, in each window.
	low := &Cost{Input: pm("0.00000066"), Cache: pm("0.000000022"), Output: pm("0.00000198")}
	high := &Cost{Input: pm("0.00000132"), Cache: pm("0.000000044"), Output: pm("0.00000396")}
	// A row an earlier sync wrote, for a schedule that has since moved.
	moved := *high
	moved.TimePrices = scheduleRows(t, `{"prompt": "0.00000066", "completion": "0.00000198", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.00000132", "completion": "0.00000396", "input_cache_read": "0.000000044"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"}
	]}`)
	plan := PlanPrices([]Entry{
		entry("or/new", deepseek, nil),
		entry("or/low", deepseek, low),
		entry("or/high", deepseek, high),
		entry("or/moved", deepseek, &moved),
		entry("or/hy4", "tencent/hy4-preview", nil),
		entry("or/tier", "anthropic/claude-haiku-5.5", nil),
	}, api)
	const schedule = "1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00 04:00-06:00 10:00-24:00, sat-sun 00:00-24:00)"
	want := strings.Join([]string{
		"openrouter.ai: 6 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)",
		"Price updates (6):",
		"  or/new: no cost -> " + schedule,
		"  or/low: 0.66/0.022/1.98 -> " + schedule,
		"  or/high: 1.32/0.044/3.96 -> " + schedule,
		"  or/moved: 1.32/0.044/3.96 (openrouter 0.66/0.022/1.98 mon-sun 12:00-24:00) -> " + schedule,
		"  or/hy4: no cost -> 0.834/0.042/2.501 (openrouter 0.7506/0.0378/2.2509 mon-sun 16:00-24:00)",
		"  or/tier: no cost -> 0.1/0.01/0.5",
		"Unchanged prices: 0",
	}, "\n")
	if got := plan.Format(); got != want {
		t.Errorf("Format() =\n%s\n\nwant\n%s", got, want)
	}
}

// TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds pins the plan for a
// registry someone edited by hand: a row labelled openrouter whose windows
// are not what the sync writes (not a list, a list of other things, days
// that are no days). The plan must still print, show the row as it found it
// and replace it; a plan that panicked here would leave the user with no way
// to refresh prices but to find the row themselves. And a row that is the
// sync's own but for its timezone, or for one added key, is replaced too
// (rows are compared whole), so the line must show that difference: a line
// that read the same on both sides would be an update the user cannot see.
func TestPlanPricesReplacesAnOpenRouterRowWhateverItHolds(t *testing.T) {
	noList, oddList, oddDays := tomlw.NewTable(), tomlw.NewTable(), tomlw.NewTable()
	noList.Set("label", "openrouter")
	noList.Set("windows", "all day")
	oddList.Set("label", "openrouter")
	oddList.Set("windows", []any{int64(7), "x"})
	w := tomlw.NewTable()
	w.Set("days", []any{"fri", int64(1), "mon"})
	w.Set("start", int64(9))
	oddDays.Set("label", "openrouter")
	oddDays.Set("input_price_per_million", "cheap")
	oddDays.Set("windows", []any{w})
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	plan := PlanPrices([]Entry{{ID: "or/hy3", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud",
		Cost: &Cost{TimePrices: []*tomlw.Table{noList, oddList, oddDays}}}}, api)
	want := "  or/hy3: -/-/- (openrouter -/-/- timezone=\"\" ?) (openrouter -/-/- timezone=\"\" ?, ?) (openrouter -/-/- timezone=\"\" fri,,mon -) -> " +
		"0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)"
	if !strings.Contains(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant the line\n%s", plan.Format(), want)
	}

	// The sync's own row, edited: another timezone on one model, a note on
	// a window of another, a note on the row itself of a third (its windows
	// untouched, so the row's own keys are what is looked at). Each is a
	// fresh copy of what the sync writes.
	synced := func() *Cost {
		return &PlanPrices([]Entry{{ID: "or/hy3", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud"}}, api).Matched[0].After
	}
	zoned, noted, tagged := synced(), synced(), synced()
	zoned.TimePrices[0].Set("timezone", "America/New_York")
	tagged.TimePrices[0].Set("note", "mine")
	windows, _ := noted.TimePrices[0].Get("windows")
	windows.([]any)[0].(*tomlw.Table).Set("note", "mine")
	plan = PlanPrices([]Entry{
		{ID: "or/zoned", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: zoned},
		{ID: "or/noted", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: noted},
		{ID: "or/tagged", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: tagged},
		{ID: "or/synced", ProviderID: "openrouter", ModelName: "tencent/hy3", Location: "cloud", Cost: synced()},
	}, api)
	const row = "0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00)"
	want = strings.Join([]string{
		"Price updates (3):",
		"  or/zoned: 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 timezone=\"America/New_York\" mon-sun 16:00-24:00) -> " + row,
		"  or/noted: 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00 +keys) -> " + row,
		"  or/tagged: 0.132/0.033/0.528 (openrouter 0.0825/0.020625/0.33 mon-sun 16:00-24:00 +keys) -> " + row,
		"Unchanged prices: 1",
	}, "\n")
	if !strings.HasSuffix(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant it to end\n%s", plan.Format(), want)
	}
}

// TestPlanPricesLeavesItsRowsWhereAUserPutThem pins the plan for a model with
// two rows of the flow's and a row of the user's between them, which only a
// hand-reordered file holds. When the flow's rows already say what OpenRouter
// publishes there is nothing to write: gathering them at the first one's
// place would move the user's row behind a row it was put ahead of (rows
// resolve first match wins), on a plan line that prints the same text on
// both sides of its arrow, since a row of another label is not printed. When
// the schedule has changed, the rows are replaced where the first one stood,
// and the line shows the change.
func TestPlanPricesLeavesItsRowsWhereAUserPutThem(t *testing.T) {
	const three = `{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 800, "utc_end": 2000, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 1400, "prompt": "0.000002", "completion": "0.000004"}
	]}`
	own := scheduleRows(t, three)
	if len(own) != 2 {
		t.Fatalf("rows = %d, want 2", len(own))
	}
	mine := timeRow("mine", "sun")
	stored := []*tomlw.Table{own[0], mine, own[1]}
	plan := PlanPrices([]Entry{orEntry("m", &Cost{Input: f(3), Output: f(6), TimePrices: stored})}, apiOf(t, one(three)))
	if len(plan.Matched) != 1 {
		t.Fatalf("plan = %s", plan.Format())
	}
	if plan.Matched[0].Changed {
		t.Errorf("rows that already hold the schedule are an update:\n%s", plan.Format())
	}
	if got := plan.Matched[0].After.TimePrices; len(got) != 3 || got[0] != own[0] || got[1] != mine || got[2] != own[1] {
		t.Errorf("rows after = %v, want the three tables in the order they were read", labels(got))
	}

	// The schedule moved: the flow's rows are replaced, gathered where the
	// first stood, and both sides of the line differ.
	moved := strings.Replace(three, `"utc_end": 1400`, `"utc_end": 1500`, 1)
	plan = PlanPrices([]Entry{orEntry("m", &Cost{Input: f(3), Output: f(6), TimePrices: stored})}, apiOf(t, one(moved)))
	wantIDs(t, "rows after the schedule moved", labels(plan.Matched[0].After.TimePrices), "openrouter", "openrouter", "mine")
	if want := "  openrouter/m: 3/-/6 (openrouter 2/-/4 mon-sun 12:00-14:00) (openrouter 1/-/2 mon-sun 00:00-08:00 20:00-24:00) -> " +
		"3/-/6 (openrouter 2/-/4 mon-sun 12:00-15:00) (openrouter 1/-/2 mon-sun 00:00-08:00 20:00-24:00)"; !strings.Contains(plan.Format(), want) {
		t.Errorf("Format() =\n%s\n\nwant the line\n%s", plan.Format(), want)
	}
}

// scheduleRegistry is the registry the schedule's Apply test starts from, in
// the layout wt's writer gives one: a model a sync before the fix left at
// its off-peak price, with a row the user wrote; a model with no cost; and a
// model with no schedule that holds a row of each owner, the openrouter flow's
// from a schedule OpenRouter has since dropped.
const scheduleRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
pricing_updated_at = "2026-10-09T00:28:00+00:00"

[models.cost]
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

` + userTimePrice + `
[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"

[[models]]
id = "openrouter/z-ai--glm-5.2"
family = "glm"
provider_id = "openrouter"
model_name = "z-ai/glm-5.2"
location = "cloud"

[models.cost]
input_price_per_million = 0.06
cache_price_per_million = 0.059
output_price_per_million = 6.0

` + userTimePrice + `
[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.03

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

` + catalogTimePrice

// catalogTimePrice is an off-peak row as the ollama flow writes one.
const catalogTimePrice = `[[models.cost.time_prices]]
label = "off-peak"
timezone = "UTC"
input_price_per_million = 0.01

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "00:00"
end = "12:00"

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "18:00"
end = "24:00"

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"
`

// TestPricesApplyStoresAScheduleOnce runs the openrouter flow twice through the
// registry writer, on the saved response as OpenRouter serves it in two
// different windows, and reads the file's bytes. The first apply writes the
// peak price and the schedule row, drops the row of a schedule that is gone,
// and leaves the user's rows and the catalog's off-peak row byte for byte.
// The second, fetched when every one of these models is in its other window,
// changes no price (which is what #322 asks: no update because the clock
// moved) and writes only the stamp.
func TestPricesApplyStoresAScheduleOnce(t *testing.T) {
	path := scratchRegistry(t, scheduleRegistry)
	apply := func(fetched, now time.Time) PricesApplied {
		t.Helper()
		api := apiOf(t, fetchedAt(t, fetched))
		var done PricesApplied
		if _, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
			var err error
			done, err = PlanPrices(Entries(d.Models()), api).Apply(d, now)
			return err
		}); err != nil {
			t.Fatalf("UpdateRegistry: %v", err)
		}
		return done
	}

	// Friday 02:45 UTC: deepseek's dear window.
	first := time.Date(2026, 10, 9, 2, 45, 0, 0, time.UTC)
	if done := apply(first, first); done != (PricesApplied{Stamped: 3, Changed: 3}) {
		t.Errorf("first apply = %+v, want 3 stamped, 3 changed", done)
	}
	const stamp1, stamp2 = "2026-10-09T02:45:00+00:00", "2026-10-10T17:00:00+00:00"
	want := `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
pricing_updated_at = "` + stamp1 + `"

[models.cost]
input_price_per_million = 1.32
cache_price_per_million = 0.044
output_price_per_million = 3.9600000000000004

` + userTimePrice + `
[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "00:00"
end = "01:00"

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "04:00"
end = "06:00"

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "10:00"
end = "24:00"

[[models.cost.time_prices.windows]]
days = [
    "sat",
    "sun",
]
start = "00:00"
end = "24:00"

[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"
pricing_updated_at = "` + stamp1 + `"

[models.cost]
input_price_per_million = 0.13199999999999998
cache_price_per_million = 0.032999999999999995
output_price_per_million = 0.5279999999999999

[[models.cost.time_prices]]
label = "openrouter"
timezone = "UTC"
input_price_per_million = 0.0825
cache_price_per_million = 0.020625
output_price_per_million = 0.33

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
start = "16:00"
end = "24:00"

[[models]]
id = "openrouter/z-ai--glm-5.2"
family = "glm"
provider_id = "openrouter"
model_name = "z-ai/glm-5.2"
location = "cloud"
pricing_updated_at = "` + stamp1 + `"

[models.cost]
input_price_per_million = 0.06
cache_price_per_million = 0.059
output_price_per_million = 6.0

` + userTimePrice + `
` + catalogTimePrice
	if got := readFile(t, path); got != want {
		t.Fatalf("registry.toml after the first apply:\n%s\n\nwant:\n%s", got, want)
	}

	// Saturday 17:00 UTC: deepseek and hy3 are both in their cheap window, so
	// the response's top-level prices are not the ones stored.
	second := time.Date(2026, 10, 10, 17, 0, 0, 0, time.UTC)
	if h := headline(t, fetchedAt(t, second), "tencent/hy3"); h != "0.0000000825/0.000000020625/0.00000033" {
		t.Fatalf("the second response is not from hy3's other window: %s", h)
	}
	if done := apply(second, second); done != (PricesApplied{Stamped: 3, Changed: 0}) {
		t.Errorf("second apply = %+v, want 3 stamped and no price changed", done)
	}
	if got, want := readFile(t, path), strings.ReplaceAll(want, stamp1, stamp2); got != want {
		t.Errorf("registry.toml after the second apply:\n%s\n\nwant the first apply's file with the new stamp and nothing else:\n%s", got, want)
	}
	// What was written loads: the registry's validator accepts the rows.
	if _, err := config.Load(); err != nil {
		t.Errorf("config.Load after the applies: %v", err)
	}
}

// nightTimePrice is a cost.time_prices row a user wrote with a window that
// runs past midnight, in the layout registries on disk have.
const nightTimePrice = `[[models.cost.time_prices]]
label = "night"
timezone = "America/New_York"
output_price_per_million = 0.25

[[models.cost.time_prices.windows]]
days = [
    "mon",
    "tue",
    "wed",
    "thu",
    "fri",
]
start = "22:00"
end = "06:00"
`

// TestBothFlowsKeepAUsersWindowPastMidnight pins that a row a user wrote
// with a window that runs past midnight ("22:00" to "06:00", which the
// registry accepts since #322) comes through an apply of each flow byte for
// byte, on a model that flow changes and writes its own row to. The flows'
// own rows never hold such a window (each day's hours are written under
// that day), but neither flow reads the windows of a row it does not own,
// and the registry's validator, which checks every row of a model a write
// touches, accepts it. Before #322 both applies here were refused with
// "start must be before end", so one hand-written night rate stopped every
// price refresh. The picker then applies the user's row and the flow's row
// side by side, first match first.
func TestBothFlowsKeepAUsersWindowPastMidnight(t *testing.T) {
	path := scratchRegistry(t, `[[providers]]
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
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"

[models.cost]
input_price_per_million = 9.0

`+nightTimePrice+`
[[models]]
id = "ollama/glm-5.3:cloud"
family = "glm"
provider_id = "ollama"
model_name = "glm-5.3:cloud"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 9.0

`+nightTimePrice)

	at := time.Date(2026, 10, 9, 2, 45, 0, 0, time.UTC)
	var prices PricesApplied
	if _, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		var err error
		prices, err = PlanPrices(Entries(d.Models()), apiOf(t, fetchedAt(t, at))).Apply(d, at)
		return err
	}); err != nil {
		t.Fatalf("the openrouter flow's apply: %v", err)
	}
	if prices != (PricesApplied{Stamped: 1, Changed: 1}) {
		t.Errorf("the openrouter flow applied %+v, want 1 stamped, 1 changed", prices)
	}
	catalog := catalogOf(withOffpeak(cm("glm-5.3", 1.4, 0.26, 4.4), f(0.7), f(0.13), f(2.2)))
	done, changed := applyCatalog(t, catalog, []string{"glm-5.3:cloud"}, map[string]string{"glm-5.3": "glm-5.3:cloud"})
	if !changed || done.Updated != 1 {
		t.Errorf("the ollama flow applied %+v (changed %v), want 1 updated", done, changed)
	}

	text := readFile(t, path)
	if got := strings.Count(text, nightTimePrice); got != 2 {
		t.Errorf("the user's night row is in the file %d times byte for byte, want 2 (once per model):\n%s", got, text)
	}
	// Each flow's own row follows the user's, on its own model.
	for _, own := range []string{
		nightTimePrice + "\n[[models.cost.time_prices]]\nlabel = \"openrouter\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.0825\n",
		nightTimePrice + "\n[[models.cost.time_prices]]\nlabel = \"off-peak\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.7\n",
	} {
		if !strings.Contains(text, own) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", own, text)
		}
	}

	// What was written loads, and the resolver applies both rows of the
	// openrouter model: the user's in the New York night, the flow's from
	// 16:00 UTC, the flat (dearest) price otherwise.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load after the applies: %v", err)
	}
	for _, m := range cfg.Models {
		if got := m.Malformed(); len(got) != 0 {
			t.Errorf("%s: Malformed() = %q, want nothing", m.ID, got)
		}
		if m.ID != "openrouter/tencent--hy3" {
			continue
		}
		for _, c := range []struct {
			what string
			at   time.Time
			row  int
			out  float64
		}{
			{"Monday 23:00 in New York", time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC), 0, 0.25},
			{"Tuesday 05:59 in New York", time.Date(2026, 10, 13, 9, 59, 0, 0, time.UTC), 0, 0.25},
			{"Tuesday 06:00 in New York", time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC), -1, 0.5279999999999999},
			{"Tuesday 17:00 UTC", time.Date(2026, 10, 13, 17, 0, 0, 0, time.UTC), 1, 0.33},
		} {
			if got := m.Cost.PriceAt(c.at); got.Row != c.row || got.Output == nil || *got.Output != c.out {
				t.Errorf("%s: output %v from row %d, want %v from row %d", c.what, got.Output, got.Row, c.out, c.row)
			}
		}
	}
}
