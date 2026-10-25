package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
)

// A process server that is still loading has no listener — mtplx reads its
// weights before it opens its port, so its port refuses and `mtplx stop
// --port` cannot reach it (#308). The only handle on it is the pidfile wt
// wrote at spawn. A pid by itself proves nothing — it can have been reused —
// so this file identifies the process before anything is signalled, and
// identifies it again before a SIGKILL. What identifies a family's server
// comes from its process spec (provider.ProcessSpec).

// processSpecFor is the one seam through which lifecycle reads a process
// family's spec; a test swaps it to give a synthetic family a spec.
var processSpecFor = provider.ProcessFor

// serverPort is the port family's server is identified on.
func serverPort(cfg *config.Config, family string) int {
	_, _, port := serverEndpoint(cfg, family)
	return port
}

// Loading is what wt's pidfile says about a provider whose port refuses. The
// zero value is the usual one: no pidfile, or one that names no live process.
type Loading struct {
	// PID > 0: a live process wt verified as this provider's server. Its
	// port refuses, so it is still loading.
	PID int
	// Stray says, when not empty, that the pidfile names a live process that
	// is not verifiably that server, and why. wt leaves it alone.
	Stray string
	// Err: the pidfile names a live pid and the process table could not be
	// read, so wt cannot tell.
	Err error
}

// LoadingServer reads what the pidfile of family's server says (Loading).
// Only a process family (provider.ProcessFor) has one. The caller asks it for
// a family whose port refused; it probes nothing and signals nothing.
func LoadingServer(cfg *config.Config, family string) Loading {
	_, l := defaultEnv().loadingServer(cfg, family)
	return l
}

// StopLoading ends the still-loading server LoadingServer reported as pid and
// removes the family's routes, like Stop. It verifies the process again
// itself and signals nothing unless the pidfile still names pid and pid is
// still that server. nil means the process is confirmed gone.
func StopLoading(ctx context.Context, cfg *config.Config, providerID string, pid int) error {
	family := localmodels.Family(providerID)
	if _, ok := processSpecFor(family); !ok {
		return &UnsupportedError{ProviderID: providerID}
	}
	if err := defaultEnv().stopLoadingServer(ctx, cfg, family, pid); err != nil {
		return err
	}
	routeAfterStop(ctx, cfg, providerID)
	return nil
}

// StopInterruptedError is StopLoading ended by its context (Ctrl+C) while it
// waited for a process it had already signalled. The server has the signal
// and, unless it ignores it, exits a moment later: the caller must not report
// it as "not stopped". wt did not see it go, so the pidfile, the start record
// and the family's routes are as they were.
type StopInterruptedError struct {
	PID    int
	Signal string // "SIGTERM" or "SIGKILL": the last signal sent
	Err    error  // the context's error
	// Binary is the server's binary (its family's spec.Binary), which the
	// message names.
	Binary string
}

func (e *StopInterruptedError) Error() string {
	name := e.Binary
	if name == "" {
		name = "the server"
	}
	return fmt.Sprintf("%v — %s was sent to %s (pid %d) and wt did not wait for it to exit", e.Err, e.Signal, name, e.PID)
}

func (e *StopInterruptedError) Unwrap() error { return e.Err }

// readOwnFile reads path only when it is a regular file that uid owns; ok is
// false for anything else. The pidfile and the start record live in /tmp,
// where another local user can create either one while it is absent. A file
// this user did not write names nothing wt may signal, so it reads as no file
// at all. The check is made on the open descriptor, and a symlink is not
// followed, so the file checked is the file read.
func readOwnFile(path string, uid int) (b []byte, ok bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	if st, isStat := fi.Sys().(*syscall.Stat_t); !isStat || int(st.Uid) != uid {
		return nil, false
	}
	b, err = io.ReadAll(io.LimitReader(f, 4096))
	return b, err == nil
}

// startRecord is what wt writes beside the pidfile when it spawns the server.
// The pidfile itself stays a bare pid, the format llmbench's backend reads and
// writes on the same path; the record binds that pid to one process by its
// start time, and its presence says the process leads a group wt created
// (spawn sets Setsid).
type startRecord struct {
	PID     int    `json:"pid"`
	Started string `json:"started"`
}

// startfile is the path of the start record: beside the pidfile.
func (p pidProcess) startfile() string { return p.pidfile + ".wt" }

