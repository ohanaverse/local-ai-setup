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
// command, exit code and stderr, as one sanitised line (gitErrText): it
// becomes a row's detail and reaches the terminal, and git's stderr is
// several lines that may hold anything. It is never ErrNoDefaultBranch: a
// broken repo is not a repo without a default.
func gitFailure(o Out, args ...string) error {
	return errors.New(gitErrText(fmt.Sprintf("cleanup: git %s: exit %d: %s", strings.Join(args, " "), o.Code, o.Stderr)))
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
	OK  bool
	Err string
	At  time.Time // zero when no fetch ran (not asked for, or Skipped)
	// Skipped is why no fetch was attempted although one was asked for
	// ("no origin remote"); "" otherwise. A skip is neither ok nor a failure.
	Skipped string
}

// fetchTimeout bounds the fetch when g.Timeout is zero: a fetch over the
// network takes longer than the 30s every other git call gets.
const fetchTimeout = 120 * time.Second

// Fetch runs `git fetch --prune origin`. It only updates remote-tracking refs.
// It names origin because the base is origin's: a bare `git fetch` fetches
// the current branch's upstream remote, which may not be origin. With no
// origin remote nothing is fetched and the result says why (Skipped).
func Fetch(g Git) FetchResult {
	o, err := g.Run("remote")
	if err != nil {
		return FetchResult{Err: fetchErrText(err.Error()), At: time.Now()}
	}
	if o.Code != 0 {
		return FetchResult{Err: fetchErrText(gitFailure(o, "remote").Error()), At: time.Now()}
	}
	if !slices.Contains(strings.Fields(o.Stdout), "origin") {
		return FetchResult{Skipped: "no origin remote"}
	}
	if g.Timeout == 0 {
		g.Timeout = fetchTimeout
	}
	at := time.Now()
	o, err = g.Run("fetch", "--prune", "origin")
	if errors.Is(err, context.DeadlineExceeded) {
		return FetchResult{Err: fmt.Sprintf("git fetch --prune origin timed out after %s", g.Timeout), At: at}
	}
	if err != nil {
		return FetchResult{Err: fetchErrText(err.Error()), At: at}
	}
	if o.Code != 0 {
		// git's stderr is several lines and may hold anything: the header
		// and JSON fetch.error show it as one sanitised line.
		msg := fetchErrText(o.Stderr)
		if msg == "" {
			msg = "git fetch exited non-zero"
		}
		return FetchResult{Err: msg, At: at}
	}
	return FetchResult{OK: true, At: at}
}
