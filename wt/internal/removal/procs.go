package removal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// lsofTimeout bounds one whole LsofFinder lookup. The engine sets no timeout
// of its own around a ProcFinder, so the finder must bound itself.
const lsofTimeout = 5 * time.Second

// lsofArgs list every process's working directory, one field per NUL
// (-F0: p pid, R parent pid, c command, n path; lsof adds f), with no
// host or port lookups (-n -P). It names no path: `+D <dir>` walks the
// whole tree, which is what makes lsof slow on a large worktree.
var lsofArgs = []string{"-a", "-d", "cwd", "-n", "-P", "-F0pcRn"}

// lsofFinder is the macOS ProcFinder: one lsof listing every process's cwd,
// filtered here. run is a seam (tests hand it canned output; no test runs
// lsof); self is the process the finder runs in.
type lsofFinder struct {
	run     func(ctx context.Context, args ...string) ([]byte, error)
	timeout time.Duration
	self    int
}

// LsofFinder is the ProcFinder wt uses: the live processes whose working
// directory is a directory or below it, read with lsof, leaving out wt's own
// process and its parent chain (wt run from inside the worktree is not a
// reason to warn). The whole lookup is bounded by 5 seconds. A missing lsof,
// a timeout, a failed lsof or output it cannot read is an error; the engine
// shows it as a warning, never a refusal.
func LsofFinder() ProcFinder {
	return lsofFinder{run: runLsof, timeout: lsofTimeout, self: os.Getpid()}
}

// runLsof runs lsof (from PATH, else /usr/sbin/lsof, where macOS keeps it
// and where a launchd PATH may not reach) and returns its stdout.
func runLsof(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := exec.LookPath("lsof")
	if err != nil {
		if _, serr := os.Stat("/usr/sbin/lsof"); serr != nil {
			return nil, err
		}
		bin = "/usr/sbin/lsof"
	}
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%v: %s", err, s)
		}
		return nil, err
	}
	return out, nil
}

// InDir lists the processes whose cwd is dir or below it.
func (f lsofFinder) InDir(dir string) ([]Proc, error) {
	entries, err := f.list()
	if err != nil {
		return nil, err
	}
	return matchCwd(entries, dir, f.self), nil
}

// batchFinder is a ProcFinder that can take one machine-wide listing and
// answer every directory from it (Preview asks once per call, not once per
// item).
type batchFinder interface {
	snapshot() ProcFinder
}

// snapshot runs lsof once; the finder it returns answers every InDir from
// that listing, or with its error.
func (f lsofFinder) snapshot() ProcFinder {
	entries, err := f.list()
	return listedProcs{entries: entries, err: err, self: f.self}
}

// listedProcs answers InDir from one lsof listing.
type listedProcs struct {
	entries []lsofEntry
	err     error
	self    int
}

func (l listedProcs) InDir(dir string) ([]Proc, error) {
	if l.err != nil {
		return nil, l.err
	}
	return matchCwd(l.entries, dir, l.self), nil
}

// list runs lsof once, bounded by f.timeout, and parses its listing.
func (f lsofFinder) list() ([]lsofEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()
	type answer struct {
		out []byte
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		out, err := f.run(ctx, lsofArgs...)
		ch <- answer{out, err}
	}()
	var a answer
	select {
	case a = <-ch:
	case <-ctx.Done():
		// The runner may ignore its context; the lookup still ends here.
		return nil, fmt.Errorf("lsof timed out after %s", f.timeout)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("lsof timed out after %s", f.timeout)
	}
	if a.err != nil {
		return nil, fmt.Errorf("lsof: %v", a.err)
	}
	return parseLsof(a.out)
}

// lsofEntry is one process's cwd from lsof's field output.
type lsofEntry struct {
	pid, ppid int
	command   string
	cwd       string
}

