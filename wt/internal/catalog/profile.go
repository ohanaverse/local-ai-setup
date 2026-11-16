package catalog

import (
	"fmt"
	"maps"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// Distinct lists the resolved slot models once each, in the order of
// Check.FilledOrder(): the model of the slot the session rests on first.
func (p *ProfileRow) Distinct() []config.Model {
	var out []config.Model
	seen := map[string]bool{}
	for _, s := range p.Check.FilledOrder() {
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

// SlotPinReason says why a "profile/<name>" pin found no row, when checks
// hold a Usable profile of that name: an agent's own profile hidden by the
// tags or family filter is "model "profile/<name>" is a slot profile,
// excluded by -T and -F" (naming only the flags that are set), and one that
// only another agent has is "... is a slot profile for agent "<that agent>",
// not "<agent>"". A filter hides a blocked profile too, and its block reason
// is not shown. "" when pin is not a profile id, no Usable profile has its
// name (unknown, malformed, or the file did not load), or none of these
// explains the miss: the caller then keeps its generic message. (A catalog
// that holds the "profile/" prefix is the caller's check:
// SlotPrefixHolderWire.)
func SlotPinReason(checks []profiles.SlotCheck, agent, pin, tags, family string) string {
	name, ok := strings.CutPrefix(pin, profiles.SlotIDPrefix)
	if !ok {
		return ""
	}
	other := ""
	for _, c := range checks {
		if !c.Usable || c.Profile.Name != name {
			continue
		}
		if c.Profile.Agent != agent {
			if other == "" {
				other = c.Profile.Agent
			}
			continue
		}
		var flags []string
		if tags != "" {
			flags = append(flags, "-T")
		}
		if family != "" {
			flags = append(flags, "-F")
		}
		if len(flags) == 0 {
			return ""
		}
		return fmt.Sprintf("model %q is a slot profile, excluded by %s", pin, strings.Join(flags, " and "))
	}
	if other != "" {
		return fmt.Sprintf("model %q is a slot profile for agent %q, not %q", pin, other, agent)
	}
	return ""
}
