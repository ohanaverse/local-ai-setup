package modelmode

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/urlrule"
)

// Result is what an Apply did: the steps that ran, in order, and the one
// that failed with its error. Notes say what a step left behind (a backup's
// path); Recovery, after a failure that left the LiteLLM plist, wt-model's
// pair or a running service out of step, says what is true and the exact
// commands to run by hand. Nothing in it holds a secret.
type Result struct {
	Done     []Step
	Failed   *Step
	Err      error
	Warnings []string
	Notes    []string
	Recovery string
}

// Apply executes p's steps in order and stops at the first failure. It runs
// only the session-side steps (a token step other than generating one, and
// the config step); any other step kind fails on the spot, so a session can
// never run an admin step. It calls progress with one short ASCII line
// before each step. A cancelled ctx stops before the next step and is that
// step's failure. A pasted token is revealed only inside the step that
// writes it and never appears in progress, the Result or an error. Nothing
// it runs leaves Recovery set.
func Apply(ctx context.Context, env Env, p Plan, progress func(string)) Result {
	if progress == nil {
		progress = func(string) {}
	}
	var r Result
	for i, s := range p.Steps {
		if err := ctx.Err(); err != nil {
			return failed(r, s, err)
		}
		progress(fmt.Sprintf("step %d/%d: %s", i+1, len(p.Steps), applyLabel(s.Kind)))
		handled, err := ApplyStep(ctx, env, p, s, &r)
		if !handled && err == nil {
			err = fmt.Errorf("step kind %q is not one Apply knows; nothing of it was done", s.Kind)
		}
		if err != nil {
			return failed(r, s, err)
		}
		r.Done = append(r.Done, s)
	}
	return r
}

// failed records s and err on a copy of r.
func failed(r Result, s Step, err error) Result {
	r.Failed = &s
	r.Err = fmt.Errorf("%s: %w", applyLabel(s.Kind), err)
	return r
}

// applyLabel is the short ASCII name a progress line and an error use.
func applyLabel(k StepKind) string {
	if l, ok := StepLabel(k); ok {
		return l
	}
	return "unknown step"
}

// ApplyStep runs one session-side step: the config step, or a token step
// (a missing path, tighten, write or replace a pasted token; any other
// token action is refused). handled is false, with a nil error, for every
// other kind, which the caller either runs itself (modelsetup's admin
// steps) or refuses (Apply).
func ApplyStep(ctx context.Context, env Env, p Plan, s Step, r *Result) (handled bool, err error) {
	switch s.Kind {
	case StepConfig:
		if urlrule.HasUserinfo(p.Want.URL) {
			return true, errors.New("the url contains an @ (read as a user:password); wt does not store credentials in config.toml; use a token file; config.toml was not written")
		}
		return true, env.Config.Update(func(t config.ModelServerState) (config.ModelServerState, error) {
			t.Mode, t.URL, t.TokenFile = string(p.Want.Mode), p.Want.URL, p.Want.TokenFile
			return t, nil
		})
	case StepToken:
		return true, applyToken(env, p, s)
	}
	return false, nil
}

// applyToken carries out a token step's Action.
func applyToken(env Env, p Plan, s Step) error {
	if s.Path == "" {
		return errors.New("the token step names no file")
	}
	switch s.Action {
	case ActionTightenToken:
		return env.FS.Chmod(s.Path, 0o600)
	case ActionWritePastedToken, ActionReplacePastedToken:
		tok := strings.TrimSpace(p.Want.PastedToken.Reveal())
		if tok == "" {
			return errors.New("no token was pasted")
		}
		if same, tight := sameToken(env, s.Path, tok); same {
			if tight {
				return nil
			}
			return env.FS.Chmod(s.Path, 0o600)
		}
		return env.FS.WriteAtomic(s.Path, []byte(tok+"\n"), 0o600)
	}
	return fmt.Errorf("token action %q is not one Apply knows", s.Action)
}

// sameToken reports whether path already holds tok (white space trimmed) and
// whether its mode is 0600 or stricter. Any read problem is "not the same".
func sameToken(env Env, path, tok string) (same, tight bool) {
	info, err := env.FS.Stat(path)
	if err != nil {
		return false, false
	}
	raw, err := env.FS.ReadFile(path)
	if err != nil {
		return false, false
	}
	have := strings.TrimSpace(string(raw))
	same = subtle.ConstantTimeCompare([]byte(have), []byte(tok)) == 1
	return same, info.Mode().Perm()&0o077 == 0
}
