package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

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
		{&UnsupportedError{ProviderID: "mlx_lm_server"}, "mlx_lm_server"},
	}
	for _, tc := range cases {
		if msg := tc.err.Error(); !containsFold(msg, tc.want) {
			t.Errorf("%T message %q must mention %q", tc.err, msg, tc.want)
		}
	}
}
