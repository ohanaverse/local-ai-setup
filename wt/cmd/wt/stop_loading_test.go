package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// Tests for `wt stop` on an mtplx that is still loading (#308): its port
// refuses, and the stop state carries what wt's pidfile said about it
// (survey.FamilyState.Loading). The stop itself is stubbed (stubStopLoading);
// internal/lifecycle tests what it signals.

// loadingFS is mtplx behind a refused port with a verified server, pid 4242.
func loadingFS(sessions int, users ...string) survey.FamilyState {
	return survey.FamilyState{Untrusted: true, Down: true, Sessions: sessions, Users: users, Loading: lifecycle.Loading{PID: 4242}}
}

// stubStopLoading records each stop of a loading server as "<provider>:<pid>"
// and returns err from it.
func stubStopLoading(t *testing.T, err error) *[]string {
	t.Helper()
	var calls []string
	old := stopLoading
	stopLoading = func(_ context.Context, _ *config.Config, id string, pid int) error {
		calls = append(calls, fmt.Sprintf("%s:%d", id, pid))
		return err
	}
	t.Cleanup(func() { stopLoading = old })
	return &calls
}

// TestStopEndsAnMtplxThatIsStillLoading verifies `wt stop mtplx` and `wt stop
// --all` stop an mtplx whose port refuses when wt's pidfile names the live
// server: by that pid, not through `mtplx stop` (which needs the port), with
// a line that says what was found, and with no question when no live wt
// session uses mtplx. Both commands used to print that nothing was running
// and exit 0 while the process read tens of gigabytes of weights.
func TestStopEndsAnMtplxThatIsStillLoading(t *testing.T) {
	for name, run := range map[string]func(io.Writer) error{
		"wt stop mtplx": func(w io.Writer) error { return runStop(w, mtplxOnlyCfg(), "mtplx", false) },
		"wt stop --all": func(w io.Writer) error { return runStopAll(w, mtplxOnlyCfg(), false) },
	} {
		t.Run(name, func(t *testing.T) {
			stopped := stubStopState(t, families("mtplx", loadingFS(0)))
			halted := stubHalt(t)
			loading := stubStopLoading(t, nil)
			asked := stubStopConfirm(t, false)
			var out bytes.Buffer
			if err := run(&out); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*loading, []string{"mtplx:4242"}) || len(*halted) != 0 || len(*stopped) != 0 {
				t.Errorf("loading stops = %v, port halts = %v, model stops = %v; want only the verified pid stopped", *loading, *halted, *stopped)
			}
			if out.String() != "Stopping mtplx (still loading, pid 4242)... done\n" {
				t.Errorf("out = %q", out.String())
			}
			if len(*asked) != 0 {
				t.Errorf("asked = %q, want no question when no live session uses mtplx", *asked)
			}
		})
	}
}

// TestStopAsksBeforeEndingALoadingMtplxInUse verifies the in-use question
// comes first when live wt sessions are on mtplx, says that mtplx is still
// loading and which pid, and that declining signals nothing while --yes
// skips it — for `wt stop mtplx` and, beside the other providers' sessions,
// `wt stop --all`. The session waiting on that load is the one that loses it.
func TestStopAsksBeforeEndingALoadingMtplxInUse(t *testing.T) {
	st := survey.StopState{
		Candidates: []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 2)},
		Families: map[string]survey.FamilyState{
			"mtplx": func() survey.FamilyState { fs := loadingFS(1, "mtplx/big"); fs.Probed = true; return fs }(),
			"omlx":  {Probed: true, Untrusted: true, Down: true},
		},
	}
	oldWait := waitPendingRoutes
	waitPendingRoutes = func() {}
	t.Cleanup(func() { waitPendingRoutes = oldWait })
	for _, tc := range []struct {
		name     string
		run      func(yes bool) error
		question string
		declined string
	}{
		{"wt stop mtplx", func(yes bool) error { return runStop(io.Discard, stopCfg(), "mtplx", yes) },
			"mtplx is still loading (pid 4242) and is in use by 1 live wt session(s) (mtplx/big); stop anyway?", "mtplx is still running"},
		{"wt stop --all", func(yes bool) error { return runStopAll(io.Discard, stopCfg(), yes) },
			"running local models are in use by 3 live wt session(s) (ollama/a:1, mtplx/big), and mtplx is still loading (pid 4242); stop them all?", "nothing was stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stopped := stubStopState(t, st)
			stubHalt(t)
			loading := stubStopLoading(t, nil)
			asked := stubStopConfirm(t, false)
			err := tc.run(false)
			if err == nil || !strings.Contains(err.Error(), "cancelled") || !strings.Contains(err.Error(), tc.declined) {
				t.Fatalf("declined: err = %v, want a cancellation saying %q", err, tc.declined)
			}
			if !slices.Equal(*asked, []string{tc.question}) {
				t.Errorf("asked = %q\n want  [%q]", *asked, tc.question)
			}
			if len(*loading) != 0 || len(*stopped) != 0 {
				t.Fatalf("declined: loading stops = %v model stops = %v, want nothing touched", *loading, *stopped)
			}
			*asked = nil
			if err := tc.run(true); err != nil {
				t.Fatal(err)
			}
			if len(*asked) != 0 || !slices.Equal(*loading, []string{"mtplx:4242"}) {
				t.Errorf("--yes: asked = %q loading stops = %v, want no question and the stop", *asked, *loading)
			}
		})
	}
}

