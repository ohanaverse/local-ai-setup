package config

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func pf(v float64) *float64 { return &v }

// offpeakPrice is the row `wt cloud-sync`'s ollama flow writes: ollama's
// off-peak window, outside 12:00 to 18:00 UTC on weekdays and all day at
// weekends.
func offpeakPrice(in, cache, out *float64) TimePrice {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	return TimePrice{
		Label: "off-peak", Timezone: "UTC",
		InputPricePerMillion: in, CachePricePerMillion: cache, OutputPricePerMillion: out,
		Windows: []CostWindow{
			{Days: weekdays, Start: "00:00", End: "12:00"},
			{Days: weekdays, Start: "18:00", End: "24:00"},
			{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
		},
	}
}

func peakCost(rows ...TimePrice) ModelCost {
	return ModelCost{InputPricePerMillion: pf(1.32), CachePricePerMillion: pf(0.044), OutputPricePerMillion: pf(3.96), TimePrices: rows}
}

// wantPrice fails unless got is the three prices given, from the row given
// (-1: the flat prices).
func wantPrice(t *testing.T, what string, got PriceInForce, in, cache, out *float64, row int) {
	t.Helper()
	same := func(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	text := func(p *float64) any {
		if p == nil {
			return "-"
		}
		return *p
	}
	if !same(got.Input, in) || !same(got.Cache, cache) || !same(got.Output, out) || got.Row != row {
		t.Errorf("%s: %v/%v/%v from row %d, want %v/%v/%v from row %d", what,
			text(got.Input), text(got.Cache), text(got.Output), got.Row, text(in), text(cache), text(out), row)
	}
}

// TestPriceAtOffpeakBoundaries pins the price in force around each edge of
// ollama's off-peak window, which is what the model picker shows for an
// ollama cloud model: a window is [start, end) to the minute, so 11:59 is
// off-peak and 12:00 is not, and 17:59:59 is still the 17:59 minute. A
// boundary read one minute off shows the wrong price for a minute a day; a
// window read as closed at its end would never let "24:00" mean midnight.
// (The cases are modelman's test_price_at_offpeak_boundaries, the reference
// this was ported from.) 2026-09-28 is a Monday.
func TestPriceAtOffpeakBoundaries(t *testing.T) {
	cost := peakCost(offpeakPrice(pf(0.66), pf(0.022), pf(1.98)))
	for _, c := range []struct {
		at      time.Time
		offpeak bool
	}{
		{time.Date(2026, 9, 28, 11, 59, 0, 0, time.UTC), true},
		{time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 9, 28, 17, 59, 59, 0, time.UTC), false},
		{time.Date(2026, 9, 28, 18, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC), true}, // Saturday
		// "24:00" ends the day: 23:59 is inside, and the next minute is
		// Tuesday 00:00, inside Tuesday's first window.
		{time.Date(2026, 9, 28, 23, 59, 59, 0, time.UTC), true},
		{time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), true},
		// The instant is what counts, not the zone it is written in: 05:00
		// PDT on Monday is 12:00 UTC.
		{time.Date(2026, 9, 28, 5, 0, 0, 0, time.FixedZone("PDT", -7*3600)), false},
	} {
		if c.offpeak {
			wantPrice(t, c.at.Format(time.RFC3339), cost.PriceAt(c.at), pf(0.66), pf(0.022), pf(1.98), 0)
		} else {
			wantPrice(t, c.at.Format(time.RFC3339), cost.PriceAt(c.at), pf(1.32), pf(0.044), pf(3.96), -1)
		}
	}
}

// TestPriceAtReadsAWindowInItsRowsTimezone pins that a row's windows are
// wall-clock times in the row's own IANA zone, whatever zone the machine is
// in: 09:00 to 10:00 on a Monday in Honolulu (UTC-10) is 19:00 to 20:00 UTC.
// A row a user writes for a provider that bills in its local time would
// otherwise be applied ten hours off. (modelman's
// test_price_at_non_utc_timezone.)
func TestPriceAtReadsAWindowInItsRowsTimezone(t *testing.T) {
	cost := peakCost(TimePrice{Timezone: "Pacific/Honolulu", InputPricePerMillion: pf(0.5),
		Windows: []CostWindow{{Days: []string{"mon"}, Start: "09:00", End: "10:00"}}})
	wantPrice(t, "Monday 19:30 UTC", cost.PriceAt(time.Date(2026, 9, 28, 19, 30, 0, 0, time.UTC)), pf(0.5), pf(0.044), pf(3.96), 0)
	wantPrice(t, "Monday 09:30 UTC", cost.PriceAt(time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)), pf(1.32), pf(0.044), pf(3.96), -1)
	// The day is the zone's day too: Monday 23:30 in Honolulu is Tuesday
	// 09:30 UTC, and a Tuesday window in UTC terms must not catch it.
	late := peakCost(TimePrice{Timezone: "Pacific/Honolulu", InputPricePerMillion: pf(0.5),
		Windows: []CostWindow{{Days: []string{"mon"}, Start: "23:00", End: "24:00"}}})
	wantPrice(t, "Tuesday 09:30 UTC", late.PriceAt(time.Date(2026, 9, 29, 9, 30, 0, 0, time.UTC)), pf(0.5), pf(0.044), pf(3.96), 0)
	wantPrice(t, "Monday 09:30 UTC", late.PriceAt(time.Date(2026, 9, 28, 9, 30, 0, 0, time.UTC)), pf(1.32), pf(0.044), pf(3.96), -1)
}

