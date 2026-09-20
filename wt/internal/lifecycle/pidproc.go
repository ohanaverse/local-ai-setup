package lifecycle

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// pidProcess is a pidfile-tracked background process: wt spawns it, records its
// pid where modelman also records it, and keeps the log.
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
		if tail := p.logTail(512); tail != "" {
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

// kill stops the process (SIGTERM, then SIGKILL after 5s) and removes the pidfile.
func (s *spawned) kill() {
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	_ = os.Remove(s.p.pidfile)
}

// logTail returns up to max trailing bytes of the log ("" when unreadable). It
// seeks from the end instead of reading the file: the log is append-only and
// shared with modelman, so it grows without bound, and a failed start must not
// read all of it to show a 512-byte tail.
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
	return string(b)
}
