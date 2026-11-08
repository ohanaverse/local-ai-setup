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
// locks off (so `git status` or `git diff` cannot rewrite the index's stat
// cache, a change to a source that must stay as found); the fsmonitor daemon
// is switched off here too. It differs from cleanup.Git only in treating a
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
	o, err := g.runner().RunInput(string(stdin), args...)
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	if o.Code != 0 {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(o.Stderr))
	}
	return []byte(o.Stdout), nil
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
