package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// modelCmdConfig is a minimal local-only registry shared by the start/stop
// command tests: two ollama models and one omlx model, no agents needed.
func modelCmdConfig() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
			{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "omlx/c", ProviderID: "omlx", ModelName: "c", Location: config.LocationLocal},
		},
	}
	cfg.ExposeAllForTest()
	return cfg
}

func cand(provider, id, name string, sessions int) survey.Candidate {
	return survey.Candidate{
		Entry:    localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true},
		Sessions: sessions,
	}
}

// stubStop swaps the stop seams for one test: candidates is what is "running",
// and the returned slice records every entry passed to the stop loop.
func stubStop(t *testing.T, cands []survey.Candidate) *[]localmodels.Entry {
	t.Helper()
	var stopped []localmodels.Entry
	oc, oe := stopCandidates, stopEntries
	stopCandidates = func(*config.Config) []survey.Candidate { return cands }
	stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
		stopped = append(stopped, es...)
		return nil
	}
	t.Cleanup(func() { stopCandidates, stopEntries = oc, oe })
	return &stopped
}

// TestStopModelIDStopsThatModel verifies `wt stop <provider>/<name>` stops
// exactly that running model and nothing else — the core contract of the command.
func TestStopModelIDStopsThatModel(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" {
		t.Fatalf("stopped = %v, want only ollama/a:1", *stopped)
	}
}

// TestStopProviderStopsAllItsModels verifies a bare provider id stops every
// running model of that provider and leaves other providers alone; this is the
// "stop everything on ollama" shortcut.
func TestStopProviderStopsAllItsModels(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0), cand("omlx", "omlx/c", "c", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 2 {
		t.Fatalf("stopped = %v, want the two ollama models", *stopped)
	}
}

// TestStopInvalidArgErrors verifies an unknown provider, an unknown model, and
// a registered-but-not-running model each exit with an error and stop nothing.
// A typo must never silently succeed.
func TestStopInvalidArgErrors(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	cases := map[string]string{
		"bogus":         "unknown provider",
		"ollama/nope:9": "unknown model",
		"ollama/b:1":    "not running",
		"mlx_lm_server": "unknown provider", // real provider id, but wt has no stop backend
	}
	for arg, want := range cases {
		err := runStop(io.Discard, modelCmdConfig(), arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runStop(%q) err = %v, want it to contain %q", arg, err, want)
		}
	}
	if len(*stopped) != 0 {
		t.Errorf("stopped = %v, want nothing", *stopped)
	}
}

// TestStopProviderNothingRunningIsNotAnError verifies `wt stop ollama` with no
// running ollama model prints a note and exits 0, so the command is idempotent.
func TestStopProviderNothingRunningIsNotAnError(t *testing.T) {
	stubStop(t, nil)
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing running") {
		t.Errorf("out = %q, want a nothing-running note", out.String())
	}
}

// TestStopInUseConfirms verifies stopping a model a live wt session uses asks
// first, declining aborts without stopping, accepting (or --yes) stops it.
// This is the safety net for stopping another session's model out from under it.
func TestStopInUseConfirms(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)})
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })

	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err == nil {
		t.Fatal("declined confirm must return an error (non-zero exit)")
	}
	if !strings.Contains(asked, "2") || len(*stopped) != 0 {
		t.Fatalf("asked = %q stopped = %v, want a session-count prompt and no stop", asked, *stopped)
	}

	confirmStop = func(string) (bool, error) { return true, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err != nil || len(*stopped) != 1 {
		t.Fatalf("accepted: err = %v stopped = %v, want one stop", err, *stopped)
	}

	*stopped = nil
	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", true); err != nil || len(*stopped) != 1 {
		t.Fatalf("--yes: err = %v stopped = %v", err, *stopped)
	}
}

// TestStopFailurePropagates verifies a failed stop makes the command fail, so
// scripts can rely on the exit code.
func TestStopFailurePropagates(t *testing.T) {
	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama/a:1", false); err == nil {
		t.Fatal("want the stop failure returned")
	}
}

// TestStopNoArgNeedsTTYAndOpensPicker verifies `wt stop` with no argument
// errors without a TTY, says so when nothing is running, and otherwise opens
// the in-use-inclusive picker (screen 2).
func TestStopNoArgNeedsTTYAndOpensPicker(t *testing.T) {
	oldTTY, oldPick := stdinTTY, stopPickerAll
	t.Cleanup(func() { stdinTTY, stopPickerAll = oldTTY, oldPick })
	opened := false
	stopPickerAll = func(*config.Config) { opened = true }

	stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	stdinTTY = func() bool { return false }
	if err := runStop(io.Discard, modelCmdConfig(), "", false); err == nil {
		t.Fatal("no TTY and no arg must error")
	}
	stdinTTY = func() bool { return true }
	if err := runStop(io.Discard, modelCmdConfig(), "", false); err != nil || !opened {
		t.Fatalf("err = %v opened = %v, want the picker opened", err, opened)
	}

	opened = false
	stubStop(t, nil)
	var out bytes.Buffer
	if err := runStop(&out, modelCmdConfig(), "", false); err != nil || opened || !strings.Contains(out.String(), "no running local models") {
		t.Fatalf("err = %v opened = %v out = %q, want the empty note and no picker", err, opened, out.String())
	}
}

