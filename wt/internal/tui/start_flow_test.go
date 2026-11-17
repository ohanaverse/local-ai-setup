package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// recvStart reads the next message off the in-flight start's channel with a
// 2s timeout, so a flow test fails instead of hanging when the engine never
// reports.
func recvStart(t *testing.T, m model) tea.Msg {
	t.Helper()
	select {
	case msg := <-m.start.ch:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a start message")
		return nil
	}
}

// updateMsg feeds msg to m.Update and returns the converted model, so flow
// tests stay on the concrete type.
func updateMsg(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// TestRunArgsReachModel verifies the Run(...) → model hand-off: the allowReplace
// argument (from --replace) and the -M pinned model land on the initial model.
// This is the one hop TestReplaceFlagGrantsPermissionToPinnedRow skips by
// assigning m.allowReplace directly; without it, a Run that stopped copying
// allowReplace would silently disable --replace while every other test passed.
func TestRunArgsReachModel(t *testing.T) {
	for _, allow := range []bool{true, false} {
		m := newRunModel(false, allow, "", "", "ollama/gemma4:9b", "", "", nil, themes.Theme{}, "", nil, nil, nil)
		if m.allowReplace != allow {
			t.Errorf("allowReplace=%v: model.allowReplace=%v", allow, m.allowReplace)
		}
		if m.pinnedModel != "ollama/gemma4:9b" {
			t.Errorf("model.pinnedModel=%q, want the -M pin", m.pinnedModel)
		}
	}
}

// routeUpdatedLine is the one line lifecycle prints when the launch-time check
// wrote a route; the tests script the stub to "print" it.
const routeUpdatedLine = "wt: LiteLLM route for omlx/qwen3.8 updated\n"

// signalWriter is a mutex-guarded buffer that closes first on its first
// write, so a test can wait for a goroutine to have printed something.
type signalWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	once  sync.Once
	first chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	w.once.Do(func() { close(w.first) })
	return n, err
}

func (w *signalWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestFlushRouteNotesAfterRunPrintsWhatIsLeft pins Run()'s flush for a launch
// that never happened (the user backed out at the resume prompt, or quit from
// the picker): notes recorded by a finished check are printed once the alt
// screen is gone, exactly once — and with no "waiting" line, since no check is
// in flight and there is nothing to wait for.
func TestFlushRouteNotesAfterRunPrintsWhatIsLeft(t *testing.T) {
	stubRouteNotes(t)
	pendingRouteNotes = routeUpdatedLine

	var out bytes.Buffer
	flushRouteNotesAfterRun(model{}, &out)
	flushRouteNotesAfterRun(nil, &out) // p.Run() can fail and return no model

	if out.String() != routeUpdatedLine {
		t.Fatalf("printed = %q, want the pending line once", out.String())
	}
}

// TestFlushRouteNotesEndsWithNewline pins that the flush always ends the line,
// so the agent's first output never continues a route warning.
func TestFlushRouteNotesEndsWithNewline(t *testing.T) {
	stubRouteNotes(t)
	pendingRouteNotes = "wt: LiteLLM route not updated: boom"

	var out bytes.Buffer
	flushRouteNotes(&out)
	flushRouteNotes(&out) // nothing pending: prints nothing

	if out.String() != "wt: LiteLLM route not updated: boom\n" {
		t.Fatalf("printed = %q", out.String())
	}
}

// TestRunAndWaitCmdPrintsRouteNotesAboveTheAgent pins where the captured route
// output goes on a launch: to stderr once the terminal is released, before the
// agent prints anything, and only once. Printed any later it would be buried
// in or after the agent's session; not at all, and a failed proxy restart
// would be invisible from the picker.
func TestRunAndWaitCmdPrintsRouteNotesAboveTheAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh differs on windows")
	}
	stubRouteNotes(t)
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	t.Cleanup(func() { pendingSummary, pendingSurveyState = prevSummary, prevSurvey })
	pendingRouteNotes = routeUpdatedLine

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Registered before the swap, so even a panic inside runAndWaitCmd cannot
	// leave the test process's stderr pointing at a closed pipe.
	t.Cleanup(func() { os.Stderr = old })
	os.Stderr = w
	msg := runAndWaitCmd(exec.Command("sh", "-c", "echo agent-output >&2"), "shell", config.Model{}, nil)()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)

	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("msg = %#v, want a clean launchDoneMsg", msg)
	}
	if want := routeUpdatedLine + "agent-output\n"; string(out) != want {
		t.Fatalf("stderr = %q, want %q", string(out), want)
	}
	if pendingRouteNotes != "" {
		t.Fatalf("pendingRouteNotes = %q, want cleared so Run() does not print it again", pendingRouteNotes)
	}
}

