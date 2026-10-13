package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestProfilePostExitSkipsSurveyAndPriceNotice pins what follows a
// slot-profile session: the refcount lines are released, the stop picker runs,
// the summary is printed, and neither the survey nor the price notice is. A
// profile is not one priced model: a survey would be stored under an id no
// registry model has, and the notice would name prices the session never used.
func TestProfilePostExitSkipsSurveyAndPriceNotice(t *testing.T) {
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	prevRun, prevNotice, prevRelease, prevStop := runSurvey, emitPriceNotice, releaseSession, runStopPhase
	t.Cleanup(func() {
		pendingSummary, pendingSurveyState = prevSummary, prevSurvey
		runSurvey, emitPriceNotice, releaseSession, runStopPhase = prevRun, prevNotice, prevRelease, prevStop
	})

	var order []string
	pendingSummary = "wt: claude · profile/cloud · 1m0s"
	pendingSurveyState = pendingSurvey{agent: "claude", m: config.Model{ID: "profile/cloud"}, profile: true}
	releaseSession = func() { order = append(order, "release") }
	runSurvey = func(string, config.Model) string { order = append(order, "survey"); return "STATS-BLOCK" }
	runStopPhase = func(*config.Config) { order = append(order, "picker") }
	emitPriceNotice = func(*config.Config) { order = append(order, "notice") }

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	printPendingSummaryAndSurvey(&config.Config{})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if got := strings.Join(order, ","); got != "release,picker" {
		t.Errorf("order = %s, want release,picker (no survey, no price notice)", got)
	}
	if s := string(out); !strings.Contains(s, "wt: claude · profile/cloud · 1m0s") || strings.Contains(s, "STATS-BLOCK") {
		t.Errorf("stdout = %q, want the summary line and no survey stats", s)
	}
	if pendingSurveyState.agent != "" || pendingSurveyState.profile || pendingSummary != "" {
		t.Errorf("pending state not reset: %+v %q", pendingSurveyState, pendingSummary)
	}
}

// TestWithSlotProfilesCarriesChecksAndApplier pins the one place Run's slot
// argument reaches the TUI: the checks land on the model, where tableFor reads
// them, and the applier lands in the slotApplier seam. Dropping either makes
// the feature vanish without an error: no profile rows, or every profile
// launch aborting with "no slot-profile applier".
func TestWithSlotProfilesCarriesChecksAndApplier(t *testing.T) {
	old := slotApplier
	t.Cleanup(func() { slotApplier = old })
	called := false
	apply := func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		called = true
		return nil, nil
	}
	checks := []profiles.SlotCheck{{Index: 0, Usable: true, Profile: profiles.SlotProfile{Name: "cloud", Agent: "claude"}}}

	m := withSlotProfiles(newRunModel(false, false, "claude", "", "", "", nil, themes.Default, ".", &config.Config{}), SlotProfiles{Checks: checks, Apply: apply})

	if len(m.slotChecks) != 1 || m.slotChecks[0].Profile.Name != "cloud" {
		t.Fatalf("slotChecks = %+v, want the one check handed in", m.slotChecks)
	}
	if slotApplier == nil {
		t.Fatal("slotApplier is nil: a profile launch would abort")
	}
	if _, _ = slotApplier(nil, "", catalog.Row{}); !called {
		t.Fatal("slotApplier is not the function handed in")
	}
}

// slotStub scripts the slot-flow seams and records, in order, what a profile
// launch asked of them: "ensure:<id>,<id>:owed=<owedIn>", "restart", "wait"
// (for a proxy restart in flight), "probe", "settle", "build". A stage runs in a command, which one test runs
// on its own goroutine, so every access takes the lock.
type slotStub struct {
	mu     sync.Mutex
	events []string

	ensureOwed   bool   // a write here changed config.yaml
	ensureOutput string // written to the writer the stage handed in
	ensureErr    error
	restartErr   error
	probeErr     error
	buildErr     error
	cmd          *exec.Cmd // what buildSlotCmd returns; default exec.Command("true")

	// ensureEntered and ensureRelease, when set, hold the first route write
	// open: ensureEntered is closed once it is inside, and it returns only
	// when ensureRelease is closed.
	ensureEntered, ensureRelease chan struct{}
	entered                      sync.Once
}

func (s *slotStub) add(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

// got is the recorded events, comma-joined.
func (s *slotStub) got() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.events, ",")
}

