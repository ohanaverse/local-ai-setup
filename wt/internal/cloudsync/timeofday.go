package cloudsync

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

const (
	minutesPerDay = 24 * 60
	daysPerWeek   = 7
)

var (
	// openRouterDays are OpenRouter's utc_days names, Monday first.
	openRouterDays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

	// timeConditionKeys are the keys that make an overrides entry a
	// time-of-day entry.
	timeConditionKeys = []string{"utc_start", "utc_end", "utc_days"}

	// overridePriceKeys are the price keys OpenRouter documents for a pricing
	// object. A time-of-day entry may carry any of them; the openrouter flow
	// reads three. An entry's prices have "the same keys and units as the
	// base pricing object" (OpenRouter's Models guide), so a key the model's
	// own pricing object has at its top level is a price key too, whether or
	// not it is in this list: a price OpenRouter adds later does not stop a
	// schedule from being read. A key that is none of these and not a time
	// condition is a condition wt does not know, and OpenRouter's rule for a
	// consumer is not to apply such an entry.
	overridePriceKeys = []string{
		"prompt", "completion", "input_cache_read", "input_cache_write", "input_cache_write_1h",
		"audio", "audio_output", "input_audio_cache", "image", "image_output", "image_token",
		"internal_reasoning", "request", "web_search",
	}
)

// Span is the minutes [Start, End) of one UTC day, 0 to 1440.
type Span struct{ Start, End int }

// Rate is one price level of a model's time-of-day schedule and when in the
// UTC week it applies.
type Rate struct {
	Input, Cache, Output *float64
	// Spans are the times of each UTC weekday (index 0 is Monday) at this
	// price, in ascending order, adjoining minutes joined.
	Spans [daysPerWeek][]Span
}

// parseSchedule reads the time-of-day entries of a pricing object's
// `overrides` list into price levels, dearest first: by output price, then
// input, then cached-input. (The order is by one price at a time, so the
// first level is the highest on output and need not be the highest on the
// other two.) It returns nil, "" for a model with no
// time-of-day entry, and a reason when there are such entries and they
// cannot be used.
//
// OpenRouter's rules, from its Models guide: utc_start and utc_end are HHMM
// clock numbers in UTC, the window is [start, end) and wraps past midnight
// when the end is not after the start; utc_days names the UTC weekdays the
// window (or, with no window, the whole day) applies on, tested at the
// instant of the request, and every day when absent; where entries overlap,
// the later one wins. The time entries cover the whole week, which is what
// makes this a function of the response alone: the top-level prices of such
// a model are those of the window in force when the list was fetched, and
// are not read.
//
// An entry with no utc_ key is not a time-of-day entry (a prompt-size tier,
// min_prompt_tokens, or a condition wt does not know) and is ignored: the
// top-level price already is the price under default conditions.
func parseSchedule(pricing map[string]any) (rates []Rate, unusable string) {
	entries, _ := pricing["overrides"].([]any)
	var (
		levels    []level
		week      [daysPerWeek * minutesPerDay]int
		withCache int
	)
	for i := range week {
		week[i] = -1
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok || !slices.ContainsFunc(timeConditionKeys, func(k string) bool { _, has := entry[k]; return has }) {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(entry)) {
			switch {
			case key == "min_prompt_tokens":
				return nil, "an entry sets both a time window and min_prompt_tokens"
			case !slices.Contains(timeConditionKeys, key) && !isPriceKey(pricing, key):
				return nil, fmt.Sprintf("an entry has a key wt does not know (%q)", key)
			}
		}
		days, ok := entryDays(entry)
		if !ok {
			return nil, "utc_days is not a list of weekday names"
		}
		start, end, why := entryWindow(entry)
		if why != "" {
			return nil, why
		}
		var l level
		for _, f := range []struct {
			key  string
			into **float64
		}{{"prompt", &l.in}, {"completion", &l.out}, {"input_cache_read", &l.cache}} {
			v, skipped := perMillion(entry[f.key])
			if skipped {
				return nil, "its " + f.key + " price in one window is negative or not a number"
			}
			*f.into = v
		}
		if l.in == nil || l.out == nil {
			// The missing price would be inherited from the top level, which
			// is the price of whichever window is in force.
			return nil, "an entry has no prompt or no completion price"
		}
		if l.cache != nil {
			withCache++
		}
		levels = append(levels, l)
		for d := range daysPerWeek {
			if !days[d] {
				continue
			}
			for m := range minutesPerDay {
				if start < end && (m < start || m >= end) || start > end && m < start && m >= end {
					continue
				}
				week[d*minutesPerDay+m] = len(levels) - 1
			}
		}
	}
	if len(levels) == 0 {
		return nil, ""
	}
	if withCache != 0 && withCache != len(levels) {
		return nil, "some of its windows have a cached-input price and some do not"
	}
	if slices.Contains(week[:], -1) {
		return nil, "its windows do not cover the whole week"
	}
	for d := range daysPerWeek {
		for m := 0; m < minutesPerDay; {
			l, from := levels[week[d*minutesPerDay+m]], m
			for m < minutesPerDay && levels[week[d*minutesPerDay+m]].same(l) {
				m++
			}
			at := slices.IndexFunc(rates, func(r Rate) bool { return l.same(level{r.Input, r.Cache, r.Output}) })
			if at < 0 {
				rates = append(rates, Rate{Input: l.in, Cache: l.cache, Output: l.out})
				at = len(rates) - 1
			}
			rates[at].Spans[d] = append(rates[at].Spans[d], Span{from, m})
		}
	}
	slices.SortStableFunc(rates, func(a, b Rate) int {
		return cmp.Or(cmp.Compare(*b.Output, *a.Output), cmp.Compare(*b.Input, *a.Input), cmp.Compare(deref(b.Cache), deref(a.Cache)))
	})
	return rates, ""
}

