package lifecycle

import (
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// poolAdmissionMarginPct is the share of omlx's ceiling wt keeps free when it
// predicts whether a load fits. omlx starts evicting at a soft watermark below
// its ceiling and does not report where that is, so a prediction made against
// the ceiling itself would call "fits" on loads that evict.
const poolAdmissionMarginPct = 10

// Evictions reports the running models that starting t is expected to
// displace: an Exclusive server's one occupant, nobody on a Shared server, and
// on a Pool the models the plan says omlx would unload to make room. known is
// false when the snapshot's probe for the family cannot be trusted — a caller
// acting on "nobody" there would displace a model it never saw.
func Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool) {
	family := localmodels.Family(t.ProviderID)
	b := backendsByFamily[family]
	if b == nil {
		return nil, true
	}
	return evictions(b.tenancy(), family, t, snap)
}

func evictions(ten Tenancy, family string, t Target, snap localmodels.Snapshot) ([]localmodels.Entry, bool) {
	if ten != Exclusive && ten != Pool {
		return nil, true
	}
	if !ProbeTrusted(snap, family) {
		return nil, false
	}
	others := runningOthers(snap, family, t)
	if ten == Exclusive {
		if len(others) > 1 {
			others = others[:1]
		}
		return others, true
	}
	return poolVictims(snap.OmlxPool, t, others), true
}

// runningOthers lists the family's running models other than the target.
func runningOthers(snap localmodels.Snapshot, family string, t Target) []localmodels.Entry {
	var others []localmodels.Entry
	for _, en := range snap.Entries {
		if !en.Running || localmodels.Family(en.ProviderID) != family || SameModel(family, en.ModelName, t.ModelName) {
			continue
		}
		others = append(others, en)
	}
	return others
}

// poolVictims is the pool plan: which of others omlx is expected to unload so
// that t fits. Without sizes or a ceiling wt cannot tell whether t fits, so
// every other loaded model is named. With them, the walk follows omlx's own
// order — least recently used first, pinned models never — until the
// projection fits under the margin; when it never does, every unpinned model
// is named and omlx gives the final answer at load time. A running sibling the
// pool reading does not list cannot be sized, so when t does not fit it is
// named too, ahead of the rest and freeing nothing: wt has to ask about it.
func poolVictims(pool *localmodels.Pool, t Target, others []localmodels.Entry) []localmodels.Entry {
	if len(others) == 0 {
		return nil
	}
	if pool == nil || !pool.SizesKnown || pool.Ceiling <= 0 {
		return others
	}
	var targetSize int64
	if m, ok := pool.Find(t.ModelName); ok {
		targetSize = m.Size
	}
	over := pool.InUse + targetSize - (pool.Ceiling - pool.Ceiling*poolAdmissionMarginPct/100)
	if over <= 0 {
		return nil
	}
	type cand struct {
		en localmodels.Entry
		m  localmodels.PoolModel
	}
	var cands []cand
	var victims []localmodels.Entry
	for _, en := range others {
		name := en.Artifact
		if name == "" {
			name = en.ModelName
		}
		m, ok := pool.Find(name)
		switch {
		case !ok:
			victims = append(victims, en)
		case !m.Pinned:
			cands = append(cands, cand{en, m})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].m.LastAccess < cands[j].m.LastAccess })
	freed := map[string]bool{}
	for _, c := range cands {
		// Two registry rows can name one loaded model: both rows are
		// victims, and its memory is freed once.
		if over <= 0 && !freed[c.m.ID] {
			break
		}
		victims = append(victims, c.en)
		if !freed[c.m.ID] {
			freed[c.m.ID] = true
			over -= c.m.Size
		}
	}
	return victims
}
