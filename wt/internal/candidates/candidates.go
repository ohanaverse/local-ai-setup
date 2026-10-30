// Package candidates ranks registry models by their advisory tier score and
// price, to suggest models for an agent slot. It reads tiers.Index and
// registry models only; nothing is routed on the result.
package candidates

import (
	"math"
	"sort"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// Query narrows and shapes a ranking.
type Query struct {
	OpenOnly  bool
	Providers []string // empty: every provider
	// Near is a registry model id. When set, the result is, per provider, the
	// PerProvider rated models nearest in score to that model (itself
	// excluded), nearest first. Empty when the model is unknown or unscored.
	Near        string
	PerProvider int // default 3
}

// Candidate is one ranked model.
type Candidate struct {
	ID, Provider, Location, Tier, Confidence string
	Score                                    float64
	HasScore, Rated, OpenWeights             bool
	Context                                  int
	OutputPerMillion                         *float64
	Subscription                             *float64
	SubscriptionPeriod                       string
	Flags                                    []string
}

// Rank filters models (tool_call=false, non-model rows, unrated embeddings,
// provider and open-weights filters), maps them to Candidates and orders
// them: open weights first, score descending (none last), output price
// ascending (none last), then ID. With q.Near set see Query.
func Rank(models []config.Model, ix tiers.Index, q Query) []Candidate {
	var all []Candidate
	for _, m := range models {
		if c, ok := build(m, ix, q); ok {
			all = append(all, c)
		}
	}
	if q.Near == "" {
		sortCandidates(all)
		return all
	}
	// The near model is found among every model, not only those q lets through.
	var ref *Candidate
	for _, m := range models {
		if m.ID == q.Near {
			if c, ok := build(m, ix, Query{}); ok {
				ref = &c
			}
		}
	}
	return nearest(all, ref, q)
}

func build(m config.Model, ix tiers.Index, q Query) (Candidate, bool) {
	if strings.HasPrefix(m.ID, "profile/") || strings.HasPrefix(m.ID, "claude/") {
		return Candidate{}, false
	}
	if len(q.Providers) > 0 && !contains(q.Providers, m.ProviderID) {
		return Candidate{}, false
	}
	e, has := ix.Lookup(m.ProviderID, m.ModelName)
	if !has && isEmbedding(m) {
		return Candidate{}, false
	}
	if e.HasToolCall && !e.ToolCall {
		return Candidate{}, false
	}
	if q.OpenOnly && !e.OpenWeights {
		return Candidate{}, false
	}
	c := Candidate{
		ID: m.ID, Provider: m.ProviderID, Location: string(m.Location),
		Tier: e.Tier, Confidence: e.Confidence, Score: e.Aligned, HasScore: e.HasAligned,
		Rated: e.Rated(), OpenWeights: e.OpenWeights, Context: e.Context,
		OutputPerMillion: m.Cost.OutputPricePerMillion, Subscription: m.Cost.SubscriptionPrice,
		SubscriptionPeriod: m.Cost.SubscriptionPeriod,
	}
	if !c.Rated {
		c.Flags = append(c.Flags, "unrated")
	}
	if e.Confidence == "low" {
		c.Flags = append(c.Flags, "low-confidence")
	}
	if !e.HasToolCall {
		c.Flags = append(c.Flags, "tool_call unknown")
	}
	if c.Subscription != nil {
		c.Flags = append(c.Flags, "subscription")
	}
	return c, true
}

func isEmbedding(m config.Model) bool {
	if strings.Contains(strings.ToLower(m.ModelName), "embed") {
		return true
	}
	for _, t := range m.Tags {
		if strings.EqualFold(t, "embedding") || strings.EqualFold(t, "embeddings") {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sortCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.OpenWeights != b.OpenWeights {
			return a.OpenWeights
		}
		if a.HasScore != b.HasScore {
			return a.HasScore
		}
		if a.HasScore && a.Score != b.Score {
			return a.Score > b.Score
		}
		if (a.OutputPerMillion == nil) != (b.OutputPerMillion == nil) {
			return a.OutputPerMillion != nil
		}
		if a.OutputPerMillion != nil && *a.OutputPerMillion != *b.OutputPerMillion {
			return *a.OutputPerMillion < *b.OutputPerMillion
		}
		return a.ID < b.ID
	})
}

// nearest groups scored candidates by provider (sorted by name) and keeps the
// closest PerProvider to the near model in each, nearest first.
func nearest(all []Candidate, ref *Candidate, q Query) []Candidate {
	if ref == nil || !ref.HasScore {
		return nil
	}
	n := q.PerProvider
	if n <= 0 {
		n = 3
	}
	by := map[string][]Candidate{}
	for _, c := range all {
		if c.ID != ref.ID && c.HasScore && c.Rated {
			by[c.Provider] = append(by[c.Provider], c)
		}
	}
	provs := make([]string, 0, len(by))
	for p := range by {
		provs = append(provs, p)
	}
	sort.Strings(provs)
	var out []Candidate
	for _, p := range provs {
		cs := by[p]
		sort.SliceStable(cs, func(i, j int) bool {
			di, dj := math.Abs(cs[i].Score-ref.Score), math.Abs(cs[j].Score-ref.Score)
			if di != dj {
				return di < dj
			}
			return cs[i].ID < cs[j].ID
		})
		if len(cs) > n {
			cs = cs[:n]
		}
		out = append(out, cs...)
	}
	return out
}
