package catalog

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// ProfileRow is what a slot-profile row carries in place of a registry model.
type ProfileRow struct {
	// Check is the profile, its static verdict and the agent's declared slots.
	Check profiles.SlotCheck
	// Models maps a declared slot to its resolved model. It is complete on a
	// launch or start row (an unfilled slot holds the default slot's model)
	// and partial or nil on a blocked row.
	Models map[string]config.Model
	// Local is the one distinct local slot model's own row. It is nil for an
	// all-cloud profile, when that model has no row, and on a row blocked
	// before the local model was judged. Such a row still carries that
	// model's Running and Loading (slotLocalState), for the RUNNING cell.
	Local *Row
	// Verdict is ActionLaunch, ActionStart or ActionBlock.
	Verdict Action
	// Reason is the block reason; "" unless Verdict is ActionBlock.
	Reason string
	// Loc is "cloud", "local" or "mixed"; "" when no slot resolved.
	Loc string
}

// IsProfile reports whether r is a slot-profile row.
func (r Row) IsProfile() bool { return r.Profile != nil }

// slotRule is one per-slot rule of resolved validation: "" when the slot's
// model passes, else the block reason.
type slotRule func(cfg *config.Config, slot string, m config.Model) string

// slotRules are the per-slot rules that follow the registry lookup, in the
// order they are checked. Each one is checked over every filled slot before
// the next one runs.
var slotRules = []slotRule{
	func(_ *config.Config, slot string, m config.Model) string {
		if m.Native {
			return fmt.Sprintf("slot %s: %s is a native model, which has no LiteLLM route", slot, m.ID)
		}
		return ""
	},
	func(cfg *config.Config, slot string, m config.Model) string {
		if cfg.ProviderByID(m.ProviderID) == nil {
			return fmt.Sprintf("slot %s: %s names provider %q, which is not in the registry", slot, m.ID, m.ProviderID)
		}
		return ""
	},
	func(cfg *config.Config, slot string, m config.Model) string {
		if _, err := cfg.ResolveLocation(m); err != nil {
			return fmt.Sprintf("slot %s: %v", slot, err)
		}
		return ""
	},
	func(_ *config.Config, slot string, m config.Model) string {
		if _, ok := litellm.PolicyFor(m.ProviderID); !ok {
			return fmt.Sprintf("slot %s: %s cannot be routed: provider %q has no LiteLLM mapping", slot, m.ID, m.ProviderID)
		}
		return ""
	},
	// Last, the launch's own predicate (lifecycle.Routable): whatever the
	// rules above let through and the route write would still refuse, such
	// as a cloud model under a provider whose auth is native. The rule is
	// shared, not copied, so the row and the route write cannot disagree.
	func(cfg *config.Config, slot string, m config.Model) string {
		if !lifecycle.Routable(cfg, m) {
			return fmt.Sprintf("slot %s: %s cannot be routed: %v", slot, m.ID, lifecycle.ErrNotRoutable)
		}
		return ""
	},
}