// isPriceKey reports whether key, on an overrides entry of pricing, names a
// price: one of the documented price keys, or a key pricing itself has at
// its top level (other than the overrides list).
func isPriceKey(pricing map[string]any, key string) bool {
	if slices.Contains(overridePriceKeys, key) {
		return true
	}
	_, atTop := pricing[key]
	return atTop && key != "overrides"
}

// level is the three prices one overrides entry charges.
type level struct{ in, cache, out *float64 }

func (l level) same(o level) bool {
	return samePrice(l.in, o.in) && samePrice(l.cache, o.cache) && samePrice(l.out, o.out)
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// entryDays reads utc_days: the weekdays an entry applies on, every day when
// the key is absent.
func entryDays(entry map[string]any) (days [daysPerWeek]bool, ok bool) {
	raw, has := entry["utc_days"]
	if !has {
		for d := range days {
			days[d] = true
		}
		return days, true
	}
	list, isList := raw.([]any)
	if !isList || len(list) == 0 {
		return days, false
	}
	for _, item := range list {
		name, _ := item.(string)
		d := slices.Index(openRouterDays, name)
		if d < 0 {
			return days, false
		}
		days[d] = true
	}
	return days, true
}

// entryWindow reads utc_start and utc_end as minutes of the day. With
// neither key the window is the whole day, which is start == end here: a
// window whose end is not after its start wraps, and one that wraps onto
// its own start covers every minute.
func entryWindow(entry map[string]any) (start, end int, unusable string) {
	_, hasStart := entry["utc_start"]
	_, hasEnd := entry["utc_end"]
	if !hasStart && !hasEnd {
		return 0, 0, ""
	}
	if hasStart != hasEnd {
		return 0, 0, "an entry has only one of utc_start and utc_end"
	}
	start, okStart := hhmmMinutes(entry["utc_start"])
	end, okEnd := hhmmMinutes(entry["utc_end"])
	if !okStart || !okEnd {
		return 0, 0, "utc_start or utc_end is not an HHMM time"
	}
	return start, end, ""
}

// hhmmMinutes reads an HHMM clock number (1630 is 16:30, 30 is 00:30) as
// minutes of the day.
func hhmmMinutes(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	hhmm, err := strconv.Atoi(n.String())
	if err != nil || hhmm < 0 || hhmm/100 > 23 || hhmm%100 > 59 {
		return 0, false
	}
	return hhmm/100*60 + hhmm%100, true
}

// mainRate is which of a schedule's price levels (dearest first) is stored
// as the model's flat price: the first, the one with the highest output
// price. This function is the one place that says so. The other levels are
// stored as time_prices rows (timeofday_rows.go), so the flat price is the
// model's price at any time none of those rows is in force, the price
// LiteLLM's route carries, and what wt shows wherever it does not apply the
// rows. For a request LiteLLM prices from the route, the first level does
// not understate the cost as long as it is also the highest on input and
// cached input, which holds for every schedule OpenRouter published when
// this was written and is not checked: TestParseOpenRouterTimeOfDayShapes
// pins a schedule where it does not hold.
func mainRate(rates []Rate) int { return 0 }
