package cleanup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrNoDefaultBranch means no default branch could be resolved. Callers must
// stop: an empty default would make every "is it the default" guard skip.
var ErrNoDefaultBranch = errors.New("cleanup: cannot determine the default branch (no origin/HEAD, main, master, origin/main or origin/master)")

// Base is the comparison base for merge checks.
type Base struct {
	Default   string    // default branch name, e.g. "main"
	Ref       string    // origin/<Default> when it exists, else the local Default
	Fallback  bool      // true when Ref is the local default, not the remote's
	FetchedAt time.Time // set by the caller after a fetch that worked; ResolveBase leaves it zero
}

// refExists reports whether ref resolves to a commit.
func refExists(g Git, ref string) (bool, error) {
	o, err := g.Run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return false, err
	}
	switch o.Code {
	case 0:
		return true, nil
	case 1:
		return false, nil // --quiet: the ref is absent
	}
	return false, gitFailure(o, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
}

// gitFailure wraps a fatal git exit (not the "absent" exit 1) with the
// command, exit code and stderr, as one sanitised line (flat): it becomes a
// row's detail and reaches the terminal, and git's stderr is several lines
// that may hold anything. Only the stderr is capped (gitErrMax runes): a
// command line can be longer than the cap by itself (operationsInProgress,
// readRefs), and the exit code and git's reason must survive. It is never
// ErrNoDefaultBranch: a broken repo is not a repo without a default.
func gitFailure(o Out, args ...string) error {
	return &gitError{msg: fmt.Sprintf("cleanup: git %s: exit %d: %s",
		flat(strings.Join(args, " ")), o.Code, capRunes(flat(o.Stderr), gitErrMax))}
}

// Text runs a read in g and returns its trimmed stdout; a non-zero exit is an
// error "git <args[0]>: exit N: <stderr>".
func (g Git) Text(args ...string) (string, error) {
	o, err := g.Run(args...)
	if err != nil {
		return "", err
	}
	return OutText(o, args...)
}

// Lines runs a read in g and splits its NUL-separated stdout, dropping the
// trailing empty field.
func (g Git) Lines(args ...string) ([]string, error) {
	o, err := g.Run(args...)
	if err != nil {
		return nil, err
	}
	return OutLines(o, args...)
}

// OutText is Text for an already-run command (for callers that run git
// through their own seam).
func OutText(o Out, args ...string) (string, error) {
	if o.Code != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, OneLine(o.Stderr))
	}
	return strings.TrimSpace(o.Stdout), nil
}

// OutLines is Lines for an already-run command.
func OutLines(o Out, args ...string) ([]string, error) {
	if o.Code != 0 {
		return nil, fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, OneLine(o.Stderr))
	}
	out := strings.Split(o.Stdout, "\x00")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out, nil
}

