package config

import (
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// PriceInForce is a model's three per-token prices, per million tokens, at
// one instant. A nil price is one the model does not have.
type PriceInForce struct {
	Input, Cache, Output *float64
	// Row is the index in ModelCost.TimePrices of the row whose window held
	// the instant, or -1 when no row's did and the prices are the flat ones.
	Row int
}

// TimePriced reports whether c has a time_prices row that PriceAt applies,
// which is to say whether its price depends on when it is asked for. A cost
// table whose every row has a Problem has one price, the flat one.
func (c ModelCost) TimePriced() bool {
	return slices.ContainsFunc(c.TimePrices, func(row TimePrice) bool { return row.Problem() == "" })
}

// Flat is c's own three prices with no time_prices row applied: the prices
// in force whenever no row's window holds the instant, and the ones a
// LiteLLM route carries.
func (c ModelCost) Flat() PriceInForce {
	return PriceInForce{Input: c.InputPricePerMillion, Cache: c.CachePricePerMillion, Output: c.OutputPricePerMillion, Row: -1}
}

// PriceAt is the prices in force for c at the instant at. The rows are tried
// in file order and the first whose windows hold the instant wins: it
// supplies each price it sets, and a price it does not set stays the flat
// one. With no such row the flat prices are in force.
//
// A window is the minutes [start, end) of the listed weekdays on the wall
// clock of its row's IANA timezone, so it follows that zone's daylight
// saving time: an hour the clock repeats is in the window both times, and an
// hour it skips is in it at no instant that day. A window whose end is
// before its start runs past midnight: it starts at start on each listed day
// and ends at end on the next calendar day of that wall clock ("22:00" to
// "06:00" on "mon" is Monday 22:00 to Tuesday 06:00), so it is an hour
// longer on the night the clocks go back and an hour shorter on the night
// they go forward. Only at's instant counts, not the zone it is expressed
// in.
//
// A row with a Problem is passed over whole, at every instant, and the rows
// after it are still tried. PriceAt never fails: it runs while a screen is
// drawn.
//
// This is the reader of every cost.time_prices row, those `wt cloud-sync`
// writes and those written by hand. It is pure: the caller passes the
// clock.
func (c ModelCost) PriceAt(at time.Time) PriceInForce {
	p := c.Flat()
	for i, row := range c.TimePrices {
		if row.Problem() != "" || !row.holds(at) {
			continue
		}
		p.Row = i
		if row.InputPricePerMillion != nil {
			p.Input = row.InputPricePerMillion
		}
		if row.CachePricePerMillion != nil {
			p.Cache = row.CachePricePerMillion
		}
		if row.OutputPricePerMillion != nil {
			p.Output = row.OutputPricePerMillion
		}
		break
	}
	return p
}

// Problem says why PriceAt does not apply the row: the reason the registry's
// validator refuses it (validateTimePrice, the rule a registry write holds a
// row to), in the validator's words, or "" for a row it accepts. The loader
// only types a row's values, so a row written by hand can be in the file and
// break a rule: a timezone that is not an IANA name, a time that is not
// "HH:MM" ("9:00"), a window whose start is its end, a price below zero. (A
// window whose end is before its start breaks none: it runs past midnight.)
// One bad window is enough: the validator refuses the row, not the window,
// and a row applied in some of its windows only would show a price its
// writer did not mean. Model.Malformed names such a row, which is how `wt
// model list` and the Models tab say that it is in the file and not in use.
func (tp TimePrice) Problem() string {
	if err := validateTimePrice(tp.table()); err != nil {
		return err.Error()
	}
	return ""
}

// table is the row as the registry's validator reads one: the same keys,
// holding the values the typed decode gave.
func (tp TimePrice) table() *tomlw.Table {
	row := tomlw.NewTable()
	row.Set("timezone", tp.Timezone)
	for i, v := range []*float64{tp.InputPricePerMillion, tp.CachePricePerMillion, tp.OutputPricePerMillion} {
		if v != nil {
			row.Set(priceKeys[i], *v)
		}
	}
	windows := make([]any, len(tp.Windows))
	for i, w := range tp.Windows {
		days := make([]any, len(w.Days))
		for j, d := range w.Days {
			days[j] = d
		}
		window := tomlw.NewTable()
		window.Set("days", days)
		window.Set("start", w.Start)
		window.Set("end", w.End)
		windows[i] = window
	}
	row.Set("windows", windows)
	return row
}

// holds reports whether one of the row's windows holds the instant at. It is
// asked only of a row with no Problem, and still reads nothing it has not
// checked: a zone or a time it cannot read holds no instant.
func (tp TimePrice) holds(at time.Time) bool {
	// time.LoadLocation reads "" as UTC and "Local" as the host's zone;
	// neither is an IANA name, and the validator refuses both.
	if tp.Timezone == "" || tp.Timezone == "Local" {
		return false
	}
	zone, err := time.LoadLocation(tp.Timezone)
	if err != nil {
		return false
	}
	local := at.In(zone)
	// time.Weekday counts from Sunday; weekDays from Monday. The day before
	// is the calendar day before on that wall clock, whatever the zone's
	// clock did in between.
	day := weekDays[(int(local.Weekday())+6)%7]
	dayBefore := weekDays[(int(local.Weekday())+5)%7]
	minute := local.Hour()*60 + local.Minute()
	for _, w := range tp.Windows {
		start, okStart := clockMinutes(w.Start)
		end, okEnd := clockMinutes(w.End)
		switch {
		case !okStart || !okEnd || start == end:
			// Not a window the validator accepts.
		case start < end:
			if start <= minute && minute < end && slices.Contains(w.Days, day) {
				return true
			}
		default:
			// The window runs past midnight: the evening of a listed day, or
			// the morning after one.
			if minute >= start && slices.Contains(w.Days, day) || minute < end && slices.Contains(w.Days, dayBefore) {
				return true
			}
		}
	}
	return false
}

// clockMinutes reads "HH:MM", from "00:00" to "24:00", as minutes of the
// day.
func clockMinutes(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	var n [4]int
	for i, at := range []int{0, 1, 3, 4} {
		if s[at] < '0' || s[at] > '9' {
			return 0, false
		}
		n[i] = int(s[at] - '0')
	}
	minutes := n[2]*10 + n[3]
	total := (n[0]*10+n[1])*60 + minutes
	if minutes > 59 || total > 24*60 {
		return 0, false
	}
	return total, true
}
