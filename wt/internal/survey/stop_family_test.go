package survey

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestStopStateCountsSessionsFamilyWide verifies the stop state counts the
// live wt sessions of a provider family from every live session (stopDeps.live), not
// from the candidates: a model that is not running, an omlx-6bit row, and a
// discovered model no registry lists all count under their family, while a
// cloud model of the same provider and another family's sessions do not. The
// halt of a whole provider asks about these sessions; counted per candidate,
// a family whose probe was not trusted had none and was halted unasked (#307).
func TestStopStateCountsSessionsFamilyWide(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal}, {ID: "omlx", Location: config.LocationLocal},
			{ID: "omlx-6bit", Location: config.LocationLocal}, {ID: "mtplx", Location: config.LocationLocal},
		},
		Models: []config.Model{
			{ID: "omlx/c", ProviderID: "omlx", ModelName: "c"},
			{ID: "six/renamed", ProviderID: "omlx-6bit", ModelName: "d-6bit"},
			{ID: "ollama/cloudy", ProviderID: "ollama", ModelName: "cloudy", Location: config.LocationCloud},
		},
	}
	h := &stopHarness{
		snap: localmodels.Snapshot{
			// Nothing reads as running: omlx's probe was not trusted.
			Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/c", ModelName: "c"}},
			Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial, "mtplx": localmodels.StatusPartial, "ollama": localmodels.StatusOK},
			Down:      map[string]bool{"mtplx": true},
		},
		counts: map[string]int{"omlx/c": 2, "six/renamed": 1, "omlx/Discovered-4bit": 1, "ollama/cloudy": 4, "mtplx/m": 1, "claude/opus": 3},
	}
	st := stopState(cfg, h.deps())
	if len(st.Candidates) != 0 {
		t.Fatalf("Candidates = %v, want none from an untrusted probe", st.Candidates)
	}
	omlx := st.Families["omlx"]
	if omlx.Sessions != 4 || strings.Join(omlx.Users, ",") != "omlx/Discovered-4bit,omlx/c,six/renamed" {
		t.Errorf("omlx = %+v, want 4 sessions on the three omlx ids, sorted", omlx)
	}
	if !omlx.Untrusted || omlx.Down {
		t.Errorf("omlx = %+v, want untrusted and not down", omlx)
	}
	if m := st.Families["mtplx"]; !m.Untrusted || !m.Down || m.Sessions != 1 {
		t.Errorf("mtplx = %+v, want untrusted, down, 1 session", m)
	}
	if o := st.Families["ollama"]; o.Untrusted || o.Down || o.Sessions != 0 {
		t.Errorf("ollama = %+v, want trusted, up, and no session from a cloud model", o)
	}
	if got := strings.Join(st.Unknown(), ","); got != "omlx" {
		t.Errorf("Unknown = %q, want omlx alone: a refused port is known to serve nothing", got)
	}
}

// TestStopStateMarksOnlyProbedFamilies verifies FamilyState.Probed is set for
// the families the inventory probed and for no other: a family known only
// from a live session's model id, and a provider row the inventory does not
// probe (no location, no local model), have none. `wt stop --all` halts mtplx
// unless its port refused, and a family nobody probed must not read as "did
// not refuse" — that ran `mtplx stop` on machines where mtplx is only a row.
func TestStopStateMarksOnlyProbedFamilies(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
			Down:      map[string]bool{},
		},
		counts: map[string]int{"mtplx/m": 1},
	}
	st := stopState(&config.Config{}, h.deps())
	if !st.Families["omlx"].Probed {
		t.Errorf("omlx = %+v, want Probed: the snapshot carries its status", st.Families["omlx"])
	}
	if m := st.Families["mtplx"]; m.Probed || m.Sessions != 1 {
		t.Errorf("mtplx = %+v, want its session counted and Probed false", m)
	}

	// Through the real inventory: a row with no location and no local model
	// is not probed at all (nothing is dialed here).
	d := h.deps()
	d.inventory = localmodels.Inventory
	d.live = func() map[string]int { return nil }
	st = stopState(&config.Config{Providers: []config.Provider{{ID: "mtplx"}}}, d)
	if fs, ok := st.Families["mtplx"]; ok || fs.Probed {
		t.Errorf("mtplx = %+v (present %v), want no entry for a row the inventory does not probe", fs, ok)
	}
}