// stubSlotSeams swaps ensureSlotRoutes, restartProxyNow, waitPendingRoutes,
// probeProxyNow, settleRoutesNow and buildSlotCmd for recorders scripted by the returned
// slotStub, and restores them on cleanup. Nothing real is touched: no
// config.yaml, no proxy, no agent binary.
func stubSlotSeams(t *testing.T) *slotStub {
	t.Helper()
	s := &slotStub{}
	oldEnsure, oldRestart, oldProbe, oldSettle, oldBuild := ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow, buildSlotCmd
	oldWait := waitPendingRoutes
	t.Cleanup(func() {
		ensureSlotRoutes, restartProxyNow, probeProxyNow, settleRoutesNow, buildSlotCmd = oldEnsure, oldRestart, oldProbe, oldSettle, oldBuild
		waitPendingRoutes = oldWait
	})
	waitPendingRoutes = func() { s.add("wait") }
	ensureSlotRoutes = func(_ context.Context, out io.Writer, _ *config.Config, models []config.Model, owedIn bool) (bool, error) {
		ids := make([]string, len(models))
		for i, m := range models {
			ids[i] = m.ID
		}
		event := "ensure:" + strings.Join(ids, ",") + ":owed=false"
		if owedIn {
			event = "ensure:" + strings.Join(ids, ",") + ":owed=true"
		}
		s.add(event)
		if s.ensureEntered != nil {
			s.entered.Do(func() {
				close(s.ensureEntered)
				<-s.ensureRelease
			})
		}
		if out != nil && s.ensureOutput != "" {
			_, _ = io.WriteString(out, s.ensureOutput)
		}
		if s.ensureErr != nil {
			return false, s.ensureErr
		}
		return owedIn || s.ensureOwed, nil
	}
	restartProxyNow = func(context.Context, *config.Config) error {
		s.add("restart")
		return s.restartErr
	}
	probeProxyNow = func(context.Context, *config.Config) error {
		s.add("probe")
		return s.probeErr
	}
	settleRoutesNow = func(context.Context, *config.Config) { s.add("settle") }
	buildSlotCmd = func(profiles.SlotLaunch, string, bool, *config.Config, []string) (*exec.Cmd, error) {
		s.add("build")
		if s.buildErr != nil {
			return nil, s.buildErr
		}
		if s.cmd != nil {
			return s.cmd, nil
		}
		return exec.Command("true"), nil
	}
	return s
}

// stubSlotApplier sets the slotApplier seam for one test.
func stubSlotApplier(t *testing.T, apply SlotProfileApplier) {
	t.Helper()
	old := slotApplier
	slotApplier = apply
	t.Cleanup(func() { slotApplier = old })
}

// The profiles the flow tests use, over slotTestConfig's models.
func cloudProfile() profiles.SlotCheck {
	return slotCheckFor(0, "cloud", "opus", map[string]string{"opus": "openrouter/a", "sonnet": "openrouter/b"})
}

func mixedProfile() profiles.SlotCheck {
	return slotCheckFor(1, "mixed", "opus", map[string]string{"opus": "openrouter/a", "haiku": "omlx/qwen3.8"})
}

// slotFlowFixture is the claude model picker at 80x24 with the given profiles,
// a worktree to launch in, and the pending notes, summary and survey state
// cleared for the test.
func slotFlowFixture(t *testing.T, snap localmodels.Snapshot, checks ...profiles.SlotCheck) model {
	t.Helper()
	stubInventory(t, snap)
	stubRouteNotes(t)
	prevSummary, prevSurvey := pendingSummary, pendingSurveyState
	pendingSummary, pendingSurveyState = "", pendingSurvey{}
	t.Cleanup(func() { pendingSummary, pendingSurveyState = prevSummary, prevSurvey })
	m := slotPicker(t, slotTestConfig(), checks, 80, 24)
	m.agent = "claude"
	m.selectedPath = t.TempDir()
	return m
}

// enterProfileRow puts the cursor on the row with id and presses Enter.
func enterProfileRow(t *testing.T, m model, id string) (model, tea.Cmd) {
	t.Helper()
	idx := indexOfID(m, id)
	if idx < 0 {
		t.Fatalf("no row %s in %v", id, itemIDs(m))
	}
	m.models.Select(idx)
	return updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
}

// slotStageFrom splits the batch a slot stage returned: the stage's command
// and its one-second tick. The tick is never run here; it would sleep.
func slotStageFrom(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command: a slot stage must run in a command, off the update goroutine")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("command = %T (%d), want a batch of the stage and its tick", batch, len(batch))
	}
	return batch[0]
}

// assertNothingRecorded fails when a launch wrote any bookkeeping under the
// test's state directory.
func assertNothingRecorded(t *testing.T) {
	t.Helper()
	for _, name := range []string{"last-picked.state", "rotation.state", "usage.jsonl", "refcount.jsonl"} {
		if _, err := os.Stat(filepath.Join(config.Dir(), name)); err == nil {
			t.Errorf("%s was written, want nothing recorded for a launch that did not happen", name)
		}
	}
}