// ResolveSlotProfile runs resolved validation for one statically checked,
// Usable profile and returns its row: the file's rules were checked by
// profiles.ValidateSlots, and this adds the ones that need the registry, the
// live inventory and the [litellm] table. The first failing rule blocks the
// row and is its reason. snap may be nil (no probe ran), in which case the
// local model is judged as Build judges an unprobed local model.
//
// The row's id is the profile's ("profile/<name>") and its Model holds
// nothing else. Whether [litellm] is switched on is never read: a slot
// profile always uses the proxy.
func ResolveSlotProfile(cfg *config.Config, snap *localmodels.Snapshot, c profiles.SlotCheck) Row {
	if cfg == nil {
		cfg = &config.Config{}
	}
	p := &ProfileRow{Check: c, Verdict: ActionLaunch}
	row := Row{Model: config.Model{ID: c.Profile.ID()}, Status: StatusOK, Profile: p}
	// judged is set once the last rule has built the local model's own row
	// and copied its state; a row blocked by any earlier rule looks the state
	// up here, so RUNNING does not depend on which rule blocked the row.
	judged := false
	block := func(reason string) Row {
		p.Verdict, p.Reason = ActionBlock, reason
		if !judged && !c.Duplicate {
			row.Running, row.Loading = slotLocalState(cfg, snap, c.Profile)
		}
		return row
	}
	if c.Err != nil {
		return block(c.Err.Error())
	}

	// Every filled slot whose id is in the registry, then the default's model
	// for each slot the profile leaves unfilled, and the row's location from
	// the filled slots that have one. A blocked row keeps what resolved.
	slots := c.Profile.FilledOrder(c.Declared)
	p.Models = map[string]config.Model{}
	cloud, local := false, false
	for _, s := range slots {
		i := config.IndexModelByID(cfg.Models, c.Profile.Slots[s])
		if i < 0 {
			continue
		}
		m := cfg.Models[i]
		p.Models[s] = m
		switch loc, err := cfg.ResolveLocation(m); {
		case err != nil:
		case loc == config.LocationLocal:
			local = true
		default:
			cloud = true
		}
	}
	if def, ok := p.Models[c.Profile.Default]; ok {
		for _, s := range c.Declared {
			if _, filled := c.Profile.Slots[s]; !filled {
				p.Models[s] = def
			}
		}
	}
	switch {
	case cloud && local:
		p.Loc = "mixed"
	case local:
		p.Loc = string(config.LocationLocal)
	case cloud:
		p.Loc = string(config.LocationCloud)
	}

	for _, s := range slots {
		if _, ok := p.Models[s]; !ok {
			return block(fmt.Sprintf("slot %s: %s is not in the registry", s, c.Profile.Slots[s]))
		}
	}
	for _, rule := range slotRules {
		for _, s := range slots {
			if reason := rule(cfg, s, p.Models[s]); reason != "" {
				return block(reason)
			}
		}
	}

	var locals []config.Model
	for _, m := range p.Distinct() {
		if loc, _ := cfg.ResolveLocation(m); loc == config.LocationLocal {
			locals = append(locals, m)
		}
	}
	if len(locals) > 1 {
		return block(fmt.Sprintf("slots name two local models (%s and %s); a slot profile may name one", locals[0].ID, locals[1].ID))
	}

	switch {
	case cfg.LitellmBaseURL() == "":
		return block("a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'")
	case cfg.LitellmAPIKey() == "":
		return block("a slot profile always uses the LiteLLM proxy, but no API key is configured — run 'wt litellm set --url ... --api-key ...'")
	}

	if len(locals) == 0 {
		return row
	}
	// The local model is judged by the catalog's own row policy: one row built
	// for it alone from the same snapshot, whatever the agent's eligible list
	// and -T/-F say.
	m := locals[0]
	slot := p.SlotOf(m.ID)
	own, ok := slotLocalRow(cfg, snap, m)
	judged = true
	if !ok {
		if reason := MissingReason(cfg, snap, m.ID); reason != "" {
			return block(reason)
		}
		return block(fmt.Sprintf("slot %s: %s is not on disk or running", slot, m.ID))
	}
	p.Local = &own
	row.Running, row.Loading = own.Running, own.Loading
	switch own.Action() {
	case ActionBlock:
		return block("slot " + slot + ": " + own.BlockReason())
	case ActionStart:
		p.Verdict = ActionStart
	}
	return row
}

// slotLocalRow is the catalog's own row for a profile's local model m: one
// row built for it alone from snap, whatever the agent's eligible list and
// -T/-F say. ok is false when the model has no row (confirmed not on disk, a
// stopped mlx_lm_server pairing, no inventory entry).
func slotLocalRow(cfg *config.Config, snap *localmodels.Snapshot, m config.Model) (Row, bool) {
	return Find(Build(Input{Config: cfg, Models: []config.Model{m}, Inventory: snap, HideDiscovered: true}), m.ID)
}

// slotLocalState is the running state of the one local model sp's slots
// name, for a row blocked before that model was judged: the Running and
// Loading of the model's own row (slotLocalRow), the values the row would
// carry were it not blocked. It reads the file's slots, not the resolved
// ones, so it answers for a profile that failed static validation too. Both
// are false when the slots name no local model that is in the registry, more
// than one distinct local model, or one that has no row.
func slotLocalState(cfg *config.Config, snap *localmodels.Snapshot, sp profiles.SlotProfile) (running, loading bool) {
	var local *config.Model
	for _, id := range sp.Slots {
		i := config.IndexModelByID(cfg.Models, id)
		if i < 0 {
			continue
		}
		m := cfg.Models[i]
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		if local != nil && local.ID != m.ID {
			return false, false
		}
		local = &m
	}
	if local == nil {
		return false, false
	}
	own, ok := slotLocalRow(cfg, snap, *local)
	if !ok {
		return false, false
	}
	return own.Running, own.Loading
}