// TestStopOfALoadingMtplxRefusesWithoutATerminal verifies that with no
// terminal and no --yes, the stop of a loading mtplx a live session uses
// fails naming --yes and signals nothing, through the real prompt. A script
// must not end a load under a session by accident.
func TestStopOfALoadingMtplxRefusesWithoutATerminal(t *testing.T) {
	oldTTY, oldConfirm := openTTY, confirmStop
	openTTY = func() (*os.File, error) { return nil, errors.New("open /dev/tty: device not configured") }
	confirmStop = promptStop
	t.Cleanup(func() { openTTY, confirmStop = oldTTY, oldConfirm })
	for name, run := range map[string]func() error{
		"wt stop mtplx": func() error { return runStop(io.Discard, mtplxOnlyCfg(), "mtplx", false) },
		"wt stop --all": func() error { return runStopAll(io.Discard, mtplxOnlyCfg(), false) },
	} {
		stubStopState(t, families("mtplx", loadingFS(1, "mtplx/big")))
		loading := stubStopLoading(t, nil)
		err := run()
		if err == nil || !strings.Contains(err.Error(), "rerun with --yes") || !strings.Contains(err.Error(), "still loading (pid 4242)") {
			t.Fatalf("%s: err = %v, want the question and --yes named", name, err)
		}
		if len(*loading) != 0 {
			t.Fatalf("%s: loading stops = %v, want nothing signalled", name, *loading)
		}
	}
}

// TestStopOfALoadingMtplxThatFailsExitsNonZero verifies a stop wt could not
// confirm is printed as failed and returned by both commands, and one ended
// by Ctrl+C as cancelled: never "done", never exit 0, over a process that may
// still be loading. A Ctrl+C that lands after wt signalled the server says
// so: the server has SIGTERM and normally exits a moment later, so "mtplx was
// not stopped" would be the opposite of what happened — and in --all the
// servers after it, which wt did not touch, are still named as not stopped.
func TestStopOfALoadingMtplxThatFailsExitsNonZero(t *testing.T) {
	stubStopState(t, families("mtplx", loadingFS(0)))
	stubStopLoading(t, errors.New("mtplx (pid 4242) is still running after SIGKILL"))
	var out bytes.Buffer
	err := runStop(&out, mtplxOnlyCfg(), "mtplx", false)
	if err == nil || !strings.Contains(err.Error(), "still running") || out.String() != "Stopping mtplx (still loading, pid 4242)... failed\n" {
		t.Errorf("wt stop mtplx: err = %v out = %q, want the failure returned and printed", err, out.String())
	}
	out.Reset()
	err = runStopAll(&out, mtplxOnlyCfg(), false)
	if err == nil || !strings.Contains(err.Error(), "mtplx: mtplx (pid 4242) is still running") || !strings.Contains(out.String(), "... failed") {
		t.Errorf("wt stop --all: err = %v out = %q, want mtplx's failure named", err, out.String())
	}

	oldCtx := startSignalCtx
	t.Cleanup(func() { startSignalCtx = oldCtx })
	ctx, cancel := context.WithCancel(context.Background())
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	old := stopLoading
	stopLoading = func(context.Context, *config.Config, string, int) error { cancel(); return context.Canceled }
	t.Cleanup(func() { stopLoading = old })
	out.Reset()
	err = runStop(&out, mtplxOnlyCfg(), "mtplx", false)
	if !errors.Is(err, survey.ErrStopCancelled) || !strings.Contains(err.Error(), "mtplx was not stopped") || !strings.HasSuffix(out.String(), "... cancelled\n") {
		t.Errorf("Ctrl+C: err = %v out = %q, want a cancellation naming mtplx", err, out.String())
	}

	stopLoading = func(context.Context, *config.Config, string, int) error {
		cancel()
		return &lifecycle.StopInterruptedError{PID: 4242, Signal: "SIGTERM", Err: context.Canceled}
	}
	const signalled = `cancelled — mtplx (pid 4242) was sent SIGTERM and wt did not wait for it to exit; run "wt stop mtplx" again to confirm`
	out.Reset()
	err = runStop(&out, mtplxOnlyCfg(), "mtplx", false)
	if !errors.Is(err, survey.ErrStopCancelled) || err.Error() != signalled || out.String() != "Stopping mtplx (still loading, pid 4242)... cancelled\n" {
		t.Errorf("Ctrl+C after the signal: err = %v out = %q\nwant %s", err, out.String(), signalled)
	}
	st := families("mtplx", loadingFS(0))
	st.Families["omlx"] = survey.FamilyState{Probed: true}
	stubStopState(t, st)
	halted := stubHalt(t)
	ctx, cancel = context.WithCancel(context.Background()) // the one above is spent
	defer cancel()
	out.Reset()
	err = runStopAll(&out, stopCfg(), false)
	if !errors.Is(err, survey.ErrStopCancelled) || err.Error() != signalled+"\ncancelled — omlx was not stopped" {
		t.Errorf("--all, Ctrl+C after the signal: err = %v\nwant %s, then omlx named as not stopped", err, signalled)
	}
	if len(*halted) != 0 {
		t.Errorf("halts = %v, want omlx left alone after the cancel", *halted)
	}
}