// TestStopStateReadsThePidfileOnlyBehindARefusedPort verifies the stop state
// asks what a family's pidfile says exactly when its port refused, and
// carries the answer: mtplx reads its weights before it opens its port, so a
// refused port with a verified live server behind it is a server still
// loading, which `wt stop mtplx` and `wt stop --all` must be able to see
// (#308). A family that answered, or gave an unusable answer, is stopped
// through its port and the pidfile is not consulted.
func TestStopStateReadsThePidfileOnlyBehindARefusedPort(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "mtplx", Location: config.LocationLocal}, {ID: "omlx", Location: config.LocationLocal}, {ID: "ollama", Location: config.LocationLocal},
	}}
	for name, tc := range map[string]struct {
		status localmodels.Status
		down   bool
		want   lifecycle.Loading
	}{
		"refused":          {localmodels.StatusUnreachable, true, lifecycle.Loading{PID: 4242}},
		"answered":         {localmodels.StatusOK, false, lifecycle.Loading{}},
		"no usable answer": {localmodels.StatusUnreachable, false, lifecycle.Loading{}},
	} {
		t.Run(name, func(t *testing.T) {
			h := &stopHarness{
				snap: localmodels.Snapshot{
					Providers: map[string]localmodels.Status{"mtplx": tc.status, "omlx": localmodels.StatusOK, "ollama": localmodels.StatusOK},
					Down:      map[string]bool{"mtplx": tc.down},
				},
				loading: map[string]lifecycle.Loading{"mtplx": {PID: 4242}},
			}
			st := stopState(cfg, h.deps())
			if got := st.Families["mtplx"].Loading; got != tc.want {
				t.Errorf("mtplx Loading = %+v, want %+v", got, tc.want)
			}
			wantAsked := []string(nil)
			if tc.down {
				wantAsked = []string{"mtplx"}
			}
			if !slices.Equal(h.loadingAsked, wantAsked) {
				t.Errorf("pidfile read for %v, want %v", h.loadingAsked, wantAsked)
			}
		})
	}
}

// TestProcessSeamsAreClosedInThisPackage pins what TestMain arms: the mtplx
// pidfile is a scratch path and the process-table read fails, even with a
// live pid in that pidfile. A test here that reaches the production stop
// state (ReadStopState, PickerWith) would otherwise read the developer's
// mtplx pidfile and inspect their processes; this is the test that fails when
// lifecycle.IsolateProcessesForTest() is dropped from TestMain.
func TestProcessSeamsAreClosedInThisPackage(t *testing.T) {
	pidfile := os.Getenv("WT_MTPLX_PIDFILE")
	if pidfile == "" || strings.HasPrefix(pidfile, "/tmp/local-ai-setup") || strings.HasPrefix(pidfile, "/private/tmp/local-ai-setup") {
		t.Fatalf("WT_MTPLX_PIDFILE = %q, want a scratch path: TestMain no longer isolates the mtplx pidfile", pidfile)
	}
	cfg := &config.Config{Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal}}}
	if l := lifecycle.LoadingServer(cfg, "mtplx"); l != (lifecycle.Loading{}) {
		t.Errorf("LoadingServer = %+v, want nothing: no pidfile exists at the scratch path", l)
	}
	dir := filepath.Dir(pidfile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := lifecycle.LoadingServer(cfg, "mtplx"); l.Err == nil || !strings.Contains(l.Err.Error(), "not stubbed") {
		t.Errorf("LoadingServer over a live pid = %+v, want the process seam's refusal", l)
	}
}

