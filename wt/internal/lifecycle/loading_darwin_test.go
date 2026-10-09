package lifecycle

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// startChild starts a harmless child of this test in its own session, as wt
// spawns mtplx, and returns its pid and a channel closed once it has been
// reaped. It is the only process the tests in this file ever signal.
func startChild(t *testing.T, name string, args ...string) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			_ = syscall.Kill(-pid, syscall.SIGKILL) // the child's own group, made above
			<-done
		}
	})
	return pid, done
}

// fakeMtplx writes a shell script named mtplx that idles the way a loading
// server does (no listener); ignoreTerm makes it ignore SIGTERM. The script
// creates the returned ready file once it is set up.
func fakeMtplx(t *testing.T, ignoreTerm bool) (script, ready string) {
	t.Helper()
	dir := t.TempDir()
	ready = filepath.Join(dir, "ready")
	body := "#!/bin/sh\n"
	if ignoreTerm {
		body += "trap '' TERM\n"
	}
	body += ": > '" + ready + "'\nwhile :; do sleep 1; done\n"
	script = filepath.Join(dir, "mtplx")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, ready
}

// childEnv is an env on the real process table whose signal seam refuses any
// target but the child (or the group the child leads).
func childEnv(t *testing.T, child int) *env {
	t.Helper()
	dir := t.TempDir()
	e := testEnv()
	e.mtplxProc = pidProcess{name: "mtplx", pidfile: filepath.Join(dir, "mtplx.pid"), logfile: filepath.Join(dir, "mtplx.log")}
	e.describe = realDescribeProc
	e.signal = func(pid int, sig syscall.Signal) error {
		if pid != child && pid != -child {
			t.Fatalf("signal %d aimed at pid %d, not the test's own child %d", sig, pid, child)
		}
		return realSignalProc(pid, sig)
	}
	e.stopGrace = 300 * time.Millisecond
	e.stopTimeout = 3 * time.Second
	return e
}

func waitReaped(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the child is still running")
	}
}

// TestRealProcessTableDescribesAChild verifies the real process-table read on
// a child of the test: the current user's uid, the child's own group (it was
// started in its own session), a start time that stays the same between
// reads, the exact argv it was started with — an argument holding a space
// stays one token, which splitting `ps` output cannot give — and "not found"
// once the child is gone. This is the evidence every signal rests on.
func TestRealProcessTableDescribesAChild(t *testing.T) {
	pid, done := startChild(t, "/bin/sleep", "60")
	info, found, err := realDescribeProc(pid)
	if err != nil || !found {
		t.Fatalf("describe(child) = found %v err %v", found, err)
	}
	if info.uid != os.Getuid() || info.pgid != pid || info.zombie || info.start == "" {
		t.Errorf("info = %+v, want uid %d, pgid %d, alive, a start time", info, os.Getuid(), pid)
	}
	if len(info.argv) != 2 || info.argv[0] != "/bin/sleep" || info.argv[1] != "60" {
		t.Errorf("argv = %q, want [/bin/sleep 60]", info.argv)
	}
	again, _, _ := realDescribeProc(pid)
	if again.start != info.start {
		t.Errorf("start time changed between reads: %q then %q", info.start, again.start)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	// On its way out the child is exiting, then a zombie, then gone: never
	// an error, which a stop waiting for it would report as a failure.
	for deadline := time.Now().Add(5 * time.Second); ; {
		_, found, err := realDescribeProc(pid)
		if err != nil {
			t.Fatalf("describe(dying child) = %v, want no error at any point of its exit", err)
		}
		if !found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the killed child is still in the process table")
		}
	}
	waitReaped(t, done)
	if _, found, err := realDescribeProc(pid); found || err != nil {
		t.Errorf("describe(reaped child) = found %v err %v, want not found", found, err)
	}

	spaced, _ := startChild(t, "/bin/sh", "-c", "sleep 60", "an arg with spaces")
	info, found, err = realDescribeProc(spaced)
	if err != nil || !found || len(info.argv) != 4 || info.argv[3] != "an arg with spaces" {
		t.Errorf("argv = %q (found %v err %v), want four tokens, the last with its spaces", info.argv, found, err)
	}
}

