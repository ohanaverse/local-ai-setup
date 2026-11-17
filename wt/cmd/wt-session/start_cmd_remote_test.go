package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// fixedAnswerer is a modelclient.Answerer with fixed answers: it declines
// ConfirmInUse and answers Replace with replace.
type fixedAnswerer struct{ replace bool }

func (a fixedAnswerer) Replace(context.Context, modelapi.Problem) (bool, error) {
	return a.replace, nil
}

func (fixedAnswerer) Others(context.Context, modelapi.Problem) ([]string, bool, error) {
	return nil, true, nil
}

func (fixedAnswerer) ConfirmInUse(context.Context, modelapi.Problem) (bool, error) {
	return false, nil
}

// startRig opens the model session of a remote rig for the start and stop
// command tests.
func startRig(t *testing.T, rows ...modelapi.Row) (*remoteRig, *modelSession) {
	t.Helper()
	r := newRemoteRig(t, rows...)
	ms, err := r.a.openModels(context.Background(), "x")
	if err != nil {
		t.Fatalf("openModels: %v", err)
	}
	return r, ms
}

// TestStartRemoteAlreadyRunningIsANoop proves a running local model the
// gateway lists is reported ("ID is already running", stdout, no prefix) and
// nothing is registered or started; and that a running model whose route the
// server could not read is left alone too (R7: unknown is never a start).
func TestStartRemoteAlreadyRunningIsANoop(t *testing.T) {
	for name, routed := range map[string]*bool{"routed": boolp(true), "route unknown": nil} {
		t.Run(name, func(t *testing.T) {
			r, ms := startRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, routed))
			var out bytes.Buffer
			if err := runStartRemote(context.Background(), &out, ms, themes.Default, "omlx/q", false, fixedAnswerer{}); err != nil {
				t.Fatalf("err = %v", err)
			}
			if out.String() != "omlx/q is already running\n" {
				t.Errorf("stdout = %q", out.String())
			}
			if n := r.f.count("POST"); n != 0 {
				t.Errorf("POSTs = %d, want none\n%s", n, strings.Join(r.f.reqs(), "\n"))
			}
		})
	}
}

