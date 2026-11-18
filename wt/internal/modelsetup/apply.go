package modelsetup

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
)

const (
	// ReadyTimeout is how long the wait step pings before it gives up.
	ReadyTimeout = 30 * time.Second
	// ReadyPoll is the pause between two pings of the wait step.
	ReadyPoll = 500 * time.Millisecond
)

// tokenBytes is the size of a generated token before base64.
const tokenBytes = 32

// Result is what Apply did: the steps that ran, in order, and the one that
// failed with its error. Nothing in it holds a secret.
type Result struct {
	Done     []Step
	Failed   *Step
	Err      error
	Warnings []string
}

// WaitError is a wait step that ran out of time: the url it pinged, how the
// last ping failed (ServerKind and its message) and the log to read.
type WaitError struct {
	URL     string
	Timeout time.Duration
	Kind    ServerKind
	Message string
	Log     string
}

func (e *WaitError) Error() string {
	why := serverFailure(ServerState{Kind: e.Kind, Message: e.Message})
	return fmt.Sprintf("wt-model did not answer at %s within %s: %s; see %s", e.URL, e.Timeout, why, e.Log)
}

// Apply executes p's steps in order and stops at the first failure. It calls
// progress with one short ASCII line before each step. The config step is
// last in every plan, so a failure leaves the stored table as it was. A
// cancelled ctx stops before the next step and is that step's failure.
// Secrets (a generated or pasted token, the LiteLLM master key) are read or
// revealed only inside the step that writes them and never appear in
// progress, the Result or an error.
func Apply(ctx context.Context, env Env, p Plan, progress func(string)) Result {
	if progress == nil {
		progress = func(string) {}
	}
	var r Result
	for i, s := range p.Steps {
		if err := ctx.Err(); err != nil {
			return r.fail(s, err)
		}
		progress(fmt.Sprintf("step %d/%d: %s", i+1, len(p.Steps), stepLabel(s.Kind)))
		if err := applyStep(ctx, env, p, s, &r); err != nil {
			return r.fail(s, err)
		}
		r.Done = append(r.Done, s)
	}
	return r
}

func (r Result) fail(s Step, err error) Result {
	r.Failed = &s
	r.Err = fmt.Errorf("%s: %w", stepLabel(s.Kind), err)
	return r
}

// stepLabel is the short ASCII name a progress line and an error use.
func stepLabel(k StepKind) string {
	switch k {
	case StepToken:
		return "token file"
	case StepPair:
		return "LiteLLM pair"
	case StepPlist:
		return "write the LaunchAgent plist"
	case StepBootout:
		return "unload the LaunchAgent"
	case StepBootstrap:
		return "load the LaunchAgent"
	case StepKickstart:
		return "restart the LaunchAgent"
	case StepWait:
		return "wait for wt-model"
	case StepConfig:
		return "write [model-server]"
	case StepRemovePlist:
		return "remove the LaunchAgent plist"
	}
	return "unknown step"
}

func applyStep(ctx context.Context, env Env, p Plan, s Step, r *Result) error {
	switch s.Kind {
	case StepToken:
		return applyToken(env, p, s)
	case StepPair:
		return applyPair(env, p, s)
	case StepPlist:
		if p.Agent == nil || s.Path == "" {
			return errors.New("the plan has no LaunchAgent to write")
		}
		return env.FS.WriteAtomic(s.Path, RenderAgentPlist(*p.Agent), 0o644)
	case StepBootout:
		w, err := bootout(ctx, env)
		if w != "" {
			r.Warnings = append(r.Warnings, w)
		}
		return err
	case StepBootstrap:
		return env.Launchd.Bootstrap(ctx, s.Path)
	case StepKickstart:
		return env.Launchd.Kickstart(ctx, AgentLabel, true)
	case StepWait:
		return waitReady(ctx, env, p, s)
	case StepConfig:
		return env.Config.Update(func(t config.ModelServerState) (config.ModelServerState, error) {
			t.Mode, t.URL, t.TokenFile = string(p.Want.Mode), p.Want.URL, p.Want.TokenFile
			return t, nil
		})
	case StepRemovePlist:
		err := env.FS.Remove(s.Path)
		if errors.Is(err, fs.ErrNotExist) {
			r.Warnings = append(r.Warnings, s.Path+" was already gone")
			return nil
		}
		return err
	}
	return fmt.Errorf("step kind %q is not one Apply knows; nothing of it was done", s.Kind)
}

