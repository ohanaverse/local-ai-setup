package rescue

import (
	"fmt"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// gitTimeout bounds one git call.
const gitTimeout = 2 * time.Minute

// sgit runs git in one directory through cleanup.Git, which turns optional
// locks off (so `git status` cannot rewrite the index's stat cache); a
// `git diff` rewrites it even with optional locks off, so a diff-shaped
// read goes through patchOut, against a scratch index copy (#570) — the
// source must stay as found, byte for byte. The fsmonitor daemon is
// switched off here too. It differs from cleanup.Git only in treating a
// non-zero exit as an error carrying stderr.
type sgit struct {
	dir    string
	env    []string // appended to the process environment (the record's identity)
	config []string // extra `-c key=value` settings (the record's hooks off)
}

func (g sgit) runner() cleanup.Git {
	return cleanup.Git{Dir: g.dir, Timeout: gitTimeout, Config: append([]string{"core.fsmonitor=false"}, g.config...), Env: g.env}
}

// out runs git with stdin, returning stdout. A non-zero exit is an error
// carrying stderr.
func (g sgit) out(stdin []byte, args ...string) ([]byte, error) {
	return g.outGit(g.runner(), stdin, args...)
}

// outGit is out against a chosen runner; patchOut passes an insulated one.
func (g sgit) outGit(runner cleanup.Git, stdin []byte, args ...string) ([]byte, error) {
	o, err := runner.RunInput(string(stdin), args...)
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	if o.Code != 0 {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(o.Stderr))
	}
	return []byte(o.Stdout), nil
}

// patchOut runs a diff-shaped read (the carry's staged or unstaged patch)
// against a scratch copy of the source's index: `git diff` rewrites the
// index's stat cache even under GIT_OPTIONAL_LOCKS=0, and the copy carries
// the rewrite instead of the source (#570). When the copy cannot be made —
// no readable index, a degenerate source — the read runs against the real
// index as before: with no index there is nothing stale to re-stamp.
func (g sgit) patchOut(args ...string) ([]byte, error) {
	if ig, release, ok := cleanup.IndexCopy(g.runner()); ok {
		defer release()
		return g.outGit(ig, nil, args...)
	}
	return g.out(nil, args...)
}

func (g sgit) text(args ...string) (string, error) {
	b, err := g.out(nil, args...)
	return strings.TrimSpace(string(b)), err
}

// text2 is text with stdin.
func (g sgit) text2(stdin string, args ...string) (string, error) {
	b, err := g.out([]byte(stdin), args...)
	return strings.TrimSpace(string(b)), err
}
