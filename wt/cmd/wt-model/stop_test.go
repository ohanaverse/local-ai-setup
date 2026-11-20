package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/hoststop"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// The stop tests and their helpers are copied from cmd/wt-session's
// model_cmds_test.go: `wt stop <provider>|--all` moved here as `wt-model
// stop`. The seams are fields of the host each test builds (testHost), not
// package vars.

// modelCmdConfig is a minimal local-only registry shared by the stop command
// tests: two ollama models and one omlx model, no agents needed.
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
	return cfg
}

func cand(provider, id, name string, sessions int) hoststop.Candidate {
	return hoststop.Candidate{
		Entry:    localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true},
		Sessions: sessions,
	}
}

// stubStop swaps h's stop seams: candidates is what is "running" under
// trusted probes, and the returned slice records every entry passed to the
// stop loop. The family-wide session counts are the candidates' own, as the
// real read gives for a pool whose sessions are all on running models.
func stubStop(t *testing.T, h *host, cands []hoststop.Candidate) *[]localmodels.Entry {
	t.Helper()
	fams := map[string]hoststop.FamilyState{}
	for _, c := range cands {
		if c.Sessions == 0 || lifecycle.TenancyOf(c.Entry.ProviderID) != lifecycle.Pool {
			continue
		}
		fam := localmodels.Family(c.Entry.ProviderID)
		fs := fams[fam]
		fs.Sessions += c.Sessions
		fs.Users = append(fs.Users, c.Entry.ModelID)
		fams[fam] = fs
	}
	return stubStopState(t, h, hoststop.StopState{Candidates: cands, Families: fams})
}

// stubStopState is stubStop for a test that sets the provider families' own
// state: an untrusted or refused probe, or sessions no candidate carries.
func stubStopState(t *testing.T, h *host, st hoststop.StopState) *[]localmodels.Entry {
	t.Helper()
	var stopped []localmodels.Entry
	h.stopState = func(*config.Config) hoststop.StopState { return st }
	h.stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
		stopped = append(stopped, es...)
		return nil
	}
	return &stopped
}

// stubHalt records every provider `wt-model stop` halts as a whole on h, and
// fails the ones named in fail.
func stubHalt(t *testing.T, h *host, fail ...string) *[]string {
	t.Helper()
	var halted []string
	h.stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		halted = append(halted, id)
		if slices.Contains(fail, id) {
			return errors.New("still listening")
		}
		return nil
	}
	return &halted
}

// runStopCmd runs `wt-model stop` with args, as the command, on h over cfg
// with no server record. It returns stdout and stderr as one text, and the
// command's error.
func runStopCmd(h *host, cfg *config.Config, args ...string) (string, error) {
	var out bytes.Buffer
	h.hostSet, h.hostCfg, h.hostLoadErr, h.hostCfgErr = true, cfg, nil, nil
	cmd := stopCmd(h, servestate.Store{})
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

// TestStopProviderStopsAllItsModels verifies a bare provider id stops every
// running model of that provider and leaves other providers alone; this is the
// "stop everything on ollama" shortcut.
func TestStopProviderStopsAllItsModels(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0), cand("omlx", "omlx/c", "c", 0)})
	if err := h.runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 2 {
		t.Fatalf("stopped = %v, want the two ollama models", *stopped)
	}
}

// TestStopInvalidArgErrors verifies an unknown provider, and a real provider
// wt has no stop backend for, each exit with an error and stop nothing. A
// typo must never silently succeed. (A model id is the model server's: wt
// stop <model> never reaches runStop.)
func TestStopInvalidArgErrors(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	cases := map[string]string{
		"bogus":         "unknown provider",
		"mlx_lm_server": "unknown provider", // real provider id, but wt has no stop backend
	}
	for arg, want := range cases {
		err := h.runStop(io.Discard, modelCmdConfig(), arg, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("h.runStop(%q) err = %v, want it to contain %q", arg, err, want)
		}
	}
	if len(*stopped) != 0 {
		t.Errorf("stopped = %v, want nothing", *stopped)
	}
}

// TestStopProviderNothingRunningIsNotAnError verifies `wt-model stop ollama` with no
// running ollama model prints a note and exits 0, so the command is idempotent.
func TestStopProviderNothingRunningIsNotAnError(t *testing.T) {
	h := testHost()
	stubStop(t, h, nil)
	var out bytes.Buffer
	if err := h.runStop(&out, modelCmdConfig(), "ollama", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing running") {
		t.Errorf("out = %q, want a nothing-running note", out.String())
	}
}