// TestStopStateFindsAMachineInstanceOnAnotherPort verifies the stop state
// lists a ScopeMachine family's instance the registry-port probe cannot see
// (#457): rec started by hand on another port answers `rec status` but
// refuses the registry port, so the snapshot has no running entry and marks
// the family Down. Without the machine read `wt stop rec` said "nothing
// running", and `wt stop --all` and the picker left it up. The instance is a
// candidate with its sessions counted, and the family stops reading as Down.
func TestStopStateFindsAMachineInstanceOnAnotherPort(t *testing.T) {
	found := runningEntry("rec", "rec/flint", "flint")
	h := &stopHarness{
		snap: localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"rec": localmodels.StatusUnreachable},
			Down:      map[string]bool{"rec": true},
		},
		counts:  map[string]int{"rec/flint": 2},
		machine: map[string]localmodels.Entry{"rec": found},
	}
	st := stopState(&config.Config{}, h.deps())
	if len(st.Candidates) != 1 || st.Candidates[0].Entry.ModelID != "rec/flint" || st.Candidates[0].Sessions != 2 {
		t.Fatalf("candidates = %+v, want the rec instance with 2 sessions", st.Candidates)
	}
	if fs := st.Families["rec"]; fs.Down || fs.Untrusted || !fs.Probed {
		t.Errorf("rec family = %+v, want probed, not down, not untrusted", fs)
	}
	if h.loadingAsked != nil {
		t.Errorf("pidfile read for %v; an instance that was found needs no loading check", h.loadingAsked)
	}
}

// TestStopStateAsksTheMachineOnlyWhereItCanHelp verifies the readiness
// command (a process spawn) runs only for a ScopeMachine family the snapshot
// lists no instance of: not for a port-scope family or ollama, and not for rec
// when the registry-port probe already found it. A found instance must not be
// listed twice, and an idle machine costs one `rec status`, not one per read
// of every family.
func TestStopStateAsksTheMachineOnlyWhereItCanHelp(t *testing.T) {
	listed := runningEntry("rec", "rec/flint", "flint")
	h := &stopHarness{
		snap: localmodels.Snapshot{
			Entries:   []localmodels.Entry{listed},
			Providers: map[string]localmodels.Status{"rec": localmodels.StatusOK, "mtplx": localmodels.StatusOK, "ollama": localmodels.StatusOK},
		},
		machine: map[string]localmodels.Entry{"rec": listed},
	}
	st := stopState(&config.Config{}, h.deps())
	if len(st.Candidates) != 1 {
		t.Errorf("candidates = %+v, want the listed rec once", st.Candidates)
	}
	if h.machineAsked != nil {
		t.Errorf("machine read for %v, want none", h.machineAsked)
	}

	// Nothing listed and nothing on the machine: asked for rec alone, still down.
	h = &stopHarness{snap: localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"rec": localmodels.StatusUnreachable, "mtplx": localmodels.StatusUnreachable},
		Down:      map[string]bool{"rec": true, "mtplx": true},
	}}
	st = stopState(&config.Config{}, h.deps())
	if !slices.Equal(h.machineAsked, []string{"rec"}) {
		t.Errorf("machine read for %v, want [rec]", h.machineAsked)
	}
	if len(st.Candidates) != 0 || !st.Families["rec"].Down {
		t.Errorf("state = %+v, want no candidate and rec still down", st)
	}
}

// TestDefaultStopDepsReadTheGivenSessions pins that the host stop's session
// counts are the ones the caller read from the model server: counts answers
// for the ids asked, live for every id, and nil means no session anywhere.
func TestDefaultStopDepsReadTheGivenSessions(t *testing.T) {
	d := defaultStopDeps(map[string]int{"omlx/a": 2, "omlx/b": 1})
	got := d.counts([]string{"omlx/a", "omlx/zzz"})
	if len(got) != 1 || got["omlx/a"] != 2 {
		t.Errorf("counts = %v, want only omlx/a:2", got)
	}
	if live := d.live(); live["omlx/b"] != 1 || len(live) != 2 {
		t.Errorf("live = %v", live)
	}
	none := defaultStopDeps(nil)
	if n := len(none.counts([]string{"omlx/a"})); n != 0 || len(none.live()) != 0 {
		t.Errorf("nil sessions: counts %d, live %v", n, none.live())
	}
}