// TestStopLeavesAloneWhatItCannotVerifyAsMtplx verifies the other things a
// pidfile behind a refused port can name. A live process that is not
// verifiably the mtplx server gets no stop of any kind: the commands say
// nothing is running, add what the pidfile names, and exit 0. A process
// table wt could not read is not "nothing running": the commands exit 1
// saying they cannot tell. Signalling on the pid alone would be the worst
// thing this command could do.
func TestStopLeavesAloneWhatItCannotVerifyAsMtplx(t *testing.T) {
	const stray = "the mtplx pidfile names pid 77, which is not an mtplx server on port 8003 — left alone"
	t.Run("a live process that is not mtplx", func(t *testing.T) {
		stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true, Down: true, Sessions: 1, Users: []string{"mtplx/big"}, Loading: lifecycle.Loading{Stray: stray}}))
		halted := stubHalt(t)
		loading := stubStopLoading(t, nil)
		asked := stubStopConfirm(t, false)
		var out bytes.Buffer
		if err := runStop(&out, mtplxOnlyCfg(), "mtplx", false); err != nil {
			t.Fatal(err)
		}
		if err := runStopAll(&out, mtplxOnlyCfg(), false); err != nil {
			t.Fatal(err)
		}
		want := "wt: nothing running on mtplx\nwt: " + stray + "\nwt: no running local models\nwt: " + stray + "\n"
		if out.String() != want {
			t.Errorf("out = %q\nwant  %q", out.String(), want)
		}
		if len(*halted) != 0 || len(*loading) != 0 || len(*asked) != 0 {
			t.Errorf("halts = %v loading stops = %v asked = %q, want nothing stopped and nothing asked", *halted, *loading, *asked)
		}
	})
	t.Run("a process table wt could not read", func(t *testing.T) {
		cannot := errors.New("the mtplx pidfile names pid 77 and wt could not inspect it: boom")
		stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true, Down: true, Loading: lifecycle.Loading{Err: cannot}}))
		halted := stubHalt(t)
		loading := stubStopLoading(t, nil)
		for name, run := range map[string]func(io.Writer) error{
			"wt stop mtplx": func(w io.Writer) error { return runStop(w, mtplxOnlyCfg(), "mtplx", false) },
			"wt stop --all": func(w io.Writer) error { return runStopAll(w, mtplxOnlyCfg(), false) },
		} {
			var out bytes.Buffer
			err := run(&out)
			if err == nil || !strings.Contains(err.Error(), "cannot tell whether mtplx is still loading") || !strings.Contains(err.Error(), "pid 77") || !strings.Contains(err.Error(), "nothing was stopped") {
				t.Errorf("%s: err = %v, want a cannot-tell error naming the pid", name, err)
			}
			if strings.Contains(out.String(), "nothing running") || strings.Contains(out.String(), "no running local models") {
				t.Errorf("%s: out = %q, want no claim that nothing is running", name, out.String())
			}
		}
		if len(*halted) != 0 || len(*loading) != 0 {
			t.Errorf("halts = %v loading stops = %v, want nothing stopped", *halted, *loading)
		}
		// With nothing else to stop, both commands end the error the same
		// way. --all goes on to stop everything else, so there the error
		// may not say that nothing was stopped: only that mtplx was not.
		if err := runStop(io.Discard, mtplxOnlyCfg(), "mtplx", false); err == nil || !strings.HasSuffix(err.Error(), "boom — nothing was stopped") {
			t.Errorf("wt stop mtplx: err = %v, want it to end in \"nothing was stopped\"", err)
		}
		st := families("mtplx", survey.FamilyState{Untrusted: true, Down: true, Loading: lifecycle.Loading{Err: cannot}})
		st.Candidates = []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)}
		st.Families["omlx"] = survey.FamilyState{Probed: true}
		stopped := stubStopState(t, st)
		oldWait := waitPendingRoutes
		waitPendingRoutes = func() {}
		t.Cleanup(func() { waitPendingRoutes = oldWait })
		var out bytes.Buffer
		err := runStopAll(&out, stopCfg(), false)
		want := "cannot tell whether mtplx is still loading: " + cannot.Error() + " — nothing was stopped there"
		if err == nil || err.Error() != want {
			t.Errorf("wt stop --all: err = %v\nwant  %s", err, want)
		}
		if len(*stopped) != 1 || !slices.Equal(*halted, []string{"omlx"}) || !strings.Contains(out.String(), "Stopping omlx... done") {
			t.Errorf("model stops = %v halts = %v out = %q, want ollama/a:1 and omlx stopped all the same", *stopped, *halted, out.String())
		}
	})
}

