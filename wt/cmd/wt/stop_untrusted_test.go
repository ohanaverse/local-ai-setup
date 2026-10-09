package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// Tests for `wt stop` when a provider's probe gave no trusted answer, or no
// candidate (#307, #308). The stop state is stubbed through stubStopState, so
// each test names the probe outcome it is about.

// stopCfg is a registry with one provider of each tenancy: ollama (Shared),
// mtplx (Exclusive) and omlx (Pool).
func stopCfg() *config.Config {
	cfg := modelCmdConfig()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}})
	cfg.Models = append(cfg.Models, config.Model{ID: "mtplx/big", ProviderID: "mtplx", ModelName: "big", Location: config.LocationLocal})
	return cfg
}

// mtplxOnlyCfg has the one Exclusive provider and nothing else, so a command
// that halts nothing has nothing else to report.
func mtplxOnlyCfg() *config.Config {
	return &config.Config{Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal}}}
}

// families is the stop state of one probed family and no candidate.
func families(fam string, fs survey.FamilyState) survey.StopState {
	fs.Probed = true
	return survey.StopState{Families: map[string]survey.FamilyState{fam: fs}}
}

// stubStopConfirm records the questions `wt stop` asks and answers each with ok.
func stubStopConfirm(t *testing.T, ok bool) *[]string {
	t.Helper()
	var asked []string
	old := confirmStop
	confirmStop = func(q string) (bool, error) { asked = append(asked, q); return ok, nil }
	t.Cleanup(func() { confirmStop = old })
	return &asked
}

// TestStopExclusiveProviderIsHaltedWithoutACandidate verifies `wt stop mtplx`
// halts mtplx when its probe produced no candidate but its port did not
// refuse: the server answered with nothing loaded, or gave no usable answer
// in time. It used to print "nothing running on mtplx" and
// exit 0 with a 28GB server still up (#308).
func TestStopExclusiveProviderIsHaltedWithoutACandidate(t *testing.T) {
	for name, fs := range map[string]survey.FamilyState{
		"answering, nothing loaded": {},
		"no usable answer":          {Untrusted: true},
	} {
		t.Run(name, func(t *testing.T) {
			stopped := stubStopState(t, families("mtplx", fs))
			halted := stubHalt(t)
			asked := stubStopConfirm(t, false)
			var out bytes.Buffer
			if err := runStop(&out, stopCfg(), "mtplx", false); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*halted, []string{"mtplx"}) || len(*stopped) != 0 {
				t.Errorf("halted = %v per-model stops = %v, want mtplx halted as a whole", *halted, *stopped)
			}
			if out.String() != "Stopping mtplx... done\n" {
				t.Errorf("out = %q, want the halt reported and no nothing-running note", out.String())
			}
			if len(*asked) != 0 {
				t.Errorf("asked = %q, want no question when no live session uses mtplx", *asked)
			}
		})
	}
}

// TestStopExclusiveProviderWhosePortRefusesIsNotRunning verifies `wt stop
// mtplx` and `wt stop --all` leave mtplx alone and say nothing is running when
// its port actively refused the probe and no pidfile names a server behind
// it: that is the one untrusted answer known to mean "nothing to stop", so
// the commands stay idempotent on a machine where mtplx is simply off.
func TestStopExclusiveProviderWhosePortRefusesIsNotRunning(t *testing.T) {
	stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true, Down: true}))
	halted := stubHalt(t)
	var out bytes.Buffer
	if err := runStop(&out, mtplxOnlyCfg(), "mtplx", false); err != nil {
		t.Fatal(err)
	}
	if len(*halted) != 0 || out.String() != "wt: nothing running on mtplx\n" {
		t.Errorf("wt stop mtplx: halted = %v out = %q, want no halt and the nothing-running note", *halted, out.String())
	}
	out.Reset()
	if err := runStopAll(&out, mtplxOnlyCfg(), false); err != nil {
		t.Fatal(err)
	}
	if len(*halted) != 0 || out.String() != "wt: no running local models\n" {
		t.Errorf("wt stop --all: halted = %v out = %q, want no halt and the no-running note", *halted, out.String())
	}
}