// TestStopInUseConfirms verifies stopping a provider whose model a live wt
// session uses asks first, declining aborts without stopping, accepting (or
// --yes) stops it.
// This is the safety net for stopping another session's model out from under it.
func TestStopInUseConfirms(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)})

	var asked string
	h.confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	if err := h.runStop(io.Discard, modelCmdConfig(), "ollama", false); err == nil {
		t.Fatal("declined confirm must return an error (non-zero exit)")
	}
	if !strings.Contains(asked, "2") || len(*stopped) != 0 {
		t.Fatalf("asked = %q stopped = %v, want a session-count prompt and no stop", asked, *stopped)
	}

	h.confirmStop = func(string) (bool, error) { return true, nil }
	if err := h.runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil || len(*stopped) != 1 {
		t.Fatalf("accepted: err = %v stopped = %v, want one stop", err, *stopped)
	}

	*stopped = nil
	h.confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := h.runStop(io.Discard, modelCmdConfig(), "ollama", true); err != nil || len(*stopped) != 1 {
		t.Fatalf("--yes: err = %v stopped = %v", err, *stopped)
	}
}

// TestStopFailurePropagates verifies a failed stop makes the command fail, so
// scripts can rely on the exit code.
func TestStopFailurePropagates(t *testing.T) {
	h := testHost()
	stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	h.stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	if err := h.runStop(io.Discard, modelCmdConfig(), "ollama", false); err == nil {
		t.Fatal("want the stop failure returned")
	}
}

// TestStopUnknownProviderListsConfiguredStoppable verifies the error for a bad
// bare provider names the providers the check actually accepts, derived from
// the same list — so the message cannot drift from the check when a stop
// backend is added.
func TestStopUnknownProviderListsConfiguredStoppable(t *testing.T) {
	h := testHost()
	err := h.runStop(io.Discard, modelCmdConfig(), "bogus", false)
	if err == nil || !strings.Contains(err.Error(), "valid: ollama, omlx") {
		t.Fatalf("err = %v, want it to list the configured stoppable providers", err)
	}
}

// TestStopUnknownProviderErrorsBeforeProbe verifies a typo'd provider is
// rejected before the live inventory probe (the stopState seam), so a bad
// argument costs nothing.
func TestStopUnknownProviderErrorsBeforeProbe(t *testing.T) {
	h := testHost()
	called := false
	h.stopState = func(*config.Config) hoststop.StopState { called = true; return hoststop.StopState{} }
	if err := h.runStop(io.Discard, &config.Config{}, "bogus", false); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("err = %v, want unknown provider", err)
	}
	if called {
		t.Fatal("stopState was probed before provider validation")
	}
}

// TestStopInUseCountIsTotalAcrossModels verifies the in-use prompt reports the
// real number of affected sessions: summed across models on a multi-model
// provider (1 + 3 = 4, not the max 3; an omlx pool counts per model the same
// way) and counted once for a single-model provider (mtplx), whose candidates
// each carry the family total. The prompt also names the in-use models so the
// user sees what dies.
func TestStopInUseCountIsTotalAcrossModels(t *testing.T) {
	h := testHost()
	var asked string
	h.confirmStop = func(q string) (bool, error) { asked = q; return false, nil }

	stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 1), cand("ollama", "ollama/b:1", "b:1", 3)})
	_ = h.runStop(io.Discard, modelCmdConfig(), "ollama", false)
	if !strings.Contains(asked, "4 live") || !strings.Contains(asked, "ollama/a:1") || !strings.Contains(asked, "ollama/b:1") {
		t.Errorf("ollama prompt = %q, want 4 sessions and both models named", asked)
	}

	cfg := modelCmdConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
	stubStop(t, h, []hoststop.Candidate{cand("mtplx", "mtplx/c", "c", 2), cand("mtplx", "mtplx/d", "d", 2)})
	_ = h.runStop(io.Discard, cfg, "mtplx", false)
	if !strings.Contains(asked, "2 live") {
		t.Errorf("mtplx prompt = %q, want the family total (2) counted once, not 4", asked)
	}

	stubStop(t, h, []hoststop.Candidate{cand("omlx", "omlx/c", "c", 1), cand("omlx", "omlx/d", "d", 2)})
	_ = h.runStop(io.Discard, modelCmdConfig(), "omlx", false)
	if !strings.Contains(asked, "3 live") || !strings.Contains(asked, "omlx/c") || !strings.Contains(asked, "omlx/d") {
		t.Errorf("omlx prompt = %q, want 3 sessions (each pool model carries its own count) and both models named", asked)
	}
}

