package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

// stubTTYAnswer makes the launch see a terminal (stdin and /dev/tty) whose
// user types answer. The file is opened for append, so the question wt writes
// lands after the answer it reads.
func stubTTYAnswer(t *testing.T, answer string) {
	t.Helper()
	stubStdinTTY(t, true)
	path := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(path, []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	old := openTTY
	openTTY = func() (*os.File, error) { return os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0) }
	t.Cleanup(func() { openTTY = old })
}

// TestOccupiedWithoutTerminalExits3NamingReplace is Review Focus 3 for the
// replace question (P4): with no terminal and no --replace, a start the
// server refused because the slot is occupied (or its occupancy is unknown)
// exits 3 with the exact approval-missing text naming --replace, the start is
// not re-posted and nothing is launched.
func TestOccupiedWithoutTerminalExits3NamingReplace(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    modelapi.Problem
		want string
	}{
		{"occupied", modelapi.Problem{Code: modelapi.CodeOccupied, Message: "omlx is serving omlx/other", Occupants: []string{"omlx/other", "omlx/third"}},
			"approval missing: starting omlx/q would stop omlx/other, omlx/third; pass --replace to go ahead (there is no terminal to ask); nothing was started"},
		{"occupied, none named", modelapi.Problem{Code: modelapi.CodeOccupied, Message: "omlx is busy"},
			"approval missing: omlx is busy; pass --replace to go ahead (there is no terminal to ask); nothing was started"},
		{"unknown", modelapi.Problem{Code: modelapi.CodeOccupancyUnknown, Message: "cannot tell whether omlx at http://127.0.0.1:8000 is serving a model"},
			"approval missing: cannot tell whether omlx at http://127.0.0.1:8000 is serving a model; pass --replace to go ahead (there is no terminal to ask); nothing was started"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
			r.f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem { p := tc.p; return &p }
			err := r.launch(t, context.Background(), "omlx/q", "", false)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v\nwant %s", err, tc.want)
			}
			if exitcode.Of(err) != 3 {
				t.Errorf("exit = %d, want 3", exitcode.Of(err))
			}
			if len(r.f.Starts) != 1 || len(*r.built) != 0 {
				t.Errorf("starts=%d built=%d, want one start and nothing built", len(r.f.Starts), len(*r.built))
			}
		})
	}
}

// TestDeclinedReplaceExits3 proves a replace question the user answers "n" on
// the terminal (P1: only y consents) ends the launch with the declined text,
// exit 3, and the server sees no second start.
func TestDeclinedReplaceExits3(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	stubTTYAnswer(t, "n\n")
	r.f.Outcome = func(int, modelapi.StartRequest) *modelapi.Problem {
		return &modelapi.Problem{Code: modelapi.CodeOccupied, Message: "occupied", Occupants: []string{"omlx/other"}}
	}
	err := r.launch(t, context.Background(), "omlx/q", "", false)
	if err == nil || !strings.Contains(err.Error(), "declined — nothing was started") {
		t.Fatalf("err = %v, want the declined text", err)
	}
	if exitcode.Of(err) != 3 {
		t.Errorf("exit = %d, want 3", exitcode.Of(err))
	}
	if len(r.f.Starts) != 1 || len(*r.built) != 0 {
		t.Errorf("starts=%d built=%d, want no re-post and nothing built", len(r.f.Starts), len(*r.built))
	}
}

// TestAskOnTTYCtrlCSaysCancelledNotDeclined proves a prompt Ctrl-C ended (M7)
// reads "cancelled — nothing was started" with a decline's outcome (exit 3,
// nothing changed): "declined" stays the answered no's word (the test above),
// and a cancellation never borrows it.
func TestAskOnTTYCtrlCSaysCancelledNotDeclined(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl-C while the prompt waits: the terminal read cannot be interrupted
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) }) // the abandoned read ends with the test
	_, err := askOnTTY(ctx, strings.NewReader("y\n"), "nothing was started", func(io.Reader) (bool, error) {
		<-blocked
		return false, nil
	})
	const want = "cancelled — nothing was started"
	if err == nil || err.Error() != want || exitcode.Of(err) != 3 {
		t.Fatalf("err = %v (exit %d), want %q, exit 3", err, exitcode.Of(err), want)
	}
}

