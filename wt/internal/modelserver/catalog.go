// Package modelserver is the wt-model server: it assembles the model catalog
// from wt's own rows and serves the start/stop control API over it.
package modelserver

import (
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
// and carry no slot-profile rows; Generated is now in UTC.
func BuildCatalog(cfg *config.Config, snap localmodels.Snapshot, ix tiers.Index, sessions *sessionreg.Registry, now time.Time) modelapi.Catalog {
	rows := catalog.Build(catalog.Input{Config: cfg, Models: cfg.Models, Inventory: &snap})
	out := modelapi.Catalog{
		Schema:    modelapi.SchemaVersion,
		Generated: now.UTC().Format(time.RFC3339),
		Providers: make(map[string]string, len(snap.Providers)),
	}
	for family, st := range snap.Providers {
		out.Providers[family] = string(st)
	}
	out.Rows = buildRows(rows, cfg, ix, sessions, now)
	return out
}

// buildRows converts already-built catalog rows to wire rows, skipping
// slot-profile rows, sorted by ID.
func buildRows(rows []catalog.Row, cfg *config.Config, ix tiers.Index, sessions *sessionreg.Registry, now time.Time) []modelapi.Row {
	var ids []string
	for _, r := range rows {
		if !r.IsProfile() {
			ids = append(ids, r.Model.ID)
		}
	}
	counts := sessions.Counts(ids)
	out := make([]modelapi.Row, 0, len(ids))
	for _, r := range rows {
		if r.IsProfile() {
			continue
		}
		out = append(out, wireRow(cfg, r, ix, counts[r.Model.ID], now))
	}
	slices.SortFunc(out, func(a, b modelapi.Row) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// wireRow converts one catalog row.
func wireRow(cfg *config.Config, r catalog.Row, ix tiers.Index, sessions int, now time.Time) modelapi.Row {
	m := r.Model
	row := modelapi.Row{
		ID: m.ID, GatewayID: m.ID, Family: m.Family, ProviderID: m.ProviderID, ModelName: m.ModelName,
		Location:   string(r.Location),
		Tags:       append([]string{}, m.Tags...),
		Registered: !r.Discovered,
		Discovered: r.Discovered,
		State:      rowState(r),
		Action:     rowAction(r),
		Protocols:  []string{},
		Sessions:   sessions,
	}
	if row.Action == modelapi.ActionBlock {
		row.BlockReason = r.BlockReason()
	}
	if p := cfg.ProviderByID(m.ProviderID); p != nil {
		for _, proto := range p.EffectiveProtocols() {
			row.Protocols = append(row.Protocols, string(proto))
		}
	}
	if e := ix.Rating(m.ProviderID, m.ModelName); e.Rated() {
		row.Tier, row.TierCode, row.Confidence = e.Tier, e.Code, e.Confidence
		if e.HasAligned {
			a := e.Aligned
			row.Aligned = &a
		}
		if e.ContextBasis == tiers.ContextBasisHost {
			row.Context = e.Context
		}
	}
	if p := m.Cost.PriceAt(now); p.Input != nil || p.Cache != nil || p.Output != nil {
		row.Price = &modelapi.Price{Input: p.Input, Cache: p.Cache, Output: p.Output}
	}
	return row
}

// rowState maps a catalog row to its wire state.
func rowState(r catalog.Row) string {
	switch {
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
	switch r.Action() {
	case catalog.ActionStart:
		return modelapi.ActionStart
	case catalog.ActionBlock:
		return modelapi.ActionBlock
	}
	return modelapi.ActionLaunch
}