// recordStart writes the start record for family's server just spawned as
// pid, atomically. When the process table cannot be read there is no record,
// and an older start's is removed: the server is then identified by its
// command line alone, as one an older wt started is.
func (e *env) recordStart(family string, pid int) {
	p, ok := e.serverProc[family]
	if !ok || p.pidfile == "" {
		return // no pidfile for family: no path a record could go beside
	}
	path := p.startfile()
	if info, found, err := e.describe(pid); err == nil && found && info.start != "" {
		if b, err := json.Marshal(startRecord{PID: pid, Started: info.start}); err == nil && writeFileAtomic(path, b) == nil {
			return
		}
	}
	_ = os.Remove(path)
}

func writeFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(f.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(f.Name())
	}
	return werr
}

// recordedStart is the start time wt recorded for family's server as pid, ""
// when there is no record of that pid (an older wt's pidfile, llmbench's, or
// another start's record left behind) or the record is not this user's own
// file (readOwnFile): the record is what allows a signal to a whole process
// group.
func (e *env) recordedStart(family string, pid int) string {
	p, ok := e.serverProc[family]
	if !ok || p.pidfile == "" {
		return ""
	}
	b, ok := readOwnFile(p.startfile(), os.Getuid())
	if !ok {
		return ""
	}
	var r startRecord
	if json.Unmarshal(b, &r) != nil || r.PID != pid {
		return ""
	}
	return r.Started
}

// serverIdent is a verified server process: its pid, the start time that
// tells it from a later process with the same pid, and whether wt may signal
// its process group.
type serverIdent struct {
	pid   int
	start string
	// group: wt recorded this start and the process still leads its own
	// group — the one wt created at spawn, holding only the server and what
	// it forked. A server wt has no record of is signalled alone.
	group bool
	// model is the value of the spec's ModelFlag (mtplx: --model-id) the
	// server was started with, "" when its command line names none: what a
	// start warns is still loading.
	model string
	// binary is the family's spec.Binary, which messages about the process
	// name.
	binary string
}

// notServer says why info is not this user's spec server on port that
// started at wantStart ("" = no start recorded: the command line decides), or
// "" when it is. The pid has to pass every check; none is skipped because
// another passed.
func notServer(spec provider.ProcessSpec, info procInfo, port int, wantStart string) string {
	switch {
	case info.uid != os.Getuid():
		return "which another user owns"
	case wantStart != "" && info.start != wantStart:
		return "which did not start when the server wt started did (the pid was reused)"
	case !serverArgvMatches(spec, info.argv, port):
		return fmt.Sprintf("which is not an %s server on port %d", spec.Binary, port)
	}
	return ""
}

