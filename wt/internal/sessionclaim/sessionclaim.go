// Package sessionclaim records which wt sessions are running in a repository's
// root checkout, so a second launch there can warn the person first. A claim
// is one key=value file per wt process in <git-common-dir>/wt-sessions/<pid>;
// repo-cleanup (bash) reads the same files. The format is a cross-repo
// contract: docs/contracts/session-claims.md at the monorepo root.
package sessionclaim

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Claim is one session's hold on a checkout. Started is the process start time
// in epoch seconds; it is what tells a live claim from a reused pid.
type Claim struct {
	PID     int
	Started int64
	Path    string
	Branch  string
	Agent   string
}

// startTolerance is how far the recorded and actual start times may differ and
// still match: wt (Go) and repo-cleanup (bash) each take now minus a whole-second
// `ps -o etime=` reading, so the two can differ by up to 2 s from rounding alone.
const startTolerance = 5

// pidAlive reports whether pid is a running process (EPERM means alive).
// Package-level var so tests can inject liveness without real processes.
var pidAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// startTime returns pid's start time in epoch seconds: now minus the elapsed
// time `ps -o etime=` reports, the same method repo-cleanup uses. Package-level
// var so tests can inject start times.
var startTime = func(pid int) (int64, error) {
	out, err := exec.Command("ps", "-o", "etime=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	secs, err := parseEtime(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, err
	}
	return time.Now().Unix() - secs, nil
}

// parentPID returns pid's parent pid from `ps -o ppid=`. Package-level var so
// tests can inject a process tree.
var parentPID = func(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// maxAncestors bounds the walk up the parent chain.
const maxAncestors = 64

// ancestors returns this process and its parents up to pid 1, the same set
// repo-cleanup's pid_is_ancestor walks: a wt launched from inside a root
// session must not count that session as another one.
func ancestors() map[int]bool {
	set := map[int]bool{}
	p := os.Getpid()
	for i := 0; i < maxAncestors && p > 0; i++ {
		set[p] = true
		if p <= 1 {
			break
		}
		next, err := parentPID(p)
		if err != nil {
			break
		}
		p = next
	}
	return set
}

// parseEtime converts ps's [[dd-]hh:]mm:ss elapsed time to seconds.
func parseEtime(s string) (int64, error) {
	bad := fmt.Errorf("bad etime %q", s)
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil || n < 0 {
			return 0, bad
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, bad
	}
	total := int64(0)
	for _, p := range parts {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return 0, bad
		}
		total = total*60 + n
	}
	return days*86400 + total, nil
}

// Self returns a claim for this process at path.
func Self(path, branch, agent string) (Claim, error) {
	pid := os.Getpid()
	started, err := startTime(pid)
	if err != nil {
		return Claim{}, err
	}
	return Claim{PID: pid, Started: started, Path: path, Branch: branch, Agent: agent}, nil
}

func (c Claim) encode() []byte {
	return []byte(fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=%s\nagent=%s\n",
		c.PID, c.Started, c.Path, c.Branch, c.Agent))
}

// parse reads a claim file; ok is false when pid, started or path is missing or
// malformed. Unknown keys are ignored.
func parse(data []byte) (c Claim, ok bool) {
	var havePID, haveStarted, havePath bool
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		k, v, found := strings.Cut(sc.Text(), "=")
		if !found {
			continue
		}
		switch k {
		case "pid":
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return Claim{}, false
			}
			c.PID, havePID = n, true
		case "started":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return Claim{}, false
			}
			c.Started, haveStarted = n, true
		case "path":
			c.Path, havePath = v, v != ""
		case "branch":
			c.Branch = v
		case "agent":
			c.Agent = v
		}
	}
	return c, havePID && haveStarted && havePath
}

// live reports whether c's process runs and started when the claim says.
// known is false when the pid runs but its start time cannot be read: such a
// claim is neither live nor stale.
func live(c Claim) (alive, known bool) {
	if !pidAlive(c.PID) {
		return false, true
	}
	real, err := startTime(c.PID)
	if err != nil {
		return false, false
	}
	d := real - c.Started
	return d >= -startTolerance && d <= startTolerance, true
}

// withLock runs fn holding an exclusive flock on dir/.lock, so two wt
// processes launching at once cannot both see an empty directory.
func withLock(dir string, fn func() error) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// Join checks commonDir/wt-sessions for live claims on c.Path, deleting stale
// ones, and claims the path for c. With no live others the check and the
// write happen under one lock, so of two launches at once only one sees an
// empty directory. With live others it releases the lock, calls ask (which
// may wait on the person for as long as it likes without blocking other
// launches), and a false answer writes nothing; a true one locks again and
// writes, without asking again. c is written atomically and release removes
// it. release is never nil, so a caller may defer it even when err is set.
func Join(commonDir string, c Claim, ask func(others []Claim) bool) (release func(), others []Claim, err error) {
	release = func() {}
	dir := filepath.Join(commonDir, "wt-sessions")
	mine := filepath.Join(dir, strconv.Itoa(c.PID))
	write := func() error {
		if err := config.WriteFileAtomic(mine, c.encode(), 0o644); err != nil {
			return err
		}
		release = func() { _ = os.Remove(mine) }
		return nil
	}
	// The parent chain costs a `ps` per generation, so it is walked before the
	// lock, not under it, and only when there is a claim file to compare with.
	var parents map[int]bool
	if hasClaims(dir) {
		parents = ancestors()
	}
	err = withLock(dir, func() error {
		var err error
		if others, err = scan(dir, c, parents, true); err != nil || len(others) > 0 {
			return err
		}
		return write()
	})
	if err != nil || len(others) == 0 || !ask(others) {
		return release, others, err
	}
	// write sets release, so read it after the call: operand evaluation order
	// between a variable and a call is unspecified.
	err = withLock(dir, write)
	return release, others, err
}

// Others returns the live claims in commonDir/wt-sessions on c.Path other than
// c's own, without taking the lock or changing anything: a caller that must
// ask before it claims (the picker's screen) reads first and joins after.
// No wt-sessions directory means no others.
func Others(commonDir string, c Claim) ([]Claim, error) {
	others, err := scan(filepath.Join(commonDir, "wt-sessions"), c, nil, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return others, err
}

// hasClaims reports whether dir holds a claim file (a file named by a pid).
func hasClaims(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err == nil {
			return true
		}
	}
	return false
}

// scan reads the claim files in dir and returns the live ones on c.Path other
// than c's own. With prune it deletes the stale ones it finds. A claim by this
// process or one of its parents, or one whose start time cannot be read, is
// neither returned nor deleted. mine is this process and its parents
// (ancestors); nil means scan walks the chain itself, when it first needs it.
func scan(dir string, c Claim, mine map[int]bool, prune bool) (others []Claim, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		// Claim files are named by pid; skip .lock and rename temp files.
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		other, ok := parse(data)
		if ok {
			if mine == nil {
				mine = ancestors()
			}
			if mine[other.PID] {
				continue
			}
		}
		var alive, known bool
		if ok {
			if alive, known = live(other); !known {
				continue
			}
		}
		if !alive {
			if prune {
				_ = os.Remove(p)
			}
			continue
		}
		if other.Path == c.Path && other.PID != c.PID {
			others = append(others, other)
		}
	}
	return others, nil
}
