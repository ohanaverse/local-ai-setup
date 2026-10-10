package lifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// pidProcess is a pidfile-tracked background process: wt spawns it, records its
// pid where llmbench also records it, and keeps the log.
type pidProcess struct{ name, pidfile, logfile string }

// spawned is a live child started by pidProcess.spawn.
type spawned struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	p       pidProcess
}

// spawn starts bin with args detached in its own session (it survives the
// terminal closing), stdout/stderr appended to the log, pid written to the
// pidfile. It returns an error (with a log tail) if the process exits within
// 200ms.
func (p pidProcess) spawn(bin string, args []string) (*spawned, error) {
	log, err := os.OpenFile(p.logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	_ = log.Close() // the child holds its own descriptor
	sp := &spawned{cmd: cmd, done: make(chan struct{}), p: p}
	go func() {
		sp.waitErr = cmd.Wait()
		close(sp.done)
	}()
	if err := os.WriteFile(p.pidfile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		sp.kill()
		return nil, err
	}
	select {
	case <-sp.done:
		msg := fmt.Sprintf("%s exited immediately (%v)", p.name, sp.waitErr)
		if tail := p.logTailLines(512); tail != "" {
			msg += "; log tail: " + tail
		}
		_ = os.Remove(p.pidfile)
		return nil, fmt.Errorf("%s", msg)
	case <-time.After(200 * time.Millisecond):
	}
	return sp, nil
}

// exited reports whether the process has exited (and how); it satisfies procWatch.
func (s *spawned) exited() (bool, error) {
	select {
	case <-s.done:
		return true, s.waitErr
	default:
		return false, nil
	}
}

// kill stops the process (SIGTERM, then SIGKILL after 5s) and removes the
// pidfile and the start record beside it.
func (s *spawned) kill() {
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	_ = os.Remove(s.p.pidfile)
	_ = os.Remove(s.p.startfile())
}

// logTail returns up to the last max bytes of the log ("" when unreadable). It
// seeks from the end instead of reading the file: the log is append-only and
// shared with llmbench, so it grows without bound, and a failed start must not
// read all of it to show a 512-byte tail. The value it returns is then clamped to
// at most max bytes, because the subprocess may append between the stat and the
// read — without the clamp the returned string can exceed max and the guarantee
// above would not hold exactly when it matters (a failed start).
func (p pidProcess) logTail(max int) string {
	f, err := os.Open(p.logfile)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	if size := fi.Size(); size > int64(max) {
		if _, err := f.Seek(size-int64(max), io.SeekStart); err != nil {
			return ""
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	if len(b) > max {
		b = b[len(b)-max:]
	}
	return string(b)
}

// logTailLines is logTail for showing to a user: the tail begins at the start
// of a line. A cut at a byte count lands mid-word ("stained MTP runtime"), so
// one byte more than max is read and everything up to the first newline is
// dropped — the byte before the tail says whether the cut already fell on a
// line start, in which case nothing of the tail is lost. A log that fits in
// max is returned whole, and a tail with nothing after its first newline — no
// newline at all, or only the one that ends a last line longer than max — is
// one long line, returned as cut: its end is still worth more than nothing.
func (p pidProcess) logTailLines(max int) string {
	tail := p.logTail(max + 1)
	if len(tail) <= max {
		return tail
	}
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		return tail[i+1:]
	}
	return tail[1:]
}
