package profiles

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks that every profile's mechanisms are ones its agent
// declares support for via mechanismsFor (typically backed by
// agents.ByName(agent).(profiles.ProfileCapable)). It returns one
// combined error naming every offending profile, or nil if all profiles
// pass.
// validMatchTiers are the only values Resolve's tierRank recognizes; any
// other value (typically a typo) makes a profile silently unmatchable
// forever, with no error anywhere else in the package.
var validMatchTiers = map[string]bool{"location": true, "provider": true, "model": true}

func Validate(store Store, mechanismsFor func(agent string) []Mechanism) error {
	var problems []string
	for i, p := range store.Profiles {
		if !validMatchTiers[p.Match] {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s): invalid match %q (must be \"location\", \"provider\", or \"model\")",
				i, p.Agent, p.Match))
			continue
		}
		allowed := map[Mechanism]bool{}
		for _, mech := range mechanismsFor(p.Agent) {
			allowed[mech] = true
		}
		for _, mech := range usedMechanisms(p) {
			if !allowed[mech] {
				problems = append(problems, fmt.Sprintf(
					"profiles.toml[%d] (agent=%s, match=%s): agent does not support mechanism %q",
					i, p.Agent, p.Match, mech))
			}
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