// startRowCatalog is a model server's catalog with one local model that must
// be started and one cloud model that launches.
func startRowCatalog() modelapi.Catalog {
	return modelapi.Catalog{Schema: 1, Generated: "2026-11-16T12:00:00Z", Rows: []modelapi.Row{
		wireRow("omlx/qwen3.8", "qwen", func(r *modelapi.Row) {
			r.Location, r.State, r.Action, r.Order = "local", modelapi.StateStopped, modelapi.ActionStart, 1
		}),
		wireRow("openrouter/glm-5.3", "glm", func(r *modelapi.Row) { r.Order = 2 }),
	}}
}

// remoteTable is the API picker's table loaded from f at 80x24, with a
// worktree chosen.
func remoteTable(t *testing.T, f *fakeModels) model {
	t.Helper()
	m := remoteModel(t, f, 80, 24)
	m.selectedPath = t.TempDir()
	m, cmd := m.enterModelPhaseForAgent()
	m = settle(t, m, cmd)
	if m.phase != phaseModel {
		t.Fatalf("phase = %v, status %q: want the table", m.phase, m.status)
	}
	return m
}

// enterRemote presses Enter on id's row of the API table. A start it begins is
// detached when the test ends, so no fake job outlives it.
func enterRemote(t *testing.T, m model, id string) (model, tea.Cmd) {
	t.Helper()
	i := indexOfID(m, id)
	if i < 0 {
		t.Fatalf("no row %s in %v", id, itemIDs(m))
	}
	m.models.Select(i)
	next, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if next.start != nil && next.start.cancel != nil {
		t.Cleanup(next.start.cancel)
	}
	return next, cmd
}

// detached is what the fake job returns once the flow stops waiting.
func detached(id string) (modelapi.JobStatus, error) {
	return modelapi.JobStatus{}, &modelclient.DetachedError{JobID: id}
}