// TestStopExclusiveProviderHaltFailureExitsNonZero verifies a halt of mtplx
// that fails — the port still answers after `mtplx stop`, or the binary is
// missing — is printed as failed and returned, for `wt stop mtplx` and `wt
// stop --all`. The commands must never exit 0 over a server they could not
// stop.
func TestStopExclusiveProviderHaltFailureExitsNonZero(t *testing.T) {
	stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true}))
	halted := stubHalt(t, "mtplx")
	var out bytes.Buffer
	err := runStop(&out, stopCfg(), "mtplx", false)
	if err == nil || !strings.Contains(err.Error(), "still listening") || !strings.Contains(out.String(), "Stopping mtplx... failed") {
		t.Fatalf("wt stop mtplx: err = %v out = %q, want the failure returned and printed", err, out.String())
	}
	*halted = nil
	out.Reset()
	err = runStopAll(&out, stopCfg(), false)
	if err == nil || !strings.Contains(err.Error(), "mtplx: still listening") {
		t.Fatalf("wt stop --all: err = %v, want mtplx's failure named", err)
	}
	if !slices.Equal(*halted, []string{"mtplx", "omlx"}) || !strings.Contains(out.String(), "Stopping omlx... done") {
		t.Errorf("halted = %v out = %q, want omlx still halted after mtplx failed", *halted, out.String())
	}
}

// TestStopExclusiveProviderWithACandidateStopsThroughTheModelLoop pins the
// trusted case: an mtplx model the probe confirmed running is stopped through
// the model loop, as before, and mtplx is not halted a second time — by `wt
// stop mtplx` or `wt stop --all`.
func TestStopExclusiveProviderWithACandidateStopsThroughTheModelLoop(t *testing.T) {
	stopped := stubStop(t, []survey.Candidate{cand("mtplx", "mtplx/big", "big", 0)})
	halted := stubHalt(t)
	if err := runStop(io.Discard, stopCfg(), "mtplx", false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || (*stopped)[0].ModelID != "mtplx/big" || len(*halted) != 0 {
		t.Fatalf("wt stop mtplx: stopped = %v halted = %v, want the model stopped and no halt", *stopped, *halted)
	}
	*stopped = nil
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() {}
	t.Cleanup(func() { waitPendingRoutes = oldWait })
	if err := runStopAll(io.Discard, stopCfg(), false); err != nil {
		t.Fatal(err)
	}
	if len(*stopped) != 1 || !slices.Equal(*halted, []string{"omlx"}) {
		t.Errorf("wt stop --all: stopped = %v halted = %v, want the mtplx model stopped and only omlx halted", *stopped, *halted)
	}
}

// TestStopAllHaltsAnExclusiveProviderWithoutACandidate verifies `wt stop
// --all` halts mtplx when its probe produced no candidate and its port did not
// refuse, before the omlx service, and does not print "no running local
// models" on a registry with nothing else. "Stop all" left the largest server
// up and said nothing was running (#308).
func TestStopAllHaltsAnExclusiveProviderWithoutACandidate(t *testing.T) {
	stopped := stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true}))
	halted := stubHalt(t)
	var out bytes.Buffer
	if err := runStopAll(&out, stopCfg(), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"mtplx", "omlx"}) || len(*stopped) != 0 {
		t.Errorf("halted = %v per-model stops = %v, want mtplx then omlx halted", *halted, *stopped)
	}
	if out.String() != "Stopping mtplx... done\nStopping omlx... done\n" {
		t.Errorf("out = %q, want both halts reported in order", out.String())
	}

	*halted = nil
	out.Reset()
	if err := runStopAll(&out, mtplxOnlyCfg(), false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*halted, []string{"mtplx"}) || out.String() != "Stopping mtplx... done\n" {
		t.Errorf("mtplx only: halted = %v out = %q, want the halt and no no-running note", *halted, out.String())
	}
}

// TestStopAllCancelledExclusiveHaltLeavesThePoolUp verifies Ctrl+C during the
// halt of mtplx ends `wt stop --all` there: it is printed as cancelled, omlx
// is not halted, and the error names both as not stopped. The halt of an
// Exclusive provider runs under the command's one signal context, like a
// pool's.
func TestStopAllCancelledExclusiveHaltLeavesThePoolUp(t *testing.T) {
	stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true}))
	oldCtx, oldStop := startSignalCtx, stopProvider
	t.Cleanup(func() { startSignalCtx, stopProvider = oldCtx, oldStop })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	var halted []string
	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		halted = append(halted, id)
		cancel()
		return context.Canceled
	}
	var out bytes.Buffer
	err := runStopAll(&out, stopCfg(), false)
	if !errors.Is(err, survey.ErrStopCancelled) || !strings.Contains(err.Error(), "mtplx, omlx was not stopped") {
		t.Fatalf("err = %v, want a cancellation naming mtplx and omlx as not stopped", err)
	}
	if !slices.Equal(halted, []string{"mtplx"}) || out.String() != "Stopping mtplx... cancelled\n" {
		t.Errorf("halted = %v out = %q, want the one cancelled halt", halted, out.String())
	}
}

