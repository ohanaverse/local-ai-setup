package tui

import (
	"sort"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
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
// (agent-scoped usage counts).
type tableRow struct {
	catalog.Row
	counts usage.UsageCounts
	// at is the instant the row's price was read at: the one its table was
	// built at, the same for every row of the table. The zero time is a row
	// with no clock (one a test built by hand), which is priced flat.
	at time.Time
	// in and timed are the row's price in force at that instant and whether
	// its model's price depends on the time, resolved once when the row was
	// given its clock (pricedAt). Resolving checks each cost.time_prices row
	// against the registry's rules and reads its timezone, so nothing that
	// draws the table resolves again. Neither is set for a row with no clock.
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
// row the model of the slot the session rests on (the default slot, or the
// sonnet slot of an opusplan profile). A profile row whose default slot did not
// resolve (a blocked row) has the zero Model, which has no price.
func (r tableRow) costModel() config.Model {
	if r.Profile == nil {
		return r.Model
	}
	return r.Profile.Models[r.Profile.Check.RestSlot()]
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
// built, as pricedAt resolved them. It is what the COST column shows. A row
// with no clock has its model's flat prices.
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
	// now is the instant the rows are priced at. The zero time means the
	// picker's clock (pickerNow), which is what both pickers leave it at.
	now time.Time
	// slots opts in to slot-profile rows (catalog.Input.SlotProfiles): the
	// statically validated profiles. nil, which is what the picker of
	// `wt start` and `wt smoke` leaves it at, means no profile rows.
	slots []profiles.SlotCheck
}

// buildRows builds the shared catalog rows and decorates them with the
// picker's per-agent usage counts. The row rules
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
	// One reading of the clock for the whole table: the rows are priced at
	// one instant, so two rows never show the prices of two sides of a window
	// boundary and the COST cells agree.
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
			if r.slotTiers == nil {
				r.slotTiers = map[string]string{}
			}
			r.slotTiers[slot] = e.Short()
		}
	}
}

// sortRows orders rows in place, in four bands (this order is also the
// pickers' default selection, see below):
//
//  1. native models, alphabetical by id (#172: a native model carries no
//     per-token price and is the agent's own subscription model);
//  2. slot-profile rows, alphabetical by their id (profile/<name>);
//  3. local models that are ready to answer (running, not still loading),
//     by tier score, highest first;
//  4. every other model, by tier score, highest first.
//
// Within bands 3 and 4 a model with a tied score, or with no score, sorts by
// family (alphabetical, a model with no family after the named ones) and then
// by id, so a family's models sit together instead of being split by provider
// prefix; a model with no score sorts after every scored one. The score
// compared is the one the TIER column shows (rounded), so rows that look tied
// are ordered by family and id. Cost and usage no longer order anything.
//
// This order is also the pickers' default selection: newPickModel never calls
// Select, so its highlighted row is index 0 (the first sorted row), and
// enterModelPhase's no-rotation fallback picks the first actionable row. A
// native model in the list therefore becomes what a bare Enter launches.
func sortRows(rows []tableRow) {
	// Ready, not Running: a model omlx is still loading is a start row (#259)
	// and sorts with the other models, so it is never placed above them while
	// it cannot answer.
	band := func(r tableRow) int {
		switch {
		case r.Profile != nil:
			return 1
		case r.Model.Native:
			return 0
		case r.Location == config.LocationLocal && r.Ready():
			return 2
		}
		return 3
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ba, bb := band(a), band(b); ba != bb {
			return ba < bb
		}
		if band(a) >= 2 {
			sa, oka := a.tier.Score()
			sb, okb := b.tier.Score()
			if oka != okb {
				return oka
			}
			if oka && sa != sb {
				return sa > sb
			}
			if fa, fb := a.Model.Family, b.Model.Family; fa != fb {
				if fa == "" || fb == "" {
					return fb == ""
				}
				return fa < fb
			}
		}
		return a.Model.ID < b.Model.ID
	})
}
