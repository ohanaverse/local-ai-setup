package lifecycle

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Evictions is the tests' way to ask the eviction plan by provider id, the
// way the engine does: TenancyOf looks the family's tenancy up from
// backendsByFamily (the single source of truth) and evictions reads the plan.
// It reports the running models that starting t is expected to displace; see
// evictions for what that is and when known is false.
func Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool) {
	return evictions(TenancyOf(t.ProviderID), localmodels.Family(t.ProviderID), t, snap)
}

func ids(es []localmodels.Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ModelID)
	}
	return out
}

// TestPoolVictims pins who wt says a pool start would unload. Too few names
// and a session loses its model with no warning; too many and wt asks to
// replace models that would have stayed loaded.
func TestPoolVictims(t *testing.T) {
	a := localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/A", ModelName: "A", Artifact: "A", Running: true}
	b := localmodels.Entry{ProviderID: "omlx", ModelID: "omlx/B", ModelName: "B", Artifact: "B", Running: true}
	a6 := localmodels.Entry{ProviderID: "omlx-6bit", ModelID: "omlx-6bit/A", ModelName: "A", Artifact: "A", Running: true}
	target := Target{ProviderID: "omlx", ModelName: "T"}
	pool := func(ceiling, inUse int64, ms ...localmodels.PoolModel) *localmodels.Pool {
		return &localmodels.Pool{Models: ms, Ceiling: ceiling, InUse: inUse, SizesKnown: true}
	}
	pm := func(id string, size int64, last float64, pinned bool) localmodels.PoolModel {
		return localmodels.PoolModel{ID: id, Loaded: id != "T", Size: size, LastAccess: last, Pinned: pinned}
	}
	for _, tc := range []struct {
		name   string
		pool   *localmodels.Pool
		others []localmodels.Entry
		want   []string
	}{
		{"fits under the margin", pool(100, 40, pm("A", 40, 1, false), pm("T", 40, 0, false)), []localmodels.Entry{a}, nil},
		// 40 + 55 = 95 is under the ceiling but over the 85 the margin leaves.
		{"inside the margin", pool(100, 40, pm("A", 40, 1, false), pm("T", 55, 0, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
		// The boundary that tells 15% from 10%: 40 + 47 = 87 is over the 85 a
		// 15% margin leaves and under the 90 a 10% one would. omlx's default
		// watermark is 85% of the ceiling, so this load evicts.
		{"over omlx's default watermark, under 90%", pool(100, 40, pm("A", 40, 1, false), pm("T", 47, 0, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
		{"oldest goes first, and only as many as needed", pool(100, 80, pm("A", 40, 9, false), pm("B", 40, 1, false), pm("T", 30, 0, false)), []localmodels.Entry{a, b}, []string{"omlx/B"}},
		{"a pinned model is never named", pool(100, 80, pm("A", 40, 9, false), pm("B", 40, 1, true), pm("T", 30, 0, false)), []localmodels.Entry{a, b}, []string{"omlx/A"}},
		{"cannot fit: every unpinned model", pool(100, 80, pm("A", 40, 9, false), pm("B", 40, 1, false), pm("T", 200, 0, false)), []localmodels.Entry{a, b}, []string{"omlx/B", "omlx/A"}},
		{"sizes unknown", &localmodels.Pool{Models: []localmodels.PoolModel{{ID: "A", Loaded: true}}}, []localmodels.Entry{a}, []string{"omlx/A"}},
		{"memory guard off", pool(0, 40, pm("A", 40, 1, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
		{"no pool reading", nil, []localmodels.Entry{a}, []string{"omlx/A"}},
		{"target not in the pool counts as size zero", pool(100, 40, pm("A", 40, 1, false)), []localmodels.Entry{a}, nil},
		// omlx and omlx-6bit rows naming one directory are one loaded model:
		// both rows are named, its 60 is freed once, so B must go too.
		{"two rows, one model", pool(100, 100, pm("A", 60, 1, false), pm("B", 40, 2, false), pm("T", 80, 0, false)), []localmodels.Entry{a, a6, b}, []string{"omlx/A", "omlx-6bit/A", "omlx/B"}},
		{"nothing else loaded", pool(100, 0, pm("T", 200, 0, false)), nil, nil},
		// A running sibling the pool reading does not list cannot be sized,
		// so when the target does not fit wt must ask about it: skipping it
		// left the plan empty and the start went ahead unasked.
		{"a sibling missing from the pool reading is named", pool(100, 80, pm("T", 30, 0, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
		{"unresolved siblings come first and free nothing", pool(100, 80, pm("B", 40, 1, false), pm("T", 30, 0, false)), []localmodels.Entry{b, a}, []string{"omlx/A", "omlx/B"}},
		{"an unresolved sibling is not named when the target fits", pool(100, 40, pm("T", 40, 0, false)), []localmodels.Entry{a}, nil},
	} {
		got := ids(poolVictims(tc.pool, target, tc.others))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: victims = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestEvictionsByTenancy pins the rule per kind of server: an exclusive server
// displaces its one occupant, a shared one nobody, a pool whoever the plan
// names — and a probe that cannot be trusted is "unknown", never "nobody",
// because acting on a false "nobody" replaces a model wt never saw.
func TestEvictionsByTenancy(t *testing.T) {
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		running("mtplx", "mtplx/m1", "Org/M1"),
		running("omlx", "omlx/A", "A"),
		running("ollama", "ollama/a", "a:1b"),
	}}
	if v, known := evictions(Exclusive, "mtplx", Target{ProviderID: "mtplx", ModelName: "Org/M2"}, snap); !known || !reflect.DeepEqual(ids(v), []string{"mtplx/m1"}) {
		t.Errorf("exclusive: %v known=%v, want [mtplx/m1]", ids(v), known)
	}
	if v, known := evictions(Exclusive, "mtplx", Target{ProviderID: "mtplx", ModelName: "Org/M1"}, snap); !known || len(v) != 0 {
		t.Errorf("exclusive, target already running: %v, want none", ids(v))
	}
	if v, known := evictions(Shared, "ollama", Target{ProviderID: "ollama", ModelName: "b:2b"}, snap); !known || len(v) != 0 {
		t.Errorf("shared: %v known=%v, want none", ids(v), known)
	}
	if v, known := evictions(Pool, "omlx", Target{ProviderID: "omlx", ModelName: "T"}, snap); !known || !reflect.DeepEqual(ids(v), []string{"omlx/A"}) {
		t.Errorf("pool with no reading: %v known=%v, want [omlx/A]", ids(v), known)
	}
	snap.Providers = map[string]localmodels.Status{"omlx": localmodels.StatusPartial}
	if v, known := evictions(Pool, "omlx", Target{ProviderID: "omlx", ModelName: "T"}, snap); known || len(v) != 0 {
		t.Errorf("untrusted probe: %v known=%v, want none and unknown", ids(v), known)
	}
	// A refused connection is a positive answer: nothing is serving, so a cold
	// start displaces nobody. The plan calls that known and empty; the engine
	// still re-probes before it acts.
	for _, tc := range []struct {
		ten    Tenancy
		family string
		target Target
	}{
		{Exclusive, "mtplx", Target{ProviderID: "mtplx", ModelName: "Org/M2"}},
		{Pool, "omlx", Target{ProviderID: "omlx", ModelName: "T"}},
	} {
		down := localmodels.Snapshot{
			Providers: map[string]localmodels.Status{tc.family: localmodels.StatusPartial},
			Down:      map[string]bool{tc.family: true},
		}
		if v, known := evictions(tc.ten, tc.family, tc.target, down); !known || len(v) != 0 {
			t.Errorf("%s server down: %v known=%v, want none and known", tc.family, ids(v), known)
		}
	}
}

// TestOccupiedErrorNamesEveryOccupant verifies the refusal names each model a
// start would unload, since on a pool there can be several and the user is
// being asked to give all of them up.
func TestOccupiedErrorNamesEveryOccupant(t *testing.T) {
	err := &OccupiedError{Occupants: []localmodels.Entry{{ModelID: "omlx/A"}, {ModelID: "omlx/B"}}}
	if !reflect.DeepEqual(err.IDs(), []string{"omlx/A", "omlx/B"}) || !containsFold(err.Error(), "omlx/A, omlx/B") {
		t.Errorf("ids = %v, message = %q", err.IDs(), err.Error())
	}
}

// residentIDs lists the residents' model ids, each with "!" when the resident
// is not stoppable, for a table to compare in one go.
func residentIDs(rs []Resident) []string {
	var out []string
	for _, r := range rs {
		id := r.Entry.ModelID
		if !r.Stoppable {
			id += "!"
		}
		out = append(out, id)
	}
	return out
}

// TestResidentOthers pins what a start warns about on the other provider
// families (#412): every running model of another family, each family by its
// own probe — an untrusted one is "cannot tell", never "nothing loaded"; one
// that refused the connection holds nothing — and nothing at all when the
// target is already running or already loading (that start asked). The
// target's own family is the replace question's, never this one's, and
// mlx_lm_server is listed but cannot be stopped. A miss here either starts a
// model beside one the user never heard of, or offers to stop what wt cannot.
func TestResidentOthers(t *testing.T) {
	omlxT := Target{ProviderID: "omlx", ModelName: "T"}
	mtplxT := Target{ProviderID: "mtplx", ModelName: "Org/New"}
	ollamaT := Target{ProviderID: "ollama", ModelName: "b:2b"}
	ol := running("ollama", "ollama/a", "a:1b")
	mt := running("mtplx", "mtplx/m1", "Org/M1")
	om := running("omlx", "omlx/A", "A")
	om6 := running("omlx-6bit", "omlx-6bit/S", "S")
	mlx := running("mlx_lm_server", "mlx_lm_server/p", "p")
	pulled := localmodels.Entry{ProviderID: "ollama", ModelID: "ollama/idle", ModelName: "idle:1b"}
	loadingT := running("omlx", "omlx/T", "T")
	loadingT.Loading = true
	for _, tc := range []struct {
		name        string
		target      Target
		snap        localmodels.Snapshot
		want        []string
		wantUnknown []string
	}{
		{"an omlx start sees each other family's running model", omlxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{mlx, mt, ol, om6, pulled}},
			[]string{"mlx_lm_server/p!", "mtplx/m1", "ollama/a"}, nil},
		{"an mtplx start sees ollama and both omlx rows, not mtplx", mtplxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{mt, ol, om, om6}},
			[]string{"ollama/a", "omlx/A", "omlx-6bit/S"}, nil},
		{"an ollama start sees omlx and mtplx, not ollama", ollamaT,
			localmodels.Snapshot{Entries: []localmodels.Entry{mt, ol, om}},
			[]string{"mtplx/m1", "omlx/A"}, nil},
		{"the target's own family is excluded", omlxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{om, om6}}, nil, nil},
		{"an untrusted family is unknown, never empty", omlxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{mt, ol},
				Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusPartial, "ollama": localmodels.StatusOK, "omlx": localmodels.StatusPartial}},
			[]string{"ollama/a"}, []string{"mtplx"}},
		{"a Down family holds nothing", omlxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{ol},
				Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusPartial, "ollama": localmodels.StatusOK},
				Down:      map[string]bool{"mtplx": true}},
			[]string{"ollama/a"}, nil},
		{"an ambiguous mlx_lm_server is unknown", omlxT,
			localmodels.Snapshot{Providers: map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusPartial},
				Ambiguous: map[string]bool{"mlx_lm_server": true}},
			nil, []string{"mlx_lm_server"}},
		{"a target already running asks nothing", omlxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/T", "T"), mt},
				Providers: map[string]localmodels.Status{"ollama": localmodels.StatusPartial}},
			nil, nil},
		{"a target already loading asks nothing", omlxT,
			localmodels.Snapshot{Entries: []localmodels.Entry{loadingT, mt},
				Providers: map[string]localmodels.Status{"ollama": localmodels.StatusPartial}},
			nil, nil},
	} {
		others, unknown := residentOthers(tc.snap, localmodels.Family(tc.target.ProviderID), tc.target)
		if got := residentIDs(others); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: residents = %v, want %v", tc.name, got, tc.want)
		}
		if !reflect.DeepEqual(unknown, tc.wantUnknown) {
			t.Errorf("%s: unknown = %v, want %v", tc.name, unknown, tc.wantUnknown)
		}
	}
}

// TestResidentOthersLoadingOmlxIsNotStoppable pins that an omlx model of
// another family that is still loading is listed but never offered a stop
// (#412): omlx refuses to unload a model mid-load, so a stop answer would end
// the start in a failed stop, with the target never started. Both prompts
// read Stoppable, so the [s/k/N] question drops s and the picker's dialog
// drops "Stop them, then start" when nothing else can be stopped. A loaded
// omlx model, and a loading model of any other family, stays stoppable.
func TestResidentOthersLoadingOmlxIsNotStoppable(t *testing.T) {
	loading := running("omlx", "omlx/L", "L")
	loading.Loading = true
	loading6 := running("omlx-6bit", "omlx-6bit/S", "S")
	loading6.Loading = true
	loaded := running("omlx", "omlx/A", "A")
	ol := running("ollama", "ollama/a", "a:1b")
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{loading, loading6, loaded, ol}}
	target := Target{ProviderID: "mtplx", ModelName: "Org/New"}
	others, _ := residentOthers(snap, "mtplx", target)
	if got, want := residentIDs(others), []string{"omlx/L!", "omlx-6bit/S!", "omlx/A", "ollama/a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("residents = %v, want %v (a loading omlx model listed, not stoppable)", got, want)
	}
}

// TestResidentsFindAnMtplxStillLoading pins the one resident the snapshot
// cannot show: an mtplx still loading refuses its port, so it reads Down, and
// only wt's pidfile names it. It is listed as loading under its registry id
// (from the server's --model-id), stoppable only through the pidfile stop
// (PID set). A pidfile wt cannot check makes mtplx "cannot tell", and nothing
// is read for an mtplx start (its own family) or when mtplx is not Down.
// Missing it would start a model beside a ~30 GB load the user never saw.
func TestResidentsFindAnMtplxStillLoading(t *testing.T) {
	down := localmodels.Snapshot{
		Entries:   []localmodels.Entry{{ProviderID: "mtplx", ModelID: "mtplx/org--m", ModelName: "org/m"}},
		Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusPartial},
		Down:      map[string]bool{"mtplx": true},
	}
	omlxT := Target{ProviderID: "omlx", ModelName: "T"}

	_, e := loadingEnv(t, true)
	others, unknown := e.residents(loadCfg(), down, "omlx", omlxT)
	if len(others) != 1 || others[0].PID != 4242 || !others[0].Stoppable || others[0].Entry.ModelID != "mtplx/org--m" || len(unknown) != 0 {
		t.Fatalf("residents = %+v unknown = %v, want mtplx/org--m loading as pid 4242", others, unknown)
	}
	if got := others[0].Label(); got != "mtplx/org--m (loading)" {
		t.Errorf("label = %q, want mtplx/org--m (loading)", got)
	}

	if others, _ := e.residents(loadCfg(), down, "mtplx", Target{ProviderID: "mtplx", ModelName: "org/x"}); len(others) != 0 {
		t.Errorf("an mtplx start: residents = %+v, want none (its own family)", others)
	}
	up := down
	up.Down = nil
	up.Providers = nil
	if others, _ := e.residents(loadCfg(), up, "omlx", omlxT); len(others) != 0 {
		t.Errorf("mtplx not Down: residents = %+v, want none (the probe answered)", others)
	}

	pt, e := loadingEnv(t, true)
	pt.describe = errors.New("ps failed")
	if others, unknown := e.residents(loadCfg(), down, "omlx", omlxT); len(others) != 0 || !reflect.DeepEqual(unknown, []string{"mtplx"}) {
		t.Errorf("unreadable pidfile process: residents = %+v unknown = %v, want mtplx unknown", others, unknown)
	}
}

// TestResidentFigure pins how a resident's memory is worded: what its server
// says, formatted as `ollama list` does, "memory use unknown" when it says
// nothing (never 0, never a guess), and a resident wt cannot stop says so.
func TestResidentFigure(t *testing.T) {
	for _, tc := range []struct {
		r    Resident
		want string
	}{
		{Resident{Entry: localmodels.Entry{ProviderID: "ollama", LoadedSize: 9_600_000_000}, Stoppable: true}, "9.6 GB loaded"},
		{Resident{Entry: localmodels.Entry{ProviderID: "omlx-6bit", LoadedSize: 18_200_000_000}, Stoppable: true}, "18.2 GB resident (omlx estimate)"},
		{Resident{Entry: localmodels.Entry{ProviderID: "mtplx"}, Stoppable: true}, "memory use unknown"},
		{Resident{Entry: localmodels.Entry{ProviderID: "mlx_lm_server"}}, "memory use unknown; wt cannot stop it"},
	} {
		if got := tc.r.Figure(); got != tc.want {
			t.Errorf("Figure(%+v) = %q, want %q", tc.r.Entry, got, tc.want)
		}
	}
}