// TestStopBareOmlxHaltsTheService verifies `wt-model stop omlx` stops the omlx
// service as a whole, even with nothing loaded. A per-model stop only unloads,
// so this is the one command that frees the server's memory.
func TestStopBareOmlxHaltsTheService(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, nil)
	halted := stubHalt(t, h)
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := h.runStop(&out, cfg, "omlx", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) || len(*stopped) != 0 {
		t.Errorf("halted = %v, per-model stops = %v; want [omlx] and none", *halted, *stopped)
	}
}

// stubHalt records every provider `wt-model stop` halts as a whole, and fails the
// ones named in fail.
// TestStopAllStopsEveryModelThenHaltsThePool verifies `wt-model stop --all` stops
// each running model of a non-pool provider through the stop loop and halts
// the omlx service once, without first unloading its models one by one. A
// user frees the machine with one command; unloading omlx's models would
// leave the server and its memory in place.
func TestStopAllStopsEveryModelThenHaltsThePool(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("omlx", "omlx/c", "c", 0)})
	halted := stubHalt(t, h)
	var out bytes.Buffer
	if err := h.runStopAll(&out, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" {
		t.Errorf("per-model stops = %v, want only ollama/a:1 (the omlx model goes down with its service)", *stopped)
	}
	if !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("halted = %v, want [omlx]", *halted)
	}
	if !strings.Contains(out.String(), "Stopping omlx... done") {
		t.Errorf("out = %q, want the halt reported", out.String())
	}
}

// TestStopAllHaltsOneServicePerPool verifies a registry with both an omlx
// and an omlx-6bit row halts the one omlx service once. Two halts would run
// `omlx stop` twice and print a second "Stopping omlx... done" for one
// service.
func TestStopAllHaltsOneServicePerPool(t *testing.T) {
	h := testHost()
	stubStop(t, h, nil)
	halted := stubHalt(t, h)
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "omlx", Location: config.LocationLocal}, {ID: "omlx-6bit", Location: config.LocationLocal},
	}}
	if err := h.runStopAll(io.Discard, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("halted = %v, want [omlx] once", *halted)
	}
}

// TestStopAllWithNothingToStop verifies `wt-model stop --all` on a machine with no
// running model and no pool provider says so and exits 0, so a cleanup script
// can call it unconditionally.
func TestStopAllWithNothingToStop(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, nil)
	halted := stubHalt(t, h)
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := h.runStopAll(&out, cfg, false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 0 || len(*halted) != 0 || !strings.Contains(out.String(), "no running local models") {
		t.Errorf("stopped = %v halted = %v out = %q; want nothing stopped and a note", *stopped, *halted, out.String())
	}
}

// TestStopAllInUseConfirms verifies `wt-model stop --all` asks once, with the total
// session count, before stopping models other wt sessions are using; declining
// stops nothing at all, and --yes skips the question. Without the question a
// cleanup in one terminal kills the agent running in another.
func TestStopAllInUseConfirms(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
	halted := stubHalt(t, h)

	var asked string
	h.confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	err := h.runStopAll(io.Discard, modelCmdConfig(), false)
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") || exitcode.Of(err) != 3 {
		t.Fatalf("declined: err = %v, want a cancellation", err)
	}
	if !strings.Contains(asked, "3 live wt session(s)") || !strings.Contains(asked, "ollama/a:1, omlx/c") {
		t.Errorf("asked = %q, want the total and both models", asked)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Fatalf("declined: stopped = %v halted = %v, want nothing", *stopped, *halted)
	}

	h.confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := h.runStopAll(io.Discard, modelCmdConfig(), true); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("--yes: stopped = %v halted = %v, want the ollama model and the omlx halt", *stopped, *halted)
	}
}

