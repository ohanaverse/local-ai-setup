package litellm

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

const plainKickstart = "launchctl kickstart gui/$(id -u)/local.litellm.proxy"

// fakeProxy stands in for the launchd job behind the proxyProc seam: a pid
// launchd reports (or none), a record of every signal sent, and a process that
// goes away exitAfter the first SIGTERM (never, when exitAfter is negative).
// No test in this package may look up or signal a real process.
type fakeProxy struct {
	pid       int
	found     bool
	termErr   error
	exitAfter time.Duration
	lookups   int
	terms     []int
	termAt    time.Time
}

func (f *fakeProxy) seam() proxyProcess {
	return proxyProcess{
		pid: func(context.Context) (int, bool) { f.lookups++; return f.pid, f.found },
		term: func(pid int) error {
			f.terms = append(f.terms, pid)
			if f.termErr != nil {
				return f.termErr
			}
			f.termAt = time.Now()
			return nil
		},
		alive: func(int) bool {
			if f.termAt.IsZero() || f.exitAfter < 0 {
				return true
			}
			return time.Since(f.termAt) < f.exitAfter
		},
	}
}

// shellRun is one runShell call: the command, and whether its context was
// still live when it ran.
type shellRun struct {
	cmd    string
	ctxErr error
}

// stubDefaultRestart points the default restart at fp and at a recording
// shell, with timings short enough for a test, and clears both override
// variables. fail maps a command to the error its run returns.
func stubDefaultRestart(t *testing.T, fp *fakeProxy, timing drainTiming, fail map[string]error) *[]shellRun {
	t.Helper()
	t.Setenv("WT_LITELLM_RESTART_CMD", "")
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "")
	oldShell, oldProc, oldTiming := runShell, proxyProc, drain
	t.Cleanup(func() { runShell, proxyProc, drain = oldShell, oldProc, oldTiming })
	var runs []shellRun
	runShell = func(ctx context.Context, cmd string) error {
		runs = append(runs, shellRun{cmd, ctx.Err()})
		return fail[cmd]
	}
	proxyProc = fp.seam()
	drain = timing
	return &runs
}

// noticeCtx returns a context whose restart notice is recorded in the
// returned slice.
func noticeCtx() (context.Context, *[]string) {
	var got []string
	return WithRestartNotice(context.Background(), func(s string) { got = append(got, s) }), &got
}

var testTiming = drainTiming{bound: 2 * time.Second, notice: time.Second, poll: 2 * time.Millisecond, command: 5 * time.Second}

// TestGracefulRestartProcessExits pins the new default restart (#377): the
// pid comes from launchd, gets one SIGTERM, and once it has exited the job is
// started with a plain kickstart, never `-k`, whose 5-second SIGKILL is what
// cut requests in flight. An idle proxy is gone in a fraction of a second, so
// nothing is printed and nothing is warned.
func TestGracefulRestartProcessExits(t *testing.T) {
	fp := &fakeProxy{pid: 4242, found: true, exitAfter: 10 * time.Millisecond}
	runs := stubDefaultRestart(t, fp, testTiming, nil)
	ctx, notices := noticeCtx()

	if w := RestartContext(ctx); len(w) != 0 {
		t.Fatalf("warnings = %v, want none", w)
	}
	if len(fp.terms) != 1 || fp.terms[0] != 4242 {
		t.Fatalf("SIGTERMs = %v, want one, to the pid launchd reported", fp.terms)
	}
	if len(*runs) != 1 || (*runs)[0].cmd != plainKickstart {
		t.Fatalf("commands = %+v, want only %q", *runs, plainKickstart)
	}
	if len(*notices) != 0 {
		t.Fatalf("notices = %q, want none for a proxy that exited at once", *notices)
	}
}

// TestGracefulRestartSlowExitPrintsOneNotice pins the one sign of life during
// a drain: a proxy still finishing a request after the notice delay gets one
// line, once, and the restart then ends like the quick one. The drain here
// outlasts the per-command timeout, which bounds each launchctl run and not
// the wait: wrapped around the whole restart it would turn every long drain
// into "failed to restart LiteLLM proxy".
func TestGracefulRestartSlowExitPrintsOneNotice(t *testing.T) {
	timing := drainTiming{bound: 5 * time.Second, notice: 20 * time.Millisecond, poll: 2 * time.Millisecond, command: 60 * time.Millisecond}
	fp := &fakeProxy{pid: 7, found: true, exitAfter: 200 * time.Millisecond}
	runs := stubDefaultRestart(t, fp, timing, nil)
	ctx, notices := noticeCtx()

	if w := RestartContext(ctx); len(w) != 0 {
		t.Fatalf("warnings = %v, want none", w)
	}
	if len(*notices) != 1 || (*notices)[0] != drainNotice(timing.bound) {
		t.Fatalf("notices = %q, want exactly one: %q", *notices, drainNotice(timing.bound))
	}
	if len(fp.terms) != 1 {
		t.Fatalf("SIGTERMs = %v, want one", fp.terms)
	}
	if len(*runs) != 1 || (*runs)[0].cmd != plainKickstart {
		t.Fatalf("commands = %+v, want only %q", *runs, plainKickstart)
	}
	if err := (*runs)[0].ctxErr; err != nil {
		t.Fatalf("the kickstart ran on a dead context (%v): the command timeout must start with the command, not with the drain", err)
	}
}