// TestStartRemoteAttachesToActiveJob proves a second wt start for a model with
// a start job already running polls that job (the server's submit hands back
// its id) and succeeds with it, rather than starting a second one.
func TestStartRemoteAttachesToActiveJob(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	r.f.ActiveStart = "job-active"
	var out bytes.Buffer
	if err := runStartRemote(context.Background(), &out, ms, themes.Default, "omlx/q", false, fixedAnswerer{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := r.f.count("GET /control/v1/jobs/job-active"); n == 0 {
		t.Errorf("the active job was never polled\n%s", strings.Join(r.f.reqs(), "\n"))
	}
	if n := len(r.f.Starts); n != 1 {
		t.Errorf("start posts = %d, want 1", n)
	}
	if !strings.Contains(out.String(), "omlx/q is running") {
		t.Errorf("stdout = %q", out.String())
	}
}

// TestStartRemoteRegistersAndEndsASession pins R14: the client session is
// registered with the model before the start job, ended after it succeeds,
// and the note on stderr says the model is up and when the server stops it.
func TestStartRemoteRegistersAndEndsASession(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	var out bytes.Buffer
	if err := runStartRemote(context.Background(), &out, ms, themes.Default, "omlx/q", false, fixedAnswerer{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	reqs := r.f.reqs()
	reg, start, poll, del := index(reqs, "POST /control/v1/sessions"), index(reqs, "POST /control/v1/start"), index(reqs, "GET /control/v1/jobs/job-0"), index(reqs, "DELETE /control/v1/sessions/")
	if reg < 0 || start < 0 || poll < 0 || del < 0 || !(reg < start && start < poll && poll < del) {
		t.Errorf("order register=%d start=%d poll=%d delete=%d, want increasing\n%s", reg, start, poll, del, strings.Join(reqs, "\n"))
	}
	if got := r.f.Registers; len(got) == 0 || len(got[0]) != 1 || got[0][0] != "omlx/q" {
		t.Errorf("registers = %v, want the model", got)
	}
	const note = "wt: note: omlx/q is up; the model server stops it after its idle grace unless a client session uses it\n"
	if !strings.Contains(r.stderr.String(), note) {
		t.Errorf("stderr = %q, want %q", r.stderr.String(), note)
	}
	if strings.Contains(out.String(), "wt:") {
		t.Errorf("stdout carries the prefix: %q", out.String())
	}
}

// TestStartRemoteCtrlCExits2 proves Ctrl-C while the job runs detaches: exit
// 2 with the detach text naming the job, the job left running, the client
// session still ended, and no "is up" note.
func TestStartRemoteCtrlCExits2(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.f.Hang = true
	r.f.OnPoll = cancel
	err := runStartRemote(ctx, &bytes.Buffer{}, ms, themes.Default, "omlx/q", false, fixedAnswerer{})
	var det *modelclient.DetachedError
	if !errors.As(err, &det) || exitcode.Of(err) != 2 {
		t.Fatalf("err = %v (exit %d), want *DetachedError, exit 2", err, exitcode.Of(err))
	}
	if !strings.HasPrefix(err.Error(), "detached — job job-0 keeps running on the model server") {
		t.Errorf("text = %q", err.Error())
	}
	if n := r.f.count("DELETE /control/v1/sessions/"); n != 1 {
		t.Errorf("session deletes = %d, want 1 (the client session ends on a detach)", n)
	}
	if strings.Contains(r.stderr.String(), "is up") {
		t.Errorf("stderr says the model is up: %q", r.stderr.String())
	}
}

// TestStartRemoteDeclinedExits3 proves a question answered no ends the start
// with exit 3 and the declined text, the client session ended.
func TestStartRemoteDeclinedExits3(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	r.f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeOccupied, Message: "x", Occupants: []string{"mtplx/y"}}
	}
	err := runStartRemote(context.Background(), &bytes.Buffer{}, ms, themes.Default, "omlx/q", false, fixedAnswerer{replace: false})
	if exitcode.Of(err) != 3 || err == nil || err.Error() != "declined — nothing was started" {
		t.Fatalf("err = %v (exit %d), want exit 3 and the declined text", err, exitcode.Of(err))
	}
	if n := r.f.count("DELETE /control/v1/sessions/"); n != 1 {
		t.Errorf("session deletes = %d, want 1", n)
	}
}

// TestStartRemoteCloudRowRefusesWithTheServersText proves a start of a cloud
// row is the server's to refuse: its failed text comes back as the error,
// exit 2 (a job that did not finish), the session ended.
func TestStartRemoteCloudRowRefusesWithTheServersText(t *testing.T) {
	r, ms := startRig(t, wireRow("openrouter/x", modelapi.ActionLaunch, 0))
	r.f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeFailed, Message: "openrouter/x is a cloud model; wt starts local models only"}
	}
	err := runStartRemote(context.Background(), &bytes.Buffer{}, ms, themes.Default, "openrouter/x", false, fixedAnswerer{})
	if exitcode.Of(err) != 2 || err == nil || !strings.Contains(err.Error(), "is a cloud model; wt starts local models only") {
		t.Fatalf("err = %v (exit %d)", err, exitcode.Of(err))
	}
	if n := r.f.count("DELETE /control/v1/sessions/"); n != 1 {
		t.Errorf("session deletes = %d, want 1", n)
	}
}

// TestStartRemoteNoTerminalNoIdSaysPassAnId proves wt start with no model id
// and no terminal fails before the picker (P5): exit 1, the one text, and
// nothing registered or started.
func TestStartRemoteNoTerminalNoIdSaysPassAnId(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	stubStdinTTY(t, false)
	err := runStartRemote(context.Background(), &bytes.Buffer{}, ms, themes.Default, "", false, fixedAnswerer{})
	const want = "the model picker needs a TTY; pass a model id (wt start ID) to start without it"
	if err == nil || err.Error() != want || exitcode.Of(err) != 1 {
		t.Fatalf("err = %v (exit %d), want %q, exit 1", err, exitcode.Of(err), want)
	}
	if n := r.f.count("POST"); n != 0 {
		t.Errorf("POSTs = %d, want none", n)
	}
}