// TestStopPickerNamesAnMtplxThatIsStillLoading verifies bare `wt stop` says
// what its picker cannot list. A loading mtplx serves no model, so the picker
// has no row for it: the command names it and the two commands that stop it,
// after the picker when the picker listed other models, and never prints "no
// running local models" over it — the #308 symptom on the third form of the
// command. It stops nothing itself. A pidfile wt could not check is an error,
// and a live process that is not the server gets the "left alone" note the
// other two forms print.
func TestStopPickerNamesAnMtplxThatIsStillLoading(t *testing.T) {
	const note = "wt: mtplx is still loading (pid 4242) — \"wt stop mtplx\" or \"wt stop --all\" stops it\n"
	const stray = "the mtplx pidfile names pid 77, which is not an mtplx server on port 8003 — left alone"
	cannot := errors.New("the mtplx pidfile names pid 77 and wt could not inspect it: boom")
	loading := stubStopLoading(t, nil)
	halted := stubHalt(t)
	for _, tc := range []struct {
		name    string
		offered bool
		loading lifecycle.Loading
		unknown []string
		out     string
		err     string
	}{
		{name: "nothing else running", loading: lifecycle.Loading{PID: 4242}, out: note},
		{name: "after a picker that listed models", offered: true, loading: lifecycle.Loading{PID: 4242}, out: note},
		{name: "beside a provider wt could not read", loading: lifecycle.Loading{PID: 4242}, unknown: []string{"ollama"}, out: note,
			err: "cannot tell what is running on ollama: the probe gave no usable answer — nothing was stopped"},
		{name: "a pidfile wt could not check", loading: lifecycle.Loading{Err: cannot},
			err: "cannot tell whether mtplx is still loading: " + cannot.Error() + " — nothing was stopped"},
		{name: "a pidfile wt could not check, after a picker that listed models", offered: true, loading: lifecycle.Loading{Err: cannot},
			err: "cannot tell whether mtplx is still loading: " + cannot.Error()},
		{name: "a live process that is not mtplx", loading: lifecycle.Loading{Stray: stray}, out: "wt: no running local models\nwt: " + stray + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubPickerLoading(t, tc.offered, map[string]lifecycle.Loading{"mtplx": tc.loading}, tc.unknown...)
			var out bytes.Buffer
			err := runStop(&out, stopCfg(), "", false)
			if (tc.err == "") != (err == nil) || (err != nil && err.Error() != tc.err) {
				t.Errorf("err = %v\nwant  %q", err, tc.err)
			}
			if out.String() != tc.out {
				t.Errorf("out = %q\nwant  %q", out.String(), tc.out)
			}
		})
	}
	if len(*loading) != 0 || len(*halted) != 0 {
		t.Errorf("loading stops = %v halts = %v, want none: the picker stops only what was picked", *loading, *halted)
	}
}