// TestGracefulRestartBoundExpires pins the forced end: a proxy that is still
// serving at the bound is restarted with today's `kickstart -k`, and wt says
// in one warning that the request was cut off. That warning reports a restart
// that happened, so it is not a failed restart (IsCutOffWarning).
func TestGracefulRestartBoundExpires(t *testing.T) {
	timing := drainTiming{bound: 300 * time.Millisecond, notice: 5 * time.Millisecond, poll: 2 * time.Millisecond, command: time.Second}
	fp := &fakeProxy{pid: 7, found: true, exitAfter: -1}
	runs := stubDefaultRestart(t, fp, timing, nil)
	ctx, notices := noticeCtx()

	w := RestartContext(ctx)
	if len(w) != 1 || w[0] != cutOffWarning(timing.bound) {
		t.Fatalf("warnings = %q, want exactly %q", w, cutOffWarning(timing.bound))
	}
	if !IsCutOffWarning(w[0]) {
		t.Fatalf("IsCutOffWarning(%q) = false", w[0])
	}
	if len(*runs) != 1 || (*runs)[0].cmd != fallbackRestartCmd {
		t.Fatalf("commands = %+v, want only %q", *runs, fallbackRestartCmd)
	}
	if len(fp.terms) != 1 {
		t.Fatalf("SIGTERMs = %v, want one: wt never signals twice", fp.terms)
	}
	if len(*notices) != 1 {
		t.Fatalf("notices = %q, want one", *notices)
	}
}

// TestGracefulRestartForcedCommandFails pins that a forced restart which
// itself fails is reported as the failed restart it is, in the wording
// docs/contracts/litellm-cli.sample.json pins, and not as a cut-off request.
func TestGracefulRestartForcedCommandFails(t *testing.T) {
	timing := drainTiming{bound: 30 * time.Millisecond, notice: time.Second, poll: 2 * time.Millisecond, command: time.Second}
	fp := &fakeProxy{pid: 7, found: true, exitAfter: -1}
	stubDefaultRestart(t, fp, timing, map[string]error{fallbackRestartCmd: errors.New("exit status 1")})

	w := RestartContext(context.Background())
	want := "failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"
	if len(w) != 1 || w[0] != want {
		t.Fatalf("warnings = %q, want %q", w, want)
	}
	if IsCutOffWarning(w[0]) {
		t.Fatal("a failed restart must not read as a cut-off warning")
	}
}

// TestGracefulRestartNoPidRunsForcedCommand pins the degraded path: with no
// pid from launchd (the job is not running, launchctl is absent as on Linux,
// or the lookup failed) there is nothing to drain, so today's command runs
// unchanged and nothing is signalled.
func TestGracefulRestartNoPidRunsForcedCommand(t *testing.T) {
	fp := &fakeProxy{found: false}
	runs := stubDefaultRestart(t, fp, testTiming, nil)
	ctx, notices := noticeCtx()

	if w := RestartContext(ctx); len(w) != 0 {
		t.Fatalf("warnings = %v, want none", w)
	}
	if len(fp.terms) != 0 {
		t.Fatalf("SIGTERMs = %v, want none without a pid", fp.terms)
	}
	if len(*runs) != 1 || (*runs)[0].cmd != fallbackRestartCmd {
		t.Fatalf("commands = %+v, want only %q", *runs, fallbackRestartCmd)
	}
	if len(*notices) != 0 {
		t.Fatalf("notices = %q, want none", *notices)
	}
}

// TestRestartOverrideNeverTouchesTheProcess pins that an override command is
// run as given: wt looks up no pid and sends no signal, whichever of the two
// variables names it, and even when the command is the default one verbatim.
func TestRestartOverrideNeverTouchesTheProcess(t *testing.T) {
	for _, env := range []string{"WT_LITELLM_RESTART_CMD", "MODELMAN_LITELLM_RESTART_CMD"} {
		for _, cmd := range []string{"mycmd", fallbackRestartCmd} {
			fp := &fakeProxy{pid: 7, found: true}
			runs := stubDefaultRestart(t, fp, testTiming, nil)
			t.Setenv(env, cmd)
			if w := RestartContext(context.Background()); len(w) != 0 {
				t.Fatalf("%s=%q: warnings = %v", env, cmd, w)
			}
			if fp.lookups != 0 || len(fp.terms) != 0 {
				t.Fatalf("%s=%q: lookups = %d, SIGTERMs = %v, want neither", env, cmd, fp.lookups, fp.terms)
			}
			if len(*runs) != 1 || (*runs)[0].cmd != cmd {
				t.Fatalf("%s=%q: commands = %+v", env, cmd, *runs)
			}
			t.Setenv(env, "")
		}
	}
}

