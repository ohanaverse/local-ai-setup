package cleanup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// fetchTimeout bounds each remote's fetch when g.Timeout is zero: a fetch
// over the network takes longer than the 30s every other git call gets.
const fetchTimeout = 120 * time.Second

// Fetch runs `git fetch --prune <remote>` once per remote (never prompting),
// skipping a non-origin remote whose skipFetchAll is true (origin is always
// fetched). It only updates
// remote-tracking refs. A loop rather than `fetch --all` so each remote's
// result is known: the header and JSON say which were fetched. The top-level
// OK/Err/At are origin's, because the base is origin's; with no origin the
// other remotes are still fetched and Skipped says so.
func Fetch(g Git) FetchResult {
	g.Env = append(slices.Clone(g.Env), NonInteractive()...)
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
	var res FetchResult
	for _, n := range names {
		rf := RemoteFetch{Name: n}
		// skipFetchAll only leaves a remote out of `fetch --all`; a plain
		// `git fetch origin` fetches it, and the base is origin's, so origin
		// is always fetched.
		if c, err := g.Run("config", "--type=bool", "--get", "remote."+n+".skipFetchAll"); n != "origin" && err == nil && c.Code == 0 && strings.TrimSpace(c.Stdout) == "true" {
			rf.Skipped = true
			res.Remotes = append(res.Remotes, rf)
			continue
		}
		at := time.Now()
		rf.OK, rf.Err = fetchOne(g, n)
		res.Remotes = append(res.Remotes, rf)
		if n == "origin" {
			res.OK, res.Err, res.At = rf.OK, rf.Err, at
		}
	}
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