// TestOthersKeepOnNoTerminalWarnsOnce proves P4's one exception: with no
// terminal and no --others, other providers' models are kept (the start is
// re-posted with an empty stop list) and the existing warning is printed once,
// offering --others=stop because a listed model can be stopped.
func TestOthersKeepOnNoTerminalWarnsOnce(t *testing.T) {
	r := newRemoteRig(t, localRow("omlx/q", modelapi.StateStopped, modelapi.ActionStart, nil))
	r.f.Outcome = func(n int, _ modelapi.StartRequest) *modelapi.Problem {
		if n > 0 {
			return nil
		}
		return &modelapi.Problem{Code: modelapi.CodeOthers, Message: "others",
			Others: []modelapi.Resident{{ID: "mtplx/m", Label: "mtplx/m", Stoppable: true, Figure: "20.0 GB loaded"}}}
	}
	if err := r.launch(t, context.Background(), "omlx/q", "", false); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(r.f.Starts) != 2 || r.f.Starts[1].Others == nil || len(r.f.Starts[1].Others.Stop) != 0 {
		t.Fatalf("starts = %+v, want a re-post keeping the others", r.f.Starts)
	}
	errOut := r.stderr.String()
	want := "wt: warning: starting omlx/q beside mtplx/m (20.0 GB loaded); pass --others=stop to stop them, --others=keep to silence this\n"
	if strings.Count(errOut, "wt: warning:") != 1 || !strings.Contains(errOut, want) {
		t.Errorf("stderr = %q, want one warning %q", errOut, want)
	}
	if len(*r.built) != 1 {
		t.Errorf("agents built = %d, want 1", len(*r.built))
	}
}

// TestStartProgressTextCannotForgeLines is M11 for start progress: an ESC
// and a newline in the job's step (a remote party's text) come out of
// startProgressRemote as one printable line, and a queued job reads the
// queued wording.
func TestStartProgressTextCannotForgeLines(t *testing.T) {
	var b bytes.Buffer
	p := startProgressRemote(&b, "omlx/q")
	p(modelclient.JobView{ID: "j", State: modelapi.JobRunning, Step: "warming\x1b[2J\nwt: forged line", Elapsed: 12 * time.Second})
	out := b.String()
	if strings.Count(out, "\n") != 1 || strings.ContainsRune(out, '\x1b') {
		t.Fatalf("progress = %q, want one line without ESC", out)
	}
	if !strings.HasPrefix(out, "wt: starting omlx/q — warming") || !strings.HasSuffix(out, "(12s)\n") {
		t.Errorf("progress = %q, want the M5 shape", out)
	}
	b.Reset()
	q := startProgressRemote(&b, "omlx/q")
	q(modelclient.JobView{ID: "j", State: modelapi.JobQueued, Queued: true, Elapsed: 3 * time.Second})
	if want := "wt: starting omlx/q — queued behind another start or stop on the model server (3s)\n"; b.String() != want {
		t.Errorf("queued = %q, want %q", b.String(), want)
	}
}

// TestStartProgressRepeatsEveryTenSeconds proves M5's ticker on the remote
// path: a step is said when it changes and again once ten seconds have passed
// on the client's clock, never on every poll.
func TestStartProgressRepeatsEveryTenSeconds(t *testing.T) {
	var b bytes.Buffer
	p := startProgressRemote(&b, "omlx/q")
	for _, s := range []time.Duration{0, 500 * time.Millisecond, 9 * time.Second, 10 * time.Second, 15 * time.Second, 21 * time.Second} {
		p(modelclient.JobView{ID: "j", State: modelapi.JobRunning, Step: "warming the model", Elapsed: s})
	}
	p(modelclient.JobView{ID: "j", State: modelapi.JobRunning, Step: "adding the route", Elapsed: 22 * time.Second})
	want := "wt: starting omlx/q — warming the model (0s)\n" +
		"wt: starting omlx/q — warming the model (10s)\n" +
		"wt: starting omlx/q — warming the model (21s)\n" +
		"wt: starting omlx/q — adding the route (22s)\n"
	if b.String() != want {
		t.Errorf("progress =\n%s\nwant\n%s", b.String(), want)
	}
}

// TestOthersQuestionFromResidents pins the others question and warning built
// from the wire's residents: one line per resident with its figure, the
// families the server could not read, and s offered only when one can be
// stopped.
func TestOthersQuestionFromResidents(t *testing.T) {
	others := []modelapi.Resident{{ID: "mlx/a", Label: "mlx/a", Figure: "memory use unknown; wt cannot stop it"}}
	q, canStop := othersQuestion("omlx/q", others, []string{"rec"}, nil)
	want := "starting omlx/q loads it beside models of other providers:\n" +
		"  mlx/a — memory use unknown; wt cannot stop it\n" +
		"  cannot tell whether rec is serving a model\n" +
		"keep them and start, or cancel? [k/N]"
	if q != want || canStop {
		t.Errorf("question = %q (canStop %v)\nwant %q", q, canStop, want)
	}
	w := othersWarning("omlx/q", others, []string{"rec"})
	if want := "wt: warning: starting omlx/q beside mlx/a (memory use unknown; wt cannot stop it), whatever rec is serving; pass --others=keep to silence this\n"; w != want {
		t.Errorf("warning = %q\nwant %q", w, want)
	}
}
