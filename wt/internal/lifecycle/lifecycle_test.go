package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

type fakeBackend struct {
	single   bool
	calls    *[]string
	stopErr  error
	startErr error
}

func (f *fakeBackend) singleModel() bool { return f.single }
func (f *fakeBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	*f.calls = append(*f.calls, "stop")
	return f.stopErr
}
func (f *fakeBackend) start(ctx context.Context, e *env, cfg *config.Config, t Target, report func(Stage)) error {
	*f.calls = append(*f.calls, "start:"+t.ModelName)
	report(StageStarting)
	return f.startErr
}

func fakeEnv(snap localmodels.Snapshot, single bool, calls *[]string) *env {
	e := testEnv()
	e.inventory = func(*config.Config) localmodels.Snapshot { return snap }
	fb := &fakeBackend{single: single, calls: calls}
	e.backends = map[string]backend{"omlx": fb, "mtplx": fb, "ollama": &fakeBackend{single: false, calls: calls}}
	return e
}

func running(provider, id, name string) localmodels.Entry {
	return localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true}
}

// containsFold reports whether s contains sub, case-insensitively.
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// modelsServing is an OpenAI-compatible /v1/models server reporting the given
// ids (none when called with no arguments).
func modelsServing(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	body := `{"data":[`
	for i, id := range ids {
		if i > 0 {
			body += ","
		}
		body += `{"id":"` + id + `"}`
	}
	body += `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	return srv
}

// TestOccupantRules verifies who a start would replace: nobody for ollama
// (multi-tenant), a running model on the same single-model domain otherwise —
// with omlx and omlx-6bit sharing one domain — never the target itself and
// never a model that is not running. Getting this wrong either silently kills
// a model the user was using or skips a confirmation that was needed.
func TestOccupantRules(t *testing.T) {
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{
		running("ollama", "ollama/a", "a:1b"),
		running("omlx-6bit", "omlx-6bit/six", "org/Six-6bit"),
		{ProviderID: "omlx", ModelID: "omlx/idle", ModelName: "Idle-4bit"},
		running("mtplx", "mtplx/m1", "Org/M1"),
	}}

	if _, ok := Occupant(Target{"ollama", "b:2b"}, snap); ok {
		t.Error("ollama must never have an occupant")
	}
	if occ, ok := Occupant(Target{"omlx", "Qwen-4bit"}, snap); !ok || occ.ModelID != "omlx-6bit/six" {
		t.Errorf("omlx occupant = %+v ok=%v, want the running omlx-6bit model (one domain)", occ, ok)
	}
	if _, ok := Occupant(Target{"omlx", "Six-6bit"}, snap); ok {
		t.Error("the target itself (matching by lenient name) must not be its own occupant")
	}
	if occ, ok := Occupant(Target{"mtplx", "Org/M2"}, snap); !ok || occ.ModelID != "mtplx/m1" {
		t.Errorf("mtplx occupant = %+v ok=%v", occ, ok)
	}
	if _, ok := Occupant(Target{"mtplx", "Org/M1"}, snap); ok {
		t.Error("mtplx target already running must not be its own occupant")
	}
	if _, ok := Occupant(Target{"omlx", "Qwen-4bit"}, localmodels.Snapshot{Entries: []localmodels.Entry{{ProviderID: "omlx", ModelName: "Idle-4bit"}}}); ok {
		t.Error("a non-running entry is not an occupant")
	}
}

// TestStartAlreadyRunningIsNoOp verifies a target that live Inventory already
// reports running causes no backend calls and no progress — selecting a model
// that is up must not restart or re-warm it.
func TestStartAlreadyRunningIsNoOp(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/q", "Qwen-4bit")}}
	var stages []Stage
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{"omlx", "Qwen-4bit"}, Options{Progress: func(s Stage) { stages = append(stages, s) }})
	if err != nil || len(calls) != 0 || len(stages) != 0 {
		t.Errorf("err=%v calls=%v stages=%v, want no-op", err, calls, stages)
	}
}

// TestStartNeverReplacesSilently verifies that with a running occupant and
// AllowReplace false, Start returns *OccupiedError carrying that occupant and
// touches nothing — the caller (TUI/non-TUI) must confirm first.
func TestStartNeverReplacesSilently(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/old", "Old-4bit")}}
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{"omlx", "New-4bit"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) || occ.Occupant.ModelID != "omlx/old" {
		t.Fatalf("err = %v, want *OccupiedError for omlx/old", err)
	}
	if len(calls) != 0 {
		t.Errorf("backend touched despite refusal: %v", calls)
	}
}

// TestStartReplacesInOrderWhenAllowed verifies replacement stops the occupant
// BEFORE starting the target and reports stages in order (stopping-occupant,
// then the backend's own stages).
func TestStartReplacesInOrderWhenAllowed(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("mtplx", "mtplx/old", "Org/Old")}}
	var stages []Stage
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{"mtplx", "Org/New"},
		Options{AllowReplace: true, Progress: func(s Stage) { stages = append(stages, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"stop", "start:Org/New"}) {
		t.Errorf("calls = %v, want stop then start", calls)
	}
	if !reflect.DeepEqual(stages, []Stage{StageStoppingOccupant, StageStarting}) {
		t.Errorf("stages = %v", stages)
	}
}

// TestStartOllamaIgnoresOtherRunningModels verifies ollama starts alongside
// other running ollama models with no stop and no confirmation — it is
// multi-tenant and nothing is replaced.
func TestStartOllamaIgnoresOtherRunningModels(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("ollama", "ollama/a", "a:1b")}}
	if err := start(context.Background(), fakeEnv(snap, false, &calls), &config.Config{}, Target{"ollama", "b:2b"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b:2b"}) {
		t.Errorf("calls = %v", calls)
	}
}

// TestStartStopFailureDoesNotStart verifies a failed occupant stop aborts the
// start with an error naming both models — starting into a still-occupied port
// would fail confusingly or corrupt the running model.
func TestStartStopFailureDoesNotStart(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{Entries: []localmodels.Entry{running("omlx", "omlx/old", "Old-4bit")}}
	e := fakeEnv(snap, true, &calls)
	e.backends["omlx"].(*fakeBackend).stopErr = errors.New("still listening")
	err := start(context.Background(), e, &config.Config{}, Target{"omlx", "New-4bit"}, Options{AllowReplace: true})
	if err == nil || len(calls) != 1 || calls[0] != "stop" {
		t.Errorf("err=%v calls=%v, want an error after only a stop call", err, calls)
	}
}

// TestStartUnsupportedProvider verifies providers without a backend
// (mlx_lm_server, retired llamacpp, unknown ids) fail with *UnsupportedError
// rather than silently doing nothing.
func TestStartUnsupportedProvider(t *testing.T) {
	var calls []string
	for _, id := range []string{"mlx_lm_server", "llamacpp", "nope"} {
		err := start(context.Background(), fakeEnv(localmodels.Snapshot{}, true, &calls), &config.Config{}, Target{id, "x"}, Options{})
		var u *UnsupportedError
		if !errors.As(err, &u) {
			t.Errorf("%s: err = %v, want *UnsupportedError", id, err)
		}
	}
}

// TestTypedErrorMessages verifies each typed error's message names what the
// user must do — these strings are what 4b/4c will show verbatim.
func TestTypedErrorMessages(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&OccupiedError{Occupant: localmodels.Entry{ModelID: "omlx/old"}}, "omlx/old"},
		{&DaemonDownError{Provider: "ollama", Origin: "http://localhost:11434"}, "http://localhost:11434"},
		{&BinaryMissingError{Binary: "mtplx"}, "mtplx"},
		{&PortBusyError{Port: 8003}, "8003"},
		{&OccupancyUnknownError{ProviderID: "omlx", Origin: "http://localhost:8000"}, "omlx"},
		{&UnsupportedError{ProviderID: "mlx_lm_server"}, "mlx_lm_server"},
	}
	for _, tc := range cases {
		if msg := tc.err.Error(); !containsFold(msg, tc.want) {
			t.Errorf("%T message %q must mention %q", tc.err, msg, tc.want)
		}
	}
}

// TestOccupantDerivesSingleModelFromBackendRegistry verifies Occupant's
// single-model rule comes from the same registry defaultEnv builds, for every
// registered family. A backend that is startable (singleModel true) while
// Occupant reports no occupant is exactly the state in which a running model
// gets replaced with no confirmation — the trap a hand-maintained family list
// leaves for the next backend.
func TestOccupantDerivesSingleModelFromBackendRegistry(t *testing.T) {
	if len(defaultEnv().backends) != len(backendsByFamily) {
		t.Fatalf("defaultEnv registers %d backends, backendsByFamily has %d — they must not drift",
			len(defaultEnv().backends), len(backendsByFamily))
	}
	for family, b := range backendsByFamily {
		snap := localmodels.Snapshot{Entries: []localmodels.Entry{running(family, family+"/occupant", "occupant")}}
		_, ok := Occupant(Target{ProviderID: family, ModelName: "wanted"}, snap)
		if ok != b.singleModel() {
			t.Errorf("family %s: Occupant reported an occupant=%v but singleModel()=%v", family, ok, b.singleModel())
		}
	}
}

// TestStartRefusesWhenListenerStalls verifies a single-model provider whose
// /v1/models accepts the connection and then stalls produces
// *OccupancyUnknownError and touches nothing. This is the silent-eviction case:
// the daemon is up and serving a model, but the probe cannot see it, so
// starting would replace it with no confirmation.
func TestStartRefusesWhenListenerStalls(t *testing.T) {
	stall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
	}))
	defer stall.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", stall.URL)

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var unknown *OccupancyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("start with a stalled listener = %v, want *OccupancyUnknownError", err)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) while the occupant was unknown", calls)
	}
}

// TestStartProceedsWhenProviderIsDown verifies a definitively-down provider
// still starts with no confirmation. This is the ordinary cold start for
// omlx/mtplx: making a refused connection count as indeterminate would demand
// AllowReplace for every normal start, which is worse than the bug.
func TestStartProceedsWhenProviderIsDown(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", "http://"+freeAddr(t))

	if err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{}); err != nil {
		t.Fatalf("start with the provider down = %v, want nil", err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b"}) {
		t.Errorf("calls = %v, want [start:b]", calls)
	}
}

// TestStartFindsOccupantTheSnapshotMissed verifies the live re-probe catches a
// model the inventory snapshot could not report, and returns the ordinary
// *OccupiedError for it. Falling through to a start here is the eviction.
func TestStartFindsOccupantTheSnapshotMissed(t *testing.T) {
	serving := modelsServing(t, "a")
	defer serving.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", serving.URL)

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("start over a live occupant = %v, want *OccupiedError", err)
	}
	if !containsFold(occ.Occupant.ModelID, "a") {
		t.Errorf("occupant = %q, want the model the server reports (a)", occ.Occupant.ModelID)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) without AllowReplace", calls)
	}
}

// TestStartProceedsWhenServerReportsNoModel verifies a server that answers with
// an empty model list is started into normally. It answered, so it is neither
// unknown nor occupied.
func TestStartProceedsWhenServerReportsNoModel(t *testing.T) {
	empty := modelsServing(t)
	defer empty.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", empty.URL)

	if err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{}); err != nil {
		t.Fatalf("start against an idle server = %v, want nil", err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b"}) {
		t.Errorf("calls = %v, want [start:b]", calls)
	}
}

// TestStartUsesSnapshotWhenProbeIsTrustworthy verifies a trustworthy snapshot
// still decides occupancy on its own, so the common path costs no extra HTTP
// request and the existing Occupant rules keep governing.
func TestStartUsesSnapshotWhenProbeIsTrustworthy(t *testing.T) {
	var calls []string
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{running("omlx", "omlx/a", "a")},
	}
	e := fakeEnv(snap, true, &calls)
	cfg := provCfg("omlx", "http://"+freeAddr(t)) // no listener: a live probe must not be consulted

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) {
		t.Fatalf("start with a trustworthy snapshot showing an occupant = %v, want *OccupiedError", err)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) without AllowReplace", calls)
	}
}

// TestOccupantIgnoresUntrustworthySnapshot verifies Occupant does not claim
// "no occupant" from a probe it knows failed. Its contract is that the caller
// may act on a false result, so it must not answer from untrustworthy data.
func TestOccupantIgnoresUntrustworthySnapshot(t *testing.T) {
	snap := localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial},
		Entries:   []localmodels.Entry{running("omlx", "omlx/a", "a")},
	}
	if _, ok := Occupant(Target{ProviderID: "omlx", ModelName: "b"}, snap); ok {
		t.Error("Occupant must not report an occupant from an untrustworthy probe")
	}
}