// loadingServer reads family's pidfile and decides what it names. In order:
// family must be a process family (processSpecFor); the file must be this
// user's own regular file (readOwnFile) and hold one pid greater than 1 that
// is not wt itself; the process table must have a live process for it — not a
// zombie, and not one already exiting; and that process must pass notServer.
// It changes nothing: a stale pidfile is left where it is, as `wt start`
// leaves it.
func (e *env) loadingServer(cfg *config.Config, family string) (serverIdent, Loading) {
	spec, ok := processSpecFor(family)
	if !ok {
		return serverIdent{}, Loading{}
	}
	b, ok := readOwnFile(e.serverProc[family].pidfile, os.Getuid())
	if !ok {
		return serverIdent{}, Loading{}
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 || pid == os.Getpid() {
		return serverIdent{}, Loading{}
	}
	info, found, err := e.describe(pid)
	if err != nil {
		return serverIdent{}, Loading{Err: fmt.Errorf("the %s pidfile names pid %d and wt could not inspect it: %w", spec.Binary, pid, err)}
	}
	if !found || info.zombie || info.exiting {
		return serverIdent{}, Loading{}
	}
	recorded := e.recordedStart(family, pid)
	if why := notServer(spec, info, serverPort(cfg, family), recorded); why != "" {
		return serverIdent{}, Loading{Stray: fmt.Sprintf("the %s pidfile names pid %d, %s — left alone", spec.Binary, pid, why)}
	}
	return serverIdent{
		pid: pid, start: info.start, group: recorded != "" && info.pgid == pid,
		model: argAfter(info.argv, spec.ModelFlag), binary: spec.Binary,
	}, Loading{PID: pid}
}

// argAfter is the argument that follows flag in argv as a separate argument,
// "" when argv has no such pair.
func argAfter(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// stopLoadingServer is StopLoading's injectable core. SIGTERM, a wait of
// stopGrace (the grace `mtplx stop` is given), then — only if the process
// table still shows the same process, still family's server — SIGKILL and a
// second wait. A process that is already exiting when the grace ends (the
// kernel is still freeing its memory) gets the second wait and no SIGKILL.
// Every outcome but "confirmed gone" is an error, and the pidfile is removed
// only on that one.
func (e *env) stopLoadingServer(ctx context.Context, cfg *config.Config, family string, pid int) error {
	spec, ok := processSpecFor(family)
	if !ok {
		return &UnsupportedError{ProviderID: family}
	}
	name := spec.Binary
	id, l := e.loadingServer(cfg, family)
	if l.Err != nil {
		return l.Err
	}
	if pid <= 0 || l.PID != pid {
		return fmt.Errorf("pid %d is no longer the %s server wt found loading — nothing was signalled", pid, name)
	}
	target := pid
	if id.group {
		target = -pid
	}
	if err := e.signal(target, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stopping %s (pid %d): %w", name, pid, err)
	}
	gone, err := e.waitGone(ctx, id, e.stopGrace, "SIGTERM")
	if err != nil {
		return err
	}
	if !gone {
		info, found, err := e.describe(pid)
		if err != nil {
			return fmt.Errorf("cannot confirm %s (pid %d) stopped: %w", name, pid, err)
		}
		if found && !info.zombie && info.start == id.start {
			sent := "SIGTERM"
			if !info.exiting {
				if why := notServer(spec, info, serverPort(cfg, family), id.start); why != "" {
					return fmt.Errorf("pid %d outlived SIGTERM and is now a process %s — not killed", pid, why)
				}
				target = pid
				if id.group && info.pgid == pid {
					target = -pid
				}
				if err := e.signal(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return fmt.Errorf("killing %s (pid %d): %w", name, pid, err)
				}
				sent = "SIGKILL"
			}
			if gone, err = e.waitGone(ctx, id, e.stopTimeout, sent); err != nil {
				return err
			} else if !gone && sent == "SIGKILL" {
				return fmt.Errorf("%s (pid %d) is still running after SIGKILL", name, pid)
			} else if !gone {
				return fmt.Errorf("%s (pid %d) has not finished exiting after SIGTERM — not killed: wt could no longer verify it", name, pid)
			}
		}
	}
	// Confirmed gone.
	e.forgetServer(family, pid)
	return nil
}

// forgetServer removes family's pidfile and start record once its server is
// confirmed gone, as a failed start's cleanup does — each only while it still
// names pid: a new start may have put its own pid there since.
func (e *env) forgetServer(family string, pid int) {
	p, ok := e.serverProc[family]
	if !ok || p.pidfile == "" {
		return
	}
	if b, ok := readOwnFile(p.pidfile, os.Getuid()); ok && strings.TrimSpace(string(b)) == strconv.Itoa(pid) {
		_ = os.Remove(p.pidfile)
	}
	if e.recordedStart(family, pid) != "" {
		_ = os.Remove(p.startfile())
	}
}

// forgetStopped is the cleanup of a stop that went through the port
// (processServer.stop): id is what family's pidfile named before the stop
// command ran — verified then as this provider's server (loadingServer), the
// zero value when it named nothing wt could verify. The files are removed
// once the process table shows that process gone, and left for anything else:
// a process still exiting when the wait ends, a process table that does not
// answer, a cancelled wait. A pidfile left behind names a dead pid, which is
// what every stop left before (#343).
func (e *env) forgetStopped(ctx context.Context, family string, id serverIdent) {
	if id.pid == 0 {
		return
	}
	if gone, err := e.waitGone(ctx, id, e.stopTimeout, "SIGTERM"); err != nil || !gone {
		return
	}
	e.forgetServer(family, id.pid)
}

// waitGone polls the process table until id's process is gone — no such pid,
// a zombie, or a pid that now belongs to a process with another start time —
// or the wait is over. A process that is exiting is still there. A process
// table that stops answering is an error, not "gone"; so is ctx ending, which
// is reported as a *StopInterruptedError naming sent, the signal the process
// already has.
func (e *env) waitGone(ctx context.Context, id serverIdent, wait time.Duration, sent string) (bool, error) {
	deadline := time.Now().Add(wait)
	for {
		info, found, err := e.describe(id.pid)
		if err != nil {
			return false, fmt.Errorf("cannot confirm %s (pid %d) stopped: %w", id.binary, id.pid, err)
		}
		if !found || info.zombie || info.start != id.start {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, &StopInterruptedError{PID: id.pid, Signal: sent, Err: ctx.Err(), Binary: id.binary}
		case <-time.After(e.pollInterval / 10):
		}
	}
}