// keyRune is the key message for one printable key.
func keyRune(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// TestStartFlowQueuedAndProgress pins the start screen on the model server: a
// queued job says it waits its turn, a running one shows the server's step,
// and each shows the job id and the elapsed time the client's own clock
// measured (modelclient.JobView), so the user can tell a slow start from a
// stuck one.
func TestStartFlowQueuedAndProgress(t *testing.T) {
	step := make(chan struct{})
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
		prog(modelclient.JobView{ID: "j1", State: modelapi.JobQueued, Queued: true, Elapsed: 2 * time.Second})
		select {
		case <-step:
		case <-ctx.Done():
			return detached("j1")
		}
		prog(modelclient.JobView{ID: "j1", State: modelapi.JobRunning, Step: "loading weights", Elapsed: 7 * time.Second})
		<-ctx.Done()
		return detached("j1")
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	if m.phase != phaseStarting || m.start == nil {
		t.Fatalf("phase = %v: want the start screen", m.phase)
	}
	m, _ = updateMsg(m, recvStart(t, m))
	v := plain(m)
	for _, want := range []string{"Starting omlx/qwen3.8", "queued behind another start or stop on the model server", "(2s)", "job j1"} {
		if !strings.Contains(v, want) {
			t.Errorf("queued view lacks %q:\n%s", want, v)
		}
	}
	close(step)
	m, _ = updateMsg(m, recvStart(t, m))
	v = plain(m)
	for _, want := range []string{"loading weights", "(7s)", "job j1"} {
		if !strings.Contains(v, want) {
			t.Errorf("running view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "queued") {
		t.Errorf("running view still says queued:\n%s", v)
	}
}

// TestReplaceDialogFromOccupiedProblem pins the replace question on the model
// server: an occupied problem opens the same dialog the registry path opens,
// its question built from the problem's occupants, Cancel first and under the
// cursor (default No); "Replace and start" posts again with allow_replace, and
// a second refusal that asks only what was answered is reported, never asked
// again.
func TestReplaceDialogFromOccupiedProblem(t *testing.T) {
	occ := &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeOccupied, Message: "omlx is serving omlx/old", Occupants: []string{"omlx/old"}}}
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(context.Context, int, modelapi.StartRequest, modelclient.Progress) (modelapi.JobStatus, error) {
		return modelapi.JobStatus{}, occ
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	if m.phase != phaseReplaceConfirm || m.replace == nil {
		t.Fatalf("phase = %v, status %q: want the replace dialog", m.phase, m.status)
	}
	if want := "Starting omlx/qwen3.8 will stop:\n  omlx/old"; m.replace.header != want {
		t.Errorf("header = %q, want %q", m.replace.header, want)
	}
	if got := choiceTitles(m); !reflect.DeepEqual(got, []string{"Cancel", "Replace and start"}) || m.replace.choices.Index() != 0 {
		t.Errorf("choices = %v at %d, want Cancel first and under the cursor", got, m.replace.choices.Index())
	}
	m = chooseTitled(t, m, "Replace and start")
	if m.phase != phaseStarting {
		t.Fatalf("phase = %v after Replace and start, want the start screen", m.phase)
	}
	m, _ = updateMsg(m, recvStart(t, m))
	if m.phase == phaseReplaceConfirm {
		t.Fatal("the same question was asked twice")
	}
	if m.phase != phaseModel || !strings.Contains(m.status, "omlx is serving omlx/old") {
		t.Errorf("phase %v, status %q: want the table and the refusal reported", m.phase, m.status)
	}
	reqs := f.startReqs()
	if len(reqs) != 2 || reqs[0].AllowReplace || !reqs[1].AllowReplace {
		t.Errorf("start requests = %+v, want a first without and a second with allow_replace", reqs)
	}
	// The dialog asks, never the job driver: RunStart is handed no Answerer,
	// so every question comes back as the job's problem.
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.answerers {
		if a != nil {
			t.Errorf("RunStart call %d was handed an Answerer %T, want nil", i+1, a)
		}
	}
}

// TestStartCtrlCDetachesAndKeepsJob pins R8's detach: ctrl+c on the start
// screen once the job has an id goes back to the table, names the job that
// keeps running, and never cancels it; the client session lets go of the
// model, and the job's late result is not acted on.
func TestStartCtrlCDetachesAndKeepsJob(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
		prog(modelclient.JobView{ID: "j7", State: modelapi.JobRunning, Step: "loading"})
		<-ctx.Done()
		return detached("j7")
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	ch := m.start.ch
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.phase != phaseModel || m.start != nil {
		t.Fatalf("phase %v, start %v: want the table", m.phase, m.start)
	}
	if want := "detached — job j7 keeps running on the model server"; !strings.Contains(m.status, want) {
		t.Errorf("status = %q, want %q", m.status, want)
	}
	if cmd == nil {
		t.Fatal("detach returned no command to let go of the model")
	}
	if msg := cmd(); msg == tea.Quit() {
		t.Fatal("ctrl+c with a job id quit wt")
	}
	// The job's own end, which arrives later, is a stale message.
	for msg := range ch {
		m, _ = updateMsg(m, msg)
	}
	if m.phase != phaseModel || !strings.Contains(m.status, "detached") {
		t.Errorf("the job's late result moved the picker: phase %v, status %q", m.phase, m.status)
	}
	if got := f.cancels(); len(got) != 0 {
		t.Errorf("CancelJob was called %v on a detach", got)
	}
	if h := f.history(); !slices.Contains(h, "remove omlx/qwen3.8") {
		t.Errorf("calls = %v, want the client session to drop the model", h)
	}
}

// TestStartCancelKey pins the explicit cancel: c on the start screen cancels
// the job on the server (CancelJob with its id), the screen says it is
// cancelling, and the job's cancelled end returns to the table.
func TestStartCancelKey(t *testing.T) {
	gone := make(chan struct{})
	f := &fakeModels{cat: startRowCatalog()}
	f.onCancel = func(string) { close(gone) }
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
		prog(modelclient.JobView{ID: "j7", State: modelapi.JobRunning, Step: "loading"})
		select {
		case <-gone:
			return modelapi.JobStatus{ID: "j7", State: modelapi.JobCancelled}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeCancelled, Message: "job j7 cancelled"}}
		case <-ctx.Done():
			return detached("j7")
		}
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	m, cmd := updateMsg(m, keyRune('c'))
	if cmd == nil {
		t.Fatal("c with a job id returned no command")
	}
	if got := f.cancels(); len(got) != 0 {
		t.Fatalf("CancelJob ran inside Update: %v", got)
	}
	m, _ = updateMsg(m, cmd())
	if got := f.cancels(); !reflect.DeepEqual(got, []string{"j7"}) {
		t.Errorf("cancelled = %v, want [j7]", got)
	}
	if v := plain(m); !strings.Contains(v, "cancelling") {
		t.Errorf("view after c = %q, want it to say it is cancelling", v)
	}
	m, _ = updateMsg(m, recvStart(t, m))
	if m.phase != phaseModel || !strings.Contains(m.status, "cancelled") {
		t.Errorf("phase %v, status %q: want the table and a cancelled status", m.phase, m.status)
	}
}