// TestStopAllKeepsGoingAfterAFailure verifies a failed model stop does not
// stop `wt-model stop --all` from halting the pool service, and that the command
// still exits non-zero naming what failed. "Stop everything" that gives up at
// the first failure leaves the largest server running.
func TestStopAllKeepsGoingAfterAFailure(t *testing.T) {
	h := testHost()
	stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	h.stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	halted := stubHalt(t, h, "omlx")
	var out bytes.Buffer
	err := h.runStopAll(&out, modelCmdConfig(), false)
	if err == nil || !strings.Contains(err.Error(), "1 of 1 stops failed") || !strings.Contains(err.Error(), "omlx: still listening") {
		t.Fatalf("err = %v, want both failures", err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) || !strings.Contains(out.String(), "Stopping omlx... failed") {
		t.Errorf("halted = %v out = %q, want the halt attempted and reported", *halted, out.String())
	}
}

// TestStopAllCancelledModelLoopLeavesThePoolUp verifies Ctrl+C during the
// per-model stops ends `wt-model stop --all` there: the omlx service is not halted,
// and the error says so. Filed as one more failure, the cancellation was
// stepped over and the command went on to halt the largest server the user
// had just asked it to leave alone.
func TestStopAllCancelledModelLoopLeavesThePoolUp(t *testing.T) {
	h := testHost()
	stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	h.stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return hoststop.ErrStopCancelled }
	halted := stubHalt(t, h)
	var out bytes.Buffer
	err := h.runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if len(*halted) != 0 || strings.Contains(out.String(), "Stopping omlx") {
		t.Errorf("halted = %v out = %q, want no halt after a cancelled model loop", *halted, out.String())
	}
}

// TestStopAllCancelledHaltReportsCancelled verifies Ctrl+C while the omlx
// service is being halted prints "cancelled", not "failed", and the error
// names the service as not stopped. A killed `omlx stop` reported as a failure
// tells the user their provider is broken when they interrupted it.
func TestStopAllCancelledHaltReportsCancelled(t *testing.T) {
	h := testHost()
	stubStop(t, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	h.stopProvider = func(c context.Context, _ *config.Config, _ string) error {
		cancel()
		return c.Err()
	}
	var out bytes.Buffer
	err := h.runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopping omlx... cancelled") || strings.Contains(got, "failed") {
		t.Errorf("out = %q, want the halt reported as cancelled, not failed", got)
	}
}

// TestStopAllSettlesTheModelLoopsRestartBeforeAHalt verifies `wt-model stop --all`
// waits for the LiteLLM proxy restart the model loop may have started before
// it halts the omlx service, and does not wait when there was no model loop.
// Halting omlx starts a restart of its own; nothing else orders the two, and
// two at once bounce the proxy under each other.
func TestStopAllSettlesTheModelLoopsRestartBeforeAHalt(t *testing.T) {
	h := testHost()
	var events []string
	h.stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		events = append(events, "models")
		return nil
	}
	h.stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		events = append(events, "halt "+id)
		return nil
	}
	h.waitPendingRoutes = func() { events = append(events, "wait") }

	h.stopState = func(*config.Config) hoststop.StopState {
		return hoststop.StopState{Candidates: []hoststop.Candidate{cand("mtplx", "mtplx/c", "c", 0)}}
	}
	if err := h.runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"models", "wait", "halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}

	events = nil
	h.stopState = func(*config.Config) hoststop.StopState { return hoststop.StopState{} }
	if err := h.runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("no model loop: events = %v, want %v (nothing to wait for)", events, want)
	}
}

// TestStopAllYesFlagSkipsTheQuestion verifies `wt-model stop --all --yes`, run as
// the command, stops in-use models without asking, and `wt-model stop --all` alone
// asks. The other --all tests call runStopAll with the flag's value already in
// hand; if the command stopped passing --yes, a cleanup script would block on
// a prompt (or fail with no terminal) and no test would say so.
func TestStopAllYesFlagSkipsTheQuestion(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
	halted := stubHalt(t, h)
	asked := stubStopConfirm(t, h, false)

	if _, err := runStopCmd(h, modelCmdConfig(), "--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 {
		t.Errorf("--all --yes asked %d time(s), want none", len(*asked))
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("--all --yes: stopped = %v halted = %v, want ollama/a:1 and the omlx halt", *stopped, *halted)
	}

	*stopped, *halted = nil, nil
	_, err := runStopCmd(h, modelCmdConfig(), "--all")
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") || exitcode.Of(err) != 3 {
		t.Fatalf("--all, declined: err = %v, want a cancellation", err)
	}
	if len(*asked) != 1 || len(*stopped) != 0 || len(*halted) != 0 {
		t.Errorf("--all: asked = %d stopped = %v halted = %v, want one question and nothing stopped", len(*asked), *stopped, *halted)
	}
}

