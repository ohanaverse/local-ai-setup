// Package modelserver is the wt-model server: it assembles the model catalog
// from wt's own rows and serves the start/stop control API over it.
package modelserver

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionreg"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// BuildCatalog turns the registry, the live inventory snapshot, the tiers
// index and the live session registry into the wire catalog. It does no I/O:
// every input is a value, so a test hand-builds them. Rows are sorted by ID
// and carry no slot-profile rows; Generated is now in UTC. routed is the set
// of ids the gateway lists: nil means LiteLLM could not be asked and leaves
// every Row.Routed nil, a non-nil map (even empty) sets every row's.
func BuildCatalog(cfg *config.Config, snap localmodels.Snapshot, ix tiers.Index, sessions *sessionreg.Registry, routed map[string]bool, now time.Time) modelapi.Catalog {
	rows := localmodels.CatalogRows(localmodels.CatalogInput{Config: cfg, Models: cfg.Models, Inventory: &snap})
	out := modelapi.Catalog{
		Schema:    modelapi.SchemaVersion,
		Generated: now.UTC().Format(time.RFC3339),
		Providers: make(map[string]string, len(snap.Providers)),
	}
	for family, st := range snap.Providers {
		out.Providers[family] = string(st)
	}
	out.Rows = buildRows(rows, cfg, ix, sessions, routed, now)
	if cfg.OpenRouterPricedModel() {
		out.Pricing = &modelapi.Pricing{}
		if at, ok := cfg.LastPriceRefresh(now); ok {
			out.Pricing.Updated = at.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// buildRows converts already-built catalog rows to wire rows, skipping
// slot-profile rows, sorted by ID.
func buildRows(rows []catalog.Row, cfg *config.Config, ix tiers.Index, sessions *sessionreg.Registry, routed map[string]bool, now time.Time) []modelapi.Row {
	var ids []string
	for _, r := range rows {
		if !r.IsProfile() {
			ids = append(ids, r.Model.ID)
		}
	}
	counts := sessions.Counts(ids)
	order := make(map[string]int, len(cfg.Models))
	for i, m := range cfg.Models {
		order[m.ID] = i
	}
	var extra []string
	for _, id := range ids {
		if _, ok := order[id]; !ok {
			extra = append(extra, id)
		}
	}
	slices.Sort(extra)
	for i, id := range extra {
		order[id] = len(cfg.Models) + i
	}
	out := make([]modelapi.Row, 0, len(ids))
	for _, r := range rows {
		if r.IsProfile() {
			continue
		}
		w := wireRow(cfg, r, ix, counts[r.Model.ID], now)
		w.Order = order[r.Model.ID]
		if routed != nil {
			v := routed[r.Model.ID]
			w.Routed = &v
		}
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b modelapi.Row) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// wireRow converts one catalog row.
func wireRow(cfg *config.Config, r catalog.Row, ix tiers.Index, sessions int, now time.Time) modelapi.Row {
	m := r.Model
	row := modelapi.Row{
		ID: m.ID, GatewayID: m.ID, Native: m.Native, Family: m.Family, ProviderID: m.ProviderID, ModelName: m.ModelName,
		Location:   string(r.Location),
		Tags:       append([]string{}, m.Tags...),
		Registered: !r.Discovered,
		Discovered: r.Discovered,
		State:      rowState(r),
		Action:     rowAction(r),
		Protocols:  []string{},
		Sessions:   sessions,
	}
	if m.Native {
		row.GatewayID = ""
	}
	switch {
	case r.Unmapped:
		row.BlockReason = fmt.Sprintf("provider %q has no LiteLLM mapping, so the gateway cannot route %s", m.ProviderID, m.ID)
	case row.Action == modelapi.ActionBlock:
		row.BlockReason = r.BlockReason()
	}
	if p := cfg.ProviderByID(m.ProviderID); p != nil {
		for _, proto := range p.EffectiveProtocols() {
			row.Protocols = append(row.Protocols, string(proto))
		}
	}
	if e, ok := ix.Lookup(m.ProviderID, m.ModelName); ok {
		if e.Rated() {
			row.Tier, row.TierCode, row.Confidence = e.Tier, e.Code, e.Confidence
		}
		if e.HasAligned {
			a := e.Aligned
			row.Aligned = &a
		}
		row.TierSources = e.Sources
		if e.ContextBasis == tiers.ContextBasisHost {
			row.Context = e.Context
		}
	}
	if p := m.Cost.PriceAt(now); p.Input != nil || p.Cache != nil || p.Output != nil {
		row.Price = &modelapi.Price{Input: p.Input, Cache: p.Cache, Output: p.Output, TimePriced: len(m.Cost.TimePrices) > 0}
	}
	return row
}

// rowState maps a catalog row to its wire state.
func rowState(r catalog.Row) string {
	switch {
	case r.Unmapped:
		return modelapi.StateBlocked
	case r.Ready():
		return modelapi.StateRunning
	case r.Loading:
		return modelapi.StateLoading
	case r.Action() == catalog.ActionBlock:
		return modelapi.StateBlocked
	case r.Status == catalog.StatusUnknown:
		return modelapi.StateUnknown
	}
	return modelapi.StateStopped
}

// rowAction maps a catalog row to its wire action.
func rowAction(r catalog.Row) string {
	if r.Unmapped {
		return modelapi.ActionBlock
	}
	switch r.Action() {
	case catalog.ActionStart:
		return modelapi.ActionStart
	case catalog.ActionBlock:
		return modelapi.ActionBlock
	}
	return modelapi.ActionLaunch
}