// TestStartFooterShowsCancelOnlyWithAJobID pins T5 on the start screen: c is
// shown exactly when it would act. Before the job has an id the footer has no
// c, pressing c does nothing and sets no status; once the id arrives the
// footer reads `ctrl+c detach · c cancel job` (T6) and c acts. The footer and
// the key read one predicate.
func TestStartFooterShowsCancelOnlyWithAJobID(t *testing.T) {
	proceed := make(chan struct{})
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
		select {
		case <-proceed:
		case <-ctx.Done():
			return modelapi.JobStatus{}, ctx.Err()
		}
		prog(modelclient.JobView{ID: "j9", State: modelapi.JobRunning})
		<-ctx.Done()
		return detached("j9")
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	v := plain(m)
	if strings.Contains(v, "c cancel job") || !strings.Contains(v, "ctrl+c quit") {
		t.Errorf("footer before the id = %q, want ctrl+c quit and no c", v)
	}
	n, cmd := updateMsg(m, keyRune('c'))
	if cmd != nil || n.status != "" || n.start == nil || n.start.cancelSent {
		t.Errorf("c before the id: cmd %v, status %q: want nothing at all", cmd != nil, n.status)
	}
	close(proceed)
	m, _ = updateMsg(m, recvStart(t, m))
	if v := plain(m); !strings.Contains(v, "ctrl+c detach · c cancel job") {
		t.Errorf("footer with an id = %q, want %q", v, "ctrl+c detach · c cancel job")
	}
	if _, cmd := updateMsg(m, keyRune('c')); cmd == nil {
		t.Error("c with a job id did nothing")
	}
}

// TestCtrlCBeforeAJobIDLeaves pins T1's amended exception: before the job has
// an id there is nothing to detach from, so ctrl+c leaves wt, and the start's
// late result can never launch an agent.
func TestCtrlCBeforeAJobIDLeaves(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, _ modelclient.Progress) (modelapi.JobStatus, error) {
		<-ctx.Done()
		return modelapi.JobStatus{}, ctx.Err()
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	ch, cancel := m.start.ch, m.start.cancel
	m, cmd := updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("ctrl+c before the job has an id did not quit")
	}
	cancel()
	for msg := range ch {
		m, _ = updateMsg(m, msg)
	}
	if m.committed {
		t.Error("a start quit before it had an id went on to launch")
	}
}

// TestStartScreenTextCannotForgeLines pins M11 on the start flow: a step, a
// job id and a problem message carrying ESC and newlines (a fake client hands
// them over raw, which the real one already cleans) render as single
// printable lines on the start screen, in the dialog's question and on the
// status line.
func TestStartScreenTextCannotForgeLines(t *testing.T) {
	f := &fakeModels{cat: startRowCatalog()}
	f.startFn = func(ctx context.Context, n int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
		switch n {
		case 1:
			prog(modelclient.JobView{ID: "j1\x1b[2J", State: modelapi.JobRunning, Step: "loading\n\x1b[2Jforged line\r"})
			<-ctx.Done()
			return detached("j1")
		case 2:
			return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeOccupancyUnknown, Message: "cannot tell\n\x1b[31mforged question"}}
		}
		return modelapi.JobStatus{}, &modelclient.JobError{Problem: modelapi.Problem{Code: modelapi.CodeFailed, Message: "load failed\n\x1b]0;pwn\x07forged status"}}
	}
	m := remoteTable(t, f)
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	view := m.View()
	if strings.Contains(view, "\x1b[2J") {
		t.Errorf("the start screen carries the server's escape sequence: %q", view)
	}
	var found bool
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(line, "loading") {
			found = true
			if !strings.Contains(line, "forged line") {
				t.Errorf("the step is split across lines: %q", line)
			}
		}
	}
	if !found {
		t.Fatalf("the step is not on the start screen: %q", ansi.Strip(view))
	}
	// Detach, then start again: the problem's message is the dialog's question.
	m, _ = updateMsg(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	m, _ = enterRemote(t, m, "omlx/qwen3.8")
	m, _ = updateMsg(m, recvStart(t, m))
	if m.phase != phaseReplaceConfirm {
		t.Fatalf("phase = %v, want the dialog", m.phase)
	}
	if strings.ContainsAny(m.replace.header, "\n\x1b") || !strings.Contains(m.replace.header, "forged question") {
		t.Errorf("dialog question = %q, want one printable line", m.replace.header)
	}
	// The failure's message is one status line.
	m = chooseTitled(t, m, "Replace and start")
	m, _ = updateMsg(m, recvStart(t, m))
	if strings.ContainsAny(m.status, "\n\x1b\x07") || !strings.Contains(m.status, "forged status") {
		t.Errorf("status = %q, want one printable line", m.status)
	}
}