// TestStopAllTakesNoArgument verifies `wt-model stop --all <something>` is refused
// before anything is probed or stopped: a user who meant one provider must
// not have every model stopped instead.
func TestStopAllTakesNoArgument(t *testing.T) {
	h := testHost()
	stopped := stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	halted := stubHalt(t, h)
	_, err := runStopCmd(h, modelCmdConfig(), "--all", "ollama")
	if err == nil || err.Error() != `wt-model stop --all takes no provider (got "ollama")` {
		t.Fatalf("err = %v, want the --all argument error", err)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Errorf("stopped = %v halted = %v, want nothing", *stopped, *halted)
	}
}

// TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp verifies a
// Ctrl+C the model loop did not report — it landed as the last model's stop
// completed, so the loop returned nil — still ends `wt-model stop --all` before the
// omlx halt. The loop's own signal context is gone once it returns; without
// one spanning the whole command that Ctrl+C is lost and the command goes on
// to halt the service the user had just asked it to leave alone.
func TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp(t *testing.T) {
	h := testHost()
	stubStop(t, h, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	// A signal reaches the contexts installed when it arrives and no later
	// one, as with signal.NotifyContext: each call gets a context of its own,
	// and the "Ctrl+C" below cancels only those that exist by then.
	var installed []context.CancelFunc
	h.startSignalCtx = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		installed = append(installed, cancel)
		return ctx, cancel
	}
	h.stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		for _, cancel := range installed {
			cancel()
		}
		return nil
	}
	halted := stubHalt(t, h)
	var out bytes.Buffer
	err := h.runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if len(*halted) != 0 || strings.Contains(out.String(), "Stopping omlx") {
		t.Errorf("halted = %v out = %q, want no halt after the Ctrl+C", *halted, out.String())
	}
}

// TestStopBareOmlxCancelledReportsCancelled verifies Ctrl+C during `wt-model stop
// omlx` prints "cancelled", not "failed", and the error says the service was
// not stopped — the same report `wt-model stop --all` gives for the same halt. A
// killed `omlx stop` reported as a failure tells the user their provider is
// broken when they interrupted it.
func TestStopBareOmlxCancelledReportsCancelled(t *testing.T) {
	h := testHost()
	stubStop(t, h, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	h.stopProvider = func(c context.Context, _ *config.Config, _ string) error {
		cancel()
		return c.Err()
	}
	var out bytes.Buffer
	err := h.runStop(&out, modelCmdConfig(), "omlx", true)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopping omlx... cancelled") || strings.Contains(got, "failed") {
		t.Errorf("out = %q, want the halt reported as cancelled, not failed", got)
	}
}

// TestStopAllFailurePrintsNoUsage verifies a `wt-model stop --all` that fails
// while stopping does not print the command's usage text, and on wt-model one
// given an argument does not either: its one error line says what is wrong. A
// failed halt is not a usage mistake: the usage block would push the progress
// lines that say what was and was not stopped off screen.
func TestStopAllFailurePrintsNoUsage(t *testing.T) {
	h := testHost()
	stubStop(t, h, nil)
	stubHalt(t, h, "omlx")

	got, err := runStopCmd(h, modelCmdConfig(), "--all")
	if err == nil || !strings.Contains(err.Error(), "omlx: still listening") {
		t.Fatalf("err = %v, want the failed halt", err)
	}
	if strings.Contains(got, "Usage:") || !strings.Contains(got, "Stopping omlx... failed") {
		t.Errorf("out = %q, want the progress line and no usage text", got)
	}

	got, err = runStopCmd(h, modelCmdConfig(), "--all", "ollama")
	if err == nil || err.Error() != `wt-model stop --all takes no provider (got "ollama")` || strings.Contains(got, "Usage:") {
		t.Errorf("err = %v out = %q, want the --all argument error and no usage text", err, got)
	}
}