// TestProcArgvTreatsNoArgumentsAsExiting verifies what the real process-table
// read makes of kern.procargs2's answer: EINVAL for a pid the kernel just
// listed is a process being taken apart (exiting, no argv, no error), any
// other failure is an error, and a buffer is parsed. A dying mtplx spends a
// quarter of a second and more in that state; read as an error, it made
// `wt stop mtplx` print "failed" over a server that had stopped.
func TestProcArgvTreatsNoArgumentsAsExiting(t *testing.T) {
	if argv, exiting, err := procArgv(nil, unix.EINVAL); argv != nil || !exiting || err != nil {
		t.Errorf("EINVAL: argv %q exiting %v err %v, want exiting and no error", argv, exiting, err)
	}
	if _, exiting, err := procArgv(nil, unix.EPERM); exiting || !errors.Is(err, unix.EPERM) {
		t.Errorf("EPERM: exiting %v err %v, want the error", exiting, err)
	}
	raw := append(binary.NativeEndian.AppendUint32(nil, 2), "/bin/sleep\x00\x00\x00sleep\x0060\x00HOME=/x\x00"...)
	if argv, exiting, err := procArgv(raw, nil); err != nil || exiting || !slices.Equal(argv, []string{"sleep", "60"}) {
		t.Errorf("buffer: argv %q exiting %v err %v, want [sleep 60]", argv, exiting, err)
	}
}

// TestLoadingMtplxOnTheRealProcessTable runs the whole path against real
// processes the test started: a script named mtplx, run as wt runs the
// server, is identified from a pidfile, stopped with SIGTERM and confirmed
// gone; one that ignores SIGTERM is killed after the grace; and a pidfile
// naming a live `sleep` — a recycled pid — is reported as not mtplx and gets
// no signal. It is what shows the command-line rule matches how a `#!/bin/sh`
// mtplx really appears in the process table, not only the fixtures above.
func TestLoadingMtplxOnTheRealProcessTable(t *testing.T) {
	_, _, port := mtplxEndpoint(loadCfg())
	args := []string{"serve", "--model", "org/m", "--port", strconv.Itoa(port), "--host", "127.0.0.1", "--model-id", "org/m"}
	for name, ignoreTerm := range map[string]bool{"goes on SIGTERM": false, "ignores SIGTERM": true} {
		t.Run(name, func(t *testing.T) {
			script, ready := fakeMtplx(t, ignoreTerm)
			pid, done := startChild(t, script, args...)
			e := childEnv(t, pid)
			writePidfile(t, e, strconv.Itoa(pid))
			e.recordStart(pid)
			for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(ready); err == nil {
					break
				} else if time.Now().After(deadline) {
					t.Fatal("the fake mtplx never came up")
				}
			}
			id, l := e.loadingMtplx(loadCfg())
			if l.PID != pid || !id.group {
				t.Fatalf("Loading = %+v ident = %+v, want the child identified as mtplx, in the group wt recorded as its own", l, id)
			}
			if err := e.stopLoadingMtplx(context.Background(), loadCfg(), pid); err != nil {
				t.Fatal(err)
			}
			waitReaped(t, done)
			if pidfile, record := filesLeft(e); pidfile || record {
				t.Errorf("pidfile left = %v, record left = %v, want both removed", pidfile, record)
			}
		})
	}
	t.Run("a live process that is not mtplx", func(t *testing.T) {
		pid, _ := startChild(t, "/bin/sleep", "60")
		e := childEnv(t, pid)
		e.signal = func(p int, sig syscall.Signal) error {
			t.Fatalf("signal %d sent to pid %d: a process that is not mtplx must get none", sig, p)
			return nil
		}
		writePidfile(t, e, strconv.Itoa(pid))
		_, l := e.loadingMtplx(loadCfg())
		if l.PID != 0 || l.Stray == "" || l.Err != nil {
			t.Errorf("Loading = %+v, want a stray note and no pid", l)
		}
		if err := e.stopLoadingMtplx(context.Background(), loadCfg(), pid); err == nil {
			t.Error("stop of a process that is not mtplx succeeded")
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Errorf("the sleep is gone (%v): it must be left alone", err)
		}
	})
}
