package cloudsync

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixtureFetched is when testdata/openrouter_models.json was fetched from
// https://openrouter.ai/api/v1/models: a Friday afternoon, UTC. The file is
// five models of that list, cut down to their id and pricing: three priced
// by time of day (deepseek/deepseek-v4-pro-0813, tencent/hy3,
// tencent/hy4-preview), one with a prompt-size tier
// (anthropic/claude-haiku-5.5) and one with neither (z-ai/glm-5.2).
var fixtureFetched = time.Date(2026, 10, 9, 14, 6, 14, 0, time.UTC)

// fetchedAt is the fixture as OpenRouter serves it at another instant: for a
// model priced by time of day the top-level prompt, completion and
// input_cache_read are those of the overrides entry in force at that
// instant, and nothing else differs. The rule is written here from
// OpenRouter's Models guide and not from the code under test: an entry
// applies on its utc_days (every day when absent) inside [utc_start,
// utc_end), HHMM numbers compared as numbers, wrapping when the end is not
// after the start; the last entry that applies wins.
func fetchedAt(t *testing.T, at time.Time) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(readFile(t, "testdata/openrouter_models.json")))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	at = at.UTC()
	now := at.Hour()*100 + at.Minute()
	today := strings.ToLower(at.Weekday().String())
	for _, item := range doc["data"].([]any) {
		pricing := item.(map[string]any)["pricing"].(map[string]any)
		overrides, _ := pricing["overrides"].([]any)
		for _, raw := range overrides {
			entry := raw.(map[string]any)
			days, hasDays := entry["utc_days"].([]any)
			start, hasWindow := entry["utc_start"].(json.Number)
			if !hasDays && !hasWindow {
				continue
			}
			if hasDays && !slices.Contains(days, any(today)) {
				continue
			}
			if hasWindow {
				from, _ := start.Int64()
				to, _ := entry["utc_end"].(json.Number).Int64()
				inside := now >= int(from) && now < int(to)
				if to <= from {
					inside = now >= int(from) || now < int(to)
				}
				if !inside {
					continue
				}
			}
			for _, key := range []string{"prompt", "completion", "input_cache_read"} {
				pricing[key] = entry[key]
			}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// headline is a model's top-level prompt/completion/input_cache_read in
// body, as OpenRouter wrote them.
func headline(t *testing.T, body, id string) string {
	t.Helper()
	var doc struct {
		Data []struct {
			ID      string
			Pricing struct {
				Prompt, Completion string
				Cache              string `json:"input_cache_read"`
			}
		}
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	for _, m := range doc.Data {
		if m.ID == id {
			return m.Pricing.Prompt + "/" + m.Pricing.Cache + "/" + m.Pricing.Completion
		}
	}
	t.Fatalf("no model %s in the body", id)
	return ""
}

// TestFetchedAtIsWhatOpenRouterSent holds the test helper to what was seen
// of the real service: the fixture itself (fetched Friday 14:06 UTC) and the
// two runs in issue #322 (deepseek at the lower rate at 00:28 UTC, at double
// at 02:45 UTC). Only the 02:45 reading tells "the window in force" from
// "the level that holds most of the week" or "the first entry": at the other
// instants all three rules give the same price. One more reading does, and
// it is why the 16:00 case below is the rule and not what the service sent
// at that minute: the same day both tencent models were still listed at
// their before-16:00 level at 16:03 UTC and at their after-16:00 level at
// 16:09, with the same overrides throughout. The list is cached, so its
// top-level price trails the window by some minutes. The rest of the helper
// is OpenRouter's documented rule, not an observation. Every test below that
// says "at another time of day" rests on this helper, so a helper that
// modelled the windows wrongly would make those tests prove nothing about
// the service; the code under test does not read the top-level price of a
// time-priced model at all, which is what keeps it right even if the rule
// is not exact.
func TestFetchedAtIsWhatOpenRouterSent(t *testing.T) {
	const deepseek, hy3 = "deepseek/deepseek-v4-pro-0813", "tencent/hy3"
	fixture := readFile(t, "testdata/openrouter_models.json")
	same := fetchedAt(t, fixtureFetched)
	for _, id := range []string{deepseek, hy3, "tencent/hy4-preview", "anthropic/claude-haiku-5.5", "z-ai/glm-5.2"} {
		if got, want := headline(t, same, id), headline(t, fixture, id); got != want {
			t.Errorf("%s at the fixture's own instant = %s, want the fixture's %s", id, got, want)
		}
	}
	for _, c := range []struct {
		at       time.Time
		id, want string
	}{
		{time.Date(2026, 10, 9, 0, 28, 0, 0, time.UTC), deepseek, "0.00000066/0.000000022/0.00000198"},
		{time.Date(2026, 10, 9, 2, 45, 0, 0, time.UTC), deepseek, "0.00000132/0.000000044/0.00000396"},
		// The half-open window: 03:59 is inside 01:00 to 04:00, 04:00 is not.
		{time.Date(2026, 10, 9, 3, 59, 0, 0, time.UTC), deepseek, "0.00000132/0.000000044/0.00000396"},
		{time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC), deepseek, "0.00000066/0.000000022/0.00000198"},
		// A Saturday at an hour that is dear on a weekday.
		{time.Date(2026, 10, 10, 2, 45, 0, 0, time.UTC), deepseek, "0.00000066/0.000000022/0.00000198"},
		{time.Date(2026, 10, 9, 15, 59, 0, 0, time.UTC), hy3, "0.000000132/0.000000033/0.000000528"},
		{time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC), hy3, "0.0000000825/0.000000020625/0.00000033"},
		// A clock in another zone is read as its UTC instant: 17:00 PDT is
		// 00:00 UTC the next day.
		{time.Date(2026, 10, 9, 17, 0, 0, 0, time.FixedZone("PDT", -7*3600)), hy3, "0.000000132/0.000000033/0.000000528"},
	} {
		if got := headline(t, fetchedAt(t, c.at), c.id); got != c.want {
			t.Errorf("%s at %s = %s, want %s", c.id, c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

// TestParseOpenRouterTimeOfDay pins what is read for a model OpenRouter
// prices by time of day (#322), on a saved response. Its price is the
// dearest level of its schedule and not the top-level one, which is only the
// level in force when the list was fetched: here deepseek and both tencent
// models at their peak rate although the fixture was fetched in deepseek's
// cheap window. The other levels come back with the times of the UTC week
// they apply. A model with a prompt-size tier (min_prompt_tokens) is not a
// time-of-day model: its top-level price is its price, and the tier, which
// is 5 times dearer here, is not read.
func TestParseOpenRouterTimeOfDay(t *testing.T) {
	api := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	weekdays := []Span{{0, 60}, {240, 360}, {600, 1440}}
	evening := []Span{{960, 1440}}
	for id, want := range map[string]struct {
		in, cache, out string
		rates          []Rate
	}{
		"deepseek/deepseek-v4-pro-0813": {"0.00000132", "0.000000044", "0.00000396", []Rate{{
			Input: pm("0.00000066"), Cache: pm("0.000000022"), Output: pm("0.00000198"),
			Spans: [7][]Span{weekdays, weekdays, weekdays, weekdays, weekdays, {{0, 1440}}, {{0, 1440}}},
		}}},
		"tencent/hy3": {"0.000000132", "0.000000033", "0.000000528", []Rate{{
			Input: pm("0.0000000825"), Cache: pm("0.000000020625"), Output: pm("0.00000033"),
			Spans: [7][]Span{evening, evening, evening, evening, evening, evening, evening},
		}}},
		"tencent/hy4-preview": {"0.000000834", "0.000000042", "0.000002501", []Rate{{
			Input: pm("0.0000007506"), Cache: pm("0.0000000378"), Output: pm("0.0000022509"),
			Spans: [7][]Span{evening, evening, evening, evening, evening, evening, evening},
		}}},
		"anthropic/claude-haiku-5.5": {"0.0000001", "0.00000001", "0.0000005", nil},
		"z-ai/glm-5.2":               {"0.00000006", "0.000000059", "0.000006", nil},
	} {
		got := api[id]
		wantTriple(t, id, PriceTriple{Input: got.Input, Cache: got.Cache, Output: got.Output}, pm(want.in), pm(want.cache), pm(want.out))
		if got.Unusable != "" || got.Skipped != nil {
			t.Errorf("%s: unusable %q, skipped %v; want neither", id, got.Unusable, got.Skipped)
		}
		if !sameRates(got.Rates, want.rates) {
			t.Errorf("%s rates = %s\nwant %s", id, ratesText(got.Rates), ratesText(want.rates))
		}
	}
}

func sameRates(a, b []Rate) bool {
	return slices.EqualFunc(a, b, func(x, y Rate) bool {
		return samePrice(x.Input, y.Input) && samePrice(x.Cache, y.Cache) && samePrice(x.Output, y.Output) && reflect.DeepEqual(x.Spans, y.Spans)
	})
}

func ratesText(rates []Rate) string {
	var b strings.Builder
	for _, r := range rates {
		b.WriteString(num(r.Input) + "/" + num(r.Cache) + "/" + num(r.Output))
		for d, spans := range r.Spans {
			b.WriteString(" " + openRouterDays[d][:3] + ":")
			for _, s := range spans {
				b.WriteString(" " + strconv.Itoa(s.Start) + "-" + strconv.Itoa(s.End))
			}
		}
		b.WriteString("; ")
	}
	return b.String()
}

// pm is a per-token price as OpenRouter writes it, converted as the parser
// converts it: one float64 multiplication. (`0.00000396 * 1e6` written as a
// Go constant is exact arithmetic and gives 3.96; the conversion gives
// 3.9600000000000004, and that is what is stored.)
func pm(perToken string) *float64 {
	v, err := strconv.ParseFloat(perToken, 64)
	if err != nil {
		panic(err)
	}
	v *= 1_000_000
	return &v
}

// one is a response with one model, vendor/m, whose pricing object is the
// given JSON.
func one(pricing string) string { return `{"data": [{"id": "vendor/m", "pricing": ` + pricing + `}]}` }

// TestParseOpenRouterTimeOfDayShapes pins the override grammar on shapes the
// fixture does not hold, each a pricing object OpenRouter's Models guide
// describes or allows. The ones that matter most to a user: a window that
// wraps past midnight is read whole (half of it would leave part of the
// week unpriced), and entries wt cannot read with certainty are never
// guessed at.
func TestParseOpenRouterTimeOfDayShapes(t *testing.T) {
	// The guide's own example: half price from 16:30 to 00:30 UTC.
	wrap := apiOf(t, one(`{"prompt": "0.00000014", "completion": "0.00000021", "overrides": [
		{"utc_start": 30, "utc_end": 1630, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_start": 1630, "utc_end": 30, "prompt": "0.00000014", "completion": "0.00000021"}
	]}`))["vendor/m"]
	night := []Span{{0, 30}, {990, 1440}}
	wantTriple(t, "wrap", PriceTriple{Input: wrap.Input, Cache: wrap.Cache, Output: wrap.Output}, pm("0.00000028"), nil, pm("0.00000042"))
	if want := []Rate{{Input: pm("0.00000014"), Output: pm("0.00000021"), Spans: [7][]Span{night, night, night, night, night, night, night}}}; !sameRates(wrap.Rates, want) {
		t.Errorf("wrap rates = %s\nwant %s", ratesText(wrap.Rates), ratesText(want))
	}

	// The guide's weekly example: the wrapping window is tested against the
	// day of the instant, so Monday 00:00 to 00:30 belongs to Monday's entry
	// and Saturday 00:00 to 00:30 to the weekend's.
	weekly := apiOf(t, one(`{"prompt": "0.00000028", "completion": "0.00000042", "input_cache_read": "0.00000007", "overrides": [
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 30, "utc_end": 1630, "prompt": "0.00000056", "completion": "0.00000084"},
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 1630, "utc_end": 30, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_days": ["saturday", "sunday"], "prompt": "0.00000028", "completion": "0.00000042"}
	]}`))["vendor/m"]
	// No window has a cached-input price, so that price does not depend on
	// the hour and the top-level one is kept.
	wantTriple(t, "weekly", PriceTriple{Input: weekly.Input, Cache: weekly.Cache, Output: weekly.Output}, pm("0.00000056"), pm("0.00000007"), pm("0.00000084"))
	if want := []Rate{{Input: pm("0.00000028"), Output: pm("0.00000042"), Spans: [7][]Span{night, night, night, night, night, {{0, 1440}}, {{0, 1440}}}}}; !sameRates(weekly.Rates, want) {
		t.Errorf("weekly rates = %s\nwant %s", ratesText(weekly.Rates), ratesText(want))
	}

	// The same schedule with its entries in another order is the same
	// schedule: nothing may be written because OpenRouter reordered a list.
	reordered := apiOf(t, one(`{"prompt": "0.00000028", "completion": "0.00000042", "input_cache_read": "0.00000007", "overrides": [
		{"utc_days": ["sunday", "saturday"], "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_days": ["friday", "monday", "tuesday", "wednesday", "thursday"], "utc_start": 1630, "utc_end": 30, "prompt": "0.00000028", "completion": "0.00000042"},
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 30, "utc_end": 1630, "prompt": "0.00000056", "completion": "0.00000084"}
	]}`))["vendor/m"]
	if !sameRates(reordered.Rates, weekly.Rates) || !samePrice(reordered.Input, weekly.Input) {
		t.Errorf("reordered rates = %s\nwant %s", ratesText(reordered.Rates), ratesText(weekly.Rates))
	}

	// Three levels: the dearest is the price, the others follow dearest
	// first. Where entries overlap the later one wins, as the guide says.
	three := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 800, "utc_end": 2000, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 1400, "prompt": "0.000002", "completion": "0.000004"}
	]}`))["vendor/m"]
	all := func(spans ...Span) [7][]Span { return [7][]Span{spans, spans, spans, spans, spans, spans, spans} }
	wantTriple(t, "three", PriceTriple{Input: three.Input, Cache: three.Cache, Output: three.Output}, f(3), nil, f(6))
	if want := []Rate{
		{Input: f(2), Output: f(4), Spans: all(Span{720, 840})},
		{Input: f(1), Output: f(2), Spans: all(Span{0, 480}, Span{1200, 1440})},
	}; !sameRates(three.Rates, want) {
		t.Errorf("three rates = %s\nwant %s", ratesText(three.Rates), ratesText(want))
	}

	// Levels that cross: one is dearer on output, the other on input. The
	// price is the level with the highest output price (mainRate), so here
	// the other level, in its hours, costs more on input than the price
	// says. No published schedule is like this; the case pins the rule so
	// that it is not changed unnoticed.
	crossed := apiOf(t, one(`{"prompt": "0.000009", "completion": "0.000004", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000009", "completion": "0.000004"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000005"}
	]}`))["vendor/m"]
	wantTriple(t, "crossed", PriceTriple{Input: crossed.Input, Cache: crossed.Cache, Output: crossed.Output}, f(1), nil, f(5))
	if want := []Rate{{Input: f(9), Output: f(4), Spans: all(Span{0, 720})}}; !sameRates(crossed.Rates, want) {
		t.Errorf("crossed rates = %s\nwant %s", ratesText(crossed.Rates), ratesText(want))
	}

	// The top-level prices the schedule supplies are not read, not even to
	// refuse them: a top-level price that is no price ("-1" is OpenRouter's
	// "varies") does not stop a complete schedule from being used. If it
	// did, and the value were bad in one window only, the plan would depend
	// on the hour again.
	for name, top := range map[string]string{
		"a top-level prompt of -1":           `"prompt": "-1", "completion": "0.000004", "input_cache_read": "0.0000002"`,
		"a top-level completion of -1":       `"prompt": "0.000002", "completion": "-1", "input_cache_read": "0.0000002"`,
		"a top-level input_cache_read of -1": `"prompt": "0.000002", "completion": "0.000004", "input_cache_read": "-1"`,
	} {
		got := apiOf(t, one(`{`+top+`, "overrides": [
			{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004", "input_cache_read": "0.0000002"},
			{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000001"}
		]}`))["vendor/m"]
		wantTriple(t, name, PriceTriple{Input: got.Input, Cache: got.Cache, Output: got.Output}, f(2), pm("0.0000002"), f(4))
		if got.Skipped != nil || got.Unusable != "" || len(got.Rates) != 1 {
			t.Errorf("%s: skipped %v, unusable %q, %d other levels; want the schedule's prices and no refusal", name, got.Skipped, got.Unusable, len(got.Rates))
		}
	}
	// The one top-level price a schedule can leave to the top level is the
	// cached-input price, when no window has one. Then it is read, and a
	// value that is no price refuses the model as it does for any model.
	if got := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "input_cache_read": "-1", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}
	]}`))["vendor/m"]; !slices.Equal(got.Skipped, []string{"input_cache_read"}) {
		t.Errorf("a top-level input_cache_read of -1 that no window replaces: skipped = %v, want it refused", got.Skipped)
	}

	// A price key wt has no list entry for is still a price key when the
	// model's own pricing object has it at the top level: OpenRouter says an
	// entry's prices have the base object's keys. So a price OpenRouter adds
	// later does not stop the schedule from being read.
	newKey := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "input_cache_write_5m": "0.000003", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004", "input_cache_write_5m": "0.000003"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002", "input_cache_write_5m": "0.0000015"}
	]}`))["vendor/m"]
	wantTriple(t, "a new price key", PriceTriple{Input: newKey.Input, Cache: newKey.Cache, Output: newKey.Output}, f(2), nil, f(4))
	if newKey.Unusable != "" || len(newKey.Rates) != 1 {
		t.Errorf("a new price key: unusable %q, %d other levels; want the schedule read", newKey.Unusable, len(newKey.Rates))
	}

	// A model with both kinds of entry: the windows are read, the prompt-size
	// tier is not. And one price written two ways (text, a JSON number in
	// exponent form) is one level, not two.
	mixed := apiOf(t, one(`{"prompt": "0.000001", "completion": "0.000002", "overrides": [
		{"min_prompt_tokens": 200000, "prompt": "0.00001", "completion": "0.00002"},
		{"utc_start": 0, "utc_end": 600, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 600, "utc_end": 1200, "prompt": "0.000003", "completion": "0.000006"},
		{"utc_start": 1200, "utc_end": 0, "prompt": 1e-6, "completion": 2e-6}
	]}`))["vendor/m"]
	wantTriple(t, "mixed", PriceTriple{Input: mixed.Input, Cache: mixed.Cache, Output: mixed.Output}, f(3), nil, f(6))
	if want := []Rate{{Input: f(1), Output: f(2), Spans: all(Span{0, 360}, Span{720, 1440})}}; !sameRates(mixed.Rates, want) {
		t.Errorf("mixed rates = %s\nwant %s", ratesText(mixed.Rates), ratesText(want))
	}

	// One window published as two adjoining entries at one price is one
	// stretch of the day: split at the entries' seam it would be written as
	// two windows, and a sync after OpenRouter re-cut its list would report
	// an update although no price moved at any minute.
	split := apiOf(t, one(`{"prompt": "0.000001", "completion": "0.000002", "overrides": [
		{"utc_start": 0, "utc_end": 800, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 800, "utc_end": 1600, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 1600, "utc_end": 0, "prompt": "0.000003", "completion": "0.000006"}
	]}`))["vendor/m"]
	wantTriple(t, "split", PriceTriple{Input: split.Input, Cache: split.Cache, Output: split.Output}, f(3), nil, f(6))
	if want := []Rate{{Input: f(1), Output: f(2), Spans: all(Span{0, 960})}}; !sameRates(split.Rates, want) {
		t.Errorf("split rates = %s\nwant %s", ratesText(split.Rates), ratesText(want))
	}

	// Levels that tie on output and input are ordered by the cached-input
	// price, so the flat price is the dearer of them wherever it stands in
	// the week: here the cheaper one comes first.
	tied := apiOf(t, one(`{"prompt": "0.000002", "completion": "0.000004", "input_cache_read": "0.0000001", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004", "input_cache_read": "0.0000001"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000002", "completion": "0.000004", "input_cache_read": "0.0000002"}
	]}`))["vendor/m"]
	wantTriple(t, "tied", PriceTriple{Input: tied.Input, Cache: tied.Cache, Output: tied.Output}, f(2), pm("0.0000002"), f(4))
	if want := []Rate{{Input: f(2), Cache: pm("0.0000001"), Output: f(4), Spans: all(Span{0, 720})}}; !sameRates(tied.Rates, want) {
		t.Errorf("tied rates = %s\nwant %s", ratesText(tied.Rates), ratesText(want))
	}

	// Every window at one price: a schedule in form only. One price, no
	// other level.
	flat := apiOf(t, one(`{"prompt": "0.000009", "completion": "0.000009", "overrides": [
		{"utc_start": 0, "utc_end": 1200, "prompt": "0.000001", "completion": "0.000002"},
		{"utc_start": 1200, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}
	]}`))["vendor/m"]
	wantTriple(t, "flat", PriceTriple{Input: flat.Input, Cache: flat.Cache, Output: flat.Output}, f(1), nil, f(2))
	if flat.Rates != nil || flat.Unusable != "" {
		t.Errorf("flat = %+v, want no other level and nothing unusable", flat)
	}

	// An entry that is not a time-of-day entry is ignored, whatever it holds.
	for name, pricing := range map[string]string{
		"a prompt-size tier":           `{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"min_prompt_tokens": 200000, "prompt": "0.000002", "completion": "0.000004"}]}`,
		"a condition wt does not know": `{"prompt": "0.000001", "completion": "0.000002", "overrides": [{"region": "eu", "prompt": "-1", "completion": "soon"}]}`,
		"an empty overrides list":      `{"prompt": "0.000001", "completion": "0.000002", "overrides": []}`,
		"a null overrides":             `{"prompt": "0.000001", "completion": "0.000002", "overrides": null}`,
	} {
		got := apiOf(t, one(pricing))["vendor/m"]
		wantTriple(t, name, PriceTriple{Input: got.Input, Cache: got.Cache, Output: got.Output}, f(1), nil, f(2))
		if got.Rates != nil || got.Unusable != "" || got.Skipped != nil {
			t.Errorf("%s = %+v, want the top-level price and nothing else", name, got)
		}
	}

	// A schedule that cannot be read with certainty is reported, with the
	// reason, and never half used.
	const day, night2 = `"prompt": "0.000002", "completion": "0.000004"`, `"prompt": "0.000001", "completion": "0.000002"`
	for name, c := range map[string]struct{ overrides, want string }{
		// An overrides value wt cannot read as a list of entries may hold a
		// schedule, and then the top-level price is the one of the window in
		// force: taking it would store a price that follows the hour.
		"an overrides that is no list": {`{"utc_start": 0}`, "its overrides are not a list"},
		"an overrides that is text":    {`"1630-0030"`, "its overrides are not a list"},
		"an entry that is no object":   {`["utc_start", 7, null]`, "an overrides entry is not an object"},
		"a null beside a tier":         {`[{"min_prompt_tokens": 200000, ` + day + `}, null]`, "an overrides entry is not an object"},
		"a gap":                        {`[{"utc_start": 0, "utc_end": 1200, ` + day + `}, {"utc_start": 1300, "utc_end": 0, ` + night2 + `}]`, "its windows do not cover the whole week"},
		"a day with no entry":          {`[{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday", "saturday"], ` + day + `}]`, "its windows do not cover the whole week"},
		"a skipped entry's gap":        {`[{"utc_start": 0, "utc_end": 1200, ` + day + `}, {"region": "eu", ` + night2 + `}]`, "its windows do not cover the whole week"},
		"a time and size entry":        {`[{"utc_start": 0, "utc_end": 0, "min_prompt_tokens": 1000, ` + day + `}]`, "an entry sets both a time window and min_prompt_tokens"},
		"an unknown key":               {`[{"utc_start": 0, "utc_end": 0, "utc_months": ["may"], ` + day + `}]`, `an entry has a key wt does not know ("utc_months")`},
		"a discount key":               {`[{"utc_start": 0, "utc_end": 0, "discount": 0.5, ` + day + `}]`, `an entry has a key wt does not know ("discount")`},
		"a key like a price":           {`[{"utc_start": 0, "utc_end": 0, "input_cache_write_5m": "0.000003", ` + day + `}]`, `an entry has a key wt does not know ("input_cache_write_5m")`},
		"an entry's overrides":         {`[{"utc_start": 0, "utc_end": 0, "overrides": [], ` + day + `}]`, `an entry has a key wt does not know ("overrides")`},
		"no completion price":          {`[{"utc_start": 0, "utc_end": 0, "prompt": "0.000002"}]`, "an entry has no prompt or no completion price"},
		"a null prompt price":          {`[{"utc_start": 0, "utc_end": 0, "prompt": null, "completion": "0.000004"}]`, "an entry has no prompt or no completion price"},
		"a price that varies":          {`[{"utc_start": 0, "utc_end": 0, "prompt": "-1", "completion": "0.000004"}]`, "its prompt price in one window is negative or not a number"},
		"a cache price in some":        {`[{"utc_start": 0, "utc_end": 1200, "input_cache_read": "0.0000001", ` + day + `}, {"utc_start": 1200, "utc_end": 0, ` + night2 + `}]`, "some of its windows have a cached-input price and some do not"},
		"a start with no end":          {`[{"utc_start": 0, ` + day + `}]`, "an entry has only one of utc_start and utc_end"},
		"an end with no start":         {`[{"utc_days": ["monday"], "utc_end": 1200, ` + day + `}]`, "an entry has only one of utc_start and utc_end"},
		"75 minutes":                   {`[{"utc_start": 1675, "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"24:00":                        {`[{"utc_start": 0, "utc_end": 2400, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a time as text":               {`[{"utc_start": "0100", "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a fractional time":            {`[{"utc_start": 100.5, "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a negative time":              {`[{"utc_start": -100, "utc_end": 0, ` + day + `}]`, "utc_start or utc_end is not an HHMM time"},
		"a day wt cannot name":         {`[{"utc_days": ["monday", "holiday"], ` + day + `}]`, "utc_days is not a list of weekday names"},
		"days as text":                 {`[{"utc_days": "monday", ` + day + `}]`, "utc_days is not a list of weekday names"},
		"no days":                      {`[{"utc_days": [], ` + day + `}]`, "utc_days is not a list of weekday names"},
	} {
		got := apiOf(t, one(`{"prompt": "0.000001", "completion": "0.000002", "overrides": `+c.overrides+`}`))["vendor/m"]
		if got.Unusable != c.want || got.Rates != nil {
			t.Errorf("%s: unusable = %q, rates %s\nwant %q and no rates", name, got.Unusable, ratesText(got.Rates), c.want)
		}
	}
}

// TestPlanPricesDoesNotFollowTheClock is the rule #322 asks for, on the saved
// response as OpenRouter serves it at every half hour of a week: the plan
// reads the same whenever the list was fetched, and once it is applied no
// later run in any window finds a price to update. Before the fix the sync
// stored whichever window it ran in, so two runs hours apart each reported
// an update, and each update rewrote LiteLLM's routes.
func TestPlanPricesDoesNotFollowTheClock(t *testing.T) {
	entries := func(cost func(id string) *Cost) []Entry {
		var out []Entry
		for _, name := range []string{"deepseek/deepseek-v4-pro-0813", "tencent/hy3", "tencent/hy4-preview", "anthropic/claude-haiku-5.5", "z-ai/glm-5.2"} {
			id := "openrouter/" + strings.ReplaceAll(name, "/", "--")
			out = append(out, Entry{ID: id, Family: "x", ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: cost(id)})
		}
		return out
	}
	monday := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	first := PlanPrices(entries(func(string) *Cost { return nil }), apiOf(t, fetchedAt(t, monday)))
	if len(first.Matched) != 5 || len(first.Warnings) != 0 {
		t.Fatalf("first plan = %s", first.Format())
	}
	stored := map[string]*Cost{}
	for _, m := range first.Matched {
		stored[m.ModelID] = &m.After
	}
	headlines := map[string]bool{}
	for step := range 7 * 48 {
		at := monday.Add(time.Duration(step) * 30 * time.Minute)
		body := fetchedAt(t, at)
		headlines[headline(t, body, "deepseek/deepseek-v4-pro-0813")+" "+headline(t, body, "tencent/hy3")] = true
		api := apiOf(t, body)
		if got := PlanPrices(entries(func(string) *Cost { return nil }), api).Format(); got != first.Format() {
			t.Fatalf("the plan for a new registry, fetched %s:\n%s\n\nfetched Monday 00:00:\n%s", at.Format("Mon 15:04"), got, first.Format())
		}
		again := PlanPrices(entries(func(id string) *Cost { return stored[id] }), api)
		for _, m := range again.Matched {
			if m.Changed {
				t.Fatalf("fetched %s, %s is a price update: %s -> %s", at.Format("Mon 15:04"), m.ModelID, formatCost(m.Before), formatCost(&m.After))
			}
		}
		if want := "Price updates (0):\nUnchanged prices: 5"; !strings.HasSuffix(again.Format(), want) {
			t.Fatalf("fetched %s, the plan after an apply:\n%s\nwant it to end %q", at.Format("Mon 15:04"), again.Format(), want)
		}
	}
	// The sweep did cross windows: the response itself took three forms
	// (deepseek is dear only in hours when hy3 is too).
	if len(headlines) != 3 {
		t.Errorf("the week's responses had %d different pairs of deepseek and hy3 top-level prices, want 3", len(headlines))
	}
	for id, want := range map[string][3]string{
		"openrouter/deepseek--deepseek-v4-pro-0813": {"0.00000132", "0.000000044", "0.00000396"},
		"openrouter/tencent--hy3":                   {"0.000000132", "0.000000033", "0.000000528"},
		"openrouter/anthropic--claude-haiku-5.5":    {"0.0000001", "0.00000001", "0.0000005"},
	} {
		wantTriple(t, id, PriceTriple{Input: stored[id].Input, Cache: stored[id].Cache, Output: stored[id].Output}, pm(want[0]), pm(want[1]), pm(want[2]))
	}
}

// TestPlanPricesLeavesAModelWhoseScheduleItCannotRead pins the refusal: such
// a model keeps the price it has and is not counted as refreshed, and the
// warning names it and says why, in words that do not change with the hour
// (a warning is part of the plan's text, which is compared under the lock).
// Falling back to the top-level price would bring the bug back for exactly
// the models whose schedule changed shape.
func TestPlanPricesLeavesAModelWhoseScheduleItCannotRead(t *testing.T) {
	entries := []Entry{orEntry("m", &Cost{Input: f(5), Output: f(6)})}
	body := func(prompt string) string {
		return one(`{"prompt": "` + prompt + `", "completion": "0.000004", "overrides": [
			{"utc_start": 0, "utc_end": 1200, "prompt": "0.000002", "completion": "0.000004"},
			{"utc_start": 1300, "utc_end": 0, "prompt": "0.000001", "completion": "0.000002"}
		]}`)
	}
	const want = "openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"Price updates (0):\n" +
		"Unchanged prices: 0\n" +
		"warning: Could not use OpenRouter's time-of-day pricing for openrouter/m: its windows do not cover the whole week"
	// The third top-level price is no price at all: the warning is still the
	// time-of-day one, not the one about a price that is not a number.
	for _, prompt := range []string{"0.000002", "0.000001", "-1"} {
		plan := PlanPrices(entries, apiOf(t, body(prompt)))
		if got := plan.Format(); got != want || plan.HasWork() {
			t.Errorf("top-level prompt %s: Format() =\n%s\n\nwant\n%s\nand nothing to apply", prompt, got, want)
		}
	}
}

// TestPlanPricesReportsAScheduleThatComesAndGoes pins what the rule above
// does not promise. The plan follows what OpenRouter publishes for a model,
// and OpenRouter's list gives each model the pricing of one provider (its
// "top provider"). If that provider changes between two syncs, from one that
// prices by time of day to one that does not and back, the model loses and
// regains its schedule, and each time the sync reports a price update: to
// the listed price, then to the schedule's level again. That is a change in
// what is published, not the clock, and nothing here can tell it from a
// re-price. (deepseek/deepseek-v4-pro-0813 had 21 provider endpoints on
// 2026-10-09, one of them at 0.66/1.98 with no schedule; the list was not
// seen to switch to it.)
func TestPlanPricesReportsAScheduleThatComesAndGoes(t *testing.T) {
	const id, name = "openrouter/deepseek--deepseek-v4-pro-0813", "deepseek/deepseek-v4-pro-0813"
	scheduled := apiOf(t, readFile(t, "testdata/openrouter_models.json"))
	// The same model as the list would show it from a provider with one
	// price all day.
	unscheduled := apiOf(t, `{"data": [{"id": "`+name+`", "pricing": {"prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022"}}]}`)
	var stored *Cost
	for i, step := range []struct {
		api             map[string]APIPrice
		in, cache, out  string
		wantOtherLevels int
	}{
		{scheduled, "0.00000132", "0.000000044", "0.00000396", 1},
		{unscheduled, "0.00000066", "0.000000022", "0.00000198", 0},
		{scheduled, "0.00000132", "0.000000044", "0.00000396", 1},
	} {
		plan := PlanPrices([]Entry{{ID: id, ProviderID: "openrouter", ModelName: name, Location: "cloud", Cost: stored}}, step.api)
		if len(plan.Matched) != 1 || !plan.Matched[0].Changed {
			t.Fatalf("step %d: want one price update, got\n%s", i, plan.Format())
		}
		after := plan.Matched[0].After
		wantTriple(t, "the stored price", PriceTriple{Input: after.Input, Cache: after.Cache, Output: after.Output}, pm(step.in), pm(step.cache), pm(step.out))
		if got := len(step.api[name].Rates); got != step.wantOtherLevels {
			t.Errorf("step %d: %d other levels, want %d", i, got, step.wantOtherLevels)
		}
		stored = &after
	}
}
