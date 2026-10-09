package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Tests for the mtplx that is still loading (#308): its port refuses, so the
// only handle on it is the pidfile wt wrote. The process table and the signal
// are stubs here (procTable); loading_darwin_test.go runs the real ones
// against a child of the test.

// loadPort is the port of loadCfg's mtplx provider. Nothing listens there and
// nothing dials it: these tests never probe.
const loadPort = 8003

func loadCfg() *config.Config { return provCfg("mtplx", "http://127.0.0.1:8003/v1") }

// wrapperArgv is the command line of the server wt spawned before mtplx has
// replaced itself with its daemon: an interpreter running the mtplx script
// with the arguments wt passed.
func wrapperArgv(port int) []string {
	return []string{"/bin/sh", "/Users/u/.mtplx/bin/mtplx", "serve", "--model", "org/m", "--port", strconv.Itoa(port), "--host", "127.0.0.1", "--model-id", "org/m"}
}

// daemonArgv is the command line of the same pid once `mtplx serve` has
// exec'd its daemon — the one that loads the weights (mtplx 2.12.0).
func daemonArgv(port int) []string {
	return []string{"/Users/u/Library/Application Support/MTPLX/runtime-venv/bin/python", "-P", "-m", "mtplx.server.openai",
		"--model", "/weights/m", "--backend-id", "b", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--model-id", "org/m"}
}

// procTable is a fake process table: describe reads it, signal records what
// was sent and applies onSignal.
type procTable struct {
	procs    map[int]procInfo
	describe error // every describe fails with it
	signals  []string
	onSignal func(pt *procTable, pid int, sig syscall.Signal)
}

func (pt *procTable) env(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := testEnv()
	e.mtplxProc = pidProcess{name: "mtplx", pidfile: filepath.Join(dir, "mtplx.pid"), logfile: filepath.Join(dir, "mtplx.log")}
	e.stopGrace = 60 * time.Millisecond
	e.stopTimeout = 60 * time.Millisecond
	e.describe = func(pid int) (procInfo, bool, error) {
		if pt.describe != nil {
			return procInfo{}, false, pt.describe
		}
		info, ok := pt.procs[pid]
		return info, ok, nil
	}
	e.signal = func(pid int, sig syscall.Signal) error {
		pt.signals = append(pt.signals, fmt.Sprintf("%d:%d", pid, sig))
		if pt.onSignal != nil {
			pt.onSignal(pt, pid, sig)
		}
		return nil
	}
	return e
}

// mine is a live process of the current user that leads its own group.
func mine(pid int, start string, argv []string) procInfo {
	return procInfo{uid: os.Getuid(), pgid: pid, start: start, argv: argv}
}

func writePidfile(t *testing.T, e *env, content string) {
	t.Helper()
	if err := os.WriteFile(e.mtplxProc.pidfile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeStartRecord(t *testing.T, e *env, content string) {
	t.Helper()
	if err := os.WriteFile(e.mtplxProc.startfile(), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestIsMtplxServerMatchesOnlyTheServerWtSpawns pins the command-line rule a
// pid must meet before wt may signal it: an interpreter running the mtplx
// script with the `serve` subcommand, or python running mtplx's server
// module, each with `--port <this provider's port>` as two argv tokens after
// it. Everything else is not mtplx — in particular a command that merely
// mentions the words. A looser rule would let a recycled pid get another
// program of the owner's killed.
func TestIsMtplxServerMatchesOnlyTheServerWtSpawns(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want bool
	}{
		"sh wrapper":                {wrapperArgv(8003), true},
		"python wrapper":            {append([]string{"/v/bin/python3.14", "/Users/u/Library/Application Support/MTPLX/runtime-venv/bin/mtplx"}, wrapperArgv(8003)[2:]...), true},
		"mtplx as argv[0]":          {wrapperArgv(8003)[1:], true},
		"daemon":                    {daemonArgv(8003), true},
		"daemon without -P":         {[]string{"python3", "-m", "mtplx.server.openai", "--port", "8003"}, true},
		"another port":              {wrapperArgv(8004), false},
		"daemon on another port":    {daemonArgv(18003), false},
		"port as one token":         {[]string{"/bin/sh", "/b/mtplx", "serve", "--port=8003"}, false},
		"port before the server":    {[]string{"/bin/sh", "--port", "8003", "/b/mtplx", "serve"}, false},
		"another subcommand":        {[]string{"/bin/sh", "/b/mtplx", "stop", "--port", "8003"}, false},
		"words in one argument":     {[]string{"vim", "mtplx-serve-notes", "--port", "8003"}, false},
		"words as editor arguments": {[]string{"vim", "mtplx", "serve", "--port", "8003"}, false},
		"script not named mtplx":    {[]string{"/bin/sh", "/b/not-mtplx", "serve", "--port", "8003"}, false},
		"module after a script":     {[]string{"python3", "run.py", "-m", "mtplx.server.openai", "--port", "8003"}, false},
		"module as a substring":     {[]string{"python3", "-m", "mtplx.server.openai.tools", "--port", "8003"}, false},
		"sleep":                     {[]string{"sleep", "60"}, false},
		"nothing":                   {nil, false},
	} {
		if got := isMtplxServer(tc.argv, 8003); got != tc.want {
			t.Errorf("%s: isMtplxServer(%q) = %v, want %v", name, tc.argv, got, tc.want)
		}
	}
}

// TestLoadingMtplxNeverRestsOnThePidAlone verifies what wt concludes from its
// pidfile behind a refused port, state by state: a server only when the pid is
// alive, the current user's, an mtplx server on this port, and — when wt
// recorded a start time — the process that started then. A pidfile an older
// wt (or llmbench) wrote holds only the pid and is judged by the command line.
// Nothing here may signal: the read is what `wt stop` asks its question on.
func TestLoadingMtplxNeverRestsOnThePidAlone(t *testing.T) {
	const pid = 4242
	live := mine(pid, "100.5", daemonArgv(loadPort))
	otherUser := live
	otherUser.uid = os.Getuid() + 1
	zombie := live
	zombie.zombie = true
	cases := []struct {
		name            string
		pidfile, record string // "" = no such file
		procs           map[int]procInfo
		describe        error
		wantPID         int
		wantStray       string
		wantErr         string
		wantGroup       bool
	}{
		{name: "no pidfile", procs: map[int]procInfo{pid: live}},
		{name: "garbage pidfile", pidfile: "not a pid", procs: map[int]procInfo{pid: live}},
		{name: "empty pidfile", pidfile: "\n", procs: map[int]procInfo{pid: live}},
		{name: "pid 0", pidfile: "0", procs: map[int]procInfo{0: live}},
		{name: "pid 1", pidfile: "1", procs: map[int]procInfo{1: live}},
		{name: "negative pid", pidfile: "-4242", procs: map[int]procInfo{-4242: live}},
		{name: "wt's own pid", pidfile: strconv.Itoa(os.Getpid()), procs: map[int]procInfo{os.Getpid(): live}},
		{name: "dead pid", pidfile: "4242"},
		{name: "zombie", pidfile: "4242", procs: map[int]procInfo{pid: zombie}},
		{name: "process table unreadable", pidfile: "4242", describe: errors.New("sysctl: boom"), wantErr: "pid 4242"},
		{name: "another user's process", pidfile: "4242", procs: map[int]procInfo{pid: otherUser}, wantStray: "another user"},
		{name: "another program", pidfile: "4242", procs: map[int]procInfo{pid: mine(pid, "100.5", []string{"sleep", "60"})}, wantStray: "not an mtplx server on port 8003"},
		{name: "mtplx on another port", pidfile: "4242", procs: map[int]procInfo{pid: mine(pid, "100.5", daemonArgv(9999))}, wantStray: "not an mtplx server on port 8003"},
		{name: "older wt: pid only, wrapper", pidfile: "4242", procs: map[int]procInfo{pid: mine(pid, "100.5", wrapperArgv(loadPort))}, wantPID: pid},
		{name: "llmbench: pid only, daemon, trailing newline", pidfile: "4242\n", procs: map[int]procInfo{pid: live}, wantPID: pid},
		{name: "recorded start matches", pidfile: "4242", record: `{"pid":4242,"started":"100.5"}`, procs: map[int]procInfo{pid: live}, wantPID: pid, wantGroup: true},
		{name: "recorded start differs: the pid was recycled", pidfile: "4242", record: `{"pid":4242,"started":"99.0"}`, procs: map[int]procInfo{pid: live}, wantStray: "did not start when the server wt started did"},
		{name: "record of another pid is ignored", pidfile: "4242", record: `{"pid":7,"started":"99.0"}`, procs: map[int]procInfo{pid: live}, wantPID: pid},
		{name: "garbage record is ignored", pidfile: "4242", record: `{"pid":`, procs: map[int]procInfo{pid: live}, wantPID: pid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pt := &procTable{procs: tc.procs, describe: tc.describe}
			e := pt.env(t)
			if tc.pidfile != "" {
				writePidfile(t, e, tc.pidfile)
			}
			if tc.record != "" {
				writeStartRecord(t, e, tc.record)
			}
			id, l := e.loadingMtplx(loadCfg())
			if l.PID != tc.wantPID || id.pid != tc.wantPID || id.group != tc.wantGroup {
				t.Errorf("PID = %d (ident %+v), want pid %d group %v", l.PID, id, tc.wantPID, tc.wantGroup)
			}
			if (tc.wantStray == "") != (l.Stray == "") || !strings.Contains(l.Stray, tc.wantStray) {
				t.Errorf("Stray = %q, want it to contain %q", l.Stray, tc.wantStray)
			}
			if (tc.wantErr == "") != (l.Err == nil) || (l.Err != nil && !strings.Contains(l.Err.Error(), tc.wantErr)) {
				t.Errorf("Err = %v, want it to contain %q", l.Err, tc.wantErr)
			}
			if len(pt.signals) != 0 {
				t.Errorf("signals = %v, want none: reading the state signals nothing", pt.signals)
			}
			if tc.pidfile != "" {
				if _, err := os.Stat(e.mtplxProc.pidfile); err != nil {
					t.Errorf("the pidfile was removed by a read (%v): wt start does not prune it, and neither does this", err)
				}
			}
		})
	}
}

// loadingEnv is an env whose pidfile names a verified, still-loading mtplx
// (pid 4242) that wt itself started when recorded is true.
func loadingEnv(t *testing.T, recorded bool) (*procTable, *env) {
	t.Helper()
	pt := &procTable{procs: map[int]procInfo{4242: mine(4242, "100.5", daemonArgv(loadPort))}}
	e := pt.env(t)
	writePidfile(t, e, "4242")
	if recorded {
		writeStartRecord(t, e, `{"pid":4242,"started":"100.5"}`)
	}
	return pt, e
}

func filesLeft(e *env) (pidfile, record bool) {
	_, perr := os.Stat(e.mtplxProc.pidfile)
	_, rerr := os.Stat(e.mtplxProc.startfile())
	return perr == nil, rerr == nil
}

// TestStopLoadingMtplxTerminatesTheVerifiedServer verifies the stop of a
// loading mtplx: SIGTERM to the process group wt created when it spawned the
// server (it is recorded as wt's own start), to the pid alone when it is not
// — a pidfile from an older wt or from llmbench says nothing about who made
// the group — and, once the process is gone, the pidfile and the start record
// removed. No SIGKILL follows a server that went on SIGTERM.
func TestStopLoadingMtplxTerminatesTheVerifiedServer(t *testing.T) {
	for name, tc := range map[string]struct {
		recorded bool
		want     string
	}{
		"started by this wt: its group": {true, "-4242:15"},
		"pid-only pidfile: the pid":     {false, "4242:15"},
	} {
		t.Run(name, func(t *testing.T) {
			pt, e := loadingEnv(t, tc.recorded)
			pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) { delete(pt.procs, 4242) }
			if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(pt.signals, []string{tc.want}) {
				t.Errorf("signals = %v, want [%s]", pt.signals, tc.want)
			}
			if pidfile, record := filesLeft(e); pidfile || record {
				t.Errorf("pidfile left = %v, start record left = %v, want both removed after a confirmed stop", pidfile, record)
			}
		})
	}
}

// TestStopLoadingMtplxKillsOnlyTheSameProcess verifies the escalation: a
// server that outlives SIGTERM and the grace is re-read from the process
// table, and SIGKILL goes only to the process that is still the one wt
// verified — same start time, still an mtplx server on this port. If the pid
// now belongs to something else, the server is gone and nothing more is
// signalled; a kill sent on the pid alone would land on that other process.
func TestStopLoadingMtplxKillsOnlyTheSameProcess(t *testing.T) {
	t.Run("still the server: killed", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		pt.onSignal = func(pt *procTable, _ int, sig syscall.Signal) {
			if sig == syscall.SIGKILL {
				delete(pt.procs, 4242)
			}
		}
		if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15", "-4242:9"}) {
			t.Errorf("signals = %v, want SIGTERM then SIGKILL to the group", pt.signals)
		}
		if pidfile, _ := filesLeft(e); pidfile {
			t.Error("pidfile left after a confirmed kill")
		}
	})
	t.Run("the pid was recycled during the grace: gone, no kill", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) {
			pt.procs[4242] = mine(4242, "200.0", daemonArgv(loadPort)) // another process, same pid
		}
		if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15"}) {
			t.Errorf("signals = %v, want SIGTERM only", pt.signals)
		}
	})
	t.Run("same process, no longer an mtplx server: not killed, an error", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) {
			pt.procs[4242] = mine(4242, "100.5", []string{"sleep", "60"})
		}
		err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242)
		if err == nil || !strings.Contains(err.Error(), "not killed") {
			t.Fatalf("err = %v, want a refusal to kill", err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15"}) {
			t.Errorf("signals = %v, want SIGTERM only", pt.signals)
		}
		if pidfile, _ := filesLeft(e); !pidfile {
			t.Error("pidfile removed though the stop was not confirmed")
		}
	})
}