// parseLsof reads `lsof -F0` output: fields end in NUL, each process and
// file set ends in a newline (so a field after a set starts with one). A
// process set is p, then R and c; its file set carries n, the cwd. A field
// that cannot be read is an error: a partial listing would hide a process.
func parseLsof(b []byte) ([]lsofEntry, error) {
	var out []lsofEntry
	var cur *lsofEntry
	for _, tok := range strings.Split(string(b), "\x00") {
		tok = strings.TrimLeft(tok, "\n")
		if tok == "" {
			continue
		}
		val := tok[1:]
		switch tok[0] {
		case 'p':
			pid, err := strconv.Atoi(val)
			if err != nil || pid <= 0 {
				return nil, fmt.Errorf("lsof: unreadable pid %q", cleanup.OneLine(val))
			}
			out = append(out, lsofEntry{pid: pid})
			cur = &out[len(out)-1]
		case 'R':
			if cur == nil {
				return nil, errors.New("lsof: parent pid before any process")
			}
			ppid, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("lsof: unreadable parent pid %q", cleanup.OneLine(val))
			}
			cur.ppid = ppid
		case 'c':
			if cur == nil {
				return nil, errors.New("lsof: command before any process")
			}
			cur.command = val
		case 'n':
			if cur == nil {
				return nil, errors.New("lsof: path before any process")
			}
			cwd, err := unescapeLsof(val)
			if err != nil {
				return nil, err
			}
			cur.cwd = cwd
		}
	}
	return out, nil
}

// unescapeLsof undoes lsof's escaping of a name: \n \t \r \b \f \\ for the
// control characters and backslash, \xNN for any other byte it would not
// print as is (non-ASCII bytes under LC_ALL=C, other controls), and \NNN
// octal. Without it a worktree path holding a newline or a non-ASCII
// character never matches. A malformed escape is an error.
func unescapeLsof(s string) (string, error) {
	if !strings.Contains(s, `\`) {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("lsof: unreadable escape in %q", cleanup.OneLine(s))
		}
		i++
		switch c := s[i]; c {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case '\\':
			b.WriteByte('\\')
		case 'x':
			if i+3 > len(s) {
				return "", fmt.Errorf("lsof: unreadable escape in %q", cleanup.OneLine(s))
			}
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil {
				return "", fmt.Errorf("lsof: unreadable escape in %q", cleanup.OneLine(s))
			}
			b.WriteByte(byte(v))
			i += 2
		default:
			if c < '0' || c > '7' || i+2 >= len(s) {
				return "", fmt.Errorf("lsof: unreadable escape in %q", cleanup.OneLine(s))
			}
			v, err := strconv.ParseUint(s[i:i+3], 8, 8)
			if err != nil {
				return "", fmt.Errorf("lsof: unreadable escape in %q", cleanup.OneLine(s))
			}
			b.WriteByte(byte(v))
			i += 2
		}
	}
	return b.String(), nil
}

// evalSymlinks is filepath.EvalSymlinks; tests use it to spell a temp dir
// the way lsof reports it.
var evalSymlinks = filepath.EvalSymlinks

// matchCwd keeps the entries whose cwd is dir or below it (separator-safe,
// both sides cleaned; dir also compared resolved, since lsof reports the
// resolved path), one per pid, in lsof's order, leaving out self, its
// parent chain and its children (the lsof it ran). Command names become one
// printable line.
func matchCwd(entries []lsofEntry, dir string, self int) []Proc {
	roots := []string{filepath.Clean(dir)}
	if r := cleanup.ResolvePath(dir); r != roots[0] {
		roots = append(roots, r)
	}
	parent := map[int]int{}
	for _, e := range entries {
		parent[e.pid] = e.ppid
	}
	skip := map[int]bool{}
	for p := self; p > 1 && !skip[p]; p = parent[p] {
		skip[p] = true
	}
	if self == os.Getpid() {
		skip[os.Getppid()] = true // the chain lsof could not show
	}
	var out []Proc
	seen := map[int]bool{}
	for _, e := range entries {
		// e.ppid == self: lsof itself, started from wt's own directory.
		if e.cwd == "" || skip[e.pid] || e.ppid == self || seen[e.pid] {
			continue
		}
		cwd := filepath.Clean(e.cwd)
		for _, root := range roots {
			if cleanup.Within(cwd, root) {
				seen[e.pid] = true
				out = append(out, Proc{PID: e.pid, Command: cleanup.OneLine(e.command)})
				break
			}
		}
	}
	return out
}
