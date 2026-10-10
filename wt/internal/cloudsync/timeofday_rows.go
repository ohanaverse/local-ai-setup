package cloudsync

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// OpenRouterLabel is the label of the time_prices rows the openrouter flow owns:
// one per price level of a model's time-of-day schedule other than the one
// stored as the flat price. Rows with any other label are never touched by
// the openrouter flow.
const OpenRouterLabel = "openrouter"

// registryDays are the registry's names for the days of the week, Monday
// first: the order of Rate.Spans.
var registryDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// rateRow is one price level as a time_prices row, keys in schema order.
// Days with the same times share their windows, and a day's windows are in
// time order, so the row does not depend on the order of OpenRouter's list.
// Each day's hours are written under that day: no window of the row runs
// past midnight, though the registry accepts one (#322). A level that covers
// several midnights running (a weekend into Monday morning) has no one way
// to be cut into such windows, and this form has exactly one.
func rateRow(r Rate) *tomlw.Table {
	row := tomlw.NewTable()
	row.Set("label", OpenRouterLabel)
	row.Set("timezone", "UTC")
	setRowPrices(row, r.Input, r.Cache, r.Output)
	var windows []any
	done := [daysPerWeek]bool{}
	for d := range daysPerWeek {
		if done[d] || len(r.Spans[d]) == 0 {
			continue
		}
		var days []any
		for e := d; e < daysPerWeek; e++ {
			if slices.Equal(r.Spans[e], r.Spans[d]) {
				days, done[e] = append(days, registryDays[e]), true
			}
		}
		for _, span := range r.Spans[d] {
			w := tomlw.NewTable()
			w.Set("days", slices.Clone(days))
			w.Set("start", clock(span.Start))
			w.Set("end", clock(span.End))
			windows = append(windows, w)
		}
	}
	row.Set("windows", windows)
	return row
}

func clock(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

func isOpenRouterRow(row *tomlw.Table) bool { return str(row, "label") == OpenRouterLabel }

// withOpenRouterRows is existing with the openrouter flow's rows replaced by
// those for rates, where the first of them stood (time_prices resolve first
// match wins, so they are not moved), or at the end when there were none.
// With no rates the flow's rows are dropped: the model no longer has a
// schedule. Every other row is kept, in place; when there is nothing to
// replace and nothing to add, existing itself is returned.
//
// It is returned too when the flow's rows already are, one for one and in
// order, the rows for rates, wherever they stand: a file edited by hand can
// hold another label's row between two of the flow's, and gathering them
// would move that row for a plan line that reads the same on both sides (a
// row of another label is not printed).
func withOpenRouterRows(existing []*tomlw.Table, rates []Rate) []*tomlw.Table {
	at := -1
	var rows, own, want []*tomlw.Table
	for _, row := range existing {
		if !isOpenRouterRow(row) {
			rows = append(rows, row)
			continue
		}
		own = append(own, row)
		if at < 0 {
			at = len(rows)
		}
	}
	for _, r := range rates {
		want = append(want, rateRow(r))
	}
	if sameRows(own, want) {
		return existing
	}
	if at < 0 {
		at = len(rows)
	}
	return slices.Insert(rows, at, want...)
}

// rowKeys and windowKeys are the keys the sync writes on one of its rows and
// on each of the row's windows.
var (
	rowKeys    = []string{"label", "timezone", "input_price_per_million", "cache_price_per_million", "output_price_per_million", "windows"}
	windowKeys = []string{"days", "start", "end"}
)

// formatOpenRouterRow prints one of the flow's rows for a plan line:
// ` (openrouter 0.66/0.022/1.98 mon-fri 00:00-01:00, sat-sun 00:00-24:00)`.
// Rows are compared whole, so a row someone edited is replaced even when its
// prices and windows are the sync's; what else can differ is therefore
// printed too, or the line would read the same on both sides: a timezone
// that is not UTC, and ` +keys` for a row or a window with a key the sync
// does not write.
func formatOpenRouterRow(row *tomlw.Table) string {
	text := fmt.Sprintf(" (openrouter %s/%s/%s", formatPrice(number(row, "input_price_per_million")),
		formatPrice(number(row, "cache_price_per_million")), formatPrice(number(row, "output_price_per_million")))
	if tz := str(row, "timezone"); tz != "UTC" {
		text += fmt.Sprintf(" timezone=%q", tz)
	}
	text += " " + formatWindows(row)
	extra := slices.ContainsFunc(row.Keys(), func(k string) bool { return !slices.Contains(rowKeys, k) })
	raw, _ := row.Get("windows")
	list, _ := raw.([]any)
	for _, item := range list {
		if w, ok := item.(*tomlw.Table); ok && slices.ContainsFunc(w.Keys(), func(k string) bool { return !slices.Contains(windowKeys, k) }) {
			extra = true
		}
	}
	if extra {
		text += " +keys"
	}
	return text + ")"
}

// formatWindows prints a row's windows for a plan line: `mon-fri 00:00-01:00
// 04:00-06:00, sat-sun 00:00-24:00`. It prints whatever the row holds, so a
// change to the windows always changes the plan's text; `?` stands for a
// windows value that is missing or not a list, and for an entry that is not
// a table.
func formatWindows(row *tomlw.Table) string {
	raw, _ := row.Get("windows")
	list, isList := raw.([]any)
	if !isList {
		return "?"
	}
	var groups []string
	last := ""
	for _, item := range list {
		w, ok := item.(*tomlw.Table)
		if !ok {
			groups, last = append(groups, "?"), ""
			continue
		}
		days := formatDays(w)
		span := str(w, "start") + "-" + str(w, "end")
		if len(groups) > 0 && days == last {
			groups[len(groups)-1] += " " + span
			continue
		}
		groups, last = append(groups, days+" "+span), days
	}
	return strings.Join(groups, ", ")
}

// formatDays prints a window's days: a run of consecutive days as
// `mon-fri`, anything else as written, joined with commas.
func formatDays(w *tomlw.Table) string {
	raw, _ := w.Get("days")
	list, _ := raw.([]any)
	var names []string
	run := len(list) > 1
	for i, item := range list {
		name, _ := item.(string)
		names = append(names, name)
		if d := slices.Index(registryDays, name); d < 0 || d != slices.Index(registryDays, names[0])+i {
			run = false
		}
	}
	if run {
		return names[0] + "-" + names[len(names)-1]
	}
	return strings.Join(names, ",")
}