// TestStopLoadingMtplxNeverReportsAServerItLeftUp verifies every stop wt
// cannot confirm is an error and leaves the pidfile: the process survives
// SIGKILL, the process table stops answering during the wait, or the signal
// itself is refused. `wt stop` prints "done" and exits 0 only on nil.
func TestStopLoadingMtplxNeverReportsAServerItLeftUp(t *testing.T) {
	t.Run("survives SIGKILL", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242)
		if err == nil || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("err = %v, want still-running", err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15", "-4242:9"}) {
			t.Errorf("signals = %v, want SIGTERM then SIGKILL", pt.signals)
		}
		if pidfile, record := filesLeft(e); !pidfile || !record {
			t.Errorf("pidfile left = %v, record left = %v, want both kept: the server is still up", pidfile, record)
		}
	})
	t.Run("process table unreadable after the signal", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) { pt.describe = errors.New("sysctl: boom") }
		err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242)
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want the process table's error", err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15"}) {
			t.Errorf("signals = %v, want no SIGKILL sent blind", pt.signals)
		}
	})
	t.Run("signal refused", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		e.signal = func(int, syscall.Signal) error { return syscall.EPERM }
		err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242)
		if err == nil || !errors.Is(err, syscall.EPERM) {
			t.Fatalf("err = %v, want EPERM", err)
		}
		if _, ok := pt.procs[4242]; !ok {
			t.Fatal("the fake process vanished")
		}
	})
}