// TestCancelThatLosesTheRaceDoesNotLaunch pins that c means "no launch" even
// when the job finishes first: whether the server took the cancel too late
// or refused it because the job had already finished, a start that then
// succeeds goes back to the table saying the model started and the launch
// was called off (the registry path's words, with the job id), takes the
// model out of the client session, and launches no agent.
func TestCancelThatLosesTheRaceDoesNotLaunch(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("refused=%v", refused), func(t *testing.T) {
			finish := make(chan struct{})
			f := &fakeModels{cat: startRowCatalog()}
			f.onCancel = func(string) { close(finish) }
			if refused {
				f.cancelErr = errors.New("job j7 already finished")
			}
			f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
				prog(modelclient.JobView{ID: "j7", State: modelapi.JobRunning, Step: "loading"})
				select {
				case <-finish:
					return modelapi.JobStatus{ID: "j7", State: modelapi.JobSucceeded}, nil
				case <-ctx.Done():
					return detached("j7")
				}
			}
			m := remoteTable(t, f)
			m.hooks.gateway = func(string, config.Model, string, bool, agents.Gateway, []config.Model, []string) (*exec.Cmd, agents.LaunchInfo, error) {
				t.Error("an agent was built after the user cancelled")
				return nil, agents.LaunchInfo{}, errors.New("no")
			}
			m, _ = enterRemote(t, m, "omlx/qwen3.8")
			m, _ = updateMsg(m, recvStart(t, m))
			m, cmd := updateMsg(m, keyRune('c'))
			m, _ = updateMsg(m, cmd())
			m, cmd = updateMsg(m, recvStart(t, m))
			if m.phase != phaseModel || m.committed {
				t.Fatalf("phase %v, committed %v: want the table and no launch", m.phase, m.committed)
			}
			if want := "started omlx/qwen3.8; launch cancelled (job j7)"; !strings.Contains(m.status, want) {
				t.Errorf("status = %q, want %q", m.status, want)
			}
			if cmd == nil {
				t.Fatal("no command to take the model out of the client session")
			}
			if msg := cmd(); msg != nil {
				t.Errorf("drop command returned %#v, want nothing to launch", msg)
			}
			if h := f.history(); !slices.Contains(h, "remove omlx/qwen3.8") {
				t.Errorf("calls = %v, want the model dropped from the client session", h)
			}
		})
	}
}

// TestDetachedStartGoroutineNeverBlocks pins that a start the flow stopped
// waiting for cannot leak its goroutine: after a detach nothing reads the
// start's channel, so with the channel full the job's final result is
// dropped instead of blocking forever. The test cancels only once the channel
// holds all 16 progress messages, so on a goroutine that blocks on a full
// channel the cancel always finds it blocked and the test fails every time.
func TestDetachedStartGoroutineNeverBlocks(t *testing.T) {
	f := &fakeModels{}
	f.startFn = func(ctx context.Context, _ int, _ modelapi.StartRequest, prog modelclient.Progress) (modelapi.JobStatus, error) {
		for i := 0; i < 16; i++ {
			prog(modelclient.JobView{ID: "j1", State: modelapi.JobRunning})
		}
		<-ctx.Done()
		return detached("j1")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, finished := runRemoteStart(ctx, f, &clientHold{client: f}, modelapi.StartRequest{Model: "omlx/qwen3.8"}, 1)
	waitStarts(t, f, 1)
	for deadline := time.Now().Add(5 * time.Second); len(ch) < 16; runtime.Gosched() {
		if time.Now().After(deadline) {
			t.Fatalf("the progress channel holds %d messages, want 16", len(ch))
		}
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the detached start's goroutine is still blocked on its channel")
	}
}