// TestStopReachesAMachineInstanceTheRegistryPortDoesNotShow verifies `wt-model stop
// rec` and `wt-model stop --all` stop a rec instance the stop state found through
// rec's own readiness command (#457): started by hand on a port the registry
// does not name, it is a candidate on a family whose registry port refused,
// and neither command may read that refusal as "nothing running". Before the
// fix `wt-model stop rec` said so and `--all` left it up.
func TestStopReachesAMachineInstanceTheRegistryPortDoesNotShow(t *testing.T) {
	h := testHost()
	// Only rec: `--all` halts an omlx service whatever the state says.
	cfg := &config.Config{Providers: []config.Provider{{ID: "rec", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}}}
	found := cand("rec", config.DiscoveredModelID("rec", "flint"), "flint", 0)
	state := hoststop.StopState{
		Candidates: []hoststop.Candidate{found},
		Families:   map[string]hoststop.FamilyState{"rec": {Probed: true}},
	}
	for name, run := range map[string]func() error{
		"stop rec":   func() error { return h.runStop(io.Discard, cfg, "rec", false) },
		"stop --all": func() error { return h.runStopAll(io.Discard, cfg, true) },
	} {
		t.Run(name, func(t *testing.T) {
			stopped := stubStopState(t, h, state)
			if err := run(); err != nil {
				t.Fatalf("%s = %v, want nil", name, err)
			}
			if len(*stopped) != 1 || (*stopped)[0].ModelID != found.Entry.ModelID {
				t.Errorf("stopped = %v, want the rec instance", *stopped)
			}
		})
	}
}