// TestStopHaltAsksWhenTheFamilyHasLiveSessions verifies the halt of a whole
// provider asks first whenever live wt sessions use any model of its family,
// with no candidate at all: omlx with an untrusted probe (#307), omlx with a
// trusted probe whose session is on a model that is not loaded, and mtplx
// with no candidate (#308). Declining halts nothing; --yes halts without
// asking. Counted from the candidates, the question was skipped and the
// service halted under another terminal's session.
func TestStopHaltAsksWhenTheFamilyHasLiveSessions(t *testing.T) {
	cases := []struct {
		name, arg string
		fs        survey.FamilyState
		want      string
	}{
		{"omlx untrusted", "omlx", survey.FamilyState{Untrusted: true, Sessions: 2, Users: []string{"omlx/Discovered-4bit", "omlx/c"}},
			"omlx is in use by 2 live wt session(s) (omlx/Discovered-4bit, omlx/c), and wt could not tell what it has loaded; stop anyway?"},
		{"omlx trusted, session on a model not loaded", "omlx", survey.FamilyState{Sessions: 1, Users: []string{"omlx/c"}},
			"omlx is in use by 1 live wt session(s) (omlx/c); stop anyway?"},
		{"mtplx untrusted", "mtplx", survey.FamilyState{Untrusted: true, Sessions: 1, Users: []string{"mtplx/big"}},
			"mtplx is in use by 1 live wt session(s) (mtplx/big), and wt could not tell what it has loaded; stop anyway?"},
		{"mtplx answering, nothing loaded", "mtplx", survey.FamilyState{Sessions: 1, Users: []string{"mtplx/big"}},
			"mtplx is in use by 1 live wt session(s) (mtplx/big); stop anyway?"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stopped := stubStopState(t, families(tc.arg, tc.fs))
			halted := stubHalt(t)
			asked := stubStopConfirm(t, false)
			err := runStop(io.Discard, stopCfg(), tc.arg, false)
			if err == nil || !strings.Contains(err.Error(), "cancelled") {
				t.Fatalf("declined: err = %v, want a cancellation", err)
			}
			if !slices.Equal(*asked, []string{tc.want}) {
				t.Errorf("asked = %q\n want  [%q]", *asked, tc.want)
			}
			if len(*halted) != 0 || len(*stopped) != 0 {
				t.Fatalf("declined: halted = %v stopped = %v, want nothing touched", *halted, *stopped)
			}

			*asked = nil
			if err := runStop(io.Discard, stopCfg(), tc.arg, true); err != nil {
				t.Fatal(err)
			}
			if len(*asked) != 0 || !slices.Equal(*halted, []string{tc.arg}) {
				t.Errorf("--yes: asked = %q halted = %v, want no question and the halt", *asked, *halted)
			}
		})
	}
}

// TestStopHaltRefusesWithoutATerminal verifies that with no terminal and no
// --yes, a halt that live sessions would feel fails naming --yes and halts
// nothing — through the real prompt, with the terminal taken away. A script
// must not be able to halt a provider under a live session by accident.
func TestStopHaltRefusesWithoutATerminal(t *testing.T) {
	oldTTY, oldConfirm := openTTY, confirmStop
	openTTY = func() (*os.File, error) { return nil, errors.New("open /dev/tty: device not configured") }
	confirmStop = promptStop
	t.Cleanup(func() { openTTY, confirmStop = oldTTY, oldConfirm })
	fs := survey.FamilyState{Untrusted: true, Sessions: 1, Users: []string{"mtplx/big"}}
	for _, run := range map[string]func() error{
		"wt stop mtplx": func() error { return runStop(io.Discard, stopCfg(), "mtplx", false) },
		"wt stop --all": func() error { return runStopAll(io.Discard, stopCfg(), false) },
	} {
		stopped := stubStopState(t, families("mtplx", fs))
		halted := stubHalt(t)
		err := run()
		if err == nil || !strings.Contains(err.Error(), "rerun with --yes") || !strings.Contains(err.Error(), "1 live wt session(s)") {
			t.Fatalf("err = %v, want the question and --yes named", err)
		}
		if len(*halted) != 0 || len(*stopped) != 0 {
			t.Fatalf("halted = %v stopped = %v, want nothing touched", *halted, *stopped)
		}
	}
}