// TestPriceAtFollowsDaylightSavingTime pins what a window means on the two
// days a year a zone's clock jumps: it is read on the wall clock, as the row
// says it. In New York on 2026-11-01 the hour from 01:00 to 02:00 happens
// twice (EDT, then EST) and a window over it holds both times; on 2026-03-08
// the hour from 02:00 to 03:00 does not happen, and a window over it holds
// no instant that day. A fixed-offset reading would move every window of a
// user-written row by an hour for half the year.
func TestPriceAtFollowsDaylightSavingTime(t *testing.T) {
	row := func(start, end string) ModelCost {
		return peakCost(TimePrice{Timezone: "America/New_York", OutputPricePerMillion: pf(1),
			Windows: []CostWindow{{Days: []string{"sun"}, Start: start, End: end}}})
	}
	back := row("01:00", "02:00")
	for at, inside := range map[time.Time]bool{
		time.Date(2026, 11, 1, 4, 30, 0, 0, time.UTC): false, // 00:30 EDT
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC): true,  // 01:30 EDT
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC): true,  // 01:30 EST, the hour again
		time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC): false, // 02:30 EST
	} {
		if got := back.PriceAt(at).Row == 0; got != inside {
			t.Errorf("clocks back, %s: in the 01:00-02:00 window = %v, want %v", at.Format(time.RFC3339), got, inside)
		}
	}
	forward := row("02:00", "03:00")
	for _, at := range []time.Time{
		time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), // 01:30 EST
		time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC),  // 03:00 EDT: 02:00 never came
		time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), // 03:30 EDT
	} {
		if forward.PriceAt(at).Row != -1 {
			t.Errorf("clocks forward, %s: in the 02:00-03:00 window, which that day does not have", at.Format(time.RFC3339))
		}
	}
	// The same window a week later, when the hour exists: 02:30 EDT.
	if forward.PriceAt(time.Date(2026, 3, 15, 6, 30, 0, 0, time.UTC)).Row != 0 {
		t.Error("the Sunday after the change, 02:30 EDT is not in the 02:00-03:00 window")
	}
}

// TestPriceAtFallsBackPerField pins that a row replaces only the prices it
// sets: an off-peak row with an input price and nothing else leaves the
// cached and output prices at the flat ones. A row that blanked what it does
// not set would show a model as having no output price for part of the day.
// (modelman's test_price_at_per_field_fallback_to_default.)
func TestPriceAtFallsBackPerField(t *testing.T) {
	cost := peakCost(offpeakPrice(pf(0.66), nil, nil))
	wantPrice(t, "Saturday", cost.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.66), pf(0.044), pf(3.96), 0)
	// A model with rows and no flat price at all: the row's prices in its
	// window, none outside it.
	rowsOnly := ModelCost{TimePrices: []TimePrice{offpeakPrice(pf(0.66), nil, pf(1.98))}}
	wantPrice(t, "rows only, Saturday", rowsOnly.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.66), nil, pf(1.98), 0)
	wantPrice(t, "rows only, Monday noon", rowsOnly.PriceAt(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)), nil, nil, nil, -1)
}

// TestPriceAtFirstMatchingRowWins pins the order rule the two writers of
// rows rely on (each replaces its row where it stands, never moves it): when
// two rows' windows hold the instant, the first in the file supplies the
// price, whole, and the second is not consulted even for a price the first
// does not set. (modelman's test_price_at_first_matching_row_wins.)
func TestPriceAtFirstMatchingRowWins(t *testing.T) {
	weekend := []CostWindow{{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"}}
	cost := peakCost(
		TimePrice{Timezone: "UTC", Windows: weekend, InputPricePerMillion: pf(0.1)},
		TimePrice{Timezone: "UTC", Windows: weekend, InputPricePerMillion: pf(0.2), OutputPricePerMillion: pf(0.3)},
	)
	wantPrice(t, "Saturday", cost.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.1), pf(0.044), pf(3.96), 0)
	// A row whose window does not hold the instant is passed over for the
	// next one that does.
	cost.TimePrices[0].Windows = []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "24:00"}}
	wantPrice(t, "Saturday, first row Monday only", cost.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(0.2), pf(0.044), pf(0.3), 1)
}

