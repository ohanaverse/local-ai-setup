package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
	ten      Tenancy // overrides single when set
	calls    *[]string
	stopErr  error
	startErr error
}

func (f *fakeBackend) tenancy() Tenancy {
	switch {
	case f.ten != NoTenancy:
		return f.ten
	case f.single:
		return Exclusive
	}
	return Shared
}

func (f *fakeBackend) stop(ctx context.Context, e *env, cfg *config.Config) error {
	*f.calls = append(*f.calls, "stop")
	return f.stopErr
}
func (f *fakeBackend) stopModel(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	*f.calls = append(*f.calls, "stopModel:"+modelName)
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

	if v, _ := Evictions(Target{ProviderID: "ollama", ModelName: "b:2b"}, snap); len(v) != 0 {
		t.Error("ollama must never have an occupant")
	}
	if v, _ := Evictions(Target{ProviderID: "omlx", ModelName: "Qwen-4bit"}, snap); len(v) == 0 || v[0].ModelID != "omlx-6bit/six" {
		t.Errorf("omlx occupant = %+v, want the running omlx-6bit model (one domain)", v)
	}
	if v, _ := Evictions(Target{ProviderID: "omlx", ModelName: "Six-6bit"}, snap); len(v) != 0 {
		t.Error("the target itself (matching by lenient name) must not be its own occupant")
	}
	if v, _ := Evictions(Target{ProviderID: "mtplx", ModelName: "Org/M2"}, snap); len(v) == 0 || v[0].ModelID != "mtplx/m1" {
		t.Errorf("mtplx occupant = %+v", v)
	}
	if v, _ := Evictions(Target{ProviderID: "mtplx", ModelName: "Org/M1"}, snap); len(v) != 0 {
		t.Error("mtplx target already running must not be its own occupant")
	}
	if v, _ := Evictions(Target{ProviderID: "omlx", ModelName: "Qwen-4bit"}, localmodels.Snapshot{Entries: []localmodels.Entry{{ProviderID: "omlx", ModelName: "Idle-4bit"}}}); len(v) != 0 {
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
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{ProviderID: "omlx", ModelName: "Qwen-4bit"}, Options{Progress: func(s Stage) { stages = append(stages, s) }})
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
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{ProviderID: "omlx", ModelName: "New-4bit"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) || occ.IDs()[0] != "omlx/old" {
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
	err := start(context.Background(), fakeEnv(snap, true, &calls), &config.Config{}, Target{ProviderID: "mtplx", ModelName: "Org/New"},
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
	if err := start(context.Background(), fakeEnv(snap, false, &calls), &config.Config{}, Target{ProviderID: "ollama", ModelName: "b:2b"}, Options{}); err != nil {
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
	err := start(context.Background(), e, &config.Config{}, Target{ProviderID: "omlx", ModelName: "New-4bit"}, Options{AllowReplace: true})
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
		err := start(context.Background(), fakeEnv(localmodels.Snapshot{}, true, &calls), &config.Config{}, Target{ProviderID: id, ModelName: "x"}, Options{})
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
		{&OccupiedError{Occupants: []localmodels.Entry{{ModelID: "omlx/old"}}}, "omlx/old"},
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
// registered family. The map comparison is the structural half: it pins the
// registry's *contents* — which family maps to which backend — against an
// independent literal spelled out below, not merely its size, so the swap class
// it catches is a family whose entry was replaced by another with the same count
// (e.g. "mtplx": omlxBackend{}), the edit that would otherwise run mtplx's start
// through the omlx backend with nothing in the package noticing. Comparing
// against the other map could not see that: defaultEnv clones backendsByFamily,
// so both sides would move together. The per-family loop below is the behavioral
// half — a backend whose start can displace a running model (tenancy Exclusive,
// or a Pool wt has no sizes for) while Evictions reports nobody is exactly the
// state in which a running model gets replaced with no confirmation, the trap a
// hand-maintained family list leaves for the next backend.
//
// maps.Equal compares interface values with ==, so a backend struct that gained
// a non-comparable field (a slice, map or func) would make this guard panic
// rather than fail. All three backends are empty structs today, so it is safe;
// a new backend must stay comparable for this guard to work.
func TestOccupantDerivesSingleModelFromBackendRegistry(t *testing.T) {
	if !maps.Equal(defaultEnv().backends, map[string]backend{
		"ollama": ollamaBackend{},
		"omlx":   omlxBackend{},
		"mtplx":  mtplxBackend{},
	}) {
		t.Fatalf("defaultEnv's backends must be the registry wt can start: ollama, omlx and mtplx, each with its own backend")
	}
	for family, b := range backendsByFamily {
		snap := localmodels.Snapshot{Entries: []localmodels.Entry{running(family, family+"/occupant", "occupant")}}
		v, _ := Evictions(Target{ProviderID: family, ModelName: "wanted"}, snap)
		ok := len(v) > 0
		// The snapshot carries no pool reading, so a Pool cannot tell whether
		// the target fits and must name the running model, as Exclusive does.
		if ok != (b.tenancy() == Exclusive || b.tenancy() == Pool) {
			t.Errorf("family %s: Occupant reported an occupant=%v but tenancy()=%v", family, ok, b.tenancy())
		}
	}
}

// TestOccupantUnregisteredFamilyHasNoOccupant verifies Occupant reports no
// occupant for a family wt has no backend for, even when the snapshot carries a
// running model of that very family that would otherwise be the target's
// occupant. This is the arm that keeps a provider wt cannot start from reporting
// a phantom occupant, and it is the one arm Task 3's registry lookup introduced.
// "mlx_lm_server" is single-model in reality and deliberately has no backend
// entry: start would return *UnsupportedError, so there is nothing wt could
// replace. Do not "fix" that with a backend entry — the picker would then offer
// a start wt cannot perform.
func TestOccupantUnregisteredFamilyHasNoOccupant(t *testing.T) {
	for _, id := range []string{"ghost", "mlx_lm_server"} {
		snap := localmodels.Snapshot{Entries: []localmodels.Entry{
			running("omlx", "omlx/other", "other"),  // an unrelated family
			running(id, id+"/occupant", "occupant"), // this family: the occupant if the guard were gone
		}}
		occ, _ := Evictions(Target{ProviderID: id, ModelName: "wanted"}, snap)
		if len(occ) > 0 {
			t.Errorf("%s: Evictions = %+v, want no occupant — wt has no backend for this family, so there is nothing to replace", id, occ)
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

// TestStartRefusesWhenServerAnswersUnusably verifies a single-model provider
// whose /v1/models answers but cannot give a usable answer (here: 500 to
// everything) produces *OccupancyUnknownError and touches nothing. probe counts
// any HTTP response as "responded", so a server that is up while its model list
// errors is indeterminate rather than empty; reading it as "no occupant" would
// silently replace a running model, exactly like the stalled-listener case.
func TestStartRefusesWhenServerAnswersUnusably(t *testing.T) {
	unusable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer unusable.Close()

	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", unusable.URL)

	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var unknown *OccupancyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("start against a server that answers 500 = %v, want *OccupancyUnknownError", err)
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
	if !containsFold(occ.IDs()[0], "a") {
		t.Errorf("occupant = %q, want the model the server reports (a)", occ.IDs()[0])
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

// omlxListing is an omlx server whose /v1/models lists a and b — its whole
// pool, as omlx does — with loaded of them loaded according to /health, and a
// /v1/models/status that refuses to say which (the server has an API key wt
// was not given).
func omlxListing(t *testing.T, loaded int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"a"},{"id":"b"}]}`)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"engine_pool":{"model_count":2,"loaded_count":%d}}`, loaded)
	})
	mux.HandleFunc("/v1/models/status", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "API key required", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestStartIntoAnIdleOmlxThatListsItsPool pins #201 for the occupancy check:
// an omlx service that is up with nothing loaded still lists every model in
// its pool, and wt read the listed sibling as the occupant — so starting a
// model into an idle server asked to "replace" one that was never loaded, and
// with --replace stopped the service to do it. The live re-probe now asks what
// is loaded, finds nothing, and the start proceeds.
func TestStartIntoAnIdleOmlxThatListsItsPool(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", omlxListing(t, 0).URL)
	if err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{}); err != nil {
		t.Fatalf("start into an idle omlx = %v, want nil: a listed, unloaded model is not an occupant", err)
	}
	if !reflect.DeepEqual(calls, []string{"start:b"}) {
		t.Errorf("calls = %v, want [start:b]", calls)
	}
}

// TestStartAsksWhenOmlxWillNotSayWhatIsLoaded pins the other side: when omlx
// has one of its two models loaded and will not say which, the answer is
// unknown — *OccupancyUnknownError, touching nothing — not "empty". Guessing
// empty would start the target on top of a model that may be serving a
// session, which on omlx can evict it.
func TestStartAsksWhenOmlxWillNotSayWhatIsLoaded(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusPartial}}, true, &calls)
	cfg := provCfg("omlx", omlxListing(t, 1).URL)
	err := start(context.Background(), e, cfg, Target{ProviderID: "omlx", ModelName: "b"}, Options{})
	var unknown *OccupancyUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("start = %v, want *OccupancyUnknownError", err)
	}
	if len(calls) != 0 {
		t.Errorf("start touched the provider (%v) without knowing what it serves", calls)
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

// TestStartReprobesAServerTheSnapshotSawDown verifies the engine still asks
// the server itself when the snapshot's probe was refused. The plan reads a
// refused connection as "nothing serving" (evictions), which is right for a
// snapshot, but the engine acts on the answer: a server that came up with a
// model between the snapshot and the start must still be found, or the start
// replaces that model with no confirmation.
func TestStartReprobesAServerTheSnapshotSawDown(t *testing.T) {
	srv := modelsServing(t, "Org/M1")
	var calls []string
	e := fakeEnv(localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusPartial},
		Down:      map[string]bool{"mtplx": true},
	}, true, &calls)

	err := start(context.Background(), e, provCfg("mtplx", srv.URL), Target{ProviderID: "mtplx", ModelName: "Org/M2"}, Options{})
	var occ *OccupiedError
	if !errors.As(err, &occ) || !reflect.DeepEqual(occ.IDs(), []string{"Org/M1"}) {
		t.Fatalf("start = %v, want *OccupiedError naming the model the server is serving now", err)
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
	if v, _ := Evictions(Target{ProviderID: "omlx", ModelName: "b"}, snap); len(v) != 0 {
		t.Error("Occupant must not report an occupant from an untrustworthy probe")
	}
}

// TestStopUsesInjectedEnv verifies Stop's provider dispatch runs through the
// injectable env, mirroring Start/start. Without this seam the exported stop
// path — which replacement depends on — cannot be tested, so a stop that
// reports success while the model stays loaded would ship unnoticed.
func TestStopUsesInjectedEnv(t *testing.T) {
	var calls []string
	e := fakeEnv(localmodels.Snapshot{}, true, &calls)
	cfg := &config.Config{}

	if err := stop(context.Background(), e, cfg, "omlx"); err != nil {
		t.Fatalf("stop(omlx) = %v, want nil", err)
	}
	if !reflect.DeepEqual(calls, []string{"stop"}) {
		t.Errorf("calls = %v, want [stop]", calls)
	}

	err := stop(context.Background(), e, cfg, "ghost")
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Errorf("stop(ghost) = %v, want *UnsupportedError", err)
	}
}

