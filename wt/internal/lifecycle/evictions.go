package lifecycle

import (
	"context"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// poolAdmissionMarginPct is the share of omlx's ceiling wt keeps free when it
// predicts whether a load fits. omlx starts evicting once the pool passes
// ceiling * soft_threshold, not the ceiling itself, and soft_threshold defaults
// to 0.85. omlx does not report the threshold, so wt assumes the default: 15%.
// At 10% a load landing between 85% and 90% of the ceiling was predicted to
// fit and then evicted.
const poolAdmissionMarginPct = 15

// evictions reports the running models that starting t is expected to
// displace on a server of tenancy ten: an Exclusive server's one occupant,
// nobody on a Shared server, and on a Pool the models the plan says omlx would
// unload to make room. The second result is false when the snapshot's probe
// for the family cannot be trusted — a caller acting on "nobody" there would
// displace a model it never saw. A family whose server refused the connection
// is the exception: that is known, and empty. So is a target that is itself
// mid-load: it displaces nobody new.
func evictions(ten Tenancy, family string, t Target, snap localmodels.Snapshot) ([]localmodels.Entry, bool) {
	if ten != Exclusive && ten != Pool {
		return nil, true
	}
	// A refused connection positively means nothing is serving, so the family
	// is known to be empty: a cold start displaces nobody. It is checked
	// before the trust rule because the snapshot marks such a family Partial
	// too. The engine re-probes such a server before it acts
	// (resolveEvictions).
	if snap.Down[family] {
		return nil, true
	}
	if !ProbeTrusted(snap, family) {
		return nil, false
	}
	// A target that is already loading was admitted by the start that began
	// the load: whatever omlx evicts for it is decided, and a second start
	// only waits. Planning it again would count the target's size on top of a
	// pool that may already hold it, and ask about models this start cannot
	// displace (#259). What the load did unload is found afterwards
	// (reconcilePool).
	if isLoading(snap, family, t) {
		return nil, true
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

// machineOccupant is what a start of t finds of a ScopeMachine family's one
// instance on this machine, through its readiness command (foreignInstance),
// whatever port it is on and whoever started it. adopt: the instance already
// serves t, so the start has nothing to do. Otherwise found reports an
// instance serving another model, returned as occ in the shape every
// occupant has, so the start asks before replacing it (OccupiedError) and
// stops it with the family's stop. A family of any other scope runs no
// command and finds nothing; so does one whose binary or key cannot be had,
// which the backend's start then reports.
func (e *env) machineOccupant(ctx context.Context, cfg *config.Config, family string, t Target) (occ localmodels.Entry, found, adopt bool) {
	spec, ok := machineScoped(family)
	if !ok {
		return localmodels.Entry{}, false, false
	}
	bin, err := e.lookPath(spec.Binary)
	if err != nil {
		return localmodels.Entry{}, false, false
	}
	key, err := localmodels.FamilyAPIKeyErr(cfg, family)
	if err != nil {
		return localmodels.Entry{}, false, false
	}
	st, running := e.foreignInstance(ctx, cfg, family, bin, key)
	if !running {
		return localmodels.Entry{}, false, false
	}
	if st.Model != "" && SameModel(family, st.Model, t.ModelName) {
		return localmodels.Entry{}, false, true
	}
	providerID := family
	if ids := provider.FamilyProviderIDs(family); len(ids) > 0 {
		providerID = ids[0]
	}
	occ = localmodels.Entry{ProviderID: providerID, ModelID: family, ModelName: st.Model, Running: true}
	if st.Model != "" {
		occ.ModelID = config.DiscoveredModelID(family, st.Model)
	}
	return occ, true, false
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

// residentOthers lists what a start of t would load beside on the other
// provider families (#412): every running entry whose family is not the
// target's, and the families whose probe cannot be trusted, which are
// reported as unknown and never read as empty. A family whose server refused
// the connection (Down) holds nothing. A family the snapshot never probed is
// not here: env.residents asks its server (#423). It returns nothing
// when the target is already running (the start is a no-op) or is itself
// loading: the start that began that load already asked. An mtplx still loading
// is not in the snapshot (its port refuses); env.residents adds it.
func residentOthers(snap localmodels.Snapshot, family string, t Target) (others []Resident, unknown []string) {
	if isRunning(snap, family, t) || isLoading(snap, family, t) {
		return nil, nil
	}
	untrusted := map[string]bool{}
	for f := range snap.Providers {
		if f != family && f != "" && !snap.Down[f] && !ProbeTrusted(snap, f) {
			untrusted[f] = true
			unknown = append(unknown, f)
		}
	}
	sort.Strings(unknown)
	for _, en := range snap.Entries {
		f := localmodels.Family(en.ProviderID)
		if !en.Running || f == "" || f == family || snap.Down[f] || untrusted[f] {
			continue
		}
		others = append(others, Resident{Entry: en, Stoppable: CanStop(en.ProviderID) && !loadingOmlx(f, en)})
	}
	return others, unknown
}

// loadingOmlx reports whether en is an omlx model still loading. omlx refuses
// to unload one mid-load (omlxUnload says so, and points at `wt stop omlx`), so
// wt cannot stop it: offering it to the user as stoppable would fail the start
// it was asked to make room for. It is still listed — it holds memory already.
func loadingOmlx(family string, en localmodels.Entry) bool {
	return family == "omlx" && en.Loading
}

// unregisteredAnswering is the probe of families the snapshot never saw; a test
// swaps it so none dials a default port.
var unregisteredAnswering = localmodels.UnregisteredAnswering

// residents is residentOthers plus the answering unregistered families and
// any other process family's server still loading (mtplx): its port refuses,
// so the snapshot reads the family Down, and only wt's pidfile shows it
// (loadingServer). A pidfile wt cannot check makes the family unknown; one
// that names nothing wt can verify is left out, as the stop picker leaves it.
func (e *env) residents(cfg *config.Config, snap localmodels.Snapshot, family string, t Target) ([]Resident, []string) {
	others, unknown := residentOthers(snap, family, t)
	if !isRunning(snap, family, t) && !isLoading(snap, family, t) {
		// A family with no registry footprint is never probed, so the snapshot
		// cannot tell it is serving. Ask its server: one that answers is
		// unknown (never empty), one that refuses holds nothing and stays quiet.
		if extra := unregisteredAnswering(cfg, e.probeClient, snap, family); len(extra) > 0 {
			unknown = append(unknown, extra...)
			sort.Strings(unknown)
		}
	}
	if isRunning(snap, family, t) || isLoading(snap, family, t) {
		return others, unknown
	}
	for _, f := range provider.ProcessFamilies() {
		if f == family || !snap.Down[f] {
			continue
		}
		id, l := e.loadingServer(cfg, f)
		switch {
		case l.Err != nil:
			unknown = append(unknown, f)
			sort.Strings(unknown)
		case l.PID > 0:
			others = append(others, Resident{Entry: loadingServerEntry(snap, f, id.model), Stoppable: true, PID: l.PID})
		}
	}
	return others, unknown
}

// loadingServerEntry is the entry of the model family's server still loading
// is serving (model, the value of its spec's ModelFlag): the snapshot's own
// entry for it, so the resident carries the registry id the picker shows,
// else a discovered one. A server whose command line named no model is the
// provider alone.
func loadingServerEntry(snap localmodels.Snapshot, family, model string) localmodels.Entry {
	if model != "" {
		for _, en := range snap.Entries {
			if localmodels.Family(en.ProviderID) == family && SameModel(family, en.ModelName, model) {
				en.Running, en.Loading = true, true
				return en
			}
		}
	}
	en := localmodels.Entry{ProviderID: family, ModelID: family, ModelName: model, Running: true, Loading: true}
	if model != "" {
		en.ModelID = config.DiscoveredModelID(family, model)
	}
	return en
}