// TestPriceAtAnOpenRouterSchedule pins the resolver on the rows `wt
// cloud-sync`'s openrouter flow stores for deepseek/deepseek-v4-pro-0813
// (#322): the flat price is the dearest level, in force on weekdays from
// 01:00 to 04:00 and 06:00 to 10:00 UTC, and the one row is the cheaper
// level for the rest of the week. The picker must show the cheap price for
// the 79% of the week it is in force, not the flat one.
func TestPriceAtAnOpenRouterSchedule(t *testing.T) {
	weekdays := []string{"mon", "tue", "wed", "thu", "fri"}
	cost := peakCost(TimePrice{
		Label: "openrouter", Timezone: "UTC",
		InputPricePerMillion: pf(0.66), CachePricePerMillion: pf(0.022), OutputPricePerMillion: pf(1.98),
		Windows: []CostWindow{
			{Days: weekdays, Start: "00:00", End: "01:00"},
			{Days: weekdays, Start: "04:00", End: "06:00"},
			{Days: weekdays, Start: "10:00", End: "24:00"},
			{Days: []string{"sat", "sun"}, Start: "00:00", End: "24:00"},
		},
	})
	for _, c := range []struct {
		at   time.Time
		dear bool
	}{
		{time.Date(2026, 10, 12, 0, 59, 0, 0, time.UTC), false}, // Monday
		{time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 2, 45, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 9, 59, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 10, 10, 2, 45, 0, 0, time.UTC), false}, // Saturday
	} {
		if c.dear {
			wantPrice(t, c.at.Format("Mon 15:04"), cost.PriceAt(c.at), pf(1.32), pf(0.044), pf(3.96), -1)
		} else {
			wantPrice(t, c.at.Format("Mon 15:04"), cost.PriceAt(c.at), pf(0.66), pf(0.022), pf(1.98), 0)
		}
	}
}

// night is a cost table with one hand-written row of one window: output 1.0
// from start to end on the days given, in zone.
func night(zone string, days []string, start, end string) ModelCost {
	return peakCost(TimePrice{Label: "night", Timezone: zone, OutputPricePerMillion: pf(1),
		Windows: []CostWindow{{Days: days, Start: start, End: end}}})
}

// wantHeld fails unless the cost table's one row is in force at exactly the
// instants marked true.
func wantHeld(t *testing.T, what string, cost ModelCost, cases []struct {
	at     time.Time
	inside bool
}) {
	t.Helper()
	for _, c := range cases {
		if got := cost.PriceAt(c.at).Row == 0; got != c.inside {
			t.Errorf("%s, %s: in the window = %v, want %v", what, c.at.Format("Mon 2006-01-02 15:04:05 MST"), got, c.inside)
		}
	}
}

// TestPriceAtAWindowPastMidnight pins what a window means when its end is
// before its start (#322, the owner's answer: "window should run past
// midnight"): "22:00" to "06:00" on "mon" is one window, from 22:00 on
// Monday to 06:00 on Tuesday. It starts only on a listed day, and its
// morning half belongs to the day it started on, so a Monday window holds
// Tuesday 05:00 and not Monday 05:00. This is how anyone writes a night
// rate; read as two half-windows on the listed day, or not read at all, the
// picker would show the night price on the wrong morning or never.
// 2026-09-28 is a Monday.
func TestPriceAtAWindowPastMidnight(t *testing.T) {
	type at = struct {
		at     time.Time
		inside bool
	}
	utc := func(day, hour, minute, second int) time.Time {
		return time.Date(2026, 9, day, hour, minute, second, 0, time.UTC)
	}
	mon := []string{"mon"}
	wantHeld(t, "mon 22:00 to 06:00", night("UTC", mon, "22:00", "06:00"), []at{
		{utc(28, 21, 59, 59), false}, // Monday, before the start
		{utc(28, 22, 0, 0), true},
		{utc(28, 23, 59, 59), true},
		{utc(29, 0, 0, 0), true}, // Tuesday, the morning after
		{utc(29, 5, 59, 59), true},
		{utc(29, 6, 0, 0), false},  // the end is not in the window
		{utc(29, 22, 0, 0), false}, // Tuesday is not listed: nothing starts on it
		{utc(28, 5, 0, 0), false},  // Monday morning would be Sunday's window
	})
	// The week wraps too: a Sunday window runs into Monday.
	wantHeld(t, "sun 22:00 to 06:00", night("UTC", []string{"sun"}, "22:00", "06:00"), []at{
		{utc(27, 22, 0, 0), true},  // Sunday
		{utc(28, 5, 59, 0), true},  // Monday
		{utc(28, 6, 0, 0), false},  // Monday
		{utc(27, 5, 0, 0), false},  // Sunday morning would be Saturday's window
		{utc(28, 22, 0, 0), false}, // Monday evening
	})
	// An end of "24:00" is the end of the listed day and never a window past
	// midnight. "00:00" as end with start > end is rejected by the validator
	// (it would mean 24 hours, which is almost never intended).
	wantHeld(t, "mon 22:00 to 24:00", night("UTC", mon, "22:00", "24:00"), []at{
		{utc(28, 21, 59, 0), false},
		{utc(28, 23, 59, 59), true},
		{utc(29, 0, 0, 0), false},
		{utc(29, 5, 0, 0), false},
	})
	// One minute short of a whole day: 06:00 to 05:59.
	wantHeld(t, "mon 06:00 to 05:59", night("UTC", mon, "06:00", "05:59"), []at{
		{utc(28, 5, 59, 0), false},
		{utc(28, 6, 0, 0), true},
		{utc(29, 5, 58, 59), true},
		{utc(29, 5, 59, 0), false},
	})
	// The window is on the wall clock of the row's zone, and so are its two
	// days. West of UTC: Monday 22:00 in Honolulu (UTC-10) is Tuesday 08:00
	// UTC, and the window ends at Tuesday 16:00 UTC, all of it on UTC's
	// Tuesday. East: Monday 22:00 in Tokyo (UTC+9) is Monday 13:00 UTC, and
	// the window ends at Monday 21:00 UTC, all of it on UTC's Monday.
	wantHeld(t, "Honolulu", night("Pacific/Honolulu", mon, "22:00", "06:00"), []at{
		{utc(29, 7, 59, 0), false},
		{utc(29, 8, 0, 0), true},
		{utc(29, 15, 59, 0), true},
		{utc(29, 16, 0, 0), false},
		{utc(28, 8, 0, 0), false}, // Sunday 22:00 in Honolulu
	})
	wantHeld(t, "Tokyo", night("Asia/Tokyo", mon, "22:00", "06:00"), []at{
		{utc(28, 12, 59, 0), false},
		{utc(28, 13, 0, 0), true},
		{utc(28, 20, 59, 0), true},
		{utc(28, 21, 0, 0), false},
		{utc(29, 13, 0, 0), false}, // Tuesday 22:00 in Tokyo
	})

	// The row is a row like any other: it supplies the prices it sets, and
	// the first row whose window holds the instant wins, whichever of them
	// runs past midnight. Two windows of one row may overlap; they are one
	// price.
	cost := peakCost(
		TimePrice{Label: "night", Timezone: "UTC", OutputPricePerMillion: pf(1), Windows: []CostWindow{
			{Days: mon, Start: "22:00", End: "06:00"},
			{Days: []string{"tue"}, Start: "00:00", End: "03:00"},
		}},
		TimePrice{Label: "tuesday", Timezone: "UTC", OutputPricePerMillion: pf(2), Windows: []CostWindow{
			{Days: []string{"tue"}, Start: "00:00", End: "24:00"},
		}},
	)
	wantPrice(t, "Tuesday 02:00, in both of the night row's windows", cost.PriceAt(utc(29, 2, 0, 0)), pf(1.32), pf(0.044), pf(1), 0)
	wantPrice(t, "Tuesday 05:00, in the night row and the Tuesday row", cost.PriceAt(utc(29, 5, 0, 0)), pf(1.32), pf(0.044), pf(1), 0)
	wantPrice(t, "Tuesday 06:00, the night row is over", cost.PriceAt(utc(29, 6, 0, 0)), pf(1.32), pf(0.044), pf(2), 1)
	if got := cost.TimePrices[0].Problem(); got != "" {
		t.Errorf("a row with a window past midnight: Problem() = %q, want none", got)
	}
}