// TestStopLoadingMtplxSignalsNothingItDidNotJustVerify verifies the stop
// re-reads the pidfile and the process table itself and refuses — signalling
// nothing — unless they still name the verified server the question was asked
// about: the pidfile now names another pid, the process is no longer mtplx,
// it died, or the process table cannot be read. The state `wt stop` asked on
// is seconds old by the time the user has answered.
func TestStopLoadingMtplxSignalsNothingItDidNotJustVerify(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, pt *procTable, e *env){
		"pidfile names another pid": func(t *testing.T, pt *procTable, e *env) {
			pt.procs[5000] = mine(5000, "300.0", daemonArgv(loadPort))
			writePidfile(t, e, "5000")
		},
		"pidfile gone": func(t *testing.T, _ *procTable, e *env) { _ = os.Remove(e.mtplxProc.pidfile) },
		"process is another program now": func(_ *testing.T, pt *procTable, _ *env) {
			pt.procs[4242] = mine(4242, "100.5", []string{"sleep", "60"})
		},
		"process recycled": func(_ *testing.T, pt *procTable, _ *env) {
			pt.procs[4242] = mine(4242, "200.0", daemonArgv(loadPort))
		},
		"process died":             func(_ *testing.T, pt *procTable, _ *env) { delete(pt.procs, 4242) },
		"process table unreadable": func(_ *testing.T, pt *procTable, _ *env) { pt.describe = errors.New("sysctl: boom") },
	} {
		t.Run(name, func(t *testing.T) {
			pt, e := loadingEnv(t, true)
			change(t, pt, e)
			if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err == nil {
				t.Error("err = nil, want a refusal")
			}
			if len(pt.signals) != 0 {
				t.Errorf("signals = %v, want none", pt.signals)
			}
		})
	}
}

