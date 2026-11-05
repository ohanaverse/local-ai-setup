package cleanup

import (
	"errors"
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
	FetchedAt time.Time // set by the caller that fetched; ResolveBase leaves it zero
}

// refExists reports whether ref resolves to a commit.
func refExists(g Git, ref string) (bool, error) {
	o, err := g.Run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return false, err
	}
	return o.Code == 0, nil
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
	if o.Code == 0 {
		if name := strings.TrimPrefix(strings.TrimSpace(o.Stdout), "origin/"); name != "" {
			if ok, err := refExists(g, "refs/remotes/origin/"+name); err != nil {
				return Base{}, err
			} else if ok {
				return Base{Default: name, Ref: "origin/" + name}, nil
			}
		}
	}
	// local main, master
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
	At  time.Time
}

// Fetch runs `git fetch --prune`. It only updates remote-tracking refs.
func Fetch(g Git) FetchResult {
	at := time.Now()
	o, err := g.Run("fetch", "--prune")
	if err != nil {
		return FetchResult{Err: err.Error(), At: at}
	}
	if o.Code != 0 {
		msg := strings.TrimSpace(o.Stderr)
		if msg == "" {
			msg = "git fetch exited non-zero"
		}
		return FetchResult{Err: msg, At: at}
	}
	return FetchResult{OK: true, At: at}
}
