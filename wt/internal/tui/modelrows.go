package tui

import (
	"sort"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
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
	// shortBlock is the mode line's one-line form of a blocked row's reason,
	// used only when the full reason does not fit there (a blocked Claude
	// subscription row's decision 2 message is cut before its fix at 80
	// columns, #699). "" keeps the full reason.
	shortBlock string
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

// tableInput gathers everything buildRows needs. models is the list the
// standalone picker shows (the model server's rows as config.Models, each
// carrying its Location). cfg is nil there; a test may pass one so rows
// resolve their location from its providers.
type tableInput struct {
	cfg    *config.Config
	agent  string
	models []config.Model
	usage  usage.Store
	// now is the instant the rows are priced at. The zero time means the
	// picker's clock (pickerNow), which is what both pickers leave it at.
	now time.Time
}

// buildRows builds the shared catalog rows and decorates them with the
// picker's per-agent usage counts. The picker reads no inventory (its models
// come from the model server's catalog), so each row is catalog.ModelRow: the
// same location and Unmapped rules localmodels.CatalogRows starts from, and
// the same verdicts `wt smoke` and the non-TUI launch path reach.
func buildRows(in tableInput) []tableRow {
	rows := catalog.ModelRows(in.cfg, in.models)
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

// isAlias reports whether the row is an alias row (#596), by the Kind the
// model server gave it, the test catalog.RegisteredOrdered makes. A row with
// no wire row (one built from the registry) is never an alias.
func isAlias(r tableRow) bool {
	return r.Wire != nil && r.Wire.Kind == modelapi.KindAlias
}

// sortRows orders rows in place, in five bands (this order is also the
// pickers' default selection, see below):
//
//  1. native models, alphabetical by id (#172: a native model carries no
//     per-token price and is the agent's own subscription model);
//  2. slot-profile rows, alphabetical by their id (profile/<name>);
//  3. alias rows, by their own tier score, highest first, a row with no score
//     after the scored ones, ties by id; whatever their state (running,
//     blocked, local or cloud) they stay in this band;
//  4. local models that are ready to answer (running, not still loading),
//     by tier score, highest first;
//  5. every other model, by tier score, highest first.
//
// In bands 4 and 5 a model with no score takes the best score of its family's
// scored rows for its place only (its TIER cell still shows "-"), and sorts
// right after the scored row it borrowed from, so a model added since the
// last modeltiers run sits with its family instead of at the end of the list.
// An alias borrows nothing: every alias has the family "alias", so the rule
// would hand an unscored one the best alias score.
//
// Within bands 4 and 5 a model with a tied score, or with no score, sorts by
// family (alphabetical, a model with no family after the named ones) and then
// by id, so a family's models sit together instead of being split by provider
// prefix; a model whose family has no scored row still sorts after every
// placed one. The score compared is the one the TIER column shows (rounded),
// so rows that look tied are ordered by family and id. Cost and usage no
// longer order anything.
//
// This order is also the pickers' default selection: newPickModel never calls
// Select, so its highlighted row is index 0 (the first sorted row), and
// newModelList's no-rotation fallback picks the first row that is not blocked
// (a stopped alias or model that needs a start counts). A native model in the
// list therefore becomes what a bare Enter launches.
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
		case isAlias(r):
			return 2
		case r.Location == config.LocationLocal && r.Ready():
			return 3
		}
		return 4
	}
	// familyBest is the highest score among each family's scored rows, keyed
	// by non-empty family only, so a row with no family never borrows.
	familyBest := map[string]int{}
	for _, r := range rows {
		if sc, ok := r.tier.Score(); ok && r.Model.Family != "" {
			if best, seen := familyBest[r.Model.Family]; !seen || sc > best {
				familyBest[r.Model.Family] = sc
			}
		}
	}
	// place is the score a row sorts by, and whether the row's own tier gave
	// it (false for a score borrowed from its family).
	place := func(r tableRow) (score int, ok, own bool) {
		if sc, has := r.tier.Score(); has {
			return sc, true, true
		}
		sc, ok := familyBest[r.Model.Family]
		return sc, ok, false
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ba, bb := band(a), band(b); ba != bb {
			return ba < bb
		}
		if band(a) == 2 {
			// Aliases: own score only, scored before unscored, then id.
			sa, oka := a.tier.Score()
			sb, okb := b.tier.Score()
			if oka != okb {
				return oka
			}
			if oka && sa != sb {
				return sa > sb
			}
		}
		if band(a) >= 3 {
			sa, oka, owna := place(a)
			sb, okb, ownb := place(b)
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
			if owna != ownb {
				return owna
			}
		}
		return a.Model.ID < b.Model.ID
	})
}
