package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

// inUseProblem is the in-use problem of a stop that two client sessions block.
func inUseProblem(int, modelapi.StopRequest) *modelapi.Problem {
	return &modelapi.Problem{Code: modelapi.CodeInUse, Message: "2 client session(s) use omlx/q", Sessions: []string{"a", "b"}}
}

// TestStopRemoteInUseNoTerminalExits3 proves a stop of a model other client
// sessions use, with no terminal and no --yes, is approval missing (P4): exit
// 3, the one text (N7: "client session(s)"), no second stop posted.
func TestStopRemoteInUseNoTerminalExits3(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.f.StopOutcome = inUseProblem
	stubStdinTTY(t, false)
	err := runStopRemote(context.Background(), &bytes.Buffer{}, ms, "omlx/q", false)
	const want = "approval missing: 2 client session(s) use omlx/q; pass --yes to stop it anyway (there is no terminal to ask); nothing was stopped"
	if err == nil || err.Error() != want || exitcode.Of(err) != 3 {
		t.Fatalf("err = %v (exit %d), want %q, exit 3", err, exitcode.Of(err), want)
	}
	if n := len(r.f.Stops); n != 1 {
		t.Errorf("stops posted = %d, want 1", n)
	}
}

// TestStopRemoteYesConfirmsInUse proves --yes answers the in-use question: the
// stop is re-posted with confirm_in_use, and no request carries a session id
// (this process registers none).
func TestStopRemoteYesConfirmsInUse(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.f.StopOutcome = func(n int, req modelapi.StopRequest) *modelapi.Problem {
		if req.ConfirmInUse {
			return nil
		}
		return inUseProblem(n, req)
	}
	var out bytes.Buffer
	if err := runStopRemote(context.Background(), &out, ms, "omlx/q", true); err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(r.f.Stops) != 2 || r.f.Stops[0].ConfirmInUse || !r.f.Stops[1].ConfirmInUse {
		t.Fatalf("stops = %+v, want one unconfirmed then one confirmed", r.f.Stops)
	}
	for _, s := range r.f.Stops {
		if s.Session != "" || s.Model != "omlx/q" {
			t.Errorf("stop request = %+v, want the model and an empty session", s)
		}
	}
	if out.String() != "omlx/q stopped\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

// TestStopRemoteNoTerminalNoIdSaysPassAnId proves wt stop with no model id and
// no terminal fails before the picker (P5): exit 1, the one text, nothing
// stopped.
func TestStopRemoteNoTerminalNoIdSaysPassAnId(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	stubStdinTTY(t, false)
	err := runStopRemote(context.Background(), &bytes.Buffer{}, ms, "", false)
	const want = "the stop picker needs a TTY; pass a model id (wt stop ID) to stop without it"
	if err == nil || err.Error() != want || exitcode.Of(err) != 1 {
		t.Fatalf("err = %v (exit %d), want %q, exit 1", err, exitcode.Of(err), want)
	}
	if n := r.f.count("POST"); n != 0 {
		t.Errorf("POSTs = %d, want none", n)
	}
}

// TestStopRemoteStillFinishingExits0 proves a stop the provider accepted but
// has not finished is a success: exit 0, the step shown on stdout.
func TestStopRemoteStillFinishingExits0(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.f.StopStep = modelapi.StepStillFinishing + ": ollama unloads it when its request ends"
	var out bytes.Buffer
	if err := runStopRemote(context.Background(), &out, ms, "omlx/q", false); err != nil {
		t.Fatalf("err = %v, want exit 0", err)
	}
	if !strings.Contains(out.String(), "omlx/q — still finishing: ollama unloads it when its request ends") {
		t.Errorf("stdout = %q", out.String())
	}
}

// TestStopRemotePickerMarksClientSessions proves the numbered picker lists the
// running local rows only, marks the ones client sessions use, and stops the
// one picked.
func TestStopRemotePickerMarksClientSessions(t *testing.T) {
	busy := localRow("omlx/busy", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true))
	busy.Sessions = 2
	idle := localRow("omlx/idle", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true))
	down := localRow("omlx/down", modelapi.StateStopped, modelapi.ActionStart, nil)
	r, ms := startRig(t, busy, idle, down, wireRow("openrouter/c", modelapi.ActionLaunch, 3))
	stubStdinTTY(t, true)
	old := stopSelectIn
	stopSelectIn = func() io.Reader { return strings.NewReader("2\n") }
	t.Cleanup(func() { stopSelectIn = old })
	var out bytes.Buffer
	if err := runStopRemote(context.Background(), &out, ms, "", false); err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"1  omlx/busy (2 client session(s))", "2  omlx/idle\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("picker output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "omlx/down") || strings.Contains(out.String(), "openrouter/c") {
		t.Errorf("picker lists a row that is not a running local model:\n%s", out.String())
	}
	if len(r.f.Stops) != 1 || r.f.Stops[0].Model != "omlx/idle" {
		t.Errorf("stops = %+v, want omlx/idle", r.f.Stops)
	}
}

