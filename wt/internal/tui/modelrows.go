package tui

import (
	"sort"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// loadTiers reads modeltiers' advisory tiers file (tiers.toml), once for each
// table that is built (buildRows). It never fails: a missing or unparseable
// file is no ratings, and the TIER cells are all "-". Tests point it at a
// file of their own through MODELTIERS_OUT (tiers.IsolateForTest in
// TestMain points it at no file).
var loadTiers = tiers.Load

// pickerNow is the clock a model table reads its prices at. It is read once
// for each table that is built (buildRows), never while one is drawn. A test
// replaces it to draw the picker at a chosen instant.
var pickerNow = time.Now

// tableRow is one line of the selector table: the shared catalog rules
// (presence status, action, block reason) plus the picker-only decorations
// (agent-scoped usage counts, survey stats).
type tableRow struct {
	catalog.Row
	counts usage.UsageCounts
	stats  survey.Stats
	// at is the instant the row's price was read at: the one its table was
	// built at, the same for every row of the table. The zero time is a row
	// with no clock (one a test built by hand), which is priced flat.
	at time.Time
	// in and timed are the row's price in force at that instant and whether
	// its model's price depends on the time, resolved once when the row was
	// given its clock (pricedAt). Resolving checks each cost.time_prices row
	// against the registry's rules and reads its timezone, and the sort asks
	// for a row's price twice per comparison, so nothing that sorts or draws
	// the table resolves again. Neither is set for a row with no clock.
	in    config.PriceInForce
	timed bool
	// tier is the model's rating in the tiers file (tiers.Index.Rating): the
	// zero Entry, which shows as "-", for a model the file gives neither a
	// tier nor an aligned score and for a slot-profile row. slotTiers is a
	// slot-profile row's: each slot's model's short form (tiers.Entry.Short),
	// by slot, for the slots whose model is rated.
	tier      tiers.Entry
	slotTiers map[string]string
}

// costModel is the model a row is priced as: its own, or for a slot-profile
// row the default slot's model. A profile row whose default slot did not
// resolve (a blocked row) has the zero Model, which has no price.
func (r tableRow) costModel() config.Model {
	if r.Profile == nil {
		return r.Model
	}
	return r.Profile.Models[r.Profile.Check.Profile.Default]
}

// pricedAt is the row with its clock set to at and its price resolved for
// that instant: the model's flat prices, or those of the cost.time_prices
// row whose window holds it (config.ModelCost.PriceAt). The zero time gives
// a row with no clock. This is the one place a table's row is priced.
func (r tableRow) pricedAt(at time.Time) tableRow {
	r.at, r.in, r.timed = at, config.PriceInForce{}, false
	if !at.IsZero() {
		cost := r.costModel().Cost
		r.in, r.timed = cost.PriceAt(at), cost.TimePriced()
	}
	return r
}

// price is the row's three prices in force at the instant its table was
// built, as pricedAt resolved them. It is what the COST column shows and
// what the cost sort compares. A row with no clock has its model's flat
// prices.
func (r tableRow) price() config.PriceInForce {
	if r.at.IsZero() {
		return r.costModel().Cost.Flat()
	}
	return r.in
}

// timePriced reports whether the row's price depends on the time (its model
// has a cost.time_prices row that is applied), as pricedAt found it. It is
// what marks the row's price with "~". A row with no clock asks its model.
func (r tableRow) timePriced() bool {
	if r.at.IsZero() {
		return r.costModel().Cost.TimePriced()
	}
	return r.timed
}

// tableInput gathers everything buildRows needs. models is the agent's
// eligible list (every catalog model the agent supports, already
// filtered by agent/-T/-F); inventory is nil when no local probe ran.
type tableInput struct {
	cfg            *config.Config
	agent          string
	models         []config.Model
	inventory      *localmodels.Snapshot
	hideDiscovered bool
	// skipRoute suppresses the launch-route blocking/decoration in
	// buildTable: `wt start` starts a model, it does not launch an agent on
	// it, so a route verdict must not gate its rows.
	skipRoute bool
	usage     usage.Store
	stats     map[string]survey.Stats
	// now is the instant the rows are priced at. The zero time means the
	// picker's clock (pickerNow), which is what both pickers leave it at.
	now time.Time
	// slots opts in to slot-profile rows (catalog.Input.SlotProfiles): the
	// statically validated profiles. nil, which is what the picker of
	// `wt start` and `wt smoke` leaves it at, means no profile rows.
	slots []profiles.SlotCheck
}

// buildRows builds the shared catalog rows and decorates them with the
// picker's per-agent usage counts and per-model survey stats. The row rules
// themselves live in internal/catalog so the non-TUI launch path and
// `wt smoke` reach the same verdicts this table displays.
func buildRows(in tableInput) []tableRow {
	rows := catalog.Build(catalog.Input{
		Config:         in.cfg,
		Agent:          in.agent,
		Models:         in.models,
		Inventory:      in.inventory,
		HideDiscovered: in.hideDiscovered,
		SlotProfiles:   in.slots,
	})
	// One reading of the clock for the whole table: the rows are sorted by
	// the price in force, and two rows priced on two sides of a window
	// boundary would be compared at different times.
	now := in.now
	if now.IsZero() {
		now = pickerNow()
	}
	out := make([]tableRow, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		out[i] = tableRow{Row: r}.pricedAt(now)
		ids[i] = r.Model.ID
	}
	var counts map[string]usage.UsageCounts
	if in.usage != nil {
		if in.agent != "" {
			counts = in.usage.CountsForAgent(in.agent, ids)
		} else {
			counts = in.usage.Counts(ids)
		}
	}
	ratings := loadTiers()
	for i := range out {
		out[i].counts = counts[out[i].Model.ID]
		out[i].stats = in.stats[out[i].Model.ID]
		out[i].rate(ratings)
	}
	return out
}

// rate looks the row's model up in the tiers file by the identity modeltiers
// keys it by (provider_id and model_name, the inventory's own fields). A
// slot-profile row is not a model and has no rating of its own; each of its
// slots' models is looked up instead, for the slot line.
func (r *tableRow) rate(ratings tiers.Index) {
	if r.Profile == nil {
		r.tier = ratings.Rating(r.Model.ProviderID, r.Model.ModelName)
		return
	}
	for slot, m := range r.Profile.Models {
		// The slot line names rated models only: a bare score after a
		// model name would read as part of it.
		if e := ratings.Rating(m.ProviderID, m.ModelName); e.Rated() {
			code := e.Short()
			if r.slotTiers == nil {
				r.slotTiers = map[string]string{}
			}
			r.slotTiers[slot] = code
		}
	}
}

// costKey is the sort key for "cost ascending": output price per million,
// then input price per million, each the price in force when the table was
// built (tableRow.price), so a model priced by time of day sorts by what it
// costs now. Local models and subscription-only models (no per-token prices)
// count as $0; a model with no cost data at all sorts after every priced
// model.
type costKey struct {
	noData  bool
	out, in float64
}

func rowCostKey(r tableRow) costKey {
	if r.Location == config.LocationLocal {
		return costKey{}
	}
	p := r.price()
	if p.Input == nil && p.Cache == nil && p.Output == nil {
		if r.Model.Cost.SubscriptionPrice != nil {
			return costKey{}
		}
		return costKey{noData: true}
	}
	var k costKey
	if p.Output != nil {
		k.out = *p.Output
	}
	if p.Input != nil {
		k.in = *p.Input
	}
	return k
}

// sortRows orders rows in place, native models first (#172). A native model
// carries no per-token price, so plain cost-ascending sank it as "no cost
// data" below every priced model, when it is in fact the agent's own
// subscription model. The native/non-native split is the only special case:
// each half then falls through to the same group rules — group 1 (cloud +
// running local) by cost ascending then 7-day usage ascending then id; group 2
// (local that is not running, or still loading) alphabetical by id — so a
// native row that resolves local and is not running still sorts after the
// native group-1 rows, by id, not "in group-1 order".
//
// Slot-profile rows are a band of their own between the two halves:
// native rows, then profile rows in the order of slot-profiles.toml, then
// every other row. A profile row is never compared by cost, usage or id.
//
// This order is also the pickers' default selection: newPickModel never calls
// Select, so its highlighted row is index 0 (the first sorted row), and
// enterModelPhase's no-rotation fallback picks the first actionable row. A
// native model in the list therefore becomes what a bare Enter launches.
func sortRows(rows []tableRow) {
	// Ready, not Running: a model omlx is still loading is a start row (#259)
	// and sorts with them, so it is never the default pick while it cannot
	// answer.
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Ready() }
	// band is the first thing compared: 0 for a native row, 1 for a
	// slot-profile row, 2 for every other row. A lower band sorts first.
	band := func(r tableRow) int {
		switch {
		case r.Profile != nil:
			return 1
		case r.Model.Native:
			return 0
		}
		return 2
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ba, bb := band(a), band(b); ba != bb {
			return ba < bb
		}
		if a.Profile != nil {
			return a.Profile.Check.Index < b.Profile.Check.Index
		}
		ga, gb := group1(a), group1(b)
		if ga != gb {
			return ga
		}
		if !ga {
			return a.Model.ID < b.Model.ID
		}
		ka, kb := rowCostKey(a), rowCostKey(b)
		if ka.noData != kb.noData {
			return !ka.noData
		}
		if ka.out != kb.out {
			return ka.out < kb.out
		}
		if ka.in != kb.in {
			return ka.in < kb.in
		}
		if a.counts.SevenDay != b.counts.SevenDay {
			return a.counts.SevenDay < b.counts.SevenDay
		}
		return a.Model.ID < b.Model.ID
	})
}