// TestStartableMatchesBackendRegistry verifies Startable is true for exactly
// the families in backendsByFamily (including the omlx-6bit alias) and false
// for providers wt has no backend for. catalog.startable delegates to it, so
// this is the guard that a new backend cannot be registered yet still render
// as an unstartable row in the pickers.
func TestStartableMatchesBackendRegistry(t *testing.T) {
	for family := range backendsByFamily {
		if !Startable(family) {
			t.Errorf("Startable(%q) = false, but %q is in backendsByFamily", family, family)
		}
	}
	for id, want := range map[string]bool{
		"omlx-6bit": true, "mlx_lm_server": false, "anthropic": false, "": false,
	} {
		if got := Startable(id); got != want {
			t.Errorf("Startable(%q) = %v, want %v", id, got, want)
		}
	}
}

// TestProbeTrustedStatuses pins the exported probe-trust rule: a family the
// inventory never mentioned counts as trusted (nothing is being started into
// it, and hand-built snapshots carry no status map), StatusOK counts as trusted,
// and every other status does not. The stop picker calls this directly now, so a
// consumer that read it backwards would offer a model whose Running flag no
// probe ever confirmed — and on a single-model provider, stopping that row takes
// down whatever is actually loaded.
func TestProbeTrustedStatuses(t *testing.T) {
	// An absent key is trusted, and is the one case with no status to read —
	// asserted separately so the table below holds only rows that set a status.
	if got := ProbeTrusted(localmodels.Snapshot{}, "ollama"); !got {
		t.Error("absent key: ProbeTrusted = false, want true")
	}

	cases := []struct {
		name   string
		status localmodels.Status
		want   bool
	}{
		{"ok", localmodels.StatusOK, true},
		{"partial", localmodels.StatusPartial, false},
		{"unreachable", localmodels.StatusUnreachable, false},
	}
	for _, c := range cases {
		snap := localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": c.status}}
		if got := ProbeTrusted(snap, "ollama"); got != c.want {
			t.Errorf("%s: ProbeTrusted = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTenancyOfEachFamily pins how wt treats each provider's server. The
// start, stop and route rules all switch on it, so a wrong value either
// replaces a model that could have stayed or leaves a stopped one routed.
func TestTenancyOfEachFamily(t *testing.T) {
	for id, want := range map[string]Tenancy{
		"ollama": Shared, "mtplx": Exclusive, "omlx": Pool, "omlx-6bit": Pool,
		"mlx_lm_server": NoTenancy, "llamacpp": NoTenancy,
	} {
		if got := TenancyOf(id); got != want {
			t.Errorf("TenancyOf(%q) = %v, want %v", id, got, want)
		}
	}
}