// TestStopAllAsksAboutEveryHaltedFamily verifies `wt stop --all` asks once,
// counting the live sessions of every family it is about to halt beside those
// of the models it stops one by one, and says which providers it could not
// read; declining stops and halts nothing. With omlx's probe untrusted the
// question was skipped and omlx halted under a live session (#307).
func TestStopAllAsksAboutEveryHaltedFamily(t *testing.T) {
	st := survey.StopState{
		Candidates: []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)},
		Families: map[string]survey.FamilyState{
			"omlx":  {Probed: true, Untrusted: true, Sessions: 1, Users: []string{"omlx/c"}},
			"mtplx": {Probed: true, Untrusted: true, Sessions: 1, Users: []string{"mtplx/big"}},
		},
	}
	stopped := stubStopState(t, st)
	halted := stubHalt(t)
	asked := stubStopConfirm(t, false)
	err := runStopAll(io.Discard, stopCfg(), false)
	if err == nil || !strings.Contains(err.Error(), "nothing was stopped") {
		t.Fatalf("declined: err = %v, want a cancellation", err)
	}
	want := "running local models are in use by 4 live wt session(s) (ollama/a:1, mtplx/big, omlx/c), and wt could not tell what mtplx and omlx have loaded; stop them all?"
	if !slices.Equal(*asked, []string{want}) {
		t.Errorf("asked = %q\n want  [%q]", *asked, want)
	}
	if len(*stopped) != 0 || len(*halted) != 0 {
		t.Fatalf("declined: stopped = %v halted = %v, want nothing touched", *stopped, *halted)
	}

	*asked = nil
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() {}
	t.Cleanup(func() { waitPendingRoutes = oldWait })
	if err := runStopAll(io.Discard, stopCfg(), true); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 || len(*stopped) != 1 || !slices.Equal(*halted, []string{"mtplx", "omlx"}) {
		t.Errorf("--yes: asked = %q stopped = %v halted = %v, want no question, the ollama model stopped, mtplx and omlx halted", *asked, *stopped, *halted)
	}
}

// TestStopSaysNothingRunningOnlyWhenThatIsKnown verifies that when ollama's
// probe gave no usable answer and its port did not refuse, `wt stop ollama`
// and `wt stop --all` say wt cannot tell what is running and exit non-zero,
// instead of "nothing running on ollama" / "no running local models". wt
// stops ollama's models one by one and never its daemon, so there is nothing
// it can stop blind; a refused port is still "nothing running", exit 0.
func TestStopSaysNothingRunningOnlyWhenThatIsKnown(t *testing.T) {
	ollamaOnly := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	stubStopState(t, families("ollama", survey.FamilyState{Untrusted: true}))
	halted := stubHalt(t)
	var out bytes.Buffer
	err := runStop(&out, ollamaOnly, "ollama", false)
	if err == nil || !strings.Contains(err.Error(), "cannot tell what is running on ollama") || out.Len() != 0 {
		t.Errorf("wt stop ollama: err = %v out = %q, want a cannot-tell error and no nothing-running note", err, out.String())
	}
	err = runStopAll(&out, ollamaOnly, false)
	if err == nil || !strings.Contains(err.Error(), "cannot tell what is running on ollama") || out.Len() != 0 {
		t.Errorf("wt stop --all: err = %v out = %q, want a cannot-tell error and no no-running note", err, out.String())
	}
	// The pool is still halted: one provider wt cannot read does not leave
	// the others running.
	err = runStopAll(&out, modelCmdConfig(), false)
	if err == nil || !slices.Equal(*halted, []string{"omlx"}) || out.String() != "Stopping omlx... done\n" {
		t.Errorf("with omlx: err = %v halted = %v out = %q, want omlx halted and the error kept", err, *halted, out.String())
	}

	stubStopState(t, families("ollama", survey.FamilyState{Untrusted: true, Down: true}))
	out.Reset()
	if err := runStop(&out, ollamaOnly, "ollama", false); err != nil || out.String() != "wt: nothing running on ollama\n" {
		t.Errorf("refused port: err = %v out = %q, want the nothing-running note and exit 0", err, out.String())
	}
	out.Reset()
	if err := runStopAll(&out, ollamaOnly, false); err != nil || out.String() != "wt: no running local models\n" {
		t.Errorf("refused port, --all: err = %v out = %q, want the no-running note and exit 0", err, out.String())
	}
}

