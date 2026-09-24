// wt/internal/profiles/resolve.go
package profiles

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ResolvedProfile is the merged result of every profile matching one
// agent/model launch, ready to apply.
type ResolvedProfile struct {
	Env           map[string]string
	ExtraArgs     []string
	ConfigContent map[string]any
	Wrapper       *WrapperSpec
	Sources       []string // "tier/value" strings naming every profile that contributed, for the confirm prompt
}

// Empty reports whether nothing would change about the launch.
func (r ResolvedProfile) Empty() bool {
	return len(r.Env) == 0 && len(r.ExtraArgs) == 0 && len(r.ConfigContent) == 0 && r.Wrapper == nil
}

var tierRank = map[string]int{"location": 0, "provider": 1, "model": 2}

// Resolve filters store.Profiles to those matching agent and m's resolved
// route conditions, then merges them in tier order (location, provider,
// model) so a more specific tier overwrites a less specific tier's
// same-named Env/ConfigContent key; Args and Wrapper are whole-field
// replacement (see design spec §3). An unresolvable location (registry
// gap) is treated conservatively as non-local, mirroring
// Config.IsExposed's own fail-closed treatment.
func Resolve(store Store, agent string, cfg *config.Config, m config.Model) ResolvedProfile {
	type tiered struct {
		rank int
		p    Profile
	}
	var matches []tiered
	for _, p := range store.Profiles {
		if p.Agent != agent {
			continue
		}
		rank, ok := tierRank[p.Match]
		if !ok || !matchesTier(p, cfg, m) {
			continue
		}
		matches = append(matches, tiered{rank: rank, p: p})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].rank < matches[j].rank })

	var out ResolvedProfile
	for _, t := range matches {
		p := t.p
		out.Sources = append(out.Sources, fmt.Sprintf("%s=%s", p.Match, matchValue(p)))
		for k, v := range p.Env {
			if out.Env == nil {
				out.Env = map[string]string{}
			}
			out.Env[k] = substitute(v, m)
		}
		if len(p.ConfigContent) > 0 {
			if out.ConfigContent == nil {
				out.ConfigContent = map[string]any{}
			}
			substituted, _ := substituteAny(p.ConfigContent, m).(map[string]any)
			mergeInto(out.ConfigContent, substituted)
		}
		if len(p.Args) > 0 {
			out.ExtraArgs = substituteAll(p.Args, m)
		}
		if p.Wrapper != nil {
			out.Wrapper = &WrapperSpec{Binary: p.Wrapper.Binary, ArgsTemplate: substituteAll(p.Wrapper.ArgsTemplate, m)}
		}
	}
	return out
}

func matchesTier(p Profile, cfg *config.Config, m config.Model) bool {
	switch p.Match {
	case "location":
		if p.Location == "" {
			return false
		}
		loc, err := cfg.ResolveLocation(m)
		return err == nil && string(loc) == p.Location
	case "provider":
		if p.Provider == "" {
			return false
		}
		return providerID(m) == p.Provider
	case "model":
		if p.Model == "" {
			return false
		}
		return m.ID == p.Model
	default:
		return false
	}
}

// providerID mirrors config.ResolveRoute's own provider-id fallback:
// m.ProviderID when set, else the segment before the first "/" in m.ID.
func providerID(m config.Model) string {
	if m.ProviderID != "" {
		return m.ProviderID
	}
	if i := strings.Index(m.ID, "/"); i >= 0 {
		return m.ID[:i]
	}
	return ""
}

func matchValue(p Profile) string {
	switch p.Match {
	case "location":
		return p.Location
	case "provider":
		return p.Provider
	default:
		return p.Model
	}
}

func substitute(s string, m config.Model) string {
	return strings.ReplaceAll(s, "{{model_name}}", m.ModelName)
}

func substituteAll(ss []string, m config.Model) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = substitute(s, m)
	}
	return out
}

func substituteAny(v any, m config.Model) any {
	switch val := v.(type) {
	case string:
		return substitute(val, m)
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, vv := range val {
			out[k] = substituteAny(vv, m)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, vv := range val {
			out[i] = substituteAny(vv, m)
		}
		return out
	default:
		return v
	}
}