// applyToken carries out a token step's Action.
func applyToken(env Env, p Plan, s Step) error {
	if s.Path == "" {
		return errors.New("the token step names no file")
	}
	switch s.Action {
	case ActionGenerateToken:
		b := make([]byte, tokenBytes)
		if err := env.Host.RandRead(b); err != nil {
			return fmt.Errorf("generate a token: %w", err)
		}
		return env.FS.WriteAtomic(s.Path, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600)
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

// applyPair writes wt-model's LiteLLM pair. The master key is read from the
// LiteLLM plist (or revealed from the plan) here and nowhere else.
func applyPair(env Env, p Plan, s Step) error {
	url := s.Value
	if url == "" {
		return errors.New("the pair step names no url")
	}
	var key string
	switch s.Action {
	case ActionPairKeyFromPlist:
		v, ok, err := env.LiteLLM.PlistVar(s.Path, "LITELLM_MASTER_KEY")
		if err != nil {
			return fmt.Errorf("read LITELLM_MASTER_KEY from %s: %w", s.Path, err)
		}
		if !ok || strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s has no LITELLM_MASTER_KEY any more; check again, or enter the key", s.Path)
		}
		key = strings.TrimSpace(v)
	case ActionPairKeyEntered:
		key = strings.TrimSpace(p.Want.MasterKey.Reveal())
		if key == "" {
			return errors.New("no master key was entered")
		}
	default:
		return fmt.Errorf("pair action %q is not one Apply knows", s.Action)
	}
	if err := env.Config.SetPair(&url, &key); err != nil {
		return fmt.Errorf("write wt-model's LiteLLM pair: %w", err)
	}
	return nil
}

// bootout unloads the agent. A job that is not loaded (launchd's error, then
// a print that says so) is not a failure: the returned warning says so.
func bootout(ctx context.Context, env Env) (warning string, err error) {
	err = env.Launchd.Bootout(ctx, AgentLabel)
	if err == nil {
		return "", nil
	}
	if job, perr := env.Launchd.Print(ctx, AgentLabel); perr == nil && !job.Loaded {
		return "the LaunchAgent " + AgentLabel + " was not loaded; nothing to unload", nil
	}
	return "", err
}

// waitReady pings the plan's server every ReadyPoll until it answers or
// ReadyTimeout has passed on the Host clock.
func waitReady(ctx context.Context, env Env, p Plan, s Step) error {
	url := s.Value
	if url == "" {
		url = p.Want.URL
	}
	if url == "" || p.Want.TokenFile == "" {
		return errors.New("the plan names no url or token file to wait on")
	}
	raw, err := env.FS.ReadFile(p.Want.TokenFile)
	if err != nil {
		return fmt.Errorf("read the token file: %w", err)
	}
	ep := modelclient.Endpoint{URL: url, Token: strings.TrimSpace(string(raw))}
	deadline := env.Host.Now().Add(ReadyTimeout)
	for {
		st := pingServer(ctx, env, ep)
		if st.OK {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !env.Host.Now().Before(deadline) {
			return &WaitError{URL: url, Timeout: ReadyTimeout, Kind: st.Kind, Message: st.Message, Log: errLogPath(env, p)}
		}
		if err := env.Host.Sleep(ctx, ReadyPoll); err != nil {
			return err
		}
	}
}

// errLogPath is the agent's stderr log, <home>/.wt-model.err.log.
func errLogPath(env Env, p Plan) string {
	home := ""
	if p.Agent != nil {
		home = p.Agent.Home
	}
	if home == "" {
		home, _ = env.Host.Home()
	}
	return filepath.Join(home, ".wt-model.err.log")
}

// ControlAction is what Control does to the LaunchAgent.
type ControlAction int

const (
	ControlStart ControlAction = iota
	ControlStop
	ControlRestart
)

func (a ControlAction) String() string {
	switch a {
	case ControlStart:
		return "start"
	case ControlStop:
		return "stop"
	case ControlRestart:
		return "restart"
	}
	return fmt.Sprintf("ControlAction(%d)", int(a))
}

// Control starts, stops or restarts wt's LaunchAgent. Start bootstraps the
// plist when the job is not loaded and kickstarts it otherwise; Stop boots
// it out (a job that is not loaded is already stopped); Restart is
// kickstart -k.
func Control(ctx context.Context, env Env, a ControlAction) error {
	switch a {
	case ControlStart:
		job, err := env.Launchd.Print(ctx, AgentLabel)
		if err != nil {
			return fmt.Errorf("start: %w", err)
		}
		if job.Loaded {
			return env.Launchd.Kickstart(ctx, AgentLabel, false)
		}
		home, err := env.Host.Home()
		if err != nil || home == "" {
			return fmt.Errorf("start: the home directory is not known")
		}
		return env.Launchd.Bootstrap(ctx, filepath.Join(home, "Library", "LaunchAgents", AgentLabel+".plist"))
	case ControlStop:
		_, err := bootout(ctx, env)
		return err
	case ControlRestart:
		return env.Launchd.Kickstart(ctx, AgentLabel, true)
	}
	return fmt.Errorf("%s is not a LaunchAgent action", a)
}
