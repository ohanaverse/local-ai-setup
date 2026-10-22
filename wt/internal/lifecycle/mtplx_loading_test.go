package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
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
	// onDescribe runs before each read, so a test can change the table as
	// time passes or by how often it was read.
	onDescribe func(pt *procTable)
	signalErr  error // what signal returns, after recording and onSignal
}

func (pt *procTable) env(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := testEnv()
	e.mtplxProc = pidProcess{name: "mtplx", pidfile: filepath.Join(dir, "mtplx.pid"), logfile: filepath.Join(dir, "mtplx.log")}
	e.stopGrace = 60 * time.Millisecond
	e.stopTimeout = 60 * time.Millisecond
	e.describe = func(pid int) (procInfo, bool, error) {
		if pt.onDescribe != nil {
			pt.onDescribe(pt)
		}
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
		return pt.signalErr
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
		"framework Python daemon":   {[]string{"/opt/homebrew/Frameworks/Python.framework/Versions/3.13/Resources/Python.app/Contents/MacOS/Python", "-m", "mtplx.server.openai", "--port", "8003"}, true},
		"framework Python wrapper":  {[]string{"/x/Python.app/Contents/MacOS/Python", "/b/mtplx", "serve", "--port", "8003"}, true},
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
// The process group is wt's to signal only when wt recorded the start and the
// pid still leads the group. A pidfile or record that is a symlink is not
// this user's own file and reads as absent — both live in /tmp, where another
// user can plant one — and a process that is already exiting is not a server.
// Nothing here may signal: the read is what `wt stop` asks its question on.
func TestLoadingMtplxNeverRestsOnThePidAlone(t *testing.T) {
	const pid = 4242
	live := mine(pid, "100.5", daemonArgv(loadPort))
	otherUser := live
	otherUser.uid = os.Getuid() + 1
	zombie := live
	zombie.zombie = true
	inAnothersGroup := live
	inAnothersGroup.pgid = 77
	exiting := mine(pid, "100.5", nil)
	exiting.exiting = true
	cases := []struct {
		name            string
		pidfile, record string // "" = no such file
		linkPidfile     bool   // the pidfile is a symlink to a file with that content
		linkRecord      bool   // so is the record
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
		{name: "recorded, but the pid does not lead its group", pidfile: "4242", record: `{"pid":4242,"started":"100.5"}`, procs: map[int]procInfo{pid: inAnothersGroup}, wantPID: pid},
		{name: "exiting", pidfile: "4242", record: `{"pid":4242,"started":"100.5"}`, procs: map[int]procInfo{pid: exiting}},
		{name: "the pidfile is a symlink", pidfile: "4242", linkPidfile: true, procs: map[int]procInfo{pid: live}},
		{name: "the record is a symlink: no group", pidfile: "4242", record: `{"pid":4242,"started":"100.5"}`, linkRecord: true, procs: map[int]procInfo{pid: live}, wantPID: pid},
		{name: "the record is a symlink: its start time is not read", pidfile: "4242", record: `{"pid":4242,"started":"99.0"}`, linkRecord: true, procs: map[int]procInfo{pid: live}, wantPID: pid},
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
			if tc.linkPidfile {
				symlinked(t, e.mtplxProc.pidfile)
			}
			if tc.linkRecord {
				symlinked(t, e.mtplxProc.startfile())
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
				if _, err := os.Lstat(e.mtplxProc.pidfile); err != nil {
					t.Errorf("the pidfile was removed by a read (%v): wt start does not prune it, and neither does this", err)
				}
			}
		})
	}
}

// symlinked moves the file at path aside and leaves a symlink to it at path.
func symlinked(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+".target"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".target", path); err != nil {
		t.Fatal(err)
	}
}