// TestEnterOnProfileRowRunsTheStepsThenLaunches is the launch row's whole
// path. Enter enters phaseSlot and returns at once; the route writes, the one
// restart and the probe run in the command; its message builds the command
// and runs the agent through the slot applier, never the automatic one. The
// session is recorded under the profile id with one refcount line per slot
// model, and rotation.state is left alone, so the next rotated launch is
// unchanged. A regression here launches Claude Code with unmapped slots or
// moves the user's rotation.
func TestEnterOnProfileRowRunsTheStepsThenLaunches(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed = true
	applied := ""
	stubSlotApplier(t, func(_ *exec.Cmd, agent string, row catalog.Row) (func() error, error) {
		applied = agent + ":" + row.Model.ID
		return func() error { return nil }, nil
	})
	oldAuto := profileApplier
	profileApplier = func(*exec.Cmd, string, config.Model) (func() error, error) {
		t.Error("the automatic-profile applier ran for a slot-profile launch")
		return nil, nil
	}
	t.Cleanup(func() { profileApplier = oldAuto })

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	if preparing.phase != phaseSlot || preparing.slot == nil {
		t.Fatalf("phase = %v slot = %v, want phaseSlot with a launch in flight", preparing.phase, preparing.slot)
	}
	if got := stub.got(); got != "" {
		t.Fatalf("events after Enter = %q, want none: the stage must not run on the update goroutine", got)
	}
	if view := preparing.View(); !strings.Contains(view, "Preparing profile/cloud") {
		t.Errorf("view = %q, want the preparing screen naming the profile", view)
	}

	launching, launch := updateMsg(preparing, slotStageFrom(t, cmd)())
	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,restart,wait,probe,build"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if launching.slot != nil || launching.phase != phaseModel || launch == nil {
		t.Fatalf("slot = %v phase = %v cmd nil = %v, want the flow ended and a run command", launching.slot, launching.phase, launch == nil)
	}
	if _, ok := launch().(launchDoneMsg); !ok {
		t.Fatal("the run command did not return launchDoneMsg")
	}

	if applied != "claude:profile/cloud" {
		t.Errorf("slot applier saw %q, want claude:profile/cloud", applied)
	}
	if b, err := os.ReadFile(filepath.Join(config.Dir(), "last-picked.state")); err != nil || string(b) != "profile/cloud\n" {
		t.Errorf("last-picked.state = %q, %v, want the profile id", b, err)
	}
	if _, err := os.Stat(filepath.Join(config.Dir(), "rotation.state")); err == nil {
		t.Error("rotation.state was written: a profile launch must not move the rotation pointer")
	}
	if b, _ := os.ReadFile(filepath.Join(config.Dir(), "usage.jsonl")); !strings.Contains(string(b), `"model_id":"profile/cloud"`) {
		t.Errorf("usage.jsonl = %q, want one event under the profile id", b)
	}
	counts := refcount.NewStore().Counts([]string{"openrouter/a", "openrouter/b", "profile/cloud"})
	if counts["openrouter/a"] != 1 || counts["openrouter/b"] != 1 || counts["profile/cloud"] != 0 {
		t.Errorf("refcounts = %v, want one line per slot model and none for the profile id", counts)
	}
	if !pendingSurveyState.profile || pendingSurveyState.m.ID != "profile/cloud" || !strings.Contains(pendingSummary, "profile/cloud") {
		t.Errorf("pending = %+v %q, want a profile session named in the summary", pendingSurveyState, pendingSummary)
	}
}

// TestProfileLaunchWithEveryRouteInPlaceRestartsNothing pins success
// criterion 5: when no route write changed config.yaml the proxy is not
// restarted, only probed. A restart on every profile launch would cost each
// one 10 to 20 seconds and cut every other session's requests.
func TestProfileLaunchWithEveryRouteInPlaceRestartsNothing(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stub := stubSlotSeams(t)

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	updateMsg(preparing, slotStageFrom(t, cmd)())

	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,wait,probe,build"; got != want {
		t.Fatalf("events = %q, want %q (no restart, and the probe only after a restart in flight was waited for)", got, want)
	}
}