// TestStartRemoteStartsARunningModelKnownToBeUnrouted proves R7's one start of
// a running model: routed explicitly false starts it (the job adds the route).
func TestStartRemoteStartsARunningModelKnownToBeUnrouted(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(false)))
	var out bytes.Buffer
	if err := runStartRemote(context.Background(), &out, ms, themes.Default, "omlx/q", false, fixedAnswerer{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := r.f.count("POST /control/v1/start"); n != 1 {
		t.Errorf("starts = %d, want 1\n%s", n, strings.Join(r.f.reqs(), "\n"))
	}
	if strings.Contains(out.String(), "already running") {
		t.Errorf("stdout = %q", out.String())
	}
}

// TestStartRemoteRegisterFailureIsOneNoteAndTheStartGoesOn proves a client
// session the server refuses is one note, the start still runs, the session is
// still ended (a DELETE is sent), and that end says nothing.
func TestStartRemoteRegisterFailureIsOneNoteAndTheStartGoesOn(t *testing.T) {
	r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	r.f.RegisterStatus = 500
	if err := runStartRemote(context.Background(), &bytes.Buffer{}, ms, themes.Default, "omlx/q", false, fixedAnswerer{}); err != nil {
		t.Fatalf("err = %v", err)
	}
	if n := strings.Count(r.stderr.String(), "did not register this client session"); n != 1 {
		t.Errorf("register notes = %d, want 1:\n%s", n, r.stderr.String())
	}
	if strings.Contains(r.stderr.String(), "could not end the client session") {
		t.Errorf("the end of an unregistered session spoke: %q", r.stderr.String())
	}
	if r.f.count("POST /control/v1/start") != 1 || r.f.count("DELETE /control/v1/sessions/") != 1 {
		t.Errorf("start and delete counts wrong:\n%s", strings.Join(r.f.reqs(), "\n"))
	}
}

// TestStartAndStopCommandsDispatchOnTheEndpoint proves, at the command level
// with a model server configured: bare `wt start` with no terminal is P5's
// refusal, bare `wt stop` likewise, and bare `wt stop` with a terminal opens
// the server's picker.
func TestStartAndStopCommandsDispatchOnTheEndpoint(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateRunning, modelapi.ActionLaunch, boolp(true)))
	r.a.hooks.stopIn = func() io.Reader { return strings.NewReader("1\n") }
	run := func(cmd interface {
		SetArgs([]string)
		SetOut(io.Writer)
		SetErr(io.Writer)
		Execute() error
	}) error {
		cmd.SetArgs(nil)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}
	err := run(startCmd(r.a))
	if err == nil || err.Error() != "the model picker needs a TTY; pass a model id (wt start ID) to start without it" || exitcode.Of(err) != 1 {
		t.Errorf("start: err = %v (exit %d)", err, exitcode.Of(err))
	}
	err = run(stopCmd(r.a))
	if err == nil || err.Error() != "the stop picker needs a TTY; pass a model id (wt stop ID) to stop without it" || exitcode.Of(err) != 1 {
		t.Errorf("stop: err = %v (exit %d)", err, exitcode.Of(err))
	}
	stubStdinTTY(t, true)
	captureFile(t, &os.Stdout, func() {
		if err := run(stopCmd(r.a)); err != nil {
			t.Errorf("stop with a terminal: %v", err)
		}
	})
	if len(r.f.Stops) != 1 || r.f.Stops[0].Model != "omlx/q" {
		t.Errorf("stops = %+v, want the picked model", r.f.Stops)
	}
}

// TestStartRemoteReplaceFlagIsSentWithTheFirstJob proves wt start --replace
// puts AllowReplace on the first start request: without it the server ran one
// doomed job that failed occupied before the answer re-posted it.
func TestStartRemoteReplaceFlagIsSentWithTheFirstJob(t *testing.T) {
	for _, allow := range []bool{false, true} {
		r, ms := startRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
		var got []bool
		r.f.Outcome = func(_ int, req modelapi.StartRequest) *modelapi.Problem {
			got = append(got, req.AllowReplace)
			return nil
		}
		if err := runStartRemote(context.Background(), &bytes.Buffer{}, ms, themes.Default, "omlx/q", allow, fixedAnswerer{}); err != nil {
			t.Fatalf("allow=%v: err = %v", allow, err)
		}
		if len(got) != 1 || got[0] != allow {
			t.Errorf("allow=%v: AllowReplace of the start requests = %v, want one request carrying it", allow, got)
		}
	}
}