// stubPicker makes bare `wt stop` see a terminal and a picker that reports
// offered and the families it could not read.
func stubPicker(t *testing.T, offered bool, unknown ...string) {
	t.Helper()
	oldTTY, oldPick := stdinTTY, stopPickerAll
	t.Cleanup(func() { stdinTTY, stopPickerAll = oldTTY, oldPick })
	stdinTTY = func() bool { return true }
	stopPickerAll = func(*config.Config) (bool, []string) { return offered, unknown }
}

// TestStopPickerWithNothingToOfferSaysWhatItCouldNotRead verifies bare `wt
// stop` does not print "no running local models" when the picker had nothing
// to offer because a provider's probe gave no usable answer: it exits non-zero
// naming the provider, and names a command that stops it only for a provider
// wt stops as a whole. For ollama no such command exists — `wt stop ollama`
// and `wt stop --all` stop nothing there — so the user must not be sent to one.
func TestStopPickerWithNothingToOfferSaysWhatItCouldNotRead(t *testing.T) {
	for _, tc := range []struct {
		unknown []string
		want    string
	}{
		{[]string{"mtplx"}, `cannot tell what is running on mtplx: the probe gave no usable answer — "wt stop mtplx" or "wt stop --all" stops mtplx`},
		{[]string{"ollama"}, `cannot tell what is running on ollama: the probe gave no usable answer — nothing was stopped`},
		{[]string{"mtplx", "ollama", "omlx"}, `cannot tell what is running on mtplx, ollama, omlx: the probe gave no usable answer — "wt stop mtplx", "wt stop omlx" or "wt stop --all" stops mtplx and omlx`},
	} {
		stubPicker(t, false, tc.unknown...)
		var out bytes.Buffer
		err := runStop(&out, stopCfg(), "", false)
		if err == nil || err.Error() != tc.want || out.Len() != 0 {
			t.Errorf("unknown %v: err = %v out = %q\n want %s and no no-running note", tc.unknown, err, out.String(), tc.want)
		}
	}
}

// TestStopPickerThatListedModelsNotesWhatItCouldNotRead verifies bare `wt
// stop` says so when the picker listed models but a provider's probe gave no
// usable answer: the list is then not everything that may be running. Without
// the note the list looked complete while mtplx, the largest server, was up.
func TestStopPickerThatListedModelsNotesWhatItCouldNotRead(t *testing.T) {
	stubPicker(t, true, "mtplx", "ollama")
	var out bytes.Buffer
	if err := runStop(&out, stopCfg(), "", false); err != nil {
		t.Fatal(err)
	}
	want := "wt: could not tell what is running on mtplx, ollama — \"wt stop mtplx\" or \"wt stop --all\" stops mtplx\n"
	if out.String() != want {
		t.Errorf("out = %q, want %q", out.String(), want)
	}
	stubPicker(t, true)
	out.Reset()
	if err := runStop(&out, stopCfg(), "", false); err != nil || out.Len() != 0 {
		t.Errorf("every provider read: err = %v out = %q, want no note", err, out.String())
	}
}

