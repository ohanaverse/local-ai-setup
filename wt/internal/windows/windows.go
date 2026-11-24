// Package windows answers one question: how many tokens of context does a
// model have, for the places that must say so (a gateway slot, a LiteLLM
// route). It imports only config and tiers.
package windows

import (
	"strconv"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// Model returns m's context window in tokens, 0 when unknown. The registry's
// context_window wins. Without one, a cloud model takes the tiers file's
// window when it was read from the model's own host row
// (tiers.ContextBasisHost; a "largest_row" window is not one any host
// serves). A local model never takes a window from tiers, because the window
// a local server serves is whatever it was started with. A location that does
// not resolve reads as 0.
func Model(cfg *config.Config, m config.Model, ix tiers.Index) int {
	if m.ContextWindow > 0 {
		return int(m.ContextWindow)
	}
	loc, err := cfg.ResolveLocation(m)
	if err != nil || loc != config.LocationCloud {
		return 0
	}
	if e, ok := ix.Lookup(m.ProviderID, m.ModelName); ok && e.ContextBasis == tiers.ContextBasisHost {
		return e.Context
	}
	return 0
}

// Alias returns the context window an alias may declare: the smallest of its
// targets' windows (Model), over every configured target, and the target
// that sets it. It is withheld — 0, with limiting naming the first such
// target — when any target's window is unknown or a target does not name
// exactly one model: a minimum over the known targets alone would
// over-declare, and a client would compact too late on the target with the
// smaller window. An alias with no targets reads (0, "").
func Alias(cfg *config.Config, a config.Alias, ix tiers.Index) (min int, limiting string) {
	for _, t := range cfg.AliasTargets(a) {
		i := config.IndexModelByID(cfg.Models, t.Model)
		if i < 0 || cfg.CheckModelRow(t.Model) != nil {
			return 0, t.Model
		}
		w := Model(cfg, cfg.Models[i], ix)
		if w <= 0 {
			return 0, t.Model
		}
		if limiting == "" || w < min {
			min, limiting = w, t.Model
		}
	}
	return min, limiting
}

// Short formats a window for a table cell or a one-line summary: "262k",
// "1M", "1.5M", "512"; "-" for an unknown one (n <= 0). Powers of two read
// as their decimal neighbour (262144 is "262k", 1048576 is "1M"). It is the
// one formatter the picker's CTX column and `wt profile` share.
func Short(n int) string {
	switch {
	case n <= 0:
		return "-"
	case n < 1000:
		return strconv.Itoa(n)
	case n < 999500:
		return strconv.Itoa((n+500)/1000) + "k"
	}
	m := float64(n) / 1e6
	return strings.TrimSuffix(strconv.FormatFloat(m, 'f', 1, 64), ".0") + "M"
}

// Min is the smallest of ws, or 0 when ws is empty or any of them is unknown
// (<= 0): a minimum over the known windows alone would over-state what a
// session can hold (the claudeSlotWindow rule).
func Min(ws ...int) int {
	least := 0
	for _, w := range ws {
		if w <= 0 {
			return 0
		}
		if least == 0 || w < least {
			least = w
		}
	}
	return least
}