// TestPriceAtAWindowPastMidnightFollowsDaylightSavingTime pins the "next
// day" of a window that runs past midnight as a calendar day on the row's
// wall clock, not 24 hours and not a fixed length: Saturday "22:00" to
// "06:00" ends when Sunday's clock reads 06:00. On the night a zone's clocks
// go back that window is nine hours long, and on the night they go forward
// seven. A reading that added eight hours to the start would end the night
// rate an hour early in autumn and an hour late in spring, in the zone the
// row's writer named. Pinned on both change days, west of UTC (New York:
// 2026-03-08 and 2026-11-01) and east of it (Berlin: 2026-03-29 and
// 2026-10-25).
func TestPriceAtAWindowPastMidnightFollowsDaylightSavingTime(t *testing.T) {
	type at = struct {
		at     time.Time
		inside bool
	}
	utc := func(month time.Month, day, hour, minute int) time.Time {
		return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
	}
	sat := []string{"sat"}
	newYork := night("America/New_York", sat, "22:00", "06:00")
	// Clocks back: Saturday 22:00 EDT is 02:00 UTC, Sunday 06:00 EST is
	// 11:00 UTC. Nine hours.
	wantHeld(t, "New York, clocks back", newYork, []at{
		{utc(11, 1, 1, 59), false},
		{utc(11, 1, 2, 0), true},
		{utc(11, 1, 5, 30), true},   // 01:30 EDT
		{utc(11, 1, 6, 30), true},   // 01:30 EST, the hour again
		{utc(11, 1, 10, 30), true},  // 05:30 EST: eight hours after the start it is not over
		{utc(11, 1, 10, 59), true},  // 05:59 EST
		{utc(11, 1, 11, 0), false},  // 06:00 EST
		{utc(11, 2, 3, 0), false},   // Sunday 22:00 EST: Sunday is not listed
		{utc(10, 31, 9, 59), false}, // Saturday 05:59 EDT would be Friday's window
	})
	// Clocks forward: Saturday 22:00 EST is 03:00 UTC, Sunday 06:00 EDT is
	// 10:00 UTC. Seven hours.
	wantHeld(t, "New York, clocks forward", newYork, []at{
		{utc(3, 8, 2, 59), false},
		{utc(3, 8, 3, 0), true},
		{utc(3, 8, 6, 59), true},   // 01:59 EST
		{utc(3, 8, 7, 0), true},    // 03:00 EDT: 02:00 never came
		{utc(3, 8, 9, 59), true},   // 05:59 EDT
		{utc(3, 8, 10, 0), false},  // 06:00 EDT
		{utc(3, 8, 10, 30), false}, // eight hours after the start it is over
	})
	// A window that starts on the change day: Sunday 22:00 EST on
	// 2026-11-01 is Monday 03:00 UTC, an hour later than the Sunday before.
	sunday := night("America/New_York", []string{"sun"}, "22:00", "06:00")
	wantHeld(t, "New York, starting on the day the clocks went back", sunday, []at{
		{utc(11, 2, 2, 30), false},
		{utc(11, 2, 3, 0), true},
		{utc(11, 2, 10, 59), true},
		{utc(11, 2, 11, 0), false},
		{utc(10, 26, 2, 0), true}, // the Sunday before: 22:00 EDT
	})
	berlin := night("Europe/Berlin", sat, "22:00", "06:00")
	// Clocks forward: Saturday 22:00 CET is 21:00 UTC, Sunday 06:00 CEST is
	// 04:00 UTC. Seven hours.
	wantHeld(t, "Berlin, clocks forward", berlin, []at{
		{utc(3, 28, 20, 59), false},
		{utc(3, 28, 21, 0), true},
		{utc(3, 29, 0, 59), true},  // 01:59 CET
		{utc(3, 29, 1, 0), true},   // 03:00 CEST: 02:00 never came
		{utc(3, 29, 3, 59), true},  // 05:59 CEST
		{utc(3, 29, 4, 0), false},  // 06:00 CEST
		{utc(3, 29, 4, 30), false}, // eight hours after the start it is over
	})
	// Clocks back: Saturday 22:00 CEST is 20:00 UTC, Sunday 06:00 CET is
	// 05:00 UTC. Nine hours.
	wantHeld(t, "Berlin, clocks back", berlin, []at{
		{utc(10, 24, 19, 59), false},
		{utc(10, 24, 20, 0), true},
		{utc(10, 25, 0, 30), true}, // 02:30 CEST
		{utc(10, 25, 1, 30), true}, // 02:30 CET, the hour again
		{utc(10, 25, 4, 30), true}, // 05:30 CET: eight hours after the start it is not over
		{utc(10, 25, 4, 59), true}, // 05:59 CET
		{utc(10, 25, 5, 0), false}, // 06:00 CET
	})
}