// TestEnterOnBlockedProfileRowShowsItsReason pins the blocked row: Enter
// shows the validation reason and stays on the picker, with nothing written
// and nothing probed. A blocked profile that began its launch would write
// routes for a profile that can never run.
func TestEnterOnBlockedProfileRowShowsItsReason(t *testing.T) {
	broken := slotCheckFor(0, "broken", "opus", map[string]string{"opus": "openrouter/gone"})
	m := slotFlowFixture(t, idleOmlxSnapshot(), broken)
	stub := stubSlotSeams(t)

	got, cmd := enterProfileRow(t, m, "profile/broken")

	if got.phase != phaseModel || got.slot != nil || cmd != nil {
		t.Fatalf("phase = %v slot = %v cmd nil = %v, want the picker and no command", got.phase, got.slot, cmd == nil)
	}
	if want := "slot opus: openrouter/gone is not in the registry"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}
	if events := stub.got(); events != "" {
		t.Errorf("events = %q, want none", events)
	}
}

// TestSlotStageFailuresReturnToThePicker is the failure table's TUI half:
// every step that can fail before the agent starts ends on the picker with
// the reason as its status, no agent command run and nothing recorded. A
// failure that fell through to a launch would start Claude Code with slots
// the proxy cannot serve.
func TestSlotStageFailuresReturnToThePicker(t *testing.T) {
	const ensured = "ensure:openrouter/a,openrouter/b:owed=false"
	cases := []struct {
		name    string
		arrange func(m *model, s *slotStub)
		status  string
		events  string
	}{
		{
			name: "the profile no longer resolves",
			arrange: func(m *model, _ *slotStub) {
				m.cfg.Models = slices.DeleteFunc(slices.Clone(m.cfg.Models), func(x config.Model) bool { return x.ID == "openrouter/b" })
			},
			status: "slot sonnet: openrouter/b is not in the registry",
			events: "",
		},
		{
			name:    "a passthrough --model in its = form",
			arrange: func(m *model, _ *slotStub) { m.extraArgs = []string{"--model=opus"} },
			status:  "a slot profile sets --model itself: remove it from the arguments after --",
			events:  "",
		},
		{
			name: "a route that cannot be written",
			arrange: func(_ *model, s *slotStub) {
				s.ensureErr = &lifecycle.RouteEnsureError{ModelID: "openrouter/b", Err: errors.New("LiteLLM config not found")}
			},
			status: "slot sonnet: LiteLLM route for openrouter/b not written: LiteLLM config not found",
			events: ensured + ",wait",
		},
		{
			name: "a proxy restart that fails",
			arrange: func(_ *model, s *slotStub) {
				s.ensureOwed, s.restartErr = true, errors.New("LiteLLM proxy not ready at http://localhost:4000: timed out")
			},
			status: "LiteLLM proxy not ready at http://localhost:4000: timed out",
			events: ensured + ",restart",
		},
		{
			name: "a proxy that does not answer",
			arrange: func(_ *model, s *slotStub) {
				s.probeErr = errors.New("the LiteLLM proxy does not answer at http://localhost:4000: connection refused")
			},
			status: "the LiteLLM proxy does not answer at http://localhost:4000: connection refused",
			events: ensured + ",wait,probe",
		},
		{
			name:    "a command that cannot be built",
			arrange: func(_ *model, s *slotStub) { s.buildErr = errors.New("agent claude not installed") },
			status:  "launch failed: agent claude not installed",
			events:  ensured + ",wait,probe,build",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
			stub := stubSlotSeams(t)
			stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
				t.Error("the applier ran for a launch that was aborted")
				return nil, nil
			})
			c.arrange(&m, stub)

			preparing, cmd := enterProfileRow(t, m, "profile/cloud")
			back, after := updateMsg(preparing, slotStageFrom(t, cmd)())

			if back.phase != phaseModel || back.slot != nil {
				t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", back.phase, back.slot)
			}
			if back.status != c.status {
				t.Errorf("status = %q, want %q", back.status, c.status)
			}
			if got := stub.got(); got != c.events {
				t.Errorf("events = %q, want %q", got, c.events)
			}
			// What comes back is the table refresh, never a run command.
			if after == nil {
				t.Fatal("no command: the picker must re-probe after an aborted launch")
			}
			if _, ok := after().(tableRefreshedMsg); !ok {
				t.Error("the command after an abort is not the table refresh")
			}
			assertNothingRecorded(t)
		})
	}
}

