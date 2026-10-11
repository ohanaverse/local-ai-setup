package profiles

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Validate checks that every profile's mechanisms are ones its agent
// declares support for via mechanismsFor (typically backed by
// agents.ByName(agent).(profiles.ProfileCapable)). requiredMechanismFor
// (typically backed by agents.ByName(agent).(profiles.MechanismRequirer))
// additionally rejects a profile entry that uses a mechanism without the
// second mechanism its agent requires alongside it; pass nil to skip that
// check. It returns one combined error naming every offending profile, or
// nil if all profiles pass.
func Validate(store Store, mechanismsFor func(agent string) []Mechanism, requiredMechanismFor func(agent string, m Mechanism) (Mechanism, bool)) error {
	var problems []string
	for i, p := range store.Profiles {
		// A match Resolve's tierRank does not recognize (typically a typo)
		// would make the profile silently unmatchable forever.
		if _, ok := tierRank[p.Match]; !ok {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s): invalid match %q (must be \"location\", \"provider\", or \"model\")",
				i, p.Agent, p.Match))
			continue
		}
		if matchValue(p) == "" {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s, match=%s): empty %s value — a profile whose match tier has no value would match every launch",
				i, p.Agent, p.Match, p.Match))
			continue
		}
		allowed := map[Mechanism]bool{}
		for _, mech := range mechanismsFor(p.Agent) {
			allowed[mech] = true
		}
		used := usedMechanisms(p)
		usedSet := map[Mechanism]bool{}
		for _, mech := range used {
			usedSet[mech] = true
		}
		for _, mech := range used {
			if !allowed[mech] {
				problems = append(problems, fmt.Sprintf(
					"profiles.toml[%d] (agent=%s, match=%s): agent does not support mechanism %q",
					i, p.Agent, p.Match, mech))
			}
			if requiredMechanismFor == nil {
				continue
			}
			if req, ok := requiredMechanismFor(p.Agent, mech); ok && !usedSet[req] {
				problems = append(problems, fmt.Sprintf(
					"profiles.toml[%d] (agent=%s, match=%s): mechanism %q has no effect without %q on the same profile entry",
					i, p.Agent, p.Match, mech, req))
			}
		}
		if p.Wrapper != nil && !slices.Contains(p.Wrapper.ArgsTemplate, "{{args}}") {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s, match=%s): wrapper args_template %v has no \"{{args}}\" placeholder — the launched agent's own arguments would be silently dropped",
				i, p.Agent, p.Match, p.Wrapper.ArgsTemplate))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func usedMechanisms(p Profile) []Mechanism {
	var out []Mechanism
	if len(p.Env) > 0 {
		out = append(out, MechanismEnv)
	}
	if len(p.Args) > 0 {
		out = append(out, MechanismArgs)
	}
	if len(p.ConfigContent) > 0 {
		out = append(out, MechanismConfigFile)
	}
	if p.Wrapper != nil {
		out = append(out, MechanismWrapper)
	}
	return out
}