// TestGracefulRestartCancelledDuringDrain pins cancellation: a caller that
// gives up while the proxy drains gets control back at once, with one
// warning. Nothing more is run: the SIGTERM is already sent, so the proxy
// still finishes its request and launchd still restarts it.
func TestGracefulRestartCancelledDuringDrain(t *testing.T) {
	timing := drainTiming{bound: 30 * time.Second, notice: 10 * time.Second, poll: 2 * time.Millisecond, command: time.Second}
	fp := &fakeProxy{pid: 7, found: true, exitAfter: -1}
	runs := stubDefaultRestart(t, fp, timing, nil)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	defer cancel()

	began := time.Now()
	w := RestartContext(ctx)
	if el := time.Since(began); el > 5*time.Second {
		t.Fatalf("a cancelled drain took %v, want it to return at once", el)
	}
	if len(w) != 1 || !strings.Contains(w[0], "context canceled") {
		t.Fatalf("warnings = %q, want one naming the cancellation", w)
	}
	if IsCutOffWarning(w[0]) {
		t.Fatal("an abandoned wait is not a completed restart")
	}
	if len(*runs) != 0 {
		t.Fatalf("commands = %+v, want none after a cancelled wait", *runs)
	}
}

// TestGracefulRestartCancelledBeforeSignal pins that a caller already
// cancelled when the restart begins does not start a drain it will not wait
// for: no signal is sent, and the restart is reported failed as before.
func TestGracefulRestartCancelledBeforeSignal(t *testing.T) {
	fp := &fakeProxy{pid: 7, found: true, exitAfter: -1}
	stubDefaultRestart(t, fp, testTiming, nil)
	runShell = func(ctx context.Context, _ string) error { return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := RestartContext(ctx)
	if len(fp.terms) != 0 {
		t.Fatalf("SIGTERMs = %v, want none for a cancelled caller", fp.terms)
	}
	if len(w) != 1 || !strings.Contains(w[0], "restart it manually") {
		t.Fatalf("warnings = %q, want the failed-restart warning", w)
	}
}

// TestGracefulRestartSignalErrors pins the two ways the SIGTERM can fail. A
// process already gone (ESRCH) needs no drain and gets the plain kickstart; a
// process wt may not signal cannot be drained, so the forced command runs.
func TestGracefulRestartSignalErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"already gone", syscall.ESRCH, plainKickstart},
		{"not permitted", syscall.EPERM, fallbackRestartCmd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fakeProxy{pid: 7, found: true, termErr: tc.err, exitAfter: -1}
			runs := stubDefaultRestart(t, fp, testTiming, nil)
			began := time.Now()
			if w := RestartContext(context.Background()); len(w) != 0 {
				t.Fatalf("warnings = %v, want none", w)
			}
			if el := time.Since(began); el > time.Second {
				t.Fatalf("took %v: a signal that was not delivered must not be waited on", el)
			}
			if len(*runs) != 1 || (*runs)[0].cmd != tc.want {
				t.Fatalf("commands = %+v, want only %q", *runs, tc.want)
			}
		})
	}
}

// TestGracefulRestartPlainKickstartFails pins the warning when the proxy
// drained and exited but the job could not be started again: the manual fix
// it names is still the forced command, in the pinned wording.
func TestGracefulRestartPlainKickstartFails(t *testing.T) {
	fp := &fakeProxy{pid: 7, found: true, exitAfter: 0}
	stubDefaultRestart(t, fp, testTiming, map[string]error{plainKickstart: errors.New("exit status 113")})
	w := RestartContext(context.Background())
	want := "failed to restart LiteLLM proxy (exit status 113); restart it manually: " + fallbackRestartCmd
	if len(w) != 1 || w[0] != want {
		t.Fatalf("warnings = %q, want %q", w, want)
	}
}