// TestSlotApplierErrorAbortsTheLaunch pins fail-closed at step 9: an applier
// error, or no applier at all, runs no agent, records nothing, and comes back
// as a slotAbortMsg that returns to the picker with the error. The automatic
// applier degrades to an unprofiled launch here; a slot profile must not.
func TestSlotApplierErrorAbortsTheLaunch(t *testing.T) {
	cases := []struct {
		name  string
		apply SlotProfileApplier
		want  string
	}{
		{"the applier fails", func(*exec.Cmd, string, catalog.Row) (func() error, error) {
			return nil, errors.New("wrapper little-coder not found on PATH")
		}, "wrapper little-coder not found on PATH"},
		{"no applier was handed in", nil, "no slot-profile applier"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
			stubSlotApplier(t, c.apply)
			sentinel := filepath.Join(t.TempDir(), "agent-ran")
			row := catalog.Row{Model: config.Model{ID: "profile/cloud"}, Profile: &catalog.ProfileRow{}}

			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			oldStderr := os.Stderr
			os.Stderr = w
			msg := runSlotAndWaitCmd(exec.Command("touch", sentinel), "claude", row)()
			os.Stderr = oldStderr
			_ = w.Close()
			stderr, _ := io.ReadAll(r)

			abort, ok := msg.(slotAbortMsg)
			if !ok || abort.err == nil || abort.err.Error() != c.want {
				t.Fatalf("message = %#v, want slotAbortMsg{%q}", msg, c.want)
			}
			if !strings.Contains(string(stderr), "wt: "+c.want) {
				t.Errorf("stderr = %q, want the error printed while the terminal is the user's", stderr)
			}
			if _, err := os.Stat(sentinel); err == nil {
				t.Error("the agent command ran after the applier failed")
			}
			assertNothingRecorded(t)
			if pendingSummary != "" || pendingSurveyState.agent != "" {
				t.Errorf("pending = %q %+v, want nothing: no session ran", pendingSummary, pendingSurveyState)
			}

			back, _ := updateMsg(m, msg)
			if back.phase != phaseModel || back.status != "launch failed: "+c.want {
				t.Errorf("phase = %v status = %q, want the picker with the apply error", back.phase, back.status)
			}
		})
	}
}