// badRows are cost.time_prices rows the registry's validator refuses, each
// with the validator's reason. The loader lets every one of them into a
// Config (it only types the values), so they are what a hand edit can leave
// in the file. A window that ends before it starts is not one of them: it
// runs past midnight (TestPriceAtAWindowPastMidnight).
func badRows() map[string]struct {
	row  TimePrice
	want string
} {
	always := []CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "00:00", End: "24:00"}}
	return map[string]struct {
		row  TimePrice
		want string
	}{
		"no timezone":          {TimePrice{Windows: always}, `timezone "" is not a known IANA timezone`},
		"the host's own zone":  {TimePrice{Timezone: "Local", Windows: always}, `timezone "Local" is not a known IANA timezone`},
		"an unknown zone":      {TimePrice{Timezone: "Mars/Olympus", Windows: always}, `timezone "Mars/Olympus" is not a known IANA timezone`},
		"no windows":           {TimePrice{Timezone: "UTC"}, "windows must be a non-empty array of tables"},
		"a window with no day": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Start: "00:00", End: "24:00"}}}, "windows[0]: days must be a non-empty list"},
		"a day that is none": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"monday"}, Start: "00:00", End: "24:00"}}},
			"windows[0]: days must be drawn from [mon tue wed thu fri sat sun], got monday"},
		"a start with no zero": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "9:00", End: "24:00"}}}, "windows[0]: start must be HH:MM, got 9:00"},
		"an end past 24:00": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "24:30"}}},
			`windows[0]: end must be HH:MM between 00:00 and 24:00, got "24:30"`},
		"75 minutes": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:75", End: "24:00"}}},
			`windows[0]: start must be HH:MM between 00:00 and 24:00, got "00:75"`},
		"an end that is text": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "noon"}}}, "windows[0]: end must be HH:MM, got noon"},
		// A start equal to the end: no minute read as [start, end), a whole day
		// read as running past midnight. Refused, so neither is guessed; the
		// test asks at 12:30 on a Monday, which the second reading would hold.
		"a start that is the end": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "12:30", End: "12:30"}}},
			"windows[0]: start and end must differ (a whole day is 00:00 to 24:00)"},
		"midnight to midnight as 00:00": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"mon"}, Start: "00:00", End: "00:00"}}},
			"windows[0]: start and end must differ (a whole day is 00:00 to 24:00)"},
		// "24:00" is an end, never a start, with any end after it.
		"a start at 24:00": {TimePrice{Timezone: "UTC", Windows: []CostWindow{{Days: []string{"sun"}, Start: "24:00", End: "13:00"}}}, "windows[0]: start must be before 24:00"},
		// One window that holds the instant beside one that is refused: the
		// row is refused, not the window.
		"a good window beside a bad one": {TimePrice{Timezone: "UTC", Windows: []CostWindow{always[0], {Days: []string{"mon"}, Start: "18:00", End: "18:00"}}},
			"windows[1]: start and end must differ (a whole day is 00:00 to 24:00)"},
		"a price below zero":           {TimePrice{Timezone: "UTC", Windows: always, OutputPricePerMillion: pf(-1)}, "output_price_per_million must be non-negative"},
		"a price that is not a number": {TimePrice{Timezone: "UTC", Windows: always, CachePricePerMillion: pf(math.NaN())}, "cache_price_per_million must be finite"},
	}
}

