package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/hoststop"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	return cfg
}

func cand(provider, id, name string, sessions int) hoststop.Candidate {
	return hoststop.Candidate{
		Entry:    localmodels.Entry{ProviderID: provider, ModelID: id, ModelName: name, Running: true},
		Sessions: sessions,
	}
}

// stubStop swaps the stop seams for one test: candidates is what is "running"
// under trusted probes, and the returned slice records every entry passed to
// the stop loop. The family-wide session counts are the candidates' own, as
// the real read gives for a pool whose sessions are all on running models.
func stubStop(t *testing.T, cands []hoststop.Candidate) *[]localmodels.Entry {
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
	return stubStopState(t, hoststop.StopState{Candidates: cands, Families: fams})
}

// stubStopState is stubStop for a test that sets the provider families' own
// state: an untrusted or refused probe, or sessions no candidate carries.
func stubStopState(t *testing.T, st hoststop.StopState) *[]localmodels.Entry {
	t.Helper()
	var stopped []localmodels.Entry
	oldState, oldEntries := stopState, stopEntries
	stopState = func(*config.Config) hoststop.StopState { return st }
	stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
		stopped = append(stopped, es...)
		return nil
	}
	t.Cleanup(func() { stopState, stopEntries = oldState, oldEntries })
	return &stopped
}

// TestStopProviderStopsAllItsModels verifies a bare provider id stops every
// running model of that provider and leaves other providers alone; this is the
// "stop everything on ollama" shortcut.
func TestStopProviderStopsAllItsModels(t *testing.T) {
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("ollama", "ollama/b:1", "b:1", 0), cand("omlx", "omlx/c", "c", 0)})
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil {
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
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	cases := map[string]string{
		"bogus":         "unknown provider",
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

// TestStopInUseConfirms verifies stopping a provider whose model a live wt
// session uses asks first, declining aborts without stopping, accepting (or
// --yes) stops it.
// This is the safety net for stopping another session's model out from under it.
func TestStopInUseConfirms(t *testing.T) {
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)})
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })

	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err == nil {
		t.Fatal("declined confirm must return an error (non-zero exit)")
	}
	if !strings.Contains(asked, "2") || len(*stopped) != 0 {
		t.Fatalf("asked = %q stopped = %v, want a session-count prompt and no stop", asked, *stopped)
	}

	confirmStop = func(string) (bool, error) { return true, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err != nil || len(*stopped) != 1 {
		t.Fatalf("accepted: err = %v stopped = %v, want one stop", err, *stopped)
	}

	*stopped = nil
	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", true); err != nil || len(*stopped) != 1 {
		t.Fatalf("--yes: err = %v stopped = %v", err, *stopped)
	}
}

// TestStopFailurePropagates verifies a failed stop makes the command fail, so
// scripts can rely on the exit code.
func TestStopFailurePropagates(t *testing.T) {
	stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	if err := runStop(io.Discard, modelCmdConfig(), "ollama", false); err == nil {
		t.Fatal("want the stop failure returned")
	}
}

// TestStopUnknownProviderListsConfiguredStoppable verifies the error for a bad
// bare provider names the providers the check actually accepts, derived from
// the same list — so the message cannot drift from the check when a stop
// backend is added.
func TestStopUnknownProviderListsConfiguredStoppable(t *testing.T) {
	err := runStop(io.Discard, modelCmdConfig(), "bogus", false)
	if err == nil || !strings.Contains(err.Error(), "valid: ollama, omlx") {
		t.Fatalf("err = %v, want it to list the configured stoppable providers", err)
	}
}