// ResolveBase finds the default branch and the ref to compare against. The
// base is origin/<default>, not the local default: a local main with unpushed
// commits must not make work look merged. Chain: origin/HEAD, local main,
// master, then origin/main, origin/master.
func ResolveBase(g Git) (Base, error) {
	// origin/HEAD
	o, err := g.Run("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return Base{}, err
	}
	if o.Code > 1 {
		return Base{}, gitFailure(o, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	}
	if o.Code == 0 {
		if name := strings.TrimPrefix(strings.TrimSpace(o.Stdout), "origin/"); name != "" {
			if ok, err := refExists(g, "refs/remotes/origin/"+name); err != nil {
				return Base{}, err
			} else if ok {
				return Base{Default: name, Ref: "origin/" + name}, nil
			}
		}
	}
	// local main, master. A local master beats origin/main only when no local
	// main exists; the chain order is the spec's.
	for _, name := range []string{"main", "master"} {
		ok, err := refExists(g, "refs/heads/"+name)
		if err != nil {
			return Base{}, err
		}
		if !ok {
			continue
		}
		remote, err := refExists(g, "refs/remotes/origin/"+name)
		if err != nil {
			return Base{}, err
		}
		if remote {
			return Base{Default: name, Ref: "origin/" + name}, nil
		}
		return Base{Default: name, Ref: name, Fallback: true}, nil
	}
	// remote-only
	for _, name := range []string{"main", "master"} {
		ok, err := refExists(g, "refs/remotes/origin/"+name)
		if err != nil {
			return Base{}, err
		}
		if ok {
			return Base{Default: name, Ref: "origin/" + name}, nil
		}
	}
	return Base{}, ErrNoDefaultBranch
}

// FetchResult is the outcome of Fetch. A failure is a value: the base may be
// stale and the caller must say so.
type FetchResult struct {
	// OK means origin was fetched cleanly this run (the base and the origin
	// rows are origin's). Other remotes' results are in Remotes.
	OK  bool
	Err string
	At  time.Time // zero when no fetch ran (not asked for, or Skipped)
	// Skipped is why origin was not fetched although a fetch was asked for
	// ("no origin remote", "no remotes"); "" otherwise. A skip is neither ok
	// nor a failure.
	Skipped string
	// Remotes is every remote considered, in `git remote` order.
	Remotes []RemoteFetch
}

// RemoteFetch is one remote's fetch.
type RemoteFetch struct {
	Name    string
	OK      bool
	Err     string
	Skipped bool // remote.<name>.skipFetchAll is true: not fetched
}

// FailureSummary names each remote whose fetch failed with git's error cut to
// one line ("origin: ...; other: ..."), origin first, and is "" when none
// failed. A skip is not a failure.
func (fr FetchResult) FailureSummary() string {
	var failed []string
	seen := map[string]bool{}
	for _, r := range fr.Remotes {
		if r.Err != "" {
			failed = append(failed, r.Name+": "+OneLine(r.Err))
			seen[r.Name] = true
		}
	}
	if fr.Err != "" && !seen["origin"] {
		failed = append([]string{"origin: " + OneLine(fr.Err)}, failed...)
	}
	return strings.Join(failed, "; ")
}

// fetchTimeout bounds each remote's fetch when g.Timeout is zero: a fetch
// over the network takes longer than the 30s every other git call gets.
const fetchTimeout = 120 * time.Second

// otherFetchTimeout bounds each non-origin remote's fetch (they run
// concurrently, after origin): the classification does not depend on them, so
// a slow one must not hold the command for origin's full window. A seam for
// tests.
var otherFetchTimeout = 30 * time.Second

// Fetch runs `git fetch --prune <remote>` once per remote (never prompting;
// origin first and alone, then the others concurrently under the shorter
// otherFetchTimeout),
// skipping a non-origin remote whose skipFetchAll is true (origin is always
// fetched). It only updates
// remote-tracking refs. A loop rather than `fetch --all` so each remote's
// result is known: the header and JSON say which were fetched. The top-level
// OK/Err/At are origin's, because the base is origin's; with no origin the
// other remotes are still fetched and Skipped says so.
func Fetch(g Git) FetchResult {
	g.Env = append(slices.Clone(g.Env), NonInteractive(g.Dir)...)
	o, err := g.Run("remote")
	if err != nil {
		return FetchResult{Err: fetchErrText(err.Error()), At: time.Now()}
	}
	if o.Code != 0 {
		return FetchResult{Err: fetchErrText(gitFailure(o, "remote").Error()), At: time.Now()}
	}
	names := strings.Fields(o.Stdout)
	if len(names) == 0 {
		return FetchResult{Skipped: "no remotes"}
	}
	if g.Timeout == 0 {
		g.Timeout = fetchTimeout
	}
	res := FetchResult{Remotes: make([]RemoteFetch, len(names))}
	// Origin first and on its own: it alone drives classification. The other
	// remotes then fetch concurrently under a shorter timeout.
	var others sync.WaitGroup
	og := g
	other := g
	other.Timeout = min(g.Timeout, otherFetchTimeout)
	for i, n := range names {
		res.Remotes[i].Name = n
		if n != "origin" {
			continue
		}
		at := time.Now()
		res.Remotes[i].OK, res.Remotes[i].Err = fetchOne(og, n)
		res.OK, res.Err, res.At = res.Remotes[i].OK, res.Remotes[i].Err, at
	}
	for i, n := range names {
		if n == "origin" {
			continue
		}
		// skipFetchAll only leaves a remote out of `fetch --all`; a plain
		// `git fetch origin` fetches it, and the base is origin's, so origin
		// is always fetched (above).
		if c, err := g.Run("config", "--type=bool", "--get", "remote."+n+".skipFetchAll"); err == nil && c.Code == 0 && strings.TrimSpace(c.Stdout) == "true" {
			res.Remotes[i].Skipped = true
			continue
		}
		others.Add(1)
		go func() {
			defer others.Done()
			res.Remotes[i].OK, res.Remotes[i].Err = fetchOne(other, n)
		}()
	}
	others.Wait()
	if !slices.Contains(names, "origin") {
		res.Skipped = "no origin remote"
	}
	return res
}

// fetchOne runs `git fetch --prune name`; ok, or the one-line failure.
func fetchOne(g Git, name string) (bool, string) {
	o, err := g.Run("fetch", "--prune", name)
	if errors.Is(err, context.DeadlineExceeded) {
		return false, fmt.Sprintf("git fetch --prune %s timed out after %s", name, g.Timeout)
	}
	if err != nil {
		return false, fetchErrText(err.Error())
	}
	if o.Code != 0 {
		// git's stderr is several lines and may hold anything: the header
		// and JSON fetch.error show it as one sanitised line.
		msg := fetchErrText(o.Stderr)
		if msg == "" {
			msg = "git fetch exited non-zero"
		}
		return false, msg
	}
	return true, ""
}