// startFixture stubs the inventory: ollama/a:1 running, ollama/b:1 pulled but
// idle (start row), omlx/c missing from disk (blocked). Returns the start recorder.
func startFixture(t *testing.T) (*config.Config, *startRequest) {
	t.Helper()
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK, "omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", Artifact: "a:1", ModelID: "ollama/a:1", ModelName: "a:1", Registered: true, Running: true, ArtifactKnown: true},
			{ProviderID: "ollama", Artifact: "b:1", ModelID: "ollama/b:1", ModelName: "b:1", Registered: true, ArtifactKnown: true},
			{ProviderID: "omlx", Artifact: "", ModelID: "omlx/c", ModelName: "c", Registered: true, ArtifactKnown: true},
		},
	})
	return modelCmdConfig(), stubStartDriver(t, nil)
}

// TestStartRunningModelIsNoOp verifies `wt start` on an already-running model
// says so and never calls the start driver — starting twice must be harmless.
func TestStartRunningModelIsNoOp(t *testing.T) {
	cfg, req := startFixture(t)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "ollama/a:1", false); err != nil {
		t.Fatal(err)
	}
	if req.called || !strings.Contains(out.String(), "already running") {
		t.Fatalf("called = %v out = %q, want a no-op note", req.called, out.String())
	}
}

// TestStartIdleModelStartsIt verifies an idle local model goes through the
// start driver with the caller's --replace permission.
func TestStartIdleModelStartsIt(t *testing.T) {
	cfg, req := startFixture(t)
	if err := runStart(io.Discard, cfg, themes.Theme{}, "ollama/b:1", true); err != nil {
		t.Fatal(err)
	}
	if !req.called || req.row.Model.ID != "ollama/b:1" || !req.replace {
		t.Fatalf("req = %+v, want ollama/b:1 started with replace", req)
	}
}

// TestStartInvalidArgErrors verifies unknown ids, cloud ids, and blocked models
// exit with an error naming the problem and never start anything.
func TestStartInvalidArgErrors(t *testing.T) {
	cfg, req := startFixture(t)
	cfg.Models = append(cfg.Models, config.Model{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x", Location: config.LocationCloud})
	cases := map[string]string{"ollama/nope:9": "unknown model", "openrouter/x": "not a local model", "omlx/c": "not on disk"}
	for arg, want := range cases {
		err := runStart(io.Discard, cfg, themes.Theme{}, arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runStart(%q) err = %v, want %q", arg, err, want)
		}
	}
	if req.called {
		t.Error("nothing may be started for an invalid argument")
	}
}

// TestStartNoArgUsesPickerAndRunningPickIsNoOp verifies the no-arg flow needs a
// TTY, lists every local model via the shared picker, starts an idle pick, and
// treats a running pick as a no-op (spec: selecting a running model does nothing).
func TestStartNoArgUsesPickerAndRunningPickIsNoOp(t *testing.T) {
	cfg, req := startFixture(t)
	oldTTY, oldPick := stdinTTY, pickModelTUI
	t.Cleanup(func() { stdinTTY, pickModelTUI = oldTTY, oldPick })

	stdinTTY = func() bool { return false }
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err == nil {
		t.Fatal("no TTY and no arg must error")
	}

	stdinTTY = func() bool { return true }
	var offered []string
	pick := "ollama/b:1"
	pickModelTUI = func(_ *config.Config, models []config.Model, _ themes.Theme) (config.Model, bool, error) {
		for _, m := range models {
			offered = append(offered, m.ID)
		}
		return config.Model{ID: pick}, true, nil
	}
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err != nil || !req.called {
		t.Fatalf("idle pick: err = %v called = %v, want a start", err, req.called)
	}
	if len(offered) != 3 {
		t.Errorf("picker offered %v, want all three local models (running, idle, blocked)", offered)
	}

	*req = startRequest{}
	pick = "ollama/a:1"
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err != nil || req.called {
		t.Fatalf("running pick: err = %v called = %v, want a no-op", err, req.called)
	}
}

// TestStartCancelledPicker verifies canceling the picker (ok=false) is an
// error mentioning cancellation and starts nothing.
func TestStartCancelledPicker(t *testing.T) {
	cfg, req := startFixture(t)
	oldTTY, oldPick := stdinTTY, pickModelTUI
	t.Cleanup(func() { stdinTTY, pickModelTUI = oldTTY, oldPick })
	stdinTTY = func() bool { return true }
	pickModelTUI = func(*config.Config, []config.Model, themes.Theme) (config.Model, bool, error) {
		return config.Model{}, false, nil
	}
	if err := runStart(io.Discard, cfg, themes.Theme{}, "", false); err == nil || !strings.Contains(err.Error(), "canceled") || req.called {
		t.Fatalf("err = %v called = %v, want canceled error and no start", err, req.called)
	}
}
