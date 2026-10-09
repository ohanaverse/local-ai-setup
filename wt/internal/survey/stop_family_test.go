package survey

import (
	"bytes"
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

// TestStopPickerSingleModelFamilyBusyBlocksSiblings verifies that when one
// running model of a single-model provider (mtplx: one model per process) is
// in use by another session, a sibling row that reads as running is not
// offered either. Stopping the sibling runs a whole-provider stop, which would
// kill the server under the other session. Multi-tenant ollama is unaffected.
func TestStopPickerSingleModelFamilyBusyBlocksSiblings(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("mtplx", "mtplx/m-4bit", "m-4bit"),
			runningEntry("mtplx", "mtplx/m-6bit", "m-6bit"),
			runningEntry("ollama", "ollama/a", "a"),
		}},
		counts: map[string]int{"mtplx/m-4bit": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 || h.stops[0] != "ollama|a" {
		t.Fatalf("stops = %v, want only the ollama model", h.stops)
	}
	if strings.Contains(out.String(), "6bit") {
		t.Errorf("output offers the sibling of a busy variant: %q", out.String())
	}
}

// TestStopPickerPoolSiblingsAreIndependent pins #213 for the stop picker: omlx
// holds a pool, and stopping one model only unloads it. So an idle omlx model
// is offered while another session uses its sibling, and two selected omlx
// models are each stopped — skipping the second, as for a single-model
// provider, would print "done" for a model that is still loaded.
func TestStopPickerPoolSiblingsAreIndependent(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("omlx", "omlx/m-4bit", "m-4bit"),
			runningEntry("omlx-6bit", "omlx-6bit/m-6bit", "m-6bit"),
			runningEntry("omlx", "omlx/n-4bit", "n-4bit"),
		}},
		counts: map[string]int{"omlx/m-4bit": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 2 || h.stops[0] != "omlx-6bit|m-6bit" || h.stops[1] != "omlx|n-4bit" {
		t.Fatalf("stops = %v, want both idle omlx models stopped, each by its own stop, and the busy one left", h.stops)
	}
	if strings.Contains(out.String(), "omlx/m-4bit") {
		t.Errorf("output offers the model another session is using: %q", out.String())
	}
}

// TestStopPickerSingleModelFamilyStoppedOnce verifies selecting two idle
// rows of one single-model provider (mtplx) runs the provider stop once and
// reports both done, since the first stop already took the server down.
func TestStopPickerSingleModelFamilyStoppedOnce(t *testing.T) {
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{
		runningEntry("mtplx", "mtplx/m-4bit", "m-4bit"),
		runningEntry("mtplx", "mtplx/m-6bit", "m-6bit"),
	}}}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 {
		t.Fatalf("stops = %v, want exactly one provider stop", h.stops)
	}
	if strings.Count(out.String(), "... done") != 2 {
		t.Errorf("output = %q, want two done lines", out.String())
	}
}

// TestStopPickerAliasRowsShareUsage verifies that two registry rows naming the
// same provider-side model ("qwen3.8" and ollama's implicit "qwen3.8:latest")
// share one usage count: a live session launched from row A keeps row B off
// the list. Counting by registry id alone offered idle alias B, and
// `ollama stop` on it unloaded the single loaded copy under the live session.
func TestStopPickerAliasRowsShareUsage(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{Entries: []localmodels.Entry{
			runningEntry("ollama", "ollama/qwen", "qwen3.8"),
			runningEntry("ollama", "ollama/qwen-latest", "qwen3.8:latest"),
			runningEntry("ollama", "ollama/other", "other"),
		}},
		counts: map[string]int{"ollama/qwen": 1},
	}
	var out bytes.Buffer
	runStopPicker(strings.NewReader("all\n\n"), &out, &config.Config{}, h.deps())
	if len(h.stops) != 1 || h.stops[0] != "ollama|other" {
		t.Fatalf("stops = %v, want only ollama|other (both qwen aliases are in use)", h.stops)
	}
}

// TestStopStateCountsSessionsFamilyWide verifies the stop state counts the
// live wt sessions of a provider family from every live refcount entry, not
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

// TestStopPickerReportsFamiliesItCouldNotRead verifies the picker hands back
// the families whose probe gave no usable answer when it has nothing to offer.
// `wt stop` prints "no running local models" from that result, and it may say
// so only when that is known.
func TestStopPickerReportsFamiliesItCouldNotRead(t *testing.T) {
	h := &stopHarness{snap: localmodels.Snapshot{
		Entries:   []localmodels.Entry{runningEntry("mtplx", "mtplx/m", "m")},
		Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusPartial},
	}}
	var out bytes.Buffer
	offered, unknown, loading := runStopPickerWith(strings.NewReader(""), &out, &config.Config{}, h.deps(), Options{IncludeInUse: true})
	if offered || strings.Join(unknown, ",") != "mtplx" || out.Len() != 0 || len(loading) != 0 {
		t.Fatalf("offered = %v unknown = %v loading = %v out = %q, want nothing offered, mtplx unknown, nothing loading, no output", offered, unknown, loading, out.String())
	}
}

// TestStopPickerReportsWhatIsStillLoading verifies the picker hands back what
// the pidfile behind a refused port said, for the family it said it about
// and no other. An mtplx that is still loading serves no model, so the picker
// has no row for it; bare `wt stop` printed "no running local models" over a
// process reading tens of gigabytes, and needs this to say otherwise.
func TestStopPickerReportsWhatIsStillLoading(t *testing.T) {
	h := &stopHarness{
		snap: localmodels.Snapshot{
			Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusUnreachable, "omlx": localmodels.StatusUnreachable},
			Down:      map[string]bool{"mtplx": true, "omlx": true},
		},
		loading: map[string]lifecycle.Loading{"mtplx": {PID: 4242}},
	}
	cfg := &config.Config{Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal}, {ID: "omlx", Location: config.LocationLocal}}}
	var out bytes.Buffer
	offered, unknown, loading := runStopPickerWith(strings.NewReader(""), &out, cfg, h.deps(), Options{IncludeInUse: true})
	if offered || len(unknown) != 0 || out.Len() != 0 {
		t.Errorf("offered = %v unknown = %v out = %q, want nothing offered, nothing unknown, no output", offered, unknown, out.String())
	}
	if len(loading) != 1 || loading["mtplx"] != (lifecycle.Loading{PID: 4242}) {
		t.Errorf("loading = %+v, want only mtplx, pid 4242", loading)
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