// TestPriceAtIgnoresARowItCannotRead pins the resolver on rows the
// registry's validator refuses, which a hand edit can leave in the file: the
// row is passed over whole, at every instant, and the flat price is in
// force. It must never panic (this runs while the picker draws, where a
// panic takes the terminal down mid-screen), never apply a row in some of
// its windows only, and never let a row price of -1 become the price in
// force, which would sort the model first and make it the default
// selection. A model whose only rows are such rows is not time-priced, so
// the picker does not mark a price that will never change.
func TestPriceAtIgnoresARowItCannotRead(t *testing.T) {
	always := []CostWindow{{Days: []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, Start: "00:00", End: "24:00"}}
	at := time.Date(2026, 9, 28, 12, 30, 0, 0, time.UTC) // Monday
	for name, c := range badRows() {
		row := c.row
		row.InputPricePerMillion = pf(0.01)
		cost := peakCost(row)
		wantPrice(t, name, cost.PriceAt(at), pf(1.32), pf(0.044), pf(3.96), -1)
		if cost.TimePriced() {
			t.Errorf("%s: TimePriced() = true for a model whose only row is not applied", name)
		}
	}
	// The row with a start at 24:00, at an instant a reading of it as a
	// window past midnight would hold: Monday 00:30 UTC, the morning after
	// its Sunday.
	late := peakCost(badRows()["a start at 24:00"].row)
	wantPrice(t, "a start at 24:00, Monday 00:30", late.PriceAt(time.Date(2026, 9, 28, 0, 30, 0, 0, time.UTC)), pf(1.32), pf(0.044), pf(3.96), -1)
	// A row that cannot be read does not hide a readable one after it, and
	// the model is then time-priced.
	cost := peakCost(TimePrice{Timezone: "Mars/Olympus", Windows: always, InputPricePerMillion: pf(0.01)},
		TimePrice{Timezone: "UTC", Windows: always, InputPricePerMillion: pf(0.5)})
	wantPrice(t, "an unreadable row first", cost.PriceAt(at), pf(0.5), pf(0.044), pf(3.96), 1)
	if !cost.TimePriced() {
		t.Error("TimePriced() = false for a model with one row that is applied")
	}
}

// TestTimePriceProblemIsTheValidatorsRefusal pins the words Problem gives
// for a row that is not applied. They are the registry validator's own (the
// refusal a write of that model gets), and `wt model list` prints them, so
// they are how a user learns why a row they wrote changes nothing: a time
// written "9:00" reads "start must be HH:MM, got 9:00". A row either flow's
// sync writes has no problem, and neither has a window that runs past
// midnight.
func TestTimePriceProblemIsTheValidatorsRefusal(t *testing.T) {
	for name, c := range badRows() {
		if got := c.row.Problem(); got != c.want {
			t.Errorf("%s: Problem() = %q, want %q", name, got, c.want)
		}
		// By construction, but pinned: Problem is the validator's answer for
		// the same row, so the two cannot come to disagree about a rule.
		if err := validateTimePrice(c.row.table()); err == nil || err.Error() != c.want {
			t.Errorf("%s: validateTimePrice = %v, want %q", name, err, c.want)
		}
	}
	if got := offpeakPrice(pf(0.66), pf(0.022), pf(1.98)).Problem(); got != "" {
		t.Errorf("ollama's off-peak row: Problem() = %q, want none", got)
	}
	if got := offpeakPrice(nil, nil, nil).Problem(); got != "" {
		t.Errorf("a row that sets no price: Problem() = %q, want none", got)
	}
	for _, w := range []CostWindow{
		{Days: []string{"mon"}, Start: "22:00", End: "06:00"},
		{Days: []string{"sun"}, Start: "23:59", End: "24:00"},
		{Days: []string{"mon"}, Start: "22:00", End: "24:00"},
	} {
		row := TimePrice{Timezone: "America/New_York", Windows: []CostWindow{w}}
		if got := row.Problem(); got != "" {
			t.Errorf("a window from %s to %s: Problem() = %q, want none", w.Start, w.End, got)
		}
		if err := validateTimePrice(row.table()); err != nil {
			t.Errorf("a window from %s to %s: validateTimePrice = %v, want it accepted", w.Start, w.End, err)
		}
	}
}