// TestReadOwnFileReadsOnlyThisUsersRegularFile verifies the read behind the
// pidfile and the start record: a regular file the given user owns is read;
// one another user owns, a symlink (even to such a file), a directory and a
// missing path are all "no file". Both files live in the shared /tmp, where
// another local user can create either while it is absent, and the record is
// what lets wt signal a whole process group.
func TestReadOwnFileReadsOnlyThisUsersRegularFile(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "own")
	if err := os.WriteFile(own, []byte("4242"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, ok := readOwnFile(own, os.Getuid()); !ok || string(b) != "4242" {
		t.Errorf("own file = %q, %v; want it read", b, ok)
	}
	if _, ok := readOwnFile(own, os.Getuid()+1); ok {
		t.Error("a file another user owns was read")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(own, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"symlink": link, "directory": dir, "missing": filepath.Join(dir, "none")} {
		if _, ok := readOwnFile(path, os.Getuid()); ok {
			t.Errorf("%s was read as this user's file", name)
		}
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
// removed. A recorded server that no longer leads its own group is signalled
// alone too: the group is then somebody else's. No SIGKILL follows a server
// that went on SIGTERM, and a SIGTERM that finds the process already gone
// (ESRCH) is a stop, not a failure.
func TestStopLoadingMtplxTerminatesTheVerifiedServer(t *testing.T) {
	for name, tc := range map[string]struct {
		recorded bool
		pgid     int
		sigErr   error
		want     string
	}{
		"started by this wt: its group":           {true, 4242, nil, "-4242:15"},
		"pid-only pidfile: the pid":               {false, 4242, nil, "4242:15"},
		"recorded, not the group's leader: alone": {true, 77, nil, "4242:15"},
		"gone before the signal landed":           {true, 4242, syscall.ESRCH, "-4242:15"},
	} {
		t.Run(name, func(t *testing.T) {
			pt, e := loadingEnv(t, tc.recorded)
			info := pt.procs[4242]
			info.pgid = tc.pgid
			pt.procs[4242] = info
			pt.signalErr = tc.sigErr
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
	t.Run("left its group during the grace: only the pid is killed", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		pt.onSignal = func(pt *procTable, _ int, sig syscall.Signal) {
			if sig == syscall.SIGKILL {
				delete(pt.procs, 4242)
				return
			}
			info := pt.procs[4242]
			info.pgid = 77
			pt.procs[4242] = info
		}
		if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15", "4242:9"}) {
			t.Errorf("signals = %v, want SIGTERM to the group, then SIGKILL to the pid alone", pt.signals)
		}
	})
	t.Run("gone before the kill landed: stopped", func(t *testing.T) {
		pt, e := loadingEnv(t, true)
		pt.onSignal = func(pt *procTable, _ int, sig syscall.Signal) {
			if sig == syscall.SIGKILL {
				delete(pt.procs, 4242)
				pt.signalErr = syscall.ESRCH
			}
		}
		if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err != nil {
			t.Fatalf("err = %v, want nil: ESRCH from the kill means the process is gone", err)
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
	t.Run("process table unreadable when the grace ends", func(t *testing.T) {
		// The read that fails is the one made before SIGKILL, after the
		// wait saw the process still there: with no grace, the second read
		// after the signal.
		pt, e := loadingEnv(t, true)
		e.stopGrace = 0
		reads := -1
		pt.onSignal = func(*procTable, int, syscall.Signal) { reads = 0 }
		pt.onDescribe = func(pt *procTable) {
			if reads >= 0 {
				if reads++; reads == 2 {
					pt.describe = errors.New("sysctl: boom")
				}
			}
		}
		err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242)
		if err == nil || !strings.Contains(err.Error(), "cannot confirm mtplx (pid 4242) stopped") {
			t.Fatalf("err = %v, want cannot-confirm: an unread process is not a stopped one", err)
		}
		if !slices.Equal(pt.signals, []string{"-4242:15"}) {
			t.Errorf("signals = %v, want no SIGKILL sent blind", pt.signals)
		}
		if pidfile, record := filesLeft(e); !pidfile || !record {
			t.Errorf("pidfile left = %v, record left = %v, want both kept", pidfile, record)
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

// TestStopLoadingMtplxCancelledSendsNoKill verifies Ctrl+C during a wait
// ends the stop with no further signal and an error that says what the
// server already has: a *StopInterruptedError naming the pid and the last
// signal sent, wrapping the context's error. `wt stop` prints "cancelled" for
// it and must not say the server "was not stopped" — it has SIGTERM and, in
// the normal case, exits a moment later.
func TestStopLoadingMtplxCancelledSendsNoKill(t *testing.T) {
	for name, tc := range map[string]struct {
		cancelOn syscall.Signal
		grace    time.Duration
		signals  []string
		sent     string
	}{
		"during the grace":        {syscall.SIGTERM, 10 * time.Second, []string{"-4242:15"}, "SIGTERM"},
		"after the kill was sent": {syscall.SIGKILL, 20 * time.Millisecond, []string{"-4242:15", "-4242:9"}, "SIGKILL"},
	} {
		t.Run(name, func(t *testing.T) {
			pt, e := loadingEnv(t, true)
			e.stopGrace, e.stopTimeout = tc.grace, 10*time.Second
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pt.onSignal = func(_ *procTable, _ int, sig syscall.Signal) {
				if sig == tc.cancelOn {
					cancel()
				}
			}
			err := e.stopLoadingMtplx(ctx, loadCfg(), 4242)
			var interrupted *StopInterruptedError
			if !errors.Is(err, context.Canceled) || !errors.As(err, &interrupted) {
				t.Fatalf("err = %v, want a *StopInterruptedError wrapping context.Canceled", err)
			}
			if interrupted.PID != 4242 || interrupted.Signal != tc.sent {
				t.Errorf("interrupted = %+v, want pid 4242 and %s", interrupted, tc.sent)
			}
			if want := tc.sent + " was sent to mtplx (pid 4242) and wt did not wait for it to exit"; !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to say %q", err, want)
			}
			if !slices.Equal(pt.signals, tc.signals) {
				t.Errorf("signals = %v, want %v", pt.signals, tc.signals)
			}
			if pidfile, record := filesLeft(e); !pidfile || !record {
				t.Errorf("pidfile left = %v, record left = %v, want both kept: wt did not see the server go", pidfile, record)
			}
		})
	}
}

// TestStopLoadingMtplxWaitsOutAProcessThatIsExiting verifies the stop of a
// server the size mtplx is. After SIGTERM such a process stays in the process
// table with no arguments to read while the kernel frees its memory — about a
// quarter of a second at 28 GB, longer for a bigger model. That is a process
// on its way out: wt keeps waiting, sends no SIGKILL, and reports the stop
// done when the process is gone, also when the exit outlasts the grace. wt
// used to give up reading it after 200 ms and print "failed" over a server
// that had stopped. One that never finishes exiting is an error, still with
// no SIGKILL: wt can no longer verify what the pid is.
func TestStopLoadingMtplxWaitsOutAProcessThatIsExiting(t *testing.T) {
	for name, tc := range map[string]struct {
		exitsAfter time.Duration // 0 = never
		secondWait time.Duration
		wantErr    string
	}{
		"gone within the grace":         {5 * time.Millisecond, 5 * time.Second, ""},
		"gone only after the grace":     {150 * time.Millisecond, 5 * time.Second, ""},
		"never finishes: no kill, fail": {0, 60 * time.Millisecond, "has not finished exiting after SIGTERM"},
	} {
		t.Run(name, func(t *testing.T) {
			pt, e := loadingEnv(t, true)
			e.stopGrace, e.stopTimeout = 60*time.Millisecond, tc.secondWait
			var goneAt time.Time
			pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) {
				info := pt.procs[4242]
				info.exiting, info.argv = true, nil
				pt.procs[4242] = info
				if tc.exitsAfter > 0 {
					goneAt = time.Now().Add(tc.exitsAfter)
				}
			}
			pt.onDescribe = func(pt *procTable) {
				if !goneAt.IsZero() && time.Now().After(goneAt) {
					delete(pt.procs, 4242)
				}
			}
			err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242)
			if (tc.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if !slices.Equal(pt.signals, []string{"-4242:15"}) {
				t.Errorf("signals = %v, want one SIGTERM and no SIGKILL", pt.signals)
			}
			if pidfile, record := filesLeft(e); pidfile != (err != nil) || record != (err != nil) {
				t.Errorf("pidfile left = %v, record left = %v after err = %v; want them removed exactly when the stop is confirmed", pidfile, record, err)
			}
		})
	}
}

// TestStopLoadingMtplxLeavesANewerStartsPidfile verifies the cleanup after a
// confirmed stop removes the pidfile only while it still names the process
// that was stopped. A `wt start` that began while the stop waited has written
// its own pid there, and deleting that file would leave the new server with
// nothing a later `wt stop` could find it by.
func TestStopLoadingMtplxLeavesANewerStartsPidfile(t *testing.T) {
	pt, e := loadingEnv(t, true)
	pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) {
		delete(pt.procs, 4242)
		writePidfile(t, e, "5000")
	}
	if err := e.stopLoadingMtplx(context.Background(), loadCfg(), 4242); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(e.mtplxProc.pidfile); string(b) != "5000" {
		t.Errorf("pidfile = %q, want the newer start's pid 5000 left in place", b)
	}
}

// stopEnv is loadingEnv with the server's port open on a real listener: an
// mtplx that is serving, whose `mtplx stop` (the fake runner) closes the port
// and runs onStop. It returns the process table, the env and the config for
// that port.
func stopEnv(t *testing.T, recorded bool, onStop func(pt *procTable, e *env)) (*procTable, *env, *config.Config) {
	t.Helper()
	srv, addr := serveFree(t, chatHandler())
	_, portStr, _ := net.SplitHostPort(addr)
	port := mustAtoi(t, portStr)
	pt := &procTable{procs: map[int]procInfo{4242: mine(4242, "100.5", daemonArgv(port))}}
	e := pt.env(t)
	writePidfile(t, e, "4242")
	if recorded {
		writeStartRecord(t, e, `{"pid":4242,"started":"100.5"}`)
	}
	e.lookPath = func(string) (string, error) { return "/bin/mtplx", nil }
	e.run = func(context.Context, string, ...string) ([]byte, error) {
		srv.Close()
		onStop(pt, e)
		return nil, nil
	}
	return pt, e, provCfg("mtplx", "http://"+addr+"/v1")
}

// TestMtplxStopRemovesThePidfileOfTheServerItStopped verifies the ordinary
// stop — `mtplx stop` through the port — removes the pidfile and wt's start
// record once the server they named is gone (#343): before, every stop left
// both behind, naming a dead pid that a later process could be given. It
// signals nothing: the stop went through the port.
func TestMtplxStopRemovesThePidfileOfTheServerItStopped(t *testing.T) {
	for name, recorded := range map[string]bool{"started by this wt": true, "pid-only pidfile": false} {
		t.Run(name, func(t *testing.T) {
			pt, e, cfg := stopEnv(t, recorded, func(pt *procTable, _ *env) { delete(pt.procs, 4242) })
			if err := (mtplxBackend{}).stop(context.Background(), e, cfg); err != nil {
				t.Fatal(err)
			}
			if pidfile, record := filesLeft(e); pidfile || record {
				t.Errorf("pidfile left = %v, start record left = %v, want both removed", pidfile, record)
			}
			if len(pt.signals) != 0 {
				t.Errorf("signals = %v, want none", pt.signals)
			}
		})
	}
}

// TestMtplxStopLeavesAPidfileItCannotTieToTheServerItStopped verifies the
// cleanup's limits, the same ones the stop of a loading server keeps: the
// files go only when the pidfile named a process wt verified as this
// provider's server before the stop, and only once the process table shows
// that process gone. A pidfile naming some other live process, one that is
// not this user's own regular file, one a newer start has rewritten, a
// process table that cannot be read, a server still exiting — each is left
// exactly as it was. Removing any of them would take away the only handle a
// later `wt stop` has on a server that is still there, or follow a link
// another user planted in /tmp.
func TestMtplxStopLeavesAPidfileItCannotTieToTheServerItStopped(t *testing.T) {
	for name, tc := range map[string]struct {
		before  func(t *testing.T, pt *procTable, e *env)
		onStop  func(t *testing.T, pt *procTable, e *env)
		wantPid string
	}{
		"names another live process": {
			before: func(t *testing.T, pt *procTable, e *env) {
				pt.procs[4242] = mine(4242, "100.5", []string{"sleep", "60"})
			},
			wantPid: "4242",
		},
		"names an mtplx on another port": {
			before:  func(t *testing.T, pt *procTable, e *env) { pt.procs[4242] = mine(4242, "100.5", daemonArgv(1)) },
			wantPid: "4242",
		},
		"names a pid that was reused": {
			before:  func(t *testing.T, pt *procTable, e *env) { writeStartRecord(t, e, `{"pid":4242,"started":"7.0"}`) },
			wantPid: "4242",
		},
		"names a dead pid already": {
			before:  func(t *testing.T, pt *procTable, e *env) { writePidfile(t, e, "999") },
			wantPid: "999",
		},
		"is a symlink": {
			before:  func(t *testing.T, pt *procTable, e *env) { symlinked(t, e.mtplxProc.pidfile) },
			wantPid: "4242",
		},
		"process table unreadable": {
			before:  func(t *testing.T, pt *procTable, e *env) { pt.describe = errors.New("sysctl failed") },
			wantPid: "4242",
		},
		"rewritten by a newer start": {
			onStop: func(t *testing.T, pt *procTable, e *env) {
				delete(pt.procs, 4242)
				writePidfile(t, e, "5000")
				writeStartRecord(t, e, `{"pid":5000,"started":"200.0"}`)
			},
			wantPid: "5000",
		},
		"server still exiting when the wait ends": {
			onStop: func(t *testing.T, pt *procTable, e *env) {
				info := pt.procs[4242]
				info.exiting, info.argv = true, nil
				pt.procs[4242] = info
			},
			wantPid: "4242",
		},
	} {
		t.Run(name, func(t *testing.T) {
			var onStop func(t *testing.T, pt *procTable, e *env)
			pt, e, cfg := stopEnv(t, true, func(pt *procTable, e *env) {
				if onStop != nil {
					onStop(t, pt, e)
				} else {
					delete(pt.procs, 4242)
				}
			})
			onStop = tc.onStop
			if tc.before != nil {
				tc.before(t, pt, e)
			}
			if err := (mtplxBackend{}).stop(context.Background(), e, cfg); err != nil {
				t.Fatal(err)
			}
			if b, err := os.ReadFile(e.mtplxProc.pidfile); err != nil || string(b) != tc.wantPid {
				t.Errorf("pidfile = %q (%v), want %q left in place", b, err, tc.wantPid)
			}
			if _, record := filesLeft(e); !record {
				t.Error("start record removed, want it left in place")
			}
			if len(pt.signals) != 0 {
				t.Errorf("signals = %v, want none", pt.signals)
			}
		})
	}
}

// TestMtplxStopThatFailsLeavesThePidfile verifies a stop that left the port
// answering removes nothing: the server is still there, and the pidfile is
// what the next stop finds it by.
func TestMtplxStopThatFailsLeavesThePidfile(t *testing.T) {
	_, e, cfg := stopEnv(t, true, func(*procTable, *env) {})
	// An `mtplx stop` that exits 0 and stops nothing.
	e.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if err := (mtplxBackend{}).stop(context.Background(), e, cfg); err == nil {
		t.Fatal("stop = nil over a port that still answers")
	}
	if pidfile, record := filesLeft(e); !pidfile || !record {
		t.Errorf("pidfile left = %v, start record left = %v, want both left", pidfile, record)
	}
}

// stubProcessSeams points the production env at a fake process table and a
// pidfile under t.TempDir() for one test, so the exported LoadingServer and
// StopLoading can be run whole. It returns the table and the pidfile's path.
func stubProcessSeams(t *testing.T) (*procTable, string) {
	t.Helper()
	pt := &procTable{procs: map[int]procInfo{4242: mine(4242, "100.5", daemonArgv(loadPort))}}
	fake := pt.env(t)
	oldDescribe, oldSignal := describeProc, signalProc
	describeProc, signalProc = fake.describe, fake.signal
	t.Cleanup(func() { describeProc, signalProc = oldDescribe, oldSignal })
	t.Setenv(mtplxPidfileEnv, fake.mtplxProc.pidfile)
	writePidfile(t, fake, "4242")
	return pt, fake.mtplxProc.pidfile
}

// TestLoadingStopIsMtplxOnly verifies the pidfile answers for mtplx and for
// nothing else: with a verified mtplx loading, LoadingServer reports it for
// mtplx and reports nothing for omlx or ollama, and StopLoading for another
// provider is refused as unsupported with no signal. Without the two guards
// a refused omlx or ollama would be handed mtplx's pid, and `wt stop omlx`
// could end the mtplx server.
func TestLoadingStopIsMtplxOnly(t *testing.T) {
	pt, _ := stubProcessSeams(t)
	if l := LoadingServer(loadCfg(), "mtplx"); l.PID != 4242 {
		t.Fatalf("LoadingServer(mtplx) = %+v, want pid 4242: the fixture is wrong", l)
	}
	for _, id := range []string{"omlx", "omlx-6bit", "ollama"} {
		if l := LoadingServer(loadCfg(), id); l != (Loading{}) {
			t.Errorf("LoadingServer(%s) = %+v, want nothing: only mtplx has a pidfile", id, l)
		}
		var unsupported *UnsupportedError
		if err := StopLoading(context.Background(), loadCfg(), id, 4242); !errors.As(err, &unsupported) {
			t.Errorf("StopLoading(%s) = %v, want *UnsupportedError", id, err)
		}
	}
	if len(pt.signals) != 0 {
		t.Errorf("signals = %v, want none", pt.signals)
	}
}

// TestStopLoadingRemovesTheFamilysRoutesOnlyAfterAConfirmedStop verifies
// StopLoading does what Stop does once the server is gone — every mtplx route
// leaves config.yaml — and writes no route when the stop was not confirmed. A
// route left behind sends requests to a port nothing listens on; one removed
// over a server that is still up hides a running model from the proxy.
func TestStopLoadingRemovesTheFamilysRoutesOnlyAfterAConfirmedStop(t *testing.T) {
	t.Run("not confirmed: no route write", func(t *testing.T) {
		pt, _ := stubProcessSeams(t)
		calls, _ := stubRoutes(t, litellm.Result{}, nil)
		pt.signalErr = syscall.EPERM
		if err := StopLoading(context.Background(), loadCfg(), "mtplx", 4242); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("err = %v, want EPERM", err)
		}
		if len(*calls) != 0 {
			t.Errorf("route writes = %+v, want none", *calls)
		}
	})
	t.Run("confirmed: the family's routes go", func(t *testing.T) {
		pt, pidfile := stubProcessSeams(t)
		calls, _ := stubRoutes(t, litellm.Result{}, nil)
		pt.onSignal = func(pt *procTable, _ int, _ syscall.Signal) { delete(pt.procs, 4242) }
		if err := StopLoading(context.Background(), loadCfg(), "mtplx", 4242); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || !slices.Equal((*calls)[0].families, []string{"mtplx"}) || len((*calls)[0].add) != 0 {
			t.Errorf("route writes = %+v, want one removal of the mtplx family", *calls)
		}
		if !slices.Equal(pt.signals, []string{"4242:15"}) {
			t.Errorf("signals = %v, want SIGTERM to the pid", pt.signals)
		}
		if _, err := os.Stat(pidfile); !os.IsNotExist(err) {
			t.Errorf("pidfile after a confirmed stop: stat err = %v, want it removed", err)
		}
	})
}

// TestMtplxStartThatFailsLeavesNoPidfileAndNoRecord verifies a start that
// fails after the server was spawned removes both files it wrote: the pidfile
// and the start record beside it. A record left behind would be found by the
// next server to get that pid, and would vouch for a start wt never made.
func TestMtplxStartThatFailsLeavesNoPidfileAndNoRecord(t *testing.T) {
	e, cfg, _, _ := mtplxEnv(t, "die", 0)
	recorded := false
	e.describe = func(pid int) (procInfo, bool, error) {
		recorded = true
		return procInfo{start: "1700000000.25"}, true, nil
	}
	if err := (mtplxBackend{}).start(context.Background(), e, cfg, Target{ProviderID: "mtplx", ModelName: "org/m"}, func(Stage) {}); err == nil {
		t.Fatal("start of a server that dies while loading succeeded")
	}
	if !recorded {
		t.Fatal("the start never recorded the server: the fixture proves nothing")
	}
	if pidfile, record := filesLeft(e); pidfile || record {
		t.Errorf("pidfile left = %v, start record left = %v, want neither after a failed start", pidfile, record)
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