// TestStopDoesNotHaltAnExclusiveProviderThatWasNotProbed verifies `wt stop
// mtplx` and `wt stop --all` leave alone an mtplx the inventory never probed
// (a provider row with no location and no local model): with no probe there is
// no "its port did not refuse", so it is "nothing running" as before, and
// --all still halts omlx. Read as up, every --all ran `mtplx stop` there and
// failed with "mtplx binary not found" on a machine without mtplx.
func TestStopDoesNotHaltAnExclusiveProviderThatWasNotProbed(t *testing.T) {
	for name, st := range map[string]survey.StopState{
		"no family state":     {},
		"a live session only": {Families: map[string]survey.FamilyState{"mtplx": {Sessions: 1, Users: []string{"mtplx/big"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			stubStopState(t, st)
			halted := stubHalt(t)
			asked := stubStopConfirm(t, false)
			var out bytes.Buffer
			if err := runStop(&out, stopCfg(), "mtplx", false); err != nil {
				t.Fatal(err)
			}
			if len(*halted) != 0 || out.String() != "wt: nothing running on mtplx\n" {
				t.Errorf("wt stop mtplx: halted = %v out = %q, want no halt and the nothing-running note", *halted, out.String())
			}
			out.Reset()
			if err := runStopAll(&out, stopCfg(), false); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*halted, []string{"omlx"}) || len(*asked) != 0 {
				t.Errorf("wt stop --all: halted = %v asked = %q, want omlx alone halted and no question", *halted, *asked)
			}
		})
	}
}

// TestStopFailurePrintsNoUsage verifies a `wt stop <provider>` that fails
// after its arguments were accepted — a declined question, a provider wt
// cannot read, a failed halt — prints no usage text, and an argument mistake
// still does. With an unread provider these are the ordinary output of a
// routine command, and 14 lines of flags between two copies of the message
// hide the one line that says what happened.
func TestStopFailurePrintsNoUsage(t *testing.T) {
	stubStopState(t, families("ollama", survey.FamilyState{Untrusted: true}))
	stubHalt(t)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := stopCmd(&app{cfg: stopCfg()})
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}
	got, err := run("ollama")
	if err == nil || !strings.Contains(err.Error(), "cannot tell what is running on ollama") || strings.Contains(got, "Usage:") {
		t.Errorf("wt stop ollama: err = %v out = %q, want the cannot-tell error and no usage text", err, got)
	}
	stubPicker(t, false, "ollama")
	if got, err = run(); err == nil || strings.Contains(got, "Usage:") {
		t.Errorf("wt stop: err = %v out = %q, want an error and no usage text", err, got)
	}
	if got, err = run("nosuch"); err == nil || !strings.Contains(got, "Usage:") {
		t.Errorf("wt stop nosuch: err = %v out = %q, want the usage text for an argument mistake", err, got)
	}
	stdinTTY = func() bool { return false }
	if got, err = run(); err == nil || !strings.Contains(got, "Usage:") {
		t.Errorf("wt stop without a TTY: err = %v out = %q, want the usage text", err, got)
	}
}

// TestStopAllSettlesOneHaltsRestartBeforeTheNext verifies `wt stop --all`
// waits for the LiteLLM proxy restart the halt of mtplx started before it
// halts omlx. Each halt removes its family's routes and restarts the proxy
// asynchronously; with two halts in one command, two restarts at once bounce
// the proxy under each other.
func TestStopAllSettlesOneHaltsRestartBeforeTheNext(t *testing.T) {
	stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true}))
	var events []string
	oldHalt, oldWait := stopProvider, waitPendingRoutes
	t.Cleanup(func() { stopProvider, waitPendingRoutes = oldHalt, oldWait })
	stopProvider = func(_ context.Context, _ *config.Config, id string) error {
		events = append(events, "halt "+id)
		return nil
	}
	waitPendingRoutes = func() { events = append(events, "wait") }
	if err := runStopAll(io.Discard, stopCfg(), false); err != nil {
		t.Fatal(err)
	}
	if want := []string{"halt mtplx", "wait", "halt omlx"}; !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

// liveStopState points the stop state at the real read — the real inventory
// probe and the real refcount file, both under the package's throwaway config
// home — for a registry whose providers are httptest servers. The stop
// engines stay stubbed.
func liveStopState(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := stopState
	stopState = survey.ReadStopState
	t.Cleanup(func() { stopState = old })
}

func localProvider(id, baseURL, modelDir string) config.Provider {
	return config.Provider{ID: id, Location: config.LocationLocal, ModelDir: modelDir, Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}
}