// TestStopUnknownProviderErrorsBeforeProbe verifies a typo'd provider is
// rejected before the live inventory probe (the stopState seam), so a bad
// argument costs nothing.
func TestStopUnknownProviderErrorsBeforeProbe(t *testing.T) {
	old := stopState
	t.Cleanup(func() { stopState = old })
	called := false
	stopState = func(*config.Config) hoststop.StopState { called = true; return hoststop.StopState{} }
	if err := runStop(io.Discard, &config.Config{}, "bogus", false); err == nil || !strings.Contains(err.Error(), "unknown provider") {
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
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })
	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }

	stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 1), cand("ollama", "ollama/b:1", "b:1", 3)})
	_ = runStop(io.Discard, modelCmdConfig(), "ollama", false)
	if !strings.Contains(asked, "4 live") || !strings.Contains(asked, "ollama/a:1") || !strings.Contains(asked, "ollama/b:1") {
		t.Errorf("ollama prompt = %q, want 4 sessions and both models named", asked)
	}

	cfg := modelCmdConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
	stubStop(t, []hoststop.Candidate{cand("mtplx", "mtplx/c", "c", 2), cand("mtplx", "mtplx/d", "d", 2)})
	_ = runStop(io.Discard, cfg, "mtplx", false)
	if !strings.Contains(asked, "2 live") {
		t.Errorf("mtplx prompt = %q, want the family total (2) counted once, not 4", asked)
	}

	stubStop(t, []hoststop.Candidate{cand("omlx", "omlx/c", "c", 1), cand("omlx", "omlx/d", "d", 2)})
	_ = runStop(io.Discard, modelCmdConfig(), "omlx", false)
	if !strings.Contains(asked, "3 live") || !strings.Contains(asked, "omlx/c") || !strings.Contains(asked, "omlx/d") {
		t.Errorf("omlx prompt = %q, want 3 sessions (each pool model carries its own count) and both models named", asked)
	}
}

