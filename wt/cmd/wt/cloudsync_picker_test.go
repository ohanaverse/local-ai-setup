package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// pickerRegistry holds every model of the saved OpenRouter response, none of
// them priced yet: three OpenRouter prices by time of day, one it prices by
// prompt size, and one with a single price. The last model is made up
// (madeUpListing).
const pickerRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_OPENROUTER_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "openrouter/tencent--hy4-preview"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy4-preview"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "openrouter/anthropic--claude-haiku-5.5"
family = "claude"
provider_id = "openrouter"
model_name = "anthropic/claude-haiku-5.5"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "openrouter/z-ai--glm-5.2"
family = "glm"
provider_id = "openrouter"
model_name = "z-ai/glm-5.2"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "openrouter/example--night-owl"
family = "example"
provider_id = "openrouter"
model_name = "example/night-owl"
location = "cloud"
source = "curated"
tags = []
`

// madeUpListing is one model no provider serves, added to the saved response
// for the three rules of OpenRouter's that its real schedules do not put to
// the test. Every wrapping window in the saved response ends at midnight;
// this one is OpenRouter's own example of a window that ends after it, 16:30
// to 00:30 UTC, and it is limited to weekdays, so its half past midnight
// falls on Monday to Friday mornings (the day of the minute itself) and not
// on Saturday's. The first entry prices the whole week higher than any
// other and every later entry lies over it, so it is in force at no minute:
// a reader that let the first entry win, or that took the dearest price in
// the list for the model's own, would show it. And the price at the top is
// the cheap window's, as in a list fetched at night.
const madeUpListing = `{"id": "example/night-owl", "pricing": {
	"prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000001",
	"overrides": [
		{"prompt": "0.000009", "completion": "0.000009", "input_cache_read": "0.0000009"},
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 30, "utc_end": 1630,
			"prompt": "0.000002", "completion": "0.000004", "input_cache_read": "0.0000002"},
		{"utc_days": ["monday", "tuesday", "wednesday", "thursday", "friday"], "utc_start": 1630, "utc_end": 30,
			"prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000001"},
		{"utc_days": ["saturday", "sunday"],
			"prompt": "0.0000015", "completion": "0.000003", "input_cache_read": "0.00000015"}
	]}}`

// listedPrice is the three prices of one pricing object or one overrides
// entry of OpenRouter's list, per million tokens.
type listedPrice struct{ in, cache, out float64 }

// dearerThan orders two levels the way the docs say the flat price is
// chosen: by output price, then input, then cached input.
func (p listedPrice) dearerThan(o listedPrice) bool {
	if p.out != o.out {
		return p.out > o.out
	}
	if p.in != o.in {
		return p.in > o.in
	}
	return p.cache > o.cache
}

// listedPerMillion reads one of OpenRouter's prices, a decimal string of
// dollars per token, as dollars per million tokens. It moves the decimal
// point exactly (a rational number) and rounds once, which is not how the
// sync converts, so the two can differ in the last digit of a float64:
// samePrice allows for that and for nothing a user could see.
func listedPerMillion(t *testing.T, v any) float64 {
	t.Helper()
	text, ok := v.(string)
	r, parsed := new(big.Rat).SetString(text)
	if !ok || !parsed {
		t.Fatalf("the fixture holds a price that is not a decimal string: %v", v)
	}
	f, _ := r.Mul(r, big.NewRat(1_000_000, 1)).Float64()
	return f
}

func samePrice(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) <= 1e-9*math.Max(math.Abs(want), 1e-12)
}

// paintedWeek is this test's own reading of a pricing object of OpenRouter's
// list, written from the rules in OpenRouter's Models guide and from nothing
// in internal/cloudsync: the price of each of the UTC week's 10,080 minutes
// (minute 0 is Monday 00:00), and whether the model has a time-of-day entry
// at all.
//
// The rules: an overrides entry is a time-of-day entry when it has utc_days,
// utc_start or utc_end; utc_start and utc_end are HHMM clock numbers in UTC
// (1630 is 16:30); the window is [start, end) and wraps past midnight when
// the end is not after the start; with neither key it is the whole day;
// utc_days names the UTC weekdays it applies on, tested at the minute
// itself, and every day when absent; where entries overlap, the later one in
// the list wins. An entry with none of the three keys (a prompt-size tier)
// is not a time-of-day entry and paints nothing: the price at the top of the
// object is then the model's price at every minute.
func paintedWeek(t *testing.T, pricing map[string]any) (week []listedPrice, timed bool) {
	t.Helper()
	top := listedPrice{
		in:    listedPerMillion(t, pricing["prompt"]),
		cache: listedPerMillion(t, pricing["input_cache_read"]),
		out:   listedPerMillion(t, pricing["completion"]),
	}
	const perDay = 24 * 60
	week = make([]listedPrice, 7*perDay)
	painted := make([]bool, len(week))
	days := []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
	minutes := func(v any) int {
		hhmm, ok := v.(float64)
		if !ok || hhmm != math.Trunc(hhmm) {
			t.Fatalf("the fixture holds a utc_start or utc_end that is not an HHMM number: %v", v)
		}
		return int(hhmm)/100*60 + int(hhmm)%100
	}
	entries, _ := pricing["overrides"].([]any)
	for _, raw := range entries {
		entry := raw.(map[string]any)
		_, hasDays := entry["utc_days"]
		_, hasStart := entry["utc_start"]
		_, hasEnd := entry["utc_end"]
		if !hasDays && !hasStart && !hasEnd {
			continue
		}
		timed = true
		on := [7]bool{true, true, true, true, true, true, true}
		if hasDays {
			on = [7]bool{}
			for _, name := range entry["utc_days"].([]any) {
				d := slices.Index(days, name.(string))
				if d < 0 {
					t.Fatalf("the fixture holds a utc_days name that is no weekday: %v", name)
				}
				on[d] = true
			}
		}
		start, end := 0, 0
		if hasStart || hasEnd {
			start, end = minutes(entry["utc_start"]), minutes(entry["utc_end"])
		}
		price := listedPrice{in: listedPerMillion(t, entry["prompt"]), cache: top.cache, out: listedPerMillion(t, entry["completion"])}
		if v, has := entry["input_cache_read"]; has {
			price.cache = listedPerMillion(t, v)
		}
		for d := range 7 {
			for m := range perDay {
				inside := start <= m && m < end
				if end <= start {
					inside = m >= start || m < end
				}
				if on[d] && inside {
					week[d*perDay+m], painted[d*perDay+m] = price, true
				}
			}
		}
	}
	if !timed {
		for i := range week {
			week[i] = top
		}
		return week, false
	}
	if slices.Contains(painted, false) {
		t.Fatal("the fixture's time-of-day entries leave a minute of the week with no price")
	}
	return week, true
}

// TestThePickerShowsThePriceOpenRouterChargesNow is the goal of #322 end to
// end, across the two halves that were built apart: the openrouter flow,
// which stores a time-priced model's schedule, and the resolver the model
// picker prices its rows with (config.ModelCost.PriceAt). The real command
// plans and applies a saved OpenRouter response onto a scratch registry, the
// registry is loaded the way the picker loads it, and every model is
// resolved at each of the week's 10,080 minutes. The price in force must be
// what OpenRouter's schedule says for that minute, where "what the schedule
// says" is read from the response by this file's own painter, written from
// OpenRouter's documented rules and sharing no code with the flow.
//
// A regression here is the bug the user reported, in its second form: the
// sync stores something, the picker shows something, and the number on the
// screen is not what a request sent now is charged. It would show as a
// level stored under the wrong hours or the wrong UTC day, a wrapping
// window cut at the wrong side of midnight, a cheaper level stored as the
// model's own price, or a prompt-size tier taken for a schedule.
func TestThePickerShowsThePriceOpenRouterChargesNow(t *testing.T) {
	raw, err := os.ReadFile("../../internal/cloudsync/testdata/openrouter_models.json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID      string         `json:"id"`
			Pricing map[string]any `json:"pricing"`
		} `json:"data"`
	}
	// The made-up model goes in before the list's closing bracket.
	end := strings.LastIndex(string(raw), "]")
	if end < 0 {
		t.Fatal("the fixture has no list to add a model to")
	}
	body := string(raw[:end]) + "," + madeUpListing + string(raw[end:])
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("the fixture: %v", err)
	}

	// The sync runs once, on a Friday at 14:06 UTC, when the response's
	// top-level prices are those of deepseek's cheap window and of the hy
	// models' dear one. Nothing below may depend on that hour.
	_, cfg := cloudSyncHome(t, pickerRegistry)
	stubRouteSync(t, "")
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: body})
	cloudSyncNow = func() time.Time { return time.Date(2026, 10, 9, 14, 6, 0, 0, time.UTC) }
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if stderr != "" || code != 0 || !strings.HasSuffix(stdout, "openrouter: refreshed 6 model(s); 6 price(s) changed\n") {
		t.Fatalf("the sync: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load after the sync: %v", err)
	}

	// The minutes each level of each time-priced model holds, counted by hand
	// from the fixture: deepseek's dear level is 01:00-04:00 and 06:00-10:00
	// UTC on the five weekdays (7 hours a day), the hy models' cheap level
	// 16:00-24:00 UTC every day; the made-up model is dear for 16 hours of
	// each weekday, cheap for the other 8, at a third price all weekend, and
	// at its first entry's price never. They hold the painter itself to the
	// fixture, so a painter that painted nothing cannot agree with a flow
	// that stored nothing.
	wantMinutes := map[string]map[string]int{
		"deepseek/deepseek-v4-pro-0813": {"1.32/0.044/3.96": 5 * 7 * 60, "0.66/0.022/1.98": 10080 - 5*7*60},
		"tencent/hy3":                   {"0.132/0.033/0.528": 7 * 16 * 60, "0.0825/0.020625/0.33": 7 * 8 * 60},
		"tencent/hy4-preview":           {"0.834/0.042/2.501": 7 * 16 * 60, "0.7506/0.0378/2.2509": 7 * 8 * 60},
		"anthropic/claude-haiku-5.5":    {"0.1/0.01/0.5": 10080},
		"z-ai/glm-5.2":                  {"0.06/0.059/6": 10080},
		"example/night-owl":             {"2/0.2/4": 5 * 16 * 60, "1/0.1/2": 5 * 8 * 60, "1.5/0.15/3": 2 * 24 * 60},
	}
	wantTimed := map[string]bool{"deepseek/deepseek-v4-pro-0813": true, "tencent/hy3": true, "tencent/hy4-preview": true, "example/night-owl": true}
	if len(list.Data) != len(wantMinutes) {
		t.Fatalf("the fixture holds %d models, this test knows %d", len(list.Data), len(wantMinutes))
	}

	// Monday 2026-10-12 00:00 UTC, a week after the one the sync ran in.
	monday := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	// The picker's clock is the host's, so each instant is also asked for as
	// a wall clock seven hours behind UTC, where it is another calendar day
	// for seven hours of each day: only the instant may count.
	behind := time.FixedZone("UTC-7", -7*3600)
	name := func(p listedPrice) string {
		return fmt.Sprintf("%s/%s/%s", short(p.in), short(p.cache), short(p.out))
	}

	for _, listed := range list.Data {
		t.Run(listed.ID, func(t *testing.T) {
			week, timed := paintedWeek(t, listed.Pricing)
			if timed != wantTimed[listed.ID] {
				t.Fatalf("the painter reads the fixture's entry as time-priced = %v, want %v", timed, wantTimed[listed.ID])
			}
			at := slices.IndexFunc(loaded.Models, func(m config.Model) bool { return m.ModelName == listed.ID })
			if at < 0 {
				t.Fatalf("no registry model is named %s", listed.ID)
			}
			m := loaded.Models[at]
			if got := m.Malformed(); len(got) != 0 {
				t.Errorf("Malformed() = %q, want nothing: a row the sync wrote is not applied", got)
			}

			// The flat price is the dearest level of the week, which for a
			// model with one price is that price.
			dearest := week[0]
			for _, p := range week {
				if p.dearerThan(dearest) {
					dearest = p
				}
			}
			if flat := m.Cost.Flat(); !samePrice(flat.Input, dearest.in) || !samePrice(flat.Cache, dearest.cache) || !samePrice(flat.Output, dearest.out) {
				t.Errorf("the stored price is %s, want the dearest level of the schedule, %s", priceText(flat), name(dearest))
			}

			if got := m.Cost.TimePriced(); got != timed {
				t.Errorf("TimePriced() = %v, want %v", got, timed)
			}
			if !timed && len(m.Cost.TimePrices) != 0 {
				t.Errorf("a model with no time-of-day entry has %d time_prices row(s), want none", len(m.Cost.TimePrices))
			}
			for i, row := range m.Cost.TimePrices {
				if row.Label != cloudsync.OpenRouterLabel || row.Timezone != "UTC" {
					t.Errorf("time_prices[%d] is labelled %q in %q, want %q in UTC", i, row.Label, row.Timezone, cloudsync.OpenRouterLabel)
				}
			}

			minutesAt := map[string]int{}
			rowUsed := make([]bool, len(m.Cost.TimePrices))
			wrong := 0
			for i, want := range week {
				instant := monday.Add(time.Duration(i) * time.Minute)
				got := m.Cost.PriceAt(instant)
				elsewhere := m.Cost.PriceAt(instant.In(behind))
				minutesAt[name(want)]++
				if got.Row >= 0 {
					rowUsed[got.Row] = true
				}
				ok := samePrice(got.Input, want.in) && samePrice(got.Cache, want.cache) && samePrice(got.Output, want.out) &&
					// The flat price is in force exactly in the dearest
					// level's minutes, and a row in every other.
					(got.Row == -1) == (want == dearest) &&
					priceText(elsewhere) == priceText(got) && elsewhere.Row == got.Row
				if !ok {
					if wrong++; wrong <= 5 {
						t.Errorf("%s: the price in force is %s (row %d; asked in UTC-7: %s, row %d), OpenRouter's schedule says %s",
							instant.Format("Mon 15:04 UTC"), priceText(got), got.Row, priceText(elsewhere), elsewhere.Row, name(want))
					}
				}
			}
			if wrong != 0 {
				t.Errorf("%d of the week's %d minutes show a price that is not OpenRouter's", wrong, len(week))
			}
			if slices.Contains(rowUsed, false) {
				t.Errorf("a time_prices row is in force at no minute of the week: %v", rowUsed)
			}
			if got, want := fmt.Sprint(minutesAt), fmt.Sprint(wantMinutes[listed.ID]); got != want {
				t.Errorf("minutes of the week at each price: %s, want %s", got, want)
			}
			t.Logf("%d of %d minutes agree; minutes at each price (input/cached/output per million): %v; stored price %s; time-priced: %v",
				len(week)-wrong, len(week), minutesAt, priceText(m.Cost.Flat()), m.Cost.TimePriced())
		})
	}
}

// short prints a per-million price the way a plan line does, to six
// significant digits, so 0.13199999999999998 reads 0.132.
func short(v float64) string { return fmt.Sprintf("%.6g", v) }

func priceText(p config.PriceInForce) string {
	part := func(v *float64) string {
		if v == nil {
			return "-"
		}
		return short(*v)
	}
	return part(p.Input) + "/" + part(p.Cache) + "/" + part(p.Output)
}