// runStopOn runs `wt-model stop` with args on h over cfg, with rec as the
// server's runtime record, and returns stdout and stderr apart.
func runStopOn(h *host, cfg *config.Config, rec servestate.Store, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	h.hostSet, h.hostCfg, h.hostLoadErr, h.hostCfgErr = true, cfg, nil, nil
	cmd := stopCmd(h, rec)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestStopRefusesAModelID verifies `wt-model stop <provider>/<name>` and
// `--all` with one are refused before anything is probed or stopped, naming
// `wt stop <model>`: a model is the model server's to stop, from `wt`, and
// wt-model stops providers.
func TestStopRefusesAModelID(t *testing.T) {
	h := testHost()
	h.stopState = func(*config.Config) hoststop.StopState {
		t.Fatal("stopState was read for a model id")
		return hoststop.StopState{}
	}
	const want = `"omlx/q" is a model id: wt-model stop stops a provider or --all; stop one model with 'wt stop omlx/q'`
	for _, args := range [][]string{{"omlx/q"}, {"--all", "omlx/q"}} {
		_, err := runStopCmd(h, modelCmdConfig(), args...)
		if err == nil || err.Error() != want || exitcode.Of(err) != 1 {
			t.Errorf("%v: err = %v (exit %d), want %q, exit 1", args, err, exitcode.Of(err), want)
		}
	}
}

// TestStopNeedsAProviderOrAll verifies a bare `wt-model stop` names what it
// needs and probes nothing: wt-model has no picker, and stopping one model is
// `wt stop <model>`'s.
func TestStopNeedsAProviderOrAll(t *testing.T) {
	h := testHost()
	h.stopState = func(*config.Config) hoststop.StopState {
		t.Fatal("stopState was read with no argument")
		return hoststop.StopState{}
	}
	const want = "wt-model stop needs a provider or --all; stop one model with 'wt stop <model>'"
	if _, err := runStopCmd(h, modelCmdConfig()); err == nil || err.Error() != want {
		t.Errorf("err = %v, want %q", err, want)
	}
}

// TestStopAsksAboutSessionsTheRecordNames verifies the in-use question still
// fires with the running server's client sessions, read through serve's
// runtime record: a client session on ollama/a:1 makes `wt-model stop ollama`
// ask, and declining stops nothing (exit 3). Without the record the host stop
// would take a model down under a live session.
func TestStopAsksAboutSessionsTheRecordNames(t *testing.T) {
	srv, _ := fakeServer(t, "tok", modelapi.Row{ID: "ollama/a:1", Sessions: 2})
	store := recordFor(t, srv, "tok")
	h := testHost()
	stopped := stubStop(t, h, nil)
	h.stopState = func(*config.Config) hoststop.StopState {
		return hoststop.StopState{Candidates: []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", h.stopSessions()["ollama/a:1"])}}
	}
	asked := stubStopConfirm(t, h, false)
	_, _, err := runStopOn(h, modelCmdConfig(), store, "ollama")
	const want = "ollama is in use by 2 live wt session(s) (ollama/a:1); stop anyway?"
	if !slices.Equal(*asked, []string{want}) {
		t.Errorf("asked = %q\n want  [%q]", *asked, want)
	}
	if err == nil || exitcode.Of(err) != 3 {
		t.Errorf("err = %v (exit %d), want the decline, exit 3", err, exitcode.Of(err))
	}
	if len(*stopped) != 0 {
		t.Errorf("stopped = %v, want nothing", *stopped)
	}
}

// defaultStopStateHost is testHost with newHost's own stopState kept: the
// production read of the stop state, which hands the record's client
// sessions to hoststop. The struct is overwritten in place, so the closure
// newHost built still reads this host's stopSessions (the one `wt-model
// stop` sets). Every other seam is testHost's stub; the stop engines stay
// stubbed and TestMain isolates processes beneath them.
func defaultStopStateHost() *host {
	h := newHost()
	def := h.stopState
	*h = *testHost()
	h.stopState = def
	return h
}

// TestStopDefaultStateAsksAboutSessionsTheRecordNames is
// TestStopAsksAboutSessionsTheRecordNames through newHost's real stop state
// read: an httptest ollama with a:1 loaded, a record naming a server whose
// catalog gives ollama/a:1 two client sessions. The question must be asked;
// a default that dropped the record's sessions would stop a model in use
// without asking, and no stubbed stop state could show it.
func TestStopDefaultStateAsksAboutSessionsTheRecordNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags", "/api/ps":
			_, _ = io.WriteString(w, `{"models":[{"name":"a:1"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ollama.Close()
	srv, _ := fakeServer(t, "tok", modelapi.Row{ID: "ollama/a:1", Sessions: 2})
	store := recordFor(t, srv, "tok")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", ollama.URL, "")},
		Models:    []config.Model{{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal}},
	}
	h := defaultStopStateHost()
	var stopped []localmodels.Entry
	h.stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
		stopped = append(stopped, es...)
		return nil
	}
	asked := stubStopConfirm(t, h, false)
	_, _, err := runStopOn(h, cfg, store, "ollama")
	const want = "ollama is in use by 2 live wt session(s) (ollama/a:1); stop anyway?"
	if !slices.Equal(*asked, []string{want}) {
		t.Errorf("asked = %q\n want  [%q]", *asked, want)
	}
	if err == nil || exitcode.Of(err) != 3 {
		t.Errorf("err = %v (exit %d), want the decline, exit 3", err, exitcode.Of(err))
	}
	if len(stopped) != 0 {
		t.Errorf("stopped = %v, want nothing", stopped)
	}
}

// TestStopNotesAnUnreadableServerAndGoesOn verifies a record naming a server
// that no longer answers is one note line on stderr and the stop goes on
// without asking: the note says the stop cannot tell who uses what, and the
// command still does its job.
func TestStopNotesAnUnreadableServerAndGoesOn(t *testing.T) {
	srv, _ := fakeServer(t, "tok")
	store := recordFor(t, srv, "tok")
	srv.Close()
	h := testHost()
	stubStop(t, h, nil)
	inner := h.stopState
	h.stopState = func(cfg *config.Config) hoststop.StopState {
		h.stopSessions()
		return inner(cfg)
	}
	stdout, stderr, err := runStopOn(h, modelCmdConfig(), store, "ollama")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "wt-model: nothing running on ollama\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, noteHead) {
		t.Errorf("stderr = %q, want one %q line", stderr, noteHead)
	}
}

// TestStopRegistryRowErrorNamesTheRegistry runs `wt-model stop omlx` and
// `wt-model stop --all` against a registry whose rows are wrong and pins that
// the refusal sends the user to registry.toml. The rows parse, so this is not
// a load error, and it used to fall through to "run `wt config` to repair",
// which edits config.toml and cannot repair a provider or model row (#291).
// It is cmd/wt-session's test of the same refusal, moved with the command.
func TestStopRegistryRowErrorNamesTheRegistry(t *testing.T) {
	const provider = "[[providers]]\nid = \"omlx\"\nlocation = \"local\"\n\n"
	for name, c := range map[string]struct{ body, want string }{
		"model without model_name": {
			provider + "[[models]]\nid = \"omlx/m\"\nprovider_id = \"omlx\"\n",
			`model "omlx/m": model_name is required`,
		},
		"model with an unknown provider": {
			provider + "[[models]]\nid = \"ghost/m\"\nprovider_id = \"ghost\"\nmodel_name = \"m\"\n",
			`model "ghost/m": unknown provider "ghost"`,
		},
		"duplicate provider id": {provider + provider, `duplicate provider id "omlx"`},
		"model with no location": {
			"[[providers]]\nid = \"omlx\"\n\n[[models]]\nid = \"omlx/m\"\nprovider_id = \"omlx\"\nmodel_name = \"m\"\n",
			`model "omlx/m": no location on model or provider "omlx"`,
		},
	} {
		for _, args := range [][]string{{"stop", "omlx"}, {"stop", "--all"}} {
			t.Run(name+"/"+strings.Join(args, " "), func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", dir)
				t.Setenv("WT_REGISTRY", "")
				t.Setenv("MODELMAN_REGISTRY", "")
				litellmEnv(t, "model_list: []\n")
				registry := filepath.Join(dir, "local-ai", "registry.toml")
				if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(registry, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
				h := testHost()
				h.stopState = func(*config.Config) hoststop.StopState {
					t.Fatal("stopState was read over an invalid registry")
					return hoststop.StopState{}
				}
				root := rootCmdOver(fakeDeps, h, testRuntime(t))
				var out, errOut bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&errOut)
				root.SetArgs(args)
				err := root.Execute()
				if err == nil {
					t.Fatalf("wt-model %v ran on an invalid registry (stdout %q)", args, out.String())
				}
				want := "config error: " + c.want + " (fix the entry in " + registry + ")"
				if got := err.Error(); got != want {
					t.Errorf("wt-model %v:\n  %q\nwant\n  %q", args, got, want)
				}
			})
		}
	}
}

// TestStopCtrlCBeforeAnyStopStopsNothing verifies a Ctrl+C that lands while
// `wt-model stop` reads the client sessions or probes what is running (main
// runs the tree under signal.NotifyContext, so the first Ctrl+C only cancels
// the command's context) ends the command cancelled with nothing stopped:
// no model stop, no halt, no question, and no line saying a stop began.
// Read as "no sessions known", it used to stop models client sessions use
// without asking, after the user had asked to abort.
func TestStopCtrlCBeforeAnyStopStopsNothing(t *testing.T) {
	for _, args := range [][]string{{"ollama"}, {"omlx"}, {"--all"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := testHost()
			stopped := stubStop(t, h, nil)
			halted := stubHalt(t, h)
			asked := stubStopConfirm(t, h, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.stopState = func(*config.Config) hoststop.StopState {
				h.stopSessions()
				cancel()
				return hoststop.StopState{Candidates: []hoststop.Candidate{
					cand("ollama", "ollama/a:1", "a:1", 1),
					cand("omlx", "omlx/c", "c", 1),
				}}
			}
			var out, errOut bytes.Buffer
			h.hostSet, h.hostCfg, h.hostLoadErr, h.hostCfgErr = true, modelCmdConfig(), nil, nil
			cmd := stopCmd(h, servestate.Store{})
			cmd.SilenceErrors = true // as the root does: main prints the one error line
			cmd.SetArgs(args)
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			err := cmd.ExecuteContext(ctx)
			if !errors.Is(err, hoststop.ErrStopCancelled) || exitcode.Of(err) != 1 {
				t.Errorf("err = %v (exit %d), want the cancelled error, exit 1", err, exitcode.Of(err))
			}
			if err != nil && !strings.Contains(err.Error(), "nothing was stopped") {
				t.Errorf("err = %q, want it to say nothing was stopped", err)
			}
			if len(*stopped) != 0 || len(*halted) != 0 || len(*asked) != 0 {
				t.Errorf("stopped = %v, halted = %v, asked = %q; want none", *stopped, *halted, *asked)
			}
			if strings.Contains(out.String(), "Stopping") || errOut.Len() != 0 {
				t.Errorf("stdout = %q, stderr = %q; want no stop begun and no note", out.String(), errOut.String())
			}
		})
	}
}