// TestStopProviderStaysOnTheHostPath proves R3: with a model server
// configured, a provider name and --all still take the host path (their
// legacy errors answer, and the server is never asked), while a model id
// goes to the server.
func TestStopProviderStaysOnTheHostPath(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.a.hostCfg, r.a.hostSet = modelCmdConfig(), true
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"provider name": {[]string{"bogus"}, `unknown provider "bogus"`},
		"all with name": {[]string{"--all", "omlx"}, "wt stop --all takes no model or provider"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := stopCmd(r.a)
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if n := len(r.f.reqs()); n != 0 {
		t.Errorf("the model server was asked %d times on the host path:\n%s", n, strings.Join(r.f.reqs(), "\n"))
	}
	cmd := stopCmd(r.a)
	cmd.SetArgs([]string{"omlx/q"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	captureFile(t, &os.Stdout, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("model id: %v", err)
		}
	})
	if len(r.f.Stops) != 1 || r.f.Stops[0].Model != "omlx/q" {
		t.Errorf("stops = %+v, want a model id to go to the server", r.f.Stops)
	}
}

// pickAll runs the stop picker over rows with the answer "a" (every row).
func pickAll(t *testing.T, ms *modelSession, yes bool) error {
	t.Helper()
	stubStdinTTY(t, true)
	old := stopSelectIn
	stopSelectIn = func() io.Reader { return strings.NewReader("a\n") }
	t.Cleanup(func() { stopSelectIn = old })
	return runStopRemote(context.Background(), &bytes.Buffer{}, ms, "", yes)
}

// answerNoTTY makes the stop's terminal a file that reads as an empty answer
// (a decline), whatever is written to it.
func answerNoTTY(t *testing.T) {
	t.Helper()
	old := openTTY
	openTTY = func() (*os.File, error) {
		return os.OpenFile(t.TempDir()+"/tty", os.O_RDWR|os.O_CREATE, 0o600)
	}
	t.Cleanup(func() { openTTY = old })
}

func twoRunning(t *testing.T) (*remoteRig, *modelSession) {
	return startRig(t,
		localRow("omlx/a", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)),
		localRow("omlx/b", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
}

// TestStopRemotePickerAllDeclinedExits3 proves a batch in which every stop was
// declined exits 3 (C9, nothing changed), with the per-item lines once and a
// summary that does not repeat them.
func TestStopRemotePickerAllDeclinedExits3(t *testing.T) {
	r, ms := twoRunning(t)
	r.f.StopOutcome = inUseProblem
	answerNoTTY(t)
	err := pickAll(t, ms, false)
	if err == nil || exitcode.Of(err) != 3 || err.Error() != "2 of 2 stops failed" {
		t.Fatalf("err = %v (exit %d), want exit 3, 2 of 2 stops failed", err, exitcode.Of(err))
	}
	for _, id := range []string{"omlx/a", "omlx/b"} {
		if n := strings.Count(r.stderr.String(), "wt: "+id+": declined"); n != 1 {
			t.Errorf("%s reason printed %d times:\n%s", id, n, r.stderr.String())
		}
	}
}

// TestStopRemotePickerMixedFailureExits2 proves one declined and one failed
// stop in a batch exit 2.
func TestStopRemotePickerMixedFailureExits2(t *testing.T) {
	r, ms := twoRunning(t)
	r.f.StopOutcome = func(n int, req modelapi.StopRequest) *modelapi.Problem {
		if req.Model == "omlx/a" {
			return inUseProblem(n, req)
		}
		return &modelapi.Problem{Code: modelapi.CodeFailed, Message: "omlx/b would not stop"}
	}
	answerNoTTY(t)
	err := pickAll(t, ms, false)
	if err == nil || exitcode.Of(err) != 2 || err.Error() != "2 of 2 stops failed" {
		t.Fatalf("err = %v (exit %d), want exit 2", err, exitcode.Of(err))
	}
	if !strings.Contains(r.stderr.String(), "wt: omlx/b: omlx/b would not stop") {
		t.Errorf("stderr = %q", r.stderr.String())
	}
}

// TestStopRemotePickerDetachStopsTheBatch proves a Ctrl-C detach in the middle
// of a batch ends it: exit 2, the later model never posted, and the summary
// says the rest were not stopped instead of counting them failed.
func TestStopRemotePickerDetachStopsTheBatch(t *testing.T) {
	r, ms := twoRunning(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.f.Hang = true
	r.f.OnPoll = cancel
	stubStdinTTY(t, true)
	old := stopSelectIn
	stopSelectIn = func() io.Reader { return strings.NewReader("a\n") }
	t.Cleanup(func() { stopSelectIn = old })
	err := runStopRemote(ctx, &bytes.Buffer{}, ms, "", false)
	if err == nil || exitcode.Of(err) != 2 || err.Error() != "1 of 2 stops failed; 1 was not stopped" {
		t.Fatalf("err = %v (exit %d)", err, exitcode.Of(err))
	}
	if n := len(r.f.Stops); n != 1 {
		t.Errorf("stops posted = %d, want 1", n)
	}
}

// TestStopAnswererNamesTheRealTerminalFailure proves "there is no terminal to
// ask" is said only when there is none: another failure to open it is named.
func TestStopAnswererNamesTheRealTerminalFailure(t *testing.T) {
	p := modelapi.Problem{Code: modelapi.CodeInUse, Sessions: []string{"a"}}
	a := &stopAnswerer{id: "omlx/q", tty: func() (*os.File, error) { return nil, errors.New("open /dev/tty: device not configured") }}
	_, err := a.ConfirmInUse(context.Background(), p)
	if err == nil || exitcode.Of(err) != 3 || strings.Contains(err.Error(), "no terminal to ask") ||
		!strings.Contains(err.Error(), "could not open the terminal: open /dev/tty: device not configured") {
		t.Errorf("err = %v (exit %d)", err, exitcode.Of(err))
	}
}

// TestStopAllStaysOnTheHostPathWithAnEndpoint proves a bare `wt stop --all`
// with a model server configured acts on this machine (R3): the server is
// never asked. A terminal and a picker answer are stubbed so that a regression
// of the guard would reach the server's catalog and stop; the app's registry
// has no provider, so the host path has nothing to stop and touches no real
// service, and its result is checked.
func TestStopAllStaysOnTheHostPathWithAnEndpoint(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.a.hostCfg, r.a.hostSet = &config.Config{}, true
	stubStdinTTY(t, true)
	old := stopSelectIn
	stopSelectIn = func() io.Reader { return strings.NewReader("1\n") }
	t.Cleanup(func() { stopSelectIn = old })
	cmd := stopCmd(r.a)
	cmd.SetArgs([]string{"--all", "--yes"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	var err error
	captureFile(t, &os.Stdout, func() { err = cmd.Execute() })
	if err != nil {
		t.Errorf("host path --all over an empty registry: err = %v, want nil", err)
	}
	if n := len(r.f.reqs()); n != 0 {
		t.Errorf("the model server was asked %d times:\n%s", n, strings.Join(r.f.reqs(), "\n"))
	}
	if len(r.f.Stops) != 0 {
		t.Errorf("stops = %+v, want none", r.f.Stops)
	}
}

// TestStopRemotePickerDetachPluralWording proves the detach summary says "N
// were not stopped" when more than one model was left.
func TestStopRemotePickerDetachPluralWording(t *testing.T) {
	r, ms := startRig(t,
		localRow("omlx/a", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)),
		localRow("omlx/b", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)),
		localRow("omlx/c", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.f.Hang, r.f.OnPoll = true, cancel
	stubStdinTTY(t, true)
	old := stopSelectIn
	stopSelectIn = func() io.Reader { return strings.NewReader("a\n") }
	t.Cleanup(func() { stopSelectIn = old })
	err := runStopRemote(ctx, &bytes.Buffer{}, ms, "", false)
	if err == nil || err.Error() != "1 of 3 stops failed; 2 were not stopped" {
		t.Errorf("err = %v", err)
	}
}