// TestDrainWording pins the bound and the two lines a user reads, exactly as
// the #377 design gives them.
func TestDrainWording(t *testing.T) {
	if proxyDrainTimeout != 3*time.Minute {
		t.Fatalf("proxyDrainTimeout = %v, want 3m", proxyDrainTimeout)
	}
	if drain.bound != proxyDrainTimeout || drain.notice != 2*time.Second || drain.poll != 100*time.Millisecond || drain.command != restartTimeout {
		t.Fatalf("default timing = %+v", drain)
	}
	if got, want := drainNotice(proxyDrainTimeout), "the LiteLLM proxy is finishing a request in flight; it restarts when the request ends (up to 3m0s)"; got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	want := "the LiteLLM proxy was still serving a request after 3m0s; it was restarted anyway and that request was cut off"
	if got := cutOffWarning(proxyDrainTimeout); got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
	if !IsCutOffWarning(want) {
		t.Fatal("IsCutOffWarning does not recognise the warning")
	}
}

// TestRestartNoticeFollowsTheContext pins the delivery: the notice goes to
// the function the context carries, and a context without one does not panic
// (it prints to stderr).
func TestRestartNoticeFollowsTheContext(t *testing.T) {
	ctx, got := noticeCtx()
	RestartNotice(context.WithoutCancel(ctx), "hello")
	if len(*got) != 1 || (*got)[0] != "hello" {
		t.Fatalf("notices = %q", *got)
	}
}

// TestParseLaunchctlPID pins the lookup against `launchctl list <label>` as
// launchd prints it. A job that is loaded but not running (between an exit
// and a throttled respawn) has no "PID" line and still exits 0: that is "not
// running", never pid 0.
func TestParseLaunchctlPID(t *testing.T) {
	running := "{\n\t\"StandardOutPath\" = \"/Users/x/.litellm.log\";\n\t\"LimitLoadToSessionType\" = \"Aqua\";\n\t\"Label\" = \"local.litellm.proxy\";\n\t\"OnDemand\" = false;\n\t\"LastExitStatus\" = 15;\n\t\"PID\" = 26081;\n\t\"Program\" = \"/x/litellm\";\n\t\"ProgramArguments\" = (\n\t\t\"/x/litellm\";\n\t\t\"--port\";\n\t\t\"4000\";\n\t);\n};\n"
	if pid, ok := parseLaunchctlPID(running); !ok || pid != 26081 {
		t.Fatalf("running: pid = %d, ok = %v", pid, ok)
	}
	for name, out := range map[string]string{
		"not running":     strings.Replace(running, "\t\"PID\" = 26081;\n", "", 1),
		"unknown label":   "Could not find service \"local.litellm.proxy\" in domain for port\n",
		"empty":           "",
		"pid 0":           "{\n\t\"PID\" = 0;\n};\n",
		"pid 1":           "{\n\t\"PID\" = 1;\n};\n",
		"exit status":     "{\n\t\"LastExitStatus\" = 15;\n};\n",
		"nested argument": "{\n\t\"ProgramArguments\" = (\n\t\t\"PID\" = 99;\n\t);\n};\n",
		"overflow":        "{\n\t\"PID\" = 99999999999999999999;\n};\n",
	} {
		if pid, ok := parseLaunchctlPID(out); ok {
			t.Errorf("%s: pid = %d, ok = true, want no pid", name, pid)
		}
	}
}

// TestRealProxyProcessRefusesUnderTest pins the guard for every other
// package: the real seam looks up and signals nothing inside a test binary,
// and the real shell runs no launchctl there, so a test anywhere that reaches
// the default restart with nothing stubbed can neither SIGTERM nor kickstart
// the developer's live proxy.
func TestRealProxyProcessRefusesUnderTest(t *testing.T) {
	if !underTest() {
		t.Fatal("underTest() = false inside a test binary")
	}
	if pid, ok := realProxyProcess.pid(context.Background()); ok {
		t.Fatalf("the real lookup answered pid %d inside a test binary", pid)
	}
	// `launchctl help` changes nothing, should the guard ever be lost.
	if err := realRunShell(context.Background(), "launchctl help"); !errors.Is(err, errUnderTest) {
		t.Fatalf("realRunShell ran launchctl inside a test binary (err = %v)", err)
	}
}

// TestUnstubbedSeamFailsClosed pins TestMain's stub: with nothing stubbed by
// the test, the default restart finds no pid, signals nothing, and its
// command is refused by the stubbed shell.
func TestUnstubbedSeamFailsClosed(t *testing.T) {
	t.Setenv("WT_LITELLM_RESTART_CMD", "")
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "")
	w := RestartContext(context.Background())
	if len(w) != 1 || !strings.Contains(w[0], "not stubbed") {
		t.Fatalf("warnings = %q, want the unstubbed shell's refusal", w)
	}
	if err := proxyProc.term(1 << 30); err == nil {
		t.Fatal("the unstubbed signal seam returned nil")
	}
	if proxyProc.alive(1 << 30) {
		t.Fatal("the unstubbed liveness seam reports a live process")
	}
}