// TestStopHaltsAnMtplxItCannotReadThroughTheRealProbe is #308's reproduction,
// through the real inventory probe: an mtplx server that answers /v1/models
// with an empty list, or with an error, is halted by `wt stop --all` and `wt
// stop mtplx`; one whose port refuses is not. The commands printed "no running
// local models" / "nothing running on mtplx" and exited 0 with the server up.
// Neither case stands for a model that is still loading: an mtplx that has
// not opened its port yet is the "refused" case, which is left alone here
// because no pidfile names a server (stop_loading_test.go has the other).
func TestStopHaltsAnMtplxItCannotReadThroughTheRealProbe(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"empty list": func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"data":[]}`) },
		"error":      func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) },
	} {
		t.Run(name, func(t *testing.T) {
			liveStopState(t)
			srv := httptest.NewServer(handler)
			defer srv.Close()
			cfg := &config.Config{Providers: []config.Provider{localProvider("mtplx", srv.URL+"/v1", t.TempDir())}}
			halted := stubHalt(t)
			var out bytes.Buffer
			if err := runStopAll(&out, cfg, false); err != nil {
				t.Fatal(err)
			}
			if err := runStop(&out, cfg, "mtplx", false); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*halted, []string{"mtplx", "mtplx"}) || out.String() != "Stopping mtplx... done\nStopping mtplx... done\n" {
				t.Errorf("halted = %v out = %q, want mtplx halted by each command", *halted, out.String())
			}
		})
	}
	t.Run("refused", func(t *testing.T) {
		liveStopState(t)
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close() // the port now refuses
		cfg := &config.Config{Providers: []config.Provider{localProvider("mtplx", url+"/v1", t.TempDir())}}
		halted := stubHalt(t)
		var out bytes.Buffer
		if err := runStopAll(&out, cfg, false); err != nil {
			t.Fatal(err)
		}
		if err := runStop(&out, cfg, "mtplx", false); err != nil {
			t.Fatal(err)
		}
		if len(*halted) != 0 || out.String() != "wt: no running local models\nwt: nothing running on mtplx\n" {
			t.Errorf("halted = %v out = %q, want no halt and both notes", *halted, out.String())
		}
	})
}

// TestStopAsksBeforeHaltingAnOmlxItCannotReadThroughTheRealProbe is #307's
// reproduction, through the real inventory probe and the real refcount file:
// omlx gives no usable answer, a live wt session is on a discovered omlx
// model no registry lists, and `wt stop omlx` and `wt stop --all` both ask
// before the halt and halt nothing when the answer is No.
func TestStopAsksBeforeHaltingAnOmlxItCannotReadThroughTheRealProbe(t *testing.T) {
	liveStopState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := &config.Config{Providers: []config.Provider{localProvider("omlx", srv.URL+"/v1", t.TempDir())}}
	if err := refcount.NewStore().Record(os.Getpid(), "omlx/Discovered-4bit"); err != nil {
		t.Fatal(err)
	}
	halted := stubHalt(t)
	asked := stubStopConfirm(t, false)
	if err := runStop(io.Discard, cfg, "omlx", false); err == nil {
		t.Fatal("wt stop omlx: declined question must be an error")
	}
	if err := runStopAll(io.Discard, cfg, false); err == nil {
		t.Fatal("wt stop --all: declined question must be an error")
	}
	want := []string{
		"omlx is in use by 1 live wt session(s) (omlx/Discovered-4bit), and wt could not tell what it has loaded; stop anyway?",
		"running local models are in use by 1 live wt session(s) (omlx/Discovered-4bit), and wt could not tell what omlx has loaded; stop them all?",
	}
	if !slices.Equal(*asked, want) {
		t.Errorf("asked = %q\n want  %q", *asked, want)
	}
	if len(*halted) != 0 {
		t.Fatalf("halted = %v, want nothing halted after No", *halted)
	}
}

// TestStopHaltOfARefusingPoolAsksNothing pins the idle machine: omlx's port
// refuses, so nothing is serving and no session can lose a model, whatever
// the refcount file still lists. `wt stop omlx` and `wt stop --all` run the
// halt as before, with no question and no "could not tell" — a cleanup script
// without --yes must not start failing because an agent is still open on a
// server that is already down.
func TestStopHaltOfARefusingPoolAsksNothing(t *testing.T) {
	stubStopState(t, families("omlx", survey.FamilyState{Untrusted: true, Down: true, Sessions: 1, Users: []string{"omlx/c"}}))
	halted := stubHalt(t)
	asked := stubStopConfirm(t, false)
	if err := runStop(io.Discard, modelCmdConfig(), "omlx", false); err != nil {
		t.Fatal(err)
	}
	if err := runStopAll(io.Discard, modelCmdConfig(), false); err != nil {
		t.Fatal(err)
	}
	if len(*asked) != 0 || !slices.Equal(*halted, []string{"omlx", "omlx"}) {
		t.Errorf("asked = %q halted = %v, want no question and both halts", *asked, *halted)
	}
}