// timedRegistry is a registry with one cloud model, its cost table holding
// the time_prices text given.
func timedRegistry(timePrices string) string {
	return `
[[providers]]
id = "openrouter"
location = "cloud"
[providers.auth]
type = "api_key"

[[models]]
id = "openrouter/night-owl"
family = "owl"
provider_id = "openrouter"
model_name = "acme/night-owl"
tags = ["code"]

[models.cost]
input_price_per_million = 12.5
cache_price_per_million = 1.25
output_price_per_million = 75.0
` + timePrices
}

// TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt pins what a
// hand-written row that breaks a rule does to a registry: nothing, loudly
// enough. The file still loads (a slip in one row must not stop every wt
// command), the row is not applied at any instant, the model's price is its
// flat one, and Model.Malformed names the row and the rule, which `wt model
// list` prints and the Models tab shows. A readable row beside it is still
// applied.
func TestLoadKeepsATimePricesRowTheValidatorRefusesAndNamesIt(t *testing.T) {
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	writeRegistry(t, t.TempDir(), timedRegistry(`
[[models.cost.time_prices]]
label = "office"
timezone = "America/New_York"
output_price_per_million = 10.0
windows = [{ days = ["mon", "tue", "wed", "thu", "fri"], start = "9:00", end = "17:00" }]

[[models.cost.time_prices]]
label = "weekend"
timezone = "UTC"
output_price_per_million = 40.0
windows = [{ days = ["sat", "sun"], start = "00:00", end = "24:00" }]
`))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want the registry to load", err)
	}
	m := cfg.Models[0]
	if got, want := m.Malformed(), []string{"cost.time_prices[0]: windows[0]: start must be HH:MM, got 9:00"}; !slices.Equal(got, want) {
		t.Errorf("Malformed() = %q, want %q", got, want)
	}
	// Monday 14:00 UTC is 10:00 in New York: inside the window as its writer
	// meant it, and a Monday, so outside the weekend row.
	wantPrice(t, "inside the office window", m.Cost.PriceAt(time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)), pf(12.5), pf(1.25), pf(75), -1)
	wantPrice(t, "Saturday", m.Cost.PriceAt(time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)), pf(12.5), pf(1.25), pf(40), 1)
	if !m.Cost.TimePriced() {
		t.Error("TimePriced() = false, want true: the weekend row is applied")
	}
}

// TestLoadAppliesAWindowPastMidnight pins the owner's example through the
// real loader (#322): a row written by hand in registry.toml with one
// window from "22:00" to "06:00" loads, is named by nothing, and is in
// force from 22:00 on each listed day to 06:00 the next morning in the
// row's own zone. Before #322 such a row was in the file and applied by
// nothing, and a write that touched its model was refused.
func TestLoadAppliesAWindowPastMidnight(t *testing.T) {
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	writeRegistry(t, t.TempDir(), timedRegistry(`
[[models.cost.time_prices]]
label = "night"
timezone = "America/New_York"
output_price_per_million = 10.0
windows = [{ days = ["mon", "tue", "wed", "thu", "fri"], start = "22:00", end = "06:00" }]
`))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want the registry to load", err)
	}
	m := cfg.Models[0]
	if got := m.Malformed(); len(got) != 0 {
		t.Errorf("Malformed() = %q, want nothing: the row keeps the rules", got)
	}
	if !m.Cost.TimePriced() {
		t.Error("TimePriced() = false, want true: the night row is applied")
	}
	for _, c := range []struct {
		what  string
		at    time.Time
		night bool
	}{
		{"Monday 22:45 in New York", time.Date(2026, 9, 29, 2, 45, 0, 0, time.UTC), true},
		{"Tuesday 05:59 in New York", time.Date(2026, 9, 29, 9, 59, 0, 0, time.UTC), true},
		{"Tuesday 06:00 in New York", time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC), false},
		{"Saturday 05:00 in New York, the morning after Friday", time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC), true},
		{"Saturday 22:45 in New York, not a listed day", time.Date(2026, 10, 4, 2, 45, 0, 0, time.UTC), false},
		{"Monday 05:00 in New York, the morning after Sunday", time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC), false},
	} {
		if c.night {
			wantPrice(t, c.what, m.Cost.PriceAt(c.at), pf(12.5), pf(1.25), pf(10), 0)
		} else {
			wantPrice(t, c.what, m.Cost.PriceAt(c.at), pf(12.5), pf(1.25), pf(75), -1)
		}
	}
}