// TestStopLoadingMtplxCancelledSendsNoKill verifies Ctrl+C during the grace
// ends the stop with the context's error and no SIGKILL: `wt stop` prints
// "cancelled" for it, and the server keeps the SIGTERM it already has.
func TestStopLoadingMtplxCancelledSendsNoKill(t *testing.T) {
	pt, e := loadingEnv(t, true)
	e.stopGrace = 10 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	pt.onSignal = func(*procTable, int, syscall.Signal) { cancel() }
	err := e.stopLoadingMtplx(ctx, loadCfg(), 4242)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !slices.Equal(pt.signals, []string{"-4242:15"}) {
		t.Errorf("signals = %v, want SIGTERM only", pt.signals)
	}
}

// TestMtplxStartRecordsWhatIdentifiesTheServer verifies a start leaves the
// pidfile holding the bare pid — the format llmbench's backend reads with
// int() and writes itself, on the same path — and, beside it, wt's own record
// of that pid and the process's start time, which is what lets a later `wt
// stop` tell the server from a process that reused its pid. When the process
// table cannot be read the start still succeeds and leaves no record, never
// an older start's.
func TestMtplxStartRecordsWhatIdentifiesTheServer(t *testing.T) {
	e, cfg, _, argvFile := mtplxEnv(t, "", 0)
	e.describe = func(pid int) (procInfo, bool, error) { return procInfo{start: "1700000000.25"}, true, nil }
	if err := (mtplxBackend{}).start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "org/m"}, func(Stage) {}); err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _, _ := strings.Cut(string(argv), "\n")
	if b, _ := os.ReadFile(e.mtplxProc.pidfile); string(b) != pid {
		t.Errorf("pidfile = %q, want exactly the pid %s (llmbench parses it with int())", b, pid)
	}
	want := fmt.Sprintf(`{"pid":%s,"started":"1700000000.25"}`, pid)
	if b, _ := os.ReadFile(e.mtplxProc.startfile()); string(b) != want {
		t.Errorf("start record = %q, want %s", b, want)
	}
	if got := e.recordedStart(mustAtoi(t, pid)); got != "1700000000.25" {
		t.Errorf("recordedStart = %q, want the start time just written", got)
	}

	e.describe = func(int) (procInfo, bool, error) { return procInfo{}, false, errors.New("boom") }
	e.recordStart(mustAtoi(t, pid))
	if _, err := os.Stat(e.mtplxProc.startfile()); !os.IsNotExist(err) {
		t.Errorf("start record after an unreadable process table: stat err = %v, want it removed", err)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestProcessSeamsFailClosedHere pins what TestMain arms for this package: the
// process-table read and the signal behind the production env both fail, and
// the pidfile it names is not the real one. A test that forgot to stub them
// can then neither read the developer's mtplx pidfile nor signal one of their
// processes.
func TestProcessSeamsFailClosedHere(t *testing.T) {
	e := defaultEnv()
	if _, _, err := e.describe(os.Getpid()); err == nil {
		t.Error("describe answered: the process-table seam is not closed")
	}
	if err := e.signal(os.Getpid(), 0); err == nil {
		t.Error("signal succeeded: the signal seam is not closed")
	}
	if strings.HasPrefix(e.mtplxProc.pidfile, "/tmp/local-ai-setup") || strings.HasPrefix(e.mtplxProc.logfile, "/tmp/local-ai-setup") {
		t.Errorf("mtplxProc = %+v, want paths away from the real pidfile and log", e.mtplxProc)
	}
	if l := LoadingServer(loadCfg(), "mtplx"); l != (Loading{}) {
		t.Errorf("LoadingServer = %+v, want nothing: no pidfile is readable here", l)
	}
	if err := StopLoading(context.Background(), loadCfg(), "mtplx", os.Getpid()); err == nil {
		t.Error("StopLoading succeeded with the seams closed")
	}
}