// TestCtrlCDuringSlotStageNeverLaunches is Review Focus 5. The user presses
// ctrl+c while the profile's route write is in flight. The stage runs on its
// own goroutine, as the tea runtime runs it, and Run()'s after-run flush
// settles the same stage from another. The flush announces the wait and
// returns only once the stage, its proxy restart included, has finished; the
// stage's message, delivered in the gap before QuitMsg, launches nothing; and
// no usage event, last-picked.state or refcount line exists afterwards.
// Without this wt would exit under a restart, or start an agent and record a
// session after the user quit.
func TestCtrlCDuringSlotStageNeverLaunches(t *testing.T) {
	const updated = "wt: LiteLLM route for openrouter/a updated\n"
	m := slotFlowFixture(t, idleOmlxSnapshot(), cloudProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed, stub.ensureOutput = true, updated
	stub.ensureEntered, stub.ensureRelease = make(chan struct{}), make(chan struct{})
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		t.Error("the applier ran after the user quit")
		return nil, nil
	})

	preparing, cmd := enterProfileRow(t, m, "profile/cloud")
	quitting, quit := updateMsg(preparing, tea.KeyMsg{Type: tea.KeyCtrlC})
	if preparing.slot.quitting {
		t.Fatal("ctrl+c wrote through to the model Update was given")
	}
	if quit == nil {
		t.Fatal("ctrl+c returned no command, want tea.Quit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c did not quit")
	}
	stage := slotStageFrom(t, cmd)

	timeout := time.After(10 * time.Second)
	await := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}

	stageDone := make(chan struct{})
	var done tea.Msg
	go func() {
		defer close(stageDone)
		done = stage()
	}()
	await(stub.ensureEntered, "the stage to reach the route write")

	out := &signalWriter{first: make(chan struct{})}
	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		flushRouteNotesAfterRun(quitting, out)
	}()
	await(out.first, "the flush to announce the wait")
	select {
	case <-flushDone:
		t.Fatal("the flush returned while the route write was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(stub.ensureRelease)
	await(stageDone, "the stage")
	await(flushDone, "the flush")

	if got, want := out.String(), routeWaitingLine+updated; got != want {
		t.Fatalf("printed = %q, want %q", got, want)
	}
	if got, want := stub.got(), "ensure:openrouter/a,openrouter/b:owed=false,restart,wait,probe"; got != want {
		t.Fatalf("events = %q, want %q: the stage runs once and finishes its restart", got, want)
	}

	final, launch := updateMsg(quitting, done)
	if launch != nil {
		t.Fatal("the stage's message after ctrl+c returned a command: the agent would start after the quit")
	}
	if final.phase != phaseSlot || final.slot == nil {
		t.Fatalf("phase = %v slot = %v, want the quitting flow left as it was", final.phase, final.slot)
	}
	if strings.Contains(stub.got(), "build") {
		t.Fatal("the agent command was built after the quit")
	}
	assertNothingRecorded(t)
}

// TestQuitDuringStartRowPrepareSettlesTheOwedRestart pins the other quit: a
// start row's first stage writes the cloud routes and leaves the restart to
// the start that follows. A quit before that start means nobody restarts, so
// the after-run flush must settle it, once. Without it config.yaml holds a
// route the proxy never loaded, and the next single-model launch of that
// model finds the row in place and gets "Invalid model name".
func TestQuitDuringStartRowPrepareSettlesTheOwedRestart(t *testing.T) {
	m := slotFlowFixture(t, idleOmlxSnapshot(), mixedProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed = true

	preparing, cmd := enterProfileRow(t, m, "profile/mixed")
	quitting, _ := updateMsg(preparing, tea.KeyMsg{Type: tea.KeyCtrlC})

	var out bytes.Buffer
	flushRouteNotesAfterRun(quitting, &out)
	// The command can still finish afterwards: it must not run the stage again.
	slotStageFrom(t, cmd)()

	if got, want := stub.got(), "ensure:openrouter/a:owed=false,settle"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if !strings.HasPrefix(out.String(), routeWaitingLine) {
		t.Errorf("printed = %q, want the wait announced", out.String())
	}
}

// TestSlotViewFitsTheTerminal pins the preparing screen at the three sizes
// the layout tests measure. Bubble Tea drops the top lines of a view that is
// too tall and cuts a line that is too wide, so a screen that does not fit
// loses the profile's name or the one key that works.
func TestSlotViewFitsTheTerminal(t *testing.T) {
	sizes := [][2]int{{80, 24}, {40, 12}, {120, 50}}
	for _, size := range sizes {
		stubInventory(t, idleOmlxSnapshot())
		stubSlotSeams(t)
		m := slotPicker(t, slotTestConfig(), []profiles.SlotCheck{cloudProfile()}, size[0], size[1])
		m.agent, m.selectedPath = "claude", t.TempDir()
		preparing, _ := enterProfileRow(t, m, "profile/cloud")
		if preparing.phase != phaseSlot {
			t.Fatalf("phase = %v, want phaseSlot", preparing.phase)
		}
		assertFits(t, "slot preparing", preparing.View(), size[0], size[1])
	}
}

// mixedStarting drives a mixed profile to the point where its local model's
// start is in flight: Enter, then the first stage's message. owed scripts
// whether the cloud-route write changed config.yaml.
func mixedStarting(t *testing.T, owed bool) (model, *slotStub) {
	t.Helper()
	m := slotFlowFixture(t, idleOmlxSnapshot(), mixedProfile())
	stub := stubSlotSeams(t)
	stub.ensureOwed = owed
	preparing, cmd := enterProfileRow(t, m, "profile/mixed")
	starting, _ := updateMsg(preparing, slotStageFrom(t, cmd)())
	if starting.phase != phaseStarting || starting.start == nil || starting.slot == nil {
		t.Fatalf("phase = %v start = %v slot = %v, want the local model's start in flight with the flow kept", starting.phase, starting.start, starting.slot)
	}
	return starting, stub
}

var omlxTarget = lifecycle.Target{ProviderID: "omlx", ModelName: "qwen3.8", ModelID: "omlx/qwen3.8"}

// TestStartProfileRowStartsItsLocalModelThenLaunches pins success criteria 4
// and 5 for the picker: a profile whose local model is idle starts it through
// the existing start flow, tells the engine a restart is owed for the cloud
// route it just wrote, then probes and launches. The flow itself restarts
// nothing, so the launch costs the start's one restart. Without RestartOwed
// the cloud route would stay unpublished and its slot would fail at the proxy.
func TestStartProfileRowStartsItsLocalModelThenLaunches(t *testing.T) {
	calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	starting, stub := mixedStarting(t, true)
	stubSlotApplier(t, func(*exec.Cmd, string, catalog.Row) (func() error, error) {
		return func() error { return nil }, nil
	})

	waitStartCalls(t, calls, 1)
	c := calls.at(0)
	if c.target != omlxTarget {
		t.Errorf("start target = %+v, want the profile's local model", c.target)
	}
	if !c.opts.RestartOwed || c.opts.AllowReplace {
		t.Errorf("start options = %+v, want RestartOwed and no AllowReplace", c.opts)
	}

	settling, cmd := updateMsg(starting, recvStart(t, starting))
	if settling.phase != phaseSlot || settling.slot == nil || settling.slot.owed {
		t.Fatalf("phase = %v slot = %+v, want the probe stage with nothing owed", settling.phase, settling.slot)
	}
	launching, launch := updateMsg(settling, slotStageFrom(t, cmd)())

	if got, want := stub.got(), "ensure:openrouter/a:owed=false,probe,build"; got != want {
		t.Fatalf("events = %q, want %q (no restart of the flow's own, no second route write)", got, want)
	}
	if launching.slot != nil || launch == nil {
		t.Fatal("the flow did not end in a run command")
	}
	if _, ok := launch().(launchDoneMsg); !ok {
		t.Fatal("the run command did not return launchDoneMsg")
	}
	counts := refcount.NewStore().Counts([]string{"openrouter/a", "omlx/qwen3.8"})
	if counts["openrouter/a"] != 1 || counts["omlx/qwen3.8"] != 1 {
		t.Errorf("refcounts = %v, want one line per distinct slot model, the local one included", counts)
	}
}

// TestProfileStartRefusedKeepsTheOwedRestart pins the replace dialog for a
// profile's start. A refusal touched nothing, so the owed restart is kept,
// not spent: "Replace and start" passes it on to the second attempt, and
// every way of backing out (Cancel, esc, quitting wt) settles it exactly
// once. Spending it at the refusal would restart twice; dropping it would
// leave the written cloud route unpublished.
func TestProfileStartRefusedKeepsTheOwedRestart(t *testing.T) {
	refused := func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
		return &lifecycle.OccupiedError{Occupants: []localmodels.Entry{{ModelID: "omlx/old"}}}
	}
	dialog := func(t *testing.T, owed bool) (model, *slotStub, *startCalls) {
		t.Helper()
		calls := stubStartModel(t, refused)
		starting, stub := mixedStarting(t, owed)
		got, _ := updateMsg(starting, recvStart(t, starting))
		if got.phase != phaseReplaceConfirm || got.slot == nil || got.slot.owed != owed {
			t.Fatalf("phase = %v slot = %+v, want the replace dialog with the flow kept", got.phase, got.slot)
		}
		if strings.Contains(stub.got(), "settle") {
			t.Fatal("the owed restart was settled at the refusal")
		}
		return got, stub, calls
	}

	t.Run("replace and start passes the owed restart on", func(t *testing.T) {
		got, stub, calls := dialog(t, true)
		got.replace.choices.Select(1)
		next, _ := updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})
		if next.start != nil && next.start.cancel != nil {
			t.Cleanup(next.start.cancel)
		}
		waitStartCalls(t, calls, 2)
		if c := calls.at(1); !c.opts.RestartOwed || !c.opts.AllowReplace || c.target != omlxTarget {
			t.Errorf("second start = %+v, want AllowReplace and RestartOwed for the same model", c)
		}
		if next.phase != phaseStarting || next.slot == nil {
			t.Errorf("phase = %v slot = %v, want the start in flight with the flow kept", next.phase, next.slot)
		}
		if strings.Contains(stub.got(), "settle") {
			t.Error("the owed restart was settled although the retry carries it")
		}
	})

	backOut := map[string]func(m model) model{
		"cancel": func(m model) model {
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter}) // Cancel is the default choice
			return next
		},
		"esc": func(m model) model {
			next, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
			return next
		},
	}
	for name, leave := range backOut {
		t.Run(name+" settles it once", func(t *testing.T) {
			got, stub, calls := dialog(t, true)
			next := leave(got)
			if next.phase != phaseModel || next.slot != nil {
				t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", next.phase, next.slot)
			}
			if got := stub.got(); got != "ensure:openrouter/a:owed=false,wait,settle" {
				t.Errorf("events = %q, want exactly one settle", got)
			}
			if calls.len() != 1 {
				t.Errorf("startModel calls = %d, want 1", calls.len())
			}
			assertNothingRecorded(t)
		})
	}

	t.Run("quitting wt on the dialog settles it once", func(t *testing.T) {
		got, stub, _ := dialog(t, true)
		var out bytes.Buffer
		flushRouteNotesAfterRun(got, &out)
		if got := stub.got(); got != "ensure:openrouter/a:owed=false,wait,settle" {
			t.Errorf("events = %q, want exactly one settle", got)
		}
	})

	t.Run("nothing owed, nothing settled", func(t *testing.T) {
		got, stub, _ := dialog(t, false)
		next, _ := updateMsg(got, tea.KeyMsg{Type: tea.KeyEnter})
		if next.slot != nil || strings.Contains(stub.got(), "settle") {
			t.Errorf("slot = %v events = %q, want the flow ended and no restart", next.slot, stub.got())
		}
	})
}