// TestLoadStopsOnATimePricesValueOfTheWrongType pins the one kind of slip in
// a cost.time_prices row that wt does not tolerate: a value of the wrong TOML
// type (windows that are a string, a price in quotes, days that are not a
// list). Like a wrong type anywhere else in the registry it fails the load,
// for every wt command, with an error that names the key; nothing reaches
// the resolver, so its tests cover only rows that decode. This is the
// loader's behaviour from before #322; it is pinned here because rows are
// now worth writing by hand, and the error is what such a slip gets.
func TestLoadStopsOnATimePricesValueOfTheWrongType(t *testing.T) {
	for name, c := range map[string]struct{ row, key string }{
		"windows that are a string":   {"timezone = \"UTC\"\nwindows = \"always\"", "models.cost.time_prices.windows"},
		"a price in quotes":           {"timezone = \"UTC\"\ninput_price_per_million = \"cheap\"\nwindows = []", "models.cost.time_prices.input_price_per_million"},
		"a timezone that is a number": {"timezone = 5\nwindows = []", "models.cost.time_prices.timezone"},
		"days that are a string":      {"timezone = \"UTC\"\nwindows = [{ days = \"mon\", start = \"00:00\", end = \"24:00\" }]", "models.cost.time_prices.windows.days"},
	} {
		t.Setenv("WT_REGISTRY", "")
		t.Setenv("MODELMAN_REGISTRY", "")
		writeRegistry(t, t.TempDir(), timedRegistry("\n[[models.cost.time_prices]]\n"+c.row+"\n"))
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), c.key) || !strings.Contains(err.Error(), "incompatible types") {
			t.Errorf("%s: Load() = %v, want a parse error that names %s", name, err, c.key)
		}
	}
}

// TestFlatAndTimePriced pins the two small readers the picker uses beside
// PriceAt: Flat is the cost table's own prices with no row applied (what a
// LiteLLM route carries), and TimePriced says whether the model has rows at
// all, which is what marks its price in the picker as one that changes.
func TestFlatAndTimePriced(t *testing.T) {
	plain := ModelCost{InputPricePerMillion: pf(1), OutputPricePerMillion: pf(2)}
	wantPrice(t, "Flat", plain.Flat(), pf(1), nil, pf(2), -1)
	wantPrice(t, "PriceAt with no rows", plain.PriceAt(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)), pf(1), nil, pf(2), -1)
	timed := peakCost(offpeakPrice(pf(0.66), pf(0.022), pf(1.98)))
	wantPrice(t, "Flat of a model with rows", timed.Flat(), pf(1.32), pf(0.044), pf(3.96), -1)
	if plain.TimePriced() || !timed.TimePriced() || (ModelCost{}).TimePriced() {
		t.Error("TimePriced: want true only for the cost table with a time_prices row that is applied")
	}
	// The zero time is an instant like any other (Monday 0001-01-01 00:00
	// UTC, inside the off-peak window): PriceAt has no "no clock" value.
	wantPrice(t, "the zero time", timed.PriceAt(time.Time{}), pf(0.66), pf(0.022), pf(1.98), 0)
}

// TestAZoneIsReadFromDiskOnce pins that a timezone's file is read from the
// host's zone database once for the life of the process, however many times
// a row in that zone is checked or priced. Go keeps no zone but UTC in
// memory, and the model picker checks and prices every row of its table,
// more than once each, on the goroutine that also handles keys: read per
// call, a registry of 200 models with rows in America/New_York took a third
// of a second to open. A zone that is not in the database is asked for again
// each time, so a name that is no zone is still refused and still named.
func TestAZoneIsReadFromDiskOnce(t *testing.T) {
	reads := map[string]int{}
	real := zoneLoad
	zoneLoad = func(name string) (*time.Location, error) {
		reads[name]++
		return real(name)
	}
	// Clear the cache
	zoneCache.Lock()
	zoneCache.m = make(map[string]*time.Location)
	zoneCache.Unlock()
	t.Cleanup(func() {
		zoneLoad = real
		zoneCache.Lock()
		zoneCache.m = make(map[string]*time.Location)
		zoneCache.Unlock()
	})

	cost := night("America/New_York", []string{"mon"}, "22:00", "06:00")
	cost.TimePrices = append(cost.TimePrices, night("Europe/Berlin", []string{"tue"}, "01:00", "02:00").TimePrices...)
	inside := time.Date(2026, 9, 29, 2, 30, 0, 0, time.UTC) // Monday 22:30 in New York
	for range 50 {
		if got := cost.PriceAt(inside); got.Row != 0 {
			t.Fatalf("PriceAt: row %d, want row 0 (Monday 22:30 in New York)", got.Row)
		}
		if !cost.TimePriced() {
			t.Fatal("TimePriced: false for a cost with two readable rows")
		}
		if problem := cost.TimePrices[1].Problem(); problem != "" {
			t.Fatalf("Problem: %q for a readable row", problem)
		}
	}
	for _, zone := range []string{"America/New_York", "Europe/Berlin"} {
		if reads[zone] != 1 {
			t.Errorf("%s was read from the zone database %d times, want once", zone, reads[zone])
		}
	}

	// A name that is no zone is not remembered as one: it is refused on
	// every call, in the validator's words.
	bad := TimePrice{Timezone: "Mars/Olympus", Windows: []CostWindow{{Days: []string{"mon"}, Start: "01:00", End: "02:00"}}}
	for range 3 {
		if got, want := bad.Problem(), `timezone "Mars/Olympus" is not a known IANA timezone`; got != want {
			t.Errorf("Problem: %q, want %q", got, want)
		}
	}
	if reads["Mars/Olympus"] != 3 {
		t.Errorf("an unknown zone was looked up %d times in 3 checks, want 3", reads["Mars/Olympus"])
	}
}