// omlxServer is a keyed omlx with two models in its pool and B loaded: /health
// gives the counts to anyone, /v1/models/status says which only to the key.
func omlxServer(t *testing.T, key string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"engine_pool":{"model_count":2,"loaded_count":1}}`))
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"A"},{"id":"B"}]}`))
		case "/v1/models/status":
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, `{"detail":"Invalid API key"}`, http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"id":"A","loaded":false},{"id":"B","loaded":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func omlxServedCfg(origin, secretRef string) *config.Config {
	return &config.Config{Providers: []config.Provider{{
		ID: "omlx", Location: config.LocationLocal,
		Auth: config.AuthConfig{Type: "api_key", SecretRef: secretRef, BaseURL: origin + "/v1"},
	}}}
}

// TestServedReportsWhatAKeyedOmlxHasLoaded pins the reason `wt served`
// exists: a partly loaded omlx pool names the loaded model only to a caller
// holding the server's key, and wt is the one that resolves the registry's
// secret_ref. A script whose own keyless probe is refused asks here.
func TestServedReportsWhatAKeyedOmlxHasLoaded(t *testing.T) {
	srv := omlxServer(t, "sk-omlx")
	cfg := omlxServedCfg(srv.URL, "sk-omlx")

	var out bytes.Buffer
	if err := runServed(&out, cfg, srv.Client(), "omlx", true); err != nil {
		t.Fatalf("runServed --json: %v", err)
	}
	if got, want := out.String(), `{"provider":"omlx","served":["B"]}`+"\n"; got != want {
		t.Errorf("json = %q, want %q", got, want)
	}

	out.Reset()
	if err := runServed(&out, cfg, srv.Client(), "omlx", false); err != nil {
		t.Fatalf("runServed: %v", err)
	}
	if got := out.String(); got != "B\n" {
		t.Errorf("plain = %q, want %q", got, "B\n")
	}
}

// TestRunServedSushiListsOnlyLoadedPacks pins `wt served sushi` for the real
// sushi family: sushi lists every pack it has, and only the loaded one is
// serving, so only that id is printed (plain, and as JSON); when none is
// loaded the JSON list is an empty array, never null, so a script reads
// "nothing serving" rather than a decode error. The server is an httptest
// listener the registry points at, never port 12345.
func TestRunServedSushiListsOnlyLoadedPacks(t *testing.T) {
	serve := func(body string) *config.Config {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		t.Cleanup(srv.Close)
		return &config.Config{Providers: []config.Provider{{
			ID: "sushi", Location: config.LocationLocal,
			Auth: config.AuthConfig{Type: "none", BaseURL: srv.URL + "/v1"},
		}}}
	}
	mixed := serve(`{"data":[{"id":"p/a","loaded":false,"state":"unloaded"},{"id":"p/b","loaded":true,"state":"ready"},{"id":"p/c","loaded":false,"state":"loading"}]}`)
	none := serve(`{"data":[{"id":"p/a","loaded":false,"state":"unloaded"},{"id":"p/c","loaded":false,"state":"loading"}]}`)
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		json bool
		want string
	}{
		{"plain, one loaded", mixed, false, "p/b\n"},
		{"json, one loaded", mixed, true, `{"provider":"sushi","served":["p/b"]}` + "\n"},
		{"json, none loaded", none, true, `{"provider":"sushi","served":[]}` + "\n"},
		{"plain, none loaded", none, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := runServed(&out, tc.cfg, http.DefaultClient, "sushi", tc.json); err != nil {
				t.Fatalf("runServed: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServedFailsRatherThanAnswerNothing: without the key the server will not
// say which model is loaded, and that must be an error — an empty list with
// exit 0 would read as "nothing is serving" and clear the flag of a model the
// server still has loaded.
func TestServedFailsRatherThanAnswerNothing(t *testing.T) {
	srv := omlxServer(t, "sk-omlx")

	var out bytes.Buffer
	err := runServed(&out, omlxServedCfg(srv.URL, ""), srv.Client(), "omlx", true)
	if err == nil || !strings.Contains(err.Error(), "omlx gave no usable answer") {
		t.Fatalf("err = %v, want a no-usable-answer error", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a failed probe, want nothing", out.String())
	}
}

// TestServedRefusesOllama: ollama has no "serving now" — a pulled model loads
// on request — so it is refused by name rather than answered with its pulls.
func TestServedRefusesOllama(t *testing.T) {
	var out bytes.Buffer
	err := runServed(&out, &config.Config{}, http.DefaultClient, "ollama", false)
	if err == nil || !strings.Contains(err.Error(), `unknown provider "ollama"`) {
		t.Fatalf("err = %v, want an unknown-provider error", err)
	}
}

// TestWarmSendsTheRegistryKeyToAKeyedOmlx pins `wt warm`, the step llmbench's
// omlx backend asks for when omlx refuses its keyless warmup (#256): the chat
// request carries the key the registry's omlx provider names, and names the
// model by its directory basename.
func TestWarmSendsTheRegistryKeyToAKeyedOmlx(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-omlx" {
			http.Error(w, `{"error":{"message":"API key required"}}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
			_, _ = w.Write([]byte(`{"object":"chat.completion"}`))
		}
	}))
	t.Cleanup(srv.Close)

	var out bytes.Buffer
	if err := runWarm(context.Background(), &out, omlxServedCfg(srv.URL, "sk-omlx"), "omlx", "org/Qwen-4bit"); err != nil {
		t.Fatalf("runWarm: %v", err)
	}
	if gotAuth != "Bearer sk-omlx" || !strings.Contains(gotBody, `"model":"Qwen-4bit"`) {
		t.Errorf("auth = %q body = %s", gotAuth, gotBody)
	}
	if got := out.String(); got != "org/Qwen-4bit is loaded on omlx\n" {
		t.Errorf("out = %q", got)
	}

	// Without the key the refusal is the error, with the setting to change.
	out.Reset()
	err := runWarm(context.Background(), &out, omlxServedCfg(srv.URL, ""), "omlx", "Qwen-4bit")
	if err == nil || !strings.Contains(err.Error(), "auth.secret_ref") {
		t.Fatalf("err = %v, want one naming auth.secret_ref", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q on a refused warm, want nothing", out.String())
	}
}

// TestWarmRefusesOtherProviders: the other local servers take no key, so
// there is nothing wt can add to their caller's own warmup.
func TestWarmRefusesOtherProviders(t *testing.T) {
	var out bytes.Buffer
	err := runWarm(context.Background(), &out, &config.Config{}, "mtplx", "m")
	if err == nil || !strings.Contains(err.Error(), `unknown provider "mtplx"`) {
		t.Fatalf("err = %v, want an unknown-provider error", err)
	}
}

// TestStopBareOmlxHaltsTheService verifies `wt stop omlx` stops the omlx
// service as a whole, even with nothing loaded. A per-model stop only unloads,
// so this is the one command that frees the server's memory.
func TestStopBareOmlxHaltsTheService(t *testing.T) {
	stopped := stubStop(t, nil)
	halted := stubHalt(t)
	cfg := &config.Config{Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := runStop(&out, cfg, "omlx", true); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) || len(*stopped) != 0 {
		t.Errorf("halted = %v, per-model stops = %v; want [omlx] and none", *halted, *stopped)
	}
}

// stubHalt records every provider `wt stop` halts as a whole, and fails the
// ones named in fail.
func stubHalt(t *testing.T, fail ...string) *[]string {
	t.Helper()
	var halted []string
	old := stopProvider
	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		halted = append(halted, id)
		if slices.Contains(fail, id) {
			return errors.New("still listening")
		}
		return nil
	}
	t.Cleanup(func() { stopProvider = old })
	return &halted
}

// runStopCmd runs `wt stop` with args, as the command, over cfg. It returns
// stdout and stderr as one text, and the command's error.
func runStopCmd(cfg *config.Config, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := stopCmd(&app{hostSet: true, hostCfg: cfg})
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

// TestStopAllStopsEveryModelThenHaltsThePool verifies `wt stop --all` stops
// each running model of a non-pool provider through the stop loop and halts
// the omlx service once, without first unloading its models one by one. A
// user frees the machine with one command; unloading omlx's models would
// leave the server and its memory in place.
func TestStopAllStopsEveryModelThenHaltsThePool(t *testing.T) {
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0), cand("omlx", "omlx/c", "c", 0)})
	halted := stubHalt(t)
	var out bytes.Buffer
	if err := runStopAll(&out, modelCmdConfig(), false); err != nil {
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
	stubStop(t, nil)
	halted := stubHalt(t)
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "omlx", Location: config.LocationLocal}, {ID: "omlx-6bit", Location: config.LocationLocal},
	}}
	if err := runStopAll(io.Discard, cfg, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("halted = %v, want [omlx] once", *halted)
	}
}

// TestStopAllWithNothingToStop verifies `wt stop --all` on a machine with no
// running model and no pool provider says so and exits 0, so a cleanup script
// can call it unconditionally.
func TestStopAllWithNothingToStop(t *testing.T) {
	stopped := stubStop(t, nil)
	halted := stubHalt(t)
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	var out bytes.Buffer
	if err := runStopAll(&out, cfg, false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 0 || len(*halted) != 0 || !strings.Contains(out.String(), "no running local models") {
		t.Errorf("stopped = %v halted = %v out = %q; want nothing stopped and a note", *stopped, *halted, out.String())
	}
}

// TestStopAllInUseConfirms verifies `wt stop --all` asks once, with the total
// session count, before stopping models other wt sessions are using; declining
// stops nothing at all, and --yes skips the question. Without the question a
// cleanup in one terminal kills the agent running in another.
func TestStopAllInUseConfirms(t *testing.T) {
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
	halted := stubHalt(t)
	old := confirmStop
	t.Cleanup(func() { confirmStop = old })

	var asked string
	confirmStop = func(q string) (bool, error) { asked = q; return false, nil }
	err := runStopAll(io.Discard, modelCmdConfig(), false)
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") || exitcode.Of(err) != 3 {
		t.Fatalf("declined: err = %v, want a cancellation", err)
	}
	if !strings.Contains(asked, "3 live wt session(s)") || !strings.Contains(asked, "ollama/a:1, omlx/c") {
		t.Errorf("asked = %q, want the total and both models", asked)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Fatalf("declined: stopped = %v halted = %v, want nothing", *stopped, *halted)
	}

	confirmStop = func(string) (bool, error) { t.Fatal("--yes must not prompt"); return false, nil }
	if err := runStopAll(io.Discard, modelCmdConfig(), true); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("--yes: stopped = %v halted = %v, want the ollama model and the omlx halt", *stopped, *halted)
	}
}

// TestStopAllKeepsGoingAfterAFailure verifies a failed model stop does not
// stop `wt stop --all` from halting the pool service, and that the command
// still exits non-zero naming what failed. "Stop everything" that gives up at
// the first failure leaves the largest server running.
func TestStopAllKeepsGoingAfterAFailure(t *testing.T) {
	stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return errors.New("1 of 1 stops failed") }
	halted := stubHalt(t, "omlx")
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if err == nil || !strings.Contains(err.Error(), "1 of 1 stops failed") || !strings.Contains(err.Error(), "omlx: still listening") {
		t.Fatalf("err = %v, want both failures", err)
	}
	if !slices.Equal(*halted, []string{"omlx"}) || !strings.Contains(out.String(), "Stopping omlx... failed") {
		t.Errorf("halted = %v out = %q, want the halt attempted and reported", *halted, out.String())
	}
}

// TestStopAllCancelledModelLoopLeavesThePoolUp verifies Ctrl+C during the
// per-model stops ends `wt stop --all` there: the omlx service is not halted,
// and the error says so. Filed as one more failure, the cancellation was
// stepped over and the command went on to halt the largest server the user
// had just asked it to leave alone.
func TestStopAllCancelledModelLoopLeavesThePoolUp(t *testing.T) {
	stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	old := stopEntries
	t.Cleanup(func() { stopEntries = old })
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error { return hoststop.ErrStopCancelled }
	halted := stubHalt(t)
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
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
	stubStop(t, nil)
	oldCtx, oldStop := startSignalCtx, stopProvider
	t.Cleanup(func() { startSignalCtx, stopProvider = oldCtx, oldStop })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	stopProvider = func(c context.Context, _ *config.Config, _ string) error {
		cancel()
		return c.Err()
	}
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopping omlx... cancelled") || strings.Contains(got, "failed") {
		t.Errorf("out = %q, want the halt reported as cancelled, not failed", got)
	}
}

// TestStopAllSettlesTheModelLoopsRestartBeforeAHalt verifies `wt stop --all`
// waits for the LiteLLM proxy restart the model loop may have started before
// it halts the omlx service, and does not wait when there was no model loop.
// Halting omlx starts a restart of its own; nothing else orders the two, and
// two at once bounce the proxy under each other.
func TestStopAllSettlesTheModelLoopsRestartBeforeAHalt(t *testing.T) {
	var events []string
	oldState, oldEntries, oldHalt, oldWait := stopState, stopEntries, stopProvider, waitPendingRoutes
	t.Cleanup(func() {
		stopState, stopEntries, stopProvider, waitPendingRoutes = oldState, oldEntries, oldHalt, oldWait
	})
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		events = append(events, "models")
		return nil
	}
	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		events = append(events, "halt "+id)
		return nil
	}
	waitPendingRoutes = func() { events = append(events, "wait") }

	stopState = func(*config.Config) hoststop.StopState {
		return hoststop.StopState{Candidates: []hoststop.Candidate{cand("mtplx", "mtplx/c", "c", 0)}}
	}
	if err := runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"models", "wait", "halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}

	events = nil
	stopState = func(*config.Config) hoststop.StopState { return hoststop.StopState{} }
	if err := runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("no model loop: events = %v, want %v (nothing to wait for)", events, want)
	}
}

// TestStopAllYesFlagSkipsTheQuestion verifies `wt stop --all --yes`, run as
// the command, stops in-use models without asking, and `wt stop --all` alone
// asks. The other --all tests call runStopAll with the flag's value already in
// hand; if the command stopped passing --yes, a cleanup script would block on
// a prompt (or fail with no terminal) and no test would say so.
func TestStopAllYesFlagSkipsTheQuestion(t *testing.T) {
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 2), cand("omlx", "omlx/c", "c", 1)})
	halted := stubHalt(t)
	asked := stubStopConfirm(t, false)

	if _, err := runStopCmd(modelCmdConfig(), "--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 {
		t.Errorf("--all --yes asked %d time(s), want none", len(*asked))
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "ollama/a:1" || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("--all --yes: stopped = %v halted = %v, want ollama/a:1 and the omlx halt", *stopped, *halted)
	}

	*stopped, *halted = nil, nil
	_, err := runStopCmd(modelCmdConfig(), "--all")
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") || exitcode.Of(err) != 3 {
		t.Fatalf("--all, declined: err = %v, want a cancellation", err)
	}
	if len(*asked) != 1 || len(*stopped) != 0 || len(*halted) != 0 {
		t.Errorf("--all: asked = %d stopped = %v halted = %v, want one question and nothing stopped", len(*asked), *stopped, *halted)
	}
}

// TestStopAllTakesNoArgument verifies `wt stop --all <something>` is refused
// before anything is probed or stopped: a user who meant one provider must
// not have every model stopped instead.
func TestStopAllTakesNoArgument(t *testing.T) {
	stopped := stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	halted := stubHalt(t)
	_, err := runStopCmd(modelCmdConfig(), "--all", "ollama")
	if err == nil || !strings.Contains(err.Error(), "takes no model or provider") {
		t.Fatalf("err = %v, want the --all usage error", err)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Errorf("stopped = %v halted = %v, want nothing", *stopped, *halted)
	}
}

// TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp verifies a
// Ctrl+C the model loop did not report — it landed as the last model's stop
// completed, so the loop returned nil — still ends `wt stop --all` before the
// omlx halt. The loop's own signal context is gone once it returns; without
// one spanning the whole command that Ctrl+C is lost and the command goes on
// to halt the service the user had just asked it to leave alone.
func TestStopAllCtrlCAsTheLastModelStopCompletesLeavesThePoolUp(t *testing.T) {
	stubStop(t, []hoststop.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
	oldCtx, oldEntries := startSignalCtx, stopEntries
	t.Cleanup(func() { startSignalCtx, stopEntries = oldCtx, oldEntries })
	// A signal reaches the contexts installed when it arrives and no later
	// one, as with signal.NotifyContext: each call gets a context of its own,
	// and the "Ctrl+C" below cancels only those that exist by then.
	var installed []context.CancelFunc
	startSignalCtx = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		installed = append(installed, cancel)
		return ctx, cancel
	}
	stopEntries = func(io.Writer, *config.Config, []localmodels.Entry) error {
		for _, cancel := range installed {
			cancel()
		}
		return nil
	}
	halted := stubHalt(t)
	var out bytes.Buffer
	err := runStopAll(&out, modelCmdConfig(), false)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if len(*halted) != 0 || strings.Contains(out.String(), "Stopping omlx") {
		t.Errorf("halted = %v out = %q, want no halt after the Ctrl+C", *halted, out.String())
	}
}

// TestStopBareOmlxCancelledReportsCancelled verifies Ctrl+C during `wt stop
// omlx` prints "cancelled", not "failed", and the error says the service was
// not stopped — the same report `wt stop --all` gives for the same halt. A
// killed `omlx stop` reported as a failure tells the user their provider is
// broken when they interrupted it.
func TestStopBareOmlxCancelledReportsCancelled(t *testing.T) {
	stubStop(t, nil)
	oldCtx, oldStop := startSignalCtx, stopProvider
	t.Cleanup(func() { startSignalCtx, stopProvider = oldCtx, oldStop })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	stopProvider = func(c context.Context, _ *config.Config, _ string) error {
		cancel()
		return c.Err()
	}
	var out bytes.Buffer
	err := runStop(&out, modelCmdConfig(), "omlx", true)
	if !errors.Is(err, hoststop.ErrStopCancelled) || !strings.Contains(err.Error(), "omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming omlx as not stopped", err)
	}
	if got := out.String(); !strings.Contains(got, "Stopping omlx... cancelled") || strings.Contains(got, "failed") {
		t.Errorf("out = %q, want the halt reported as cancelled, not failed", got)
	}
}

// TestStopAllFailurePrintsNoUsage verifies a `wt stop --all` that fails while
// stopping does not print the command's usage text, and one given an argument
// still does. A failed halt is not a usage mistake: the usage block would
// push the progress lines that say what was and was not stopped off screen.
func TestStopAllFailurePrintsNoUsage(t *testing.T) {
	stubStop(t, nil)
	stubHalt(t, "omlx")

	got, err := runStopCmd(modelCmdConfig(), "--all")
	if err == nil || !strings.Contains(err.Error(), "omlx: still listening") {
		t.Fatalf("err = %v, want the failed halt", err)
	}
	if strings.Contains(got, "Usage:") || !strings.Contains(got, "Stopping omlx... failed") {
		t.Errorf("out = %q, want the progress line and no usage text", got)
	}

	got, err = runStopCmd(modelCmdConfig(), "--all", "ollama")
	if err == nil || !strings.Contains(got, "Usage:") {
		t.Errorf("err = %v out = %q, want the usage text for an argument mistake", err, got)
	}
}

// TestNoHelpTextNamesModelman pins success criterion 1 of the retirement for
// wt's own help: no command's help may send a reader to modelman, which no
// longer exists. It walks the whole command tree, so a command added later is
// covered too. For each command it reads the short line, the long text, the
// examples, the deprecation notice and the usage text, and then every flag's
// description and deprecation notice one by one: the usage text alone leaves
// out the command's own short line and every hidden flag. The flags of a
// parent that a subcommand inherits (root's persistent ones) are read at the
// parent, where they are declared. The check is for
// the lowercase tool name: the env alias names (MODELMAN_REGISTRY and the
// three MODELMAN_LITELLM_ ones) are uppercase, are read forever, and may be
// named.
func TestNoHelpTextNamesModelman(t *testing.T) {
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		seen++
		for _, text := range []string{c.Short, c.Long, c.Example, c.Deprecated, c.UsageString()} {
			if strings.Contains(text, "modelman") {
				t.Errorf("%s: help names modelman:\n%s", c.CommandPath(), text)
			}
		}
		checkFlags := func(f *pflag.Flag) {
			if strings.Contains(f.Usage+f.Deprecated, "modelman") {
				t.Errorf("%s --%s: flag help names modelman: %s", c.CommandPath(), f.Name, f.Usage)
			}
		}
		// c.Flags() takes in the parent's persistent flags only when they are
		// merged for it — a side effect of resolving the usage template the
		// text loop above happened to trigger, not anything it promised.
		// Reading the command's own flags, local and persistent, directly
		// keeps the coverage from depending on that rendering; a flag both
		// sets hold after a merge may then be named twice, as two failures.
		c.Flags().VisitAll(checkFlags)
		c.PersistentFlags().VisitAll(checkFlags)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmdWith(testHooks()))
	if seen < 20 {
		t.Fatalf("walked %d commands, want the whole tree (wt has more than 20)", seen)
	}
}

// TestHelpNamesSushiWhereItListsProviders pins the three help texts that
// enumerate the providers wt seeds or stops (`wt model init`, `wt model add`
// and `wt stop`): each must name sushi. Without it a built-in provider added
// later is silently missing from the help the way sushi was.
func TestHelpNamesSushiWhereItListsProviders(t *testing.T) {
	root := rootCmdWith(testHooks())
	for _, path := range [][]string{{"model", "init"}, {"model", "add"}, {"stop"}} {
		c, _, err := root.Find(path)
		if err != nil || c == nil || c.Name() != path[len(path)-1] {
			t.Fatalf("command %v not found: %v", path, err)
		}
		if !strings.Contains(c.Long, "sushi") {
			t.Errorf("%s: Long does not name sushi:\n%s", c.CommandPath(), c.Long)
		}
	}
}

// TestStopReachesAMachineInstanceTheRegistryPortDoesNotShow verifies `wt stop
// rec` and `wt stop --all` stop a rec instance the stop state found through
// rec's own readiness command (#457): started by hand on a port the registry
// does not name, it is a candidate on a family whose registry port refused,
// and neither command may read that refusal as "nothing running". Before the
// fix `wt stop rec` said so and `--all` left it up.
func TestStopReachesAMachineInstanceTheRegistryPortDoesNotShow(t *testing.T) {
	// Only rec: `--all` halts an omlx service whatever the state says.
	cfg := &config.Config{Providers: []config.Provider{{ID: "rec", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}}}
	found := cand("rec", config.DiscoveredModelID("rec", "flint"), "flint", 0)
	state := hoststop.StopState{
		Candidates: []hoststop.Candidate{found},
		Families:   map[string]hoststop.FamilyState{"rec": {Probed: true}},
	}
	for name, run := range map[string]func() error{
		"stop rec":   func() error { return runStop(io.Discard, cfg, "rec", false) },
		"stop --all": func() error { return runStopAll(io.Discard, cfg, true) },
	} {
		t.Run(name, func(t *testing.T) {
			stopped := stubStopState(t, state)
			if err := run(); err != nil {
				t.Fatalf("%s = %v, want nil", name, err)
			}
			if len(*stopped) != 1 || (*stopped)[0].ModelID != found.Entry.ModelID {
				t.Errorf("stopped = %v, want the rec instance", *stopped)
			}
		})
	}
}