// TestStopModelIDOfALoadingMtplxPointsAtTheProvider verifies `wt stop
// <mtplx model>` while mtplx is still loading stops nothing and says which
// command does: the model is not running — nothing serves it yet — and wt
// does not know which model the loading process holds, so the error names
// `wt stop mtplx` instead of leaving the user with "is not running" over a
// process reading its weights. When wt could not check the pid in the
// pidfile it says that, as `wt stop mtplx` does, not "is not running".
func TestStopModelIDOfALoadingMtplxPointsAtTheProvider(t *testing.T) {
	stubStopState(t, families("mtplx", loadingFS(0)))
	loading := stubStopLoading(t, nil)
	err := runStop(io.Discard, stopCfg(), "mtplx/big", false)
	want := `model "mtplx/big" is not running — mtplx is still loading (pid 4242); "wt stop mtplx" stops it`
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant  %s", err, want)
	}
	if len(*loading) != 0 {
		t.Errorf("loading stops = %v, want none", *loading)
	}
	// A pidfile naming a pid wt could not inspect is not "not running":
	// `wt stop mtplx` refuses to say so, and so does the model id.
	cannot := errors.New("the mtplx pidfile names pid 77 and wt could not inspect it: boom")
	stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true, Down: true, Loading: lifecycle.Loading{Err: cannot}}))
	err = runStop(io.Discard, stopCfg(), "mtplx/big", false)
	if want := "cannot tell whether mtplx is still loading: " + cannot.Error() + " — nothing was stopped"; err == nil || err.Error() != want {
		t.Errorf("unread pidfile: err = %v\nwant  %s", err, want)
	}
	// The pidfile names a live process that is not an mtplx server on the
	// provider's port: wt stops nothing, but says what it found — the same
	// line `wt stop mtplx` prints — instead of leaving the user with "not
	// running" and no hint of the process still on the machine.
	stray := "the mtplx pidfile names pid 77, which is not an mtplx server on port 8003 — left alone"
	stubStopState(t, families("mtplx", survey.FamilyState{Untrusted: true, Down: true, Loading: lifecycle.Loading{Stray: stray}}))
	err = runStop(io.Discard, stopCfg(), "mtplx/big", false)
	if want := `model "mtplx/big" is not running — ` + stray; err == nil || err.Error() != want {
		t.Errorf("stray pidfile: err = %v\nwant  %s", err, want)
	}
	stubStopState(t, families("mtplx", loadingFS(0)))
	// Another family's model keeps the plain message.
	if err := runStop(io.Discard, stopCfg(), "ollama/a:1", false); err == nil || err.Error() != `model "ollama/a:1" is not running` {
		t.Errorf("err = %v, want the plain not-running error", err)
	}
}

// TestStopLoadingSeamFailsClosed pins TestMain's defaults: an unstubbed test
// that gets as far as stopping a loading server is refused, and beneath that
// seam the engine's own are closed — the pidfile is a scratch path, and the
// process-table read fails even with a live pid in that pidfile. The second
// half is what notices lifecycle.IsolateProcessesForTest() being removed
// from TestMain: without it a cmd/wt test that swaps the real stop state in
// would read the developer's mtplx pidfile and could signal their server.
func TestStopLoadingSeamFailsClosed(t *testing.T) {
	if err := stopLoading(context.Background(), mtplxOnlyCfg(), "mtplx", os.Getpid()); err == nil || !strings.Contains(err.Error(), "not stubbed") {
		t.Errorf("err = %v, want the seam's refusal", err)
	}
	pidfile := os.Getenv("WT_MTPLX_PIDFILE")
	if pidfile == "" || strings.HasPrefix(pidfile, "/tmp/local-ai-setup") || strings.HasPrefix(pidfile, "/private/tmp/local-ai-setup") {
		t.Fatalf("WT_MTPLX_PIDFILE = %q, want a scratch path: TestMain no longer isolates the mtplx pidfile", pidfile)
	}
	if l := lifecycle.LoadingServer(mtplxOnlyCfg(), "mtplx"); l != (lifecycle.Loading{}) {
		t.Errorf("LoadingServer = %+v, want nothing: this package's tests read no pidfile", l)
	}
	// A pidfile naming a live process (the test's parent, read by nobody:
	// the seam is closed) must get the seam's refusal, not an answer.
	dir := filepath.Dir(pidfile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getppid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if l := lifecycle.LoadingServer(mtplxOnlyCfg(), "mtplx"); l.Err == nil || !strings.Contains(l.Err.Error(), "not stubbed") {
		t.Errorf("LoadingServer over a live pid = %+v, want the process seam's refusal", l)
	}
}