// TestProfileStartFailureReturnsToThePicker pins the failure table's start
// row: a start that fails returns to the picker with the start flow's own
// error, ends the profile launch, builds no command and records nothing. The
// engine settled the owed restart itself, so the flow must not start another.
func TestProfileStartFailureReturnsToThePicker(t *testing.T) {
	stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
		return errors.New("omlx refused the load")
	})
	starting, stub := mixedStarting(t, true)

	back, _ := updateMsg(starting, recvStart(t, starting))

	if back.phase != phaseModel || back.slot != nil {
		t.Fatalf("phase = %v slot = %v, want the picker with the flow ended", back.phase, back.slot)
	}
	if want := "failed to start omlx/qwen3.8: omlx refused the load"; back.status != want {
		t.Errorf("status = %q, want the start flow's own error, %q", back.status, want)
	}
	if got := stub.got(); got != "ensure:openrouter/a:owed=false,wait" {
		t.Errorf("events = %q, want the start's own wait and no settle, probe or build after a failed start", got)
	}
	assertNothingRecorded(t)
}

// pinnedProfile enters the claude model phase with a -M pin and the given
// profiles, the way the TUI does after the agent was picked.
func pinnedProfile(t *testing.T, pin string, allowReplace bool, checks ...profiles.SlotCheck) (model, tea.Cmd) {
	t.Helper()
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubRouteNotes(t)
	stubInventory(t, idleOmlxSnapshot())
	cfg := slotTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: pin, allowReplace: allowReplace, slotChecks: checks, selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")
	return m.enterModelPhase("claude", models, "code")
}

