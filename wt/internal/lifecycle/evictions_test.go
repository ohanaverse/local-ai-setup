package lifecycle

import (
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

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
		// 40 + 55 = 95 is under the ceiling but over the 90 the margin leaves.
		{"inside the margin", pool(100, 40, pm("A", 40, 1, false), pm("T", 55, 0, false)), []localmodels.Entry{a}, []string{"omlx/A"}},
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
	// start displaces nobody. Reading it as "unknown" made `wt start <id>
	// --plan --json` answer unknown, and a scripted start fail, whenever the
	// server was simply not running.
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
