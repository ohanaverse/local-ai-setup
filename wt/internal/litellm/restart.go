package litellm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// proxyLabel is the proxy LaunchAgent's Label. The agent the fallback restart
// command kickstarts and the plist wt reads the proxy's environment from
// (defaultProxyPlist, proxyenv.go) name the same job — both derive from this
// constant so they cannot drift apart.
const proxyLabel = "local.litellm.proxy"

// fallbackRestartCmd is the forced restart of the launchd-managed proxy:
// launchd sends SIGTERM and, 5 seconds later, SIGKILL, so a request in flight
// is cut off. With no restart env var set (non-interactive launches — agents,
// worktree launchers — do not inherit interactive-shell exports) it is what
// RestartContext falls back to when it cannot let the proxy drain, and the
// manual command every restart warning names.
const fallbackRestartCmd = "launchctl kickstart -k gui/$(id -u)/" + proxyLabel

// runShell runs a shell command; a package var so tests never execute the
// real restart command.
var runShell = realRunShell

// A test binary never runs launchctl through it (underTest): that covers a
// test in any package that reaches the default restart with nothing stubbed.
func realRunShell(ctx context.Context, cmd string) error {
	if underTest() && strings.HasPrefix(cmd, "launchctl ") {
		return errUnderTest
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", cmd).Run()
}

// plainStartCmd starts the launchd job without touching a running one: a
// no-op when launchd has already respawned the proxy (KeepAlive does that
// about a millisecond after the exit), the start when the plist has no
// KeepAlive, and a wait of up to ten seconds for the spawn when launchd is
// throttling a job that ran for less than ten.
const plainStartCmd = "launchctl kickstart gui/$(id -u)/" + proxyLabel

// restartOverride is the user's own restart command and whether one is set:
// WT_LITELLM_RESTART_CMD, then legacy MODELMAN_LITELLM_RESTART_CMD.
func restartOverride() (string, bool) {
	for _, k := range []string{"WT_LITELLM_RESTART_CMD", "MODELMAN_LITELLM_RESTART_CMD"} {
		if v := os.Getenv(k); v != "" {
			return v, true
		}
	}
	return "", false
}

// RestartCommand returns the proxy restart command: WT_LITELLM_RESTART_CMD,
// then legacy MODELMAN_LITELLM_RESTART_CMD, then the launchctl fallback. With
// neither variable set RestartContext runs the fallback only as its forced
// last resort (see there); it is always the manual command a warning names.
func RestartCommand() string {
	if cmd, ok := restartOverride(); ok {
		return cmd
	}
	return fallbackRestartCmd
}

// restartTimeout bounds one restart command run: the override command, or
// each launchctl command of the default restart. It does not bound the wait
// for the proxy to finish its requests (proxyDrainTimeout).
const restartTimeout = 30 * time.Second

// proxyDrainTimeout bounds the default restart's wait for the proxy to exit
// after its SIGTERM, which is as long as the slowest request in flight takes.
// Past it the proxy is restarted with fallbackRestartCmd and that request is
// cut off.
const proxyDrainTimeout = 3 * time.Minute

// drainTiming is the default restart's clock; a package var (drain) so tests
// run it in milliseconds.
type drainTiming struct {
	bound   time.Duration // the wait for the proxy to exit
	notice  time.Duration // how long a drain runs before the user is told
	poll    time.Duration // between two looks at the process
	command time.Duration // each launchctl command
}

var drain = drainTiming{
	bound:   proxyDrainTimeout,
	notice:  2 * time.Second,
	poll:    100 * time.Millisecond,
	command: restartTimeout,
}

// proxyProcess is everything the default restart does to the live proxy
// process: find it, signal it, watch it go.
type proxyProcess struct {
	// pid is the proxy job's process id as launchd reports it. ok is false
	// when the job is not running, launchctl is absent or the lookup failed:
	// in each case there is no process to drain.
	pid func(ctx context.Context) (pid int, ok bool)
	// term sends the process one SIGTERM.
	term func(pid int) error
	// kill sends the process one SIGKILL. ESRCH means it was already gone.
	kill func(pid int) error
	// alive reports whether the process still exists.
	alive func(pid int) bool
}

// proxyProc is the seam for the live process; a package var so no test looks
// up or signals the developer's real proxy. This package's TestMain replaces
// it with a hard failure, and realProxyProcess itself refuses inside any test
// binary (underTest), as realRunShell refuses launchctl there, which covers
// the packages that can reach RestartContext with neither seam stubbed.
var proxyProc = realProxyProcess

var realProxyProcess = proxyProcess{pid: realProxyPID, term: realProxyTerm, kill: realProxyKill, alive: realProxyAlive}

// underTest reports whether this is a test binary.
func underTest() bool { return testing.Testing() }

var errUnderTest = errors.New("the LiteLLM proxy is not touched from a test binary")

// realProxyPID asks launchd for the proxy job's pid.
func realProxyPID(ctx context.Context) (int, bool) {
	if underTest() {
		return 0, false
	}
	out, err := exec.CommandContext(ctx, "launchctl", "list", proxyLabel).Output()
	if err != nil {
		return 0, false
	}
	return parseLaunchctlPID(string(out))
}

func realProxyTerm(pid int) error {
	if underTest() {
		return errUnderTest
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func realProxyKill(pid int) error {
	if underTest() {
		return errUnderTest
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// realProxyAlive probes with signal 0. Only ESRCH means the process is gone:
// EPERM is a live process this user may not signal.
func realProxyAlive(pid int) bool {
	if underTest() {
		return false
	}
	return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// launchctlPID matches the pid line of `launchctl list <label>`, a
// tab-indented dictionary: one tab, so a "PID" inside the nested
// ProgramArguments list (two tabs) is not taken for it.
var launchctlPID = regexp.MustCompile(`(?m)^\t"PID" = ([0-9]+);$`)

// parseLaunchctlPID reads the pid out of `launchctl list <label>`. A job that
// is loaded but not running prints no "PID" line (and still exits 0), which
// is "not running" and not an error. Pids 0 and 1 are never the proxy and are
// refused, since a signal to either reaches far more than one process.
func parseLaunchctlPID(out string) (int, bool) {
	m := launchctlPID.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid <= 1 {
		return 0, false
	}
	return pid, true
}

type restartNoticeKey struct{}

// Notice is one line RestartContext reports besides its warnings, which are
// all failed restarts. Msg has no "wt: " prefix and no newline. InProgress
// marks the line that is true only while the restart waits (the proxy is
// finishing a request in flight): a caller with a live screen shows it there
// and drops it once the restart is over, where printed later it would be
// stale. A notice without it reports what happened (the request was cut off
// at the drain bound), and stays true.
type Notice struct {
	Msg        string
	InProgress bool
}

// WithRestartNotice returns a context that carries where RestartContext's
// notices go. The lifecycle package sets it to the writer of the operation
// that owes the restart, so the lines follow that operation's other output.
func WithRestartNotice(ctx context.Context, fn func(Notice)) context.Context {
	return context.WithValue(ctx, restartNoticeKey{}, fn)
}

// RestartNotice delivers n to the notice function ctx carries, or prints it
// to stderr when it carries none.
func RestartNotice(ctx context.Context, n Notice) {
	if fn, ok := ctx.Value(restartNoticeKey{}).(func(Notice)); ok && fn != nil {
		fn(n)
		return
	}
	fmt.Fprintf(os.Stderr, "wt: %s\n", n.Msg)
}

func drainNotice(bound time.Duration) Notice {
	return Notice{
		Msg:        fmt.Sprintf("the LiteLLM proxy is finishing a request in flight; it restarts when the request ends (up to %s)", bound),
		InProgress: true,
	}
}

func cutOffNotice(bound time.Duration) Notice {
	return Notice{Msg: fmt.Sprintf("the LiteLLM proxy was still serving a request after %s; it was restarted anyway and that request was cut off", bound)}
}

// RestartContext bounces the proxy after a config write. It never fails the
// caller: the config write is the source of truth, and a failed restart only
// leaves the proxy stale, reported as a warning naming the manual fix. Every
// warning it returns is a failed restart; what it says of a restart that was
// made goes through RestartNotice.
//
// An override command (WT_LITELLM_RESTART_CMD or its legacy alias) is run as
// given, bounded by restartTimeout, on the caller's context.
//
// With no override the restart lets a request in flight finish (#377).
// `launchctl kickstart -k` cannot: launchd follows its own SIGTERM with a
// SIGKILL after the job's exit timeout, 5 seconds for the proxy's plist. A
// SIGTERM from wt arms no such timer, and the proxy's server (uvicorn) answers
// it by closing its listener and exiting once its open responses have ended.
// So:
//
//  1. Ask launchd for the job's pid. None (not running, no launchctl, a
//     failed lookup): run fallbackRestartCmd, as before.
//  2. Send that pid one SIGTERM.
//  3. Wait for the process to exit, for at most proxyDrainTimeout. After
//     drain.notice of waiting, say so once (RestartNotice, InProgress).
//  4. Gone: run plainStartCmd. launchd has normally respawned the job already
//     and the command does nothing.
//  5. Still there at the bound: SIGKILL that pid, wait for it to go, run
//     plainStartCmd and give cutOffNotice. The kill names the old process,
//     never the job: `kickstart -k` here would kill a proxy launchd had just
//     respawned, had the old one exited after the last look. A kill that
//     finds it gone (ESRCH) is that exit, so nothing was cut off. A kill that
//     fails otherwise falls back to fallbackRestartCmd.
//
// The proxy refuses new connections for the whole of step 3.
//
// restartTimeout bounds each launchctl command, not the wait. A caller
// cancelled during the wait gets one warning at once; the signal is already
// sent, so the proxy still drains and launchd still restarts it. A caller
// already cancelled before the signal starts no drain: the forced command
// fails on the dead context and is reported as a failed restart.
func RestartContext(ctx context.Context) []string {
	if cmd, ok := restartOverride(); ok {
		return runRestartCmd(ctx, cmd, cmd, restartTimeout)
	}
	t := drain
	forced := func() []string { return runRestartCmd(ctx, fallbackRestartCmd, fallbackRestartCmd, t.command) }

	lookupCtx, cancel := context.WithTimeout(ctx, t.command)
	pid, ok := proxyProc.pid(lookupCtx)
	cancel()
	if !ok || ctx.Err() != nil {
		return forced()
	}
	if err := proxyProc.term(pid); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			// It exited between the lookup and the signal: nothing to drain.
			return runRestartCmd(ctx, plainStartCmd, fallbackRestartCmd, t.command)
		}
		return forced()
	}
	switch err := waitProxyExit(ctx, pid, t); {
	case err == nil:
		return runRestartCmd(ctx, plainStartCmd, fallbackRestartCmd, t.command)
	case errors.Is(err, errDrainBound):
		return killAtBound(ctx, pid, t, forced)
	default:
		return []string{fmt.Sprintf("stopped waiting for the LiteLLM proxy to restart (%v); it was asked to exit and restarts when its request in flight ends", err)}
	}
}

// killAtBound is step 5 of RestartContext: the proxy outlived the drain bound.
func killAtBound(ctx context.Context, pid int, t drainTiming, forced func() []string) []string {
	plain := func() []string { return runRestartCmd(ctx, plainStartCmd, fallbackRestartCmd, t.command) }
	switch err := proxyProc.kill(pid); {
	case errors.Is(err, syscall.ESRCH):
		// It exited after the last look: it finished its request.
		return plain()
	case err != nil:
		if w := forced(); len(w) > 0 {
			return w
		}
	default:
		// A SIGKILL is not refused, but the exit is not instant: the plain
		// start is a no-op while launchd still has the job running.
		gone := time.NewTimer(t.command)
		defer gone.Stop()
		poll := time.NewTicker(t.poll)
		defer poll.Stop()
	wait:
		for proxyProc.alive(pid) {
			select {
			case <-gone.C:
				break wait
			case <-poll.C:
			}
		}
		if w := plain(); len(w) > 0 {
			return w
		}
	}
	RestartNotice(ctx, cutOffNotice(t.bound))
	return nil
}

// runRestartCmd runs one restart command under its own timeout and turns a
// failure into the warning that names the manual fix.
func runRestartCmd(ctx context.Context, cmd, manual string, timeout time.Duration) []string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := runShell(ctx, cmd); err != nil {
		return []string{fmt.Sprintf("failed to restart LiteLLM proxy (%v); restart it manually: %s", err, manual)}
	}
	return nil
}

// errDrainBound is waitProxyExit's answer when the process outlived the bound.
var errDrainBound = errors.New("the LiteLLM proxy did not exit within the drain bound")

// waitProxyExit polls until the process is gone (nil), t.bound has passed
// (errDrainBound) or ctx is done (its error). It gives the notice once, when
// the wait has lasted t.notice.
func waitProxyExit(ctx context.Context, pid int, t drainTiming) error {
	bound := time.NewTimer(t.bound)
	defer bound.Stop()
	notice := time.NewTimer(t.notice)
	defer notice.Stop()
	poll := time.NewTicker(t.poll)
	defer poll.Stop()
	for {
		if !proxyProc.alive(pid) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-bound.C:
			if !proxyProc.alive(pid) {
				return nil
			}
			return errDrainBound
		case <-notice.C:
			RestartNotice(ctx, drainNotice(t.bound))
		case <-poll.C:
		}
	}
}

// Restart is RestartContext for callers with no context of their own (the
// Apply default when Options.Restart is nil).
func Restart() []string { return RestartContext(context.Background()) }

// Listening reports whether a proxy may be serving at baseURL: false only when
// the request positively fails because nothing is there (connection refused,
// unresolvable host). A timeout, a non-200 or any other answer counts as
// listening, so a busy or half-started proxy is still waited for after a
// restart instead of being launched into mid-bounce.
func Listening(ctx context.Context, baseURL string, timeout time.Duration) bool {
	resp, err := healthGet(ctx, baseURL, timeout)
	if err != nil {
		var dns *net.DNSError
		return !errors.Is(err, syscall.ECONNREFUSED) && !errors.As(err, &dns)
	}
	resp.Body.Close()
	return true
}

// healthGet makes one bounded request to the proxy's liveness endpoint.
func healthGet(ctx context.Context, baseURL string, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL(baseURL), nil)
	if err != nil {
		return nil, err
	}
	return (&http.Client{Timeout: timeout}).Do(req)
}

func healthURL(baseURL string) string { return strings.TrimRight(baseURL, "/") + "/health/liveliness" }

// WaitReady polls <baseURL>/health/liveliness until it answers 200 or the
// timeout elapses, so a caller does not launch an agent into a proxy that is
// still coming back up.
func WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := healthURL(baseURL)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("LiteLLM proxy not ready at %s: %w", url, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}