// Distinct lists the resolved slot models once each, in the order of
// Check.Profile.FilledOrder(Check.Declared): the default slot's model first.
func (p *ProfileRow) Distinct() []config.Model {
	var out []config.Model
	seen := map[string]bool{}
	for _, s := range p.Check.Profile.FilledOrder(p.Check.Declared) {
		m, ok := p.Models[s]
		if !ok || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

// Cloud is Distinct without the local model (Local.Model).
func (p *ProfileRow) Cloud() []config.Model {
	var out []config.Model
	for _, m := range p.Distinct() {
		if p.Local != nil && m.ID == p.Local.Model.ID {
			continue
		}
		out = append(out, m)
	}
	return out
}

// SlotOf is the first slot, in FilledOrder, whose model has the given id; ""
// when none.
func (p *ProfileRow) SlotOf(modelID string) string {
	for _, s := range p.Check.Profile.FilledOrder(p.Check.Declared) {
		if m, ok := p.Models[s]; ok && m.ID == modelID {
			return s
		}
	}
	return ""
}

// Launch is the profile as an agent's slot build takes it. Models is a copy,
// so the build cannot change the row.
func (p *ProfileRow) Launch() profiles.SlotLaunch {
	return profiles.SlotLaunch{
		Agent:         p.Check.Profile.Agent,
		Models:        maps.Clone(p.Models),
		Default:       p.Check.Profile.Default,
		Env:           p.Check.Profile.Env,
		ConfigContent: p.Check.Profile.ConfigContent,
	}
}

// SlotRouteError words a route a slot profile's launch could not ensure by
// the slot it fills: "slot haiku: LiteLLM route for <id> not written: <cause>".
// Any other error is returned as it is. It lives here because both launch
// paths use it — cmd/wt's launchSlotProfile and the TUI's prepareSlot — and
// neither package may import the other.
func SlotRouteError(p *ProfileRow, err error) error {
	var re *lifecycle.RouteEnsureError
	if errors.As(err, &re) {
		return fmt.Errorf("slot %s: %w", p.SlotOf(re.ModelID), err)
	}
	return err
}

// SlotPrefixReserved reports whether something in the registry holds the
// "profile/" prefix of every profile row id (SlotPrefixHolder). Build emits
// no profile rows while it does, so a row id stays unique. It is the one
// predicate for the reserved prefix: the catalog, the launch warning,
// `wt profile list` and `wt profile show` all ask it. cfg may be nil (false).
func SlotPrefixReserved(cfg *config.Config) bool { return SlotPrefixHolder(cfg) != "" }

// SlotPrefixHolder names what in the registry holds the "profile/" prefix,
// worded to follow "the registry has "; "" while the prefix is free. A
// provider with id profiles.SlotReservedProvider holds it, since its models'
// ids start with it. So does any model whose id starts with it, under
// whatever provider: registry validation does not tie a model's id to its
// provider, so a hand-written row can have such an id. The provider is named
// when both exist, else the first such model.
func SlotPrefixHolder(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.ProviderByID(profiles.SlotReservedProvider) != nil {
		return fmt.Sprintf("a provider with id %q", profiles.SlotReservedProvider)
	}
	for _, m := range cfg.Models {
		if strings.HasPrefix(m.ID, profiles.SlotIDPrefix) {
			return fmt.Sprintf("a model whose id begins with %q (%s)", profiles.SlotIDPrefix, m.ID)
		}
	}
	return ""
}

// slotRows is Build's profile half: one row per Usable profile of in.Agent,
// in file order. It returns nil unless the caller opted in (SlotProfiles),
// named an agent and a config, applied no -T/-F filter (HideDiscovered), and
// the registry leaves the "profile/" prefix free (SlotPrefixReserved). A group of profiles sharing
// a name gets one row, blocked by the duplicate error every member carries,
// so row ids stay unique.
func slotRows(in Input) []Row {
	if len(in.SlotProfiles) == 0 || in.Agent == "" || in.Config == nil || in.HideDiscovered || SlotPrefixReserved(in.Config) {
		return nil
	}
	var rows []Row
	dup := map[string]bool{}
	for _, c := range in.SlotProfiles {
		if !c.Usable || c.Profile.Agent != in.Agent {
			continue
		}
		if c.Duplicate {
			if dup[c.Profile.Name] {
				continue
			}
			dup[c.Profile.Name] = true
		}
		rows = append(rows, ResolveSlotProfile(in.Config, in.Inventory, c))
	}
	return rows
}