// TestPinOnProfileBeginsItsLaunch pins `-M profile/<name>` in the TUI: the
// pin and the picker read the same row, and a usable profile begins its
// launch without waiting for Enter, as a pinned model does. --replace is the
// pin's permission to stop a running occupant when the profile's local model
// has to start. Without the pin branch the profile's id-only row would go to
// the single-model launch and fail with "unknown provider".
func TestPinOnProfileBeginsItsLaunch(t *testing.T) {
	t.Run("a launch row", func(t *testing.T) {
		stub := stubSlotSeams(t)
		got, cmd := pinnedProfile(t, "profile/cloud", false, cloudProfile(), mixedProfile())
		if got.phase != phaseSlot || got.slot == nil || got.slot.item.model.ID != "profile/cloud" {
			t.Fatalf("phase = %v slot = %+v, want the pinned profile's launch in flight", got.phase, got.slot)
		}
		if selectedModelID(got) != "profile/cloud" {
			t.Errorf("cursor on %q, want the pinned row, so a failed launch returns to it", selectedModelID(got))
		}
		if events := stub.got(); events != "" {
			t.Errorf("events = %q, want none before the command runs", events)
		}
		updateMsg(got, slotStageFrom(t, cmd)())
		if want := "ensure:openrouter/a,openrouter/b:owed=false,wait,probe,build"; stub.got() != want {
			t.Errorf("events = %q, want %q", stub.got(), want)
		}
	})

	t.Run("a start row under --replace", func(t *testing.T) {
		stubSlotSeams(t)
		calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
		got, cmd := pinnedProfile(t, "profile/mixed", true, cloudProfile(), mixedProfile())
		starting, _ := updateMsg(got, slotStageFrom(t, cmd)())
		if starting.start != nil && starting.start.cancel != nil {
			t.Cleanup(starting.start.cancel)
		}
		waitStartCalls(t, calls, 1)
		if c := calls.at(0); !c.opts.AllowReplace || c.target != omlxTarget {
			t.Errorf("start = %+v, want AllowReplace for the pinned profile's local model", c)
		}
	})
}

// TestPinOnUnusableProfileRoutesBackToTheAgentPicker pins the rejected pin:
// a blocked profile returns to the agent picker with the row's reason, and a
// profile that has no row for this agent gets the existing "not in the
// eligible list" message. Either way the pin is cleared and nothing is
// written or probed.
func TestPinOnUnusableProfileRoutesBackToTheAgentPicker(t *testing.T) {
	broken := slotCheckFor(0, "broken", "opus", map[string]string{"opus": "openrouter/gone"})
	cases := []struct {
		name, pin, status string
	}{
		{"a blocked profile", "profile/broken", "slot opus: openrouter/gone is not in the registry"},
		{"a profile with no row", "profile/nope", `model "profile/nope" is not in the eligible list for agent "claude"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := stubSlotSeams(t)
			got, cmd := pinnedProfile(t, c.pin, false, broken, cloudProfile())
			if got.phase != phaseAgent || got.slot != nil || cmd != nil {
				t.Fatalf("phase = %v slot = %v cmd nil = %v, want the agent picker and no command", got.phase, got.slot, cmd == nil)
			}
			if got.status != c.status {
				t.Errorf("status = %q, want %q", got.status, c.status)
			}
			if got.pinnedModel != "" {
				t.Errorf("pinnedModel = %q, want it cleared so re-entry validates fresh", got.pinnedModel)
			}
			if events := stub.got(); events != "" {
				t.Errorf("events = %q, want none", events)
			}
		})
	}
}
