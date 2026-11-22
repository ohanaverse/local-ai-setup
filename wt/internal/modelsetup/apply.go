package modelsetup

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	neturl "net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelmode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelping"
)

const (
	// ReadyTimeout is how long the wait step pings before it gives up.
	ReadyTimeout = 30 * time.Second
	// ReadyPoll is the pause between two pings of the wait step.
	ReadyPoll = 500 * time.Millisecond
	// LiteLLMReadyTimeout is how long a create plan waits for a new LiteLLM
	// to answer its liveliness check (it imports a lot before it listens).
	LiteLLMReadyTimeout = 90 * time.Second
	// LiteLLMReadyPoll is the pause between two of those checks.
	LiteLLMReadyPoll = time.Second
	// BootoutTimeout is how long a bootout waits for launchd to stop listing
	// the label: right after a bootout it can still be registered, and a
	// bootstrap then fails with an I/O error.
	BootoutTimeout = 10 * time.Second
	// BootoutPoll is the pause between two of those looks.
	BootoutPoll = 250 * time.Millisecond
)

// tokenBytes is the size of a generated token before base64.
const tokenBytes = 32

// Result is what Apply did: the steps that ran, in order, and the one that
// failed with its error. Notes say what a step left behind (a backup's
// path); Recovery, after a failure that left the LiteLLM plist, wt-model's
// pair or a running service out of step, says what is true and the exact
// commands to run by hand. Nothing in it holds a secret. It is
// modelmode.Result.
type Result = modelmode.Result

// applyRun is what one Apply carries from step to step and nowhere else: the
// master key a create or rotate step generated (held only after the plist on
// disk was read back with it), so the pair step can write the same key, and
// how far the LiteLLM change got, for Result.Recovery.
type applyRun struct {
	newKey           Secret
	rotating         bool   // a rotation's backup step began
	backup           string // the rotation's backup of the plist
	plist            string // the LiteLLM plist a create or rotate step wrote
	created          bool   // a create step wrote the plist
	edited           bool   // a rotate step wrote the plist (verified or not)
	pairURL          string
	pairSet          bool // the pair holds newKey
	litellmRestarted bool // a rotation's LiteLLM reload (bootout, then bootstrap) was accepted
	litellmUnloaded  bool // a rotation's bootout of the LiteLLM job was accepted
	litellmReady     bool // LiteLLM answered (and accepted the new key)
	wtRestarted      bool // wt-model was restarted or loaded after the pair
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
	why := modelmode.ServerFailure(ServerState{Kind: e.Kind, Message: e.Message})
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
	var run applyRun
	for i, s := range p.Steps {
		if err := ctx.Err(); err != nil {
			r = failed(r, s, err)
			r.Recovery = run.recovery(env, p, &s)
			return r
		}
		progress(fmt.Sprintf("step %d/%d: %s", i+1, len(p.Steps), stepLabel(s.Kind)))
		if err := applyStep(ctx, env, p, s, &r, &run); err != nil {
			r = failed(r, s, err)
			r.Recovery = run.recovery(env, p, &s)
			return r
		}
		r.Done = append(r.Done, s)
	}
	return r
}

// failed records s and err on a copy of r. It was a method (r.fail); Result
// is an alias of modelmode.Result now, and methods cannot be declared on an
// alias of another package's type.
func failed(r Result, s Step, err error) Result {
	r.Failed = &s
	r.Err = fmt.Errorf("%s: %w", stepLabel(s.Kind), err)
	return r
}

// stepLabel is the short ASCII name a progress line and an error use. The
// two session-side kinds are modelmode's; the rest are the admin steps this
// package adds.
func stepLabel(k StepKind) string {
	switch k {
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
	case StepRemovePlist:
		return "remove the LaunchAgent plist"
	case StepLiteLLMPlist:
		return "write the LiteLLM plist"
	case StepBackup:
		return "back up the LiteLLM plist"
	case StepMasterKey:
		return "replace the LiteLLM master key"
	case StepChmod:
		return "tighten the LiteLLM plist's mode"
	case StepKickstartLiteLLM:
		return "restart LiteLLM"
	}
	if l, ok := modelmode.StepLabel(k); ok {
		return l
	}
	return "unknown step"
}

func applyStep(ctx context.Context, env Env, p Plan, s Step, r *Result, run *applyRun) error {
	switch s.Kind {
	case StepToken:
		if s.Action == ActionGenerateToken {
			return applyToken(env, s)
		}
	case StepPair:
		if s.Action == ActionPairKeyNew {
			return applyNewPair(env, s, run)
		}
		return applyPair(env, p, s)
	case StepPlist:
		if p.Agent == nil || s.Path == "" {
			return errors.New("the plan has no LaunchAgent to write")
		}
		if _, err := agentPlistOurs(env, s.Path); err != nil {
			return err
		}
		return env.FS.WriteAtomic(s.Path, RenderAgentPlist(*p.Agent), 0o644)
	case StepBootout:
		w, err := bootout(ctx, env, AgentLabel)
		if w != "" {
			r.Warnings = append(r.Warnings, w)
		}
		return err
	case StepBootstrap:
		if err := env.Launchd.Bootstrap(ctx, s.Path); err != nil {
			return err
		}
		if s.Path != "" && s.Path == run.plist {
			return nil // the LiteLLM agent; its wait step follows
		}
		run.wtRestarted = run.pairSet
		return nil
	case StepKickstart:
		if err := env.Launchd.Kickstart(ctx, AgentLabel, true); err != nil {
			return err
		}
		run.wtRestarted = run.pairSet
		return nil
	case StepWait:
		if s.Action == ActionWaitLiteLLM {
			if err := waitLiteLLM(ctx, env, s); err != nil {
				return err
			}
			if run.created {
				if err := ownListener(ctx, env, s); err != nil {
					return err
				}
			}
			if err := keyAccepted(ctx, env, s, run); err != nil {
				return err
			}
			run.litellmReady = true
			return nil
		}
		return waitReady(ctx, env, p, s)
	case StepLiteLLMPlist:
		return applyLiteLLMPlist(ctx, env, p, s, run)
	case StepBackup:
		run.rotating, run.plist = true, s.Path
		if s.Value == "" {
			return errors.New("the backup step names no backup directory; nothing was changed")
		}
		// Everything that would refuse the key edit is found before the
		// backup, so a refused rotation leaves no copy of the old key.
		if err := rotatable(env, s.Path); err != nil {
			return err
		}
		dst, tightened, err := env.FS.Backup(s.Path, s.Value)
		if tightened {
			r.Notes = append(r.Notes, "the backup directory "+s.Value+" was readable by others and was set to mode 0700")
		}
		if err != nil {
			return fmt.Errorf("back up %s: %w", s.Path, err)
		}
		run.backup = dst
		r.Notes = append(r.Notes, "the old LiteLLM plist is kept at "+dst+" (it holds the old master key)")
		return nil
	case StepMasterKey:
		return applyMasterKey(env, s, run)
	case StepChmod:
		return env.FS.Chmod(s.Path, 0o600)
	case StepKickstartLiteLLM:
		// A reload, not a kickstart (#681): kickstart -k restarts the
		// definition launchd loaded at bootstrap time and never reads the
		// edited plist, so LiteLLM would keep the old master key. Unload the
		// job (waiting until launchd stops listing it), then bootstrap the
		// edited plist.
		w, err := bootout(ctx, env, litellm.ProxyLabel)
		if w != "" {
			r.Warnings = append(r.Warnings, w)
		}
		if err != nil {
			return err
		}
		run.litellmUnloaded = true
		if err := env.Launchd.Bootstrap(ctx, s.Path); err != nil {
			return err
		}
		run.litellmRestarted = true
		return nil
	case StepRemovePlist:
		present, err := agentPlistOurs(env, s.Path)
		if err != nil {
			return err
		}
		if !present {
			r.Warnings = append(r.Warnings, s.Path+" was already gone")
			return nil
		}
		err = env.FS.Remove(s.Path)
		if errors.Is(err, fs.ErrNotExist) {
			r.Warnings = append(r.Warnings, s.Path+" was already gone")
			return nil
		}
		return err
	}
	// The session-side steps (a token step other than generating one, and
	// the config step) are modelmode's.
	if handled, err := modelmode.ApplyStep(ctx, env.Mode(), p.Plan, s, r); handled {
		return err
	}
	return fmt.Errorf("step kind %q is not one Apply knows; nothing of it was done", s.Kind)
}

// applyToken generates a token into the step's file (0600). The other token
// actions are modelmode's (ApplyStep); generating one is the admin's, since
// it needs Host.RandRead.
func applyToken(env Env, s Step) error {
	if s.Path == "" {
		return errors.New("the token step names no file")
	}
	b := make([]byte, tokenBytes)
	if err := env.Host.RandRead(b); err != nil {
		return fmt.Errorf("generate a token: %w", err)
	}
	return env.FS.WriteAtomic(s.Path, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600)
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
			return fmt.Errorf("read LITELLM_MASTER_KEY from %s: %s", s.Path, PlistReadError(s.Path, err))
		}
		if !ok || strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s has no LITELLM_MASTER_KEY any more; check again, or enter the key", s.Path)
		}
		key = strings.TrimSpace(v)
	case ActionPairKeyEntered:
		key = strings.TrimSpace(p.Admin.MasterKey.Reveal())
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

// bootout unloads the job label. A job that is not loaded (launchd's error,
// then a print that says so) is not a failure: the returned warning says so.
// After a bootout launchd can list the label for a moment longer, and a
// bootstrap then fails with an I/O error, so it polls Print every BootoutPoll
// until the label is gone, failing after BootoutTimeout on the Host clock.
func bootout(ctx context.Context, env Env, label string) (warning string, err error) {
	err = env.Launchd.Bootout(ctx, label)
	if err != nil {
		if job, perr := env.Launchd.Print(ctx, label); perr == nil && !job.Loaded {
			return "the LaunchAgent " + label + " was not loaded; nothing to unload", nil
		}
		return "", err
	}
	deadline := env.Host.Now().Add(BootoutTimeout)
	for {
		job, perr := env.Launchd.Print(ctx, label)
		if perr == nil && !job.Loaded {
			return "", nil
		}
		if !env.Host.Now().Before(deadline) {
			target := "gui/" + strconv.Itoa(env.Host.UID()) + "/" + label
			return "", fmt.Errorf("launchd still lists %s %s after the bootout; wait a moment, check with `launchctl print %s`, then apply again",
				label, BootoutTimeout, target)
		}
		if err := env.Host.Sleep(ctx, BootoutPoll); err != nil {
			return "", err
		}
	}
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
	ep := modelping.Endpoint{URL: url, Token: strings.TrimSpace(string(raw))}
	deadline := env.Host.Now().Add(ReadyTimeout)
	for {
		st := modelmode.PingServer(ctx, env.Mode(), ep)
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
		_, err := bootout(ctx, env, AgentLabel)
		return err
	case ControlRestart:
		return env.Launchd.Kickstart(ctx, AgentLabel, true)
	}
	return fmt.Errorf("%s is not a LaunchAgent action", a)
}

// applyLiteLLMPlist writes a create plan's LiteLLM plist, 0600: never over a
// file that exists now, never while launchd already lists the label (loaded
// from another plist: wt does not replace it), and only from a spec that
// passes checkLiteLLMSpec. The keys are generated here from Host.RandRead
// alone, rendered into the file and kept only in run.
func applyLiteLLMPlist(ctx context.Context, env Env, p Plan, s Step, run *applyRun) error {
	if p.LiteLLM == nil || s.Path == "" {
		return errors.New("the plan has no LiteLLM plist to write; nothing was written")
	}
	spec := *p.LiteLLM
	spec.MasterKey, spec.SaltKey = "", ""
	if err := checkLiteLLMSpec(spec); err != nil {
		return fmt.Errorf("%w (nothing was written)", err)
	}
	// Lstat: a dangling symlink at the path is a plist that exists, never
	// one to rename a new file over.
	if _, err := env.FS.Lstat(s.Path); err == nil {
		return fmt.Errorf("%s exists now, and wt never replaces a LiteLLM plist; nothing was written: check again", s.Path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("look for %s: %w", s.Path, err)
	}
	job, err := env.Launchd.Print(ctx, litellm.ProxyLabel)
	if err != nil {
		return fmt.Errorf("ask launchd about %s: %w", litellm.ProxyLabel, err)
	}
	if job.Loaded {
		return fmt.Errorf("launchd already runs %s from another plist, which wt does not replace; nothing was written (see %s)", litellm.ProxyLabel, guide14)
	}
	master, err := GenerateKey(env.Host.RandRead)
	if err != nil {
		return err
	}
	salt, err := GenerateSaltKey(env.Host.RandRead)
	if err != nil {
		return err
	}
	spec.MasterKey, spec.SaltKey = master, salt
	if err := env.FS.WriteAtomic(s.Path, RenderLiteLLMPlist(spec), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", s.Path, err)
	}
	run.plist, run.created = s.Path, true
	if err := readBackKey(env, s.Path, master); err != nil {
		return err
	}
	run.newKey = Secret(master)
	return nil
}

// applyMasterKey replaces LITELLM_MASTER_KEY in the rotated plist with a new
// generated key, keeping every other byte and the file's mode minus group and
// other bits (0644 is written 0600; 0400 stays 0400), and reads the
// file back: only then does run hold the key the pair step writes. It refuses
// to run before a backup was made.
func applyMasterKey(env Env, s Step, run *applyRun) error {
	if run.backup == "" {
		return errors.New("no backup of the plist was made first; nothing was changed")
	}
	info, err := env.FS.Stat(s.Path)
	if err != nil {
		return fmt.Errorf("look at %s: %w", s.Path, err)
	}
	data, err := env.FS.ReadFile(s.Path)
	if err != nil {
		return fmt.Errorf("read %s: %w", s.Path, err)
	}
	// The file may have changed since the backup step looked at it.
	if err := saltGuard(s.Path, data); err != nil {
		return err
	}
	key, err := GenerateKey(env.Host.RandRead)
	if err != nil {
		return err
	}
	out, err := ReplaceMasterKey(data, key)
	if err != nil {
		return fmt.Errorf("edit %s: %w; nothing was changed", s.Path, err)
	}
	// Written in one step without group or other bits: the new key is never
	// readable by others, not even for a moment (there is no later chmod
	// step in a rotation to tighten it).
	if err := env.FS.WriteAtomic(s.Path, out, info.Mode().Perm()&^0o077); err != nil {
		return fmt.Errorf("write %s: %w", s.Path, err)
	}
	run.plist, run.edited = s.Path, true
	if err := readBackKey(env, s.Path, key); err != nil {
		return err
	}
	run.newKey = Secret(key)
	return nil
}

// rotatable checks, on the LiteLLM plist as it is when applied, everything
// that would refuse a rotation: the DATABASE_URL-without-salt guard (the
// plan checked it on an older read) and a dry run of the key edit with a
// placeholder key. Nothing is written; no error carries a value.
func rotatable(env Env, path string) error {
	data, err := env.FS.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w; nothing was changed", path, err)
	}
	if err := saltGuard(path, data); err != nil {
		return err
	}
	if _, err := ReplaceMasterKey(data, "sk-"+strings.Repeat("0", 64)); err != nil {
		return fmt.Errorf("edit %s: %w; nothing was changed", path, err)
	}
	return nil
}

// saltGuard refuses a rotation of a plist that sets DATABASE_URL but no
// LITELLM_SALT_KEY: LiteLLM then encrypts stored credentials with the master
// key, which a new one would leave unreadable. A plist that does not parse is
// left to the key edit's own refusal.
func saltGuard(path string, data []byte) error {
	sc, err := scanEnv(data)
	if err != nil {
		return nil
	}
	_, db := sc.vars["DATABASE_URL"]
	_, salt := sc.vars["LITELLM_SALT_KEY"]
	if db && !salt {
		return fmt.Errorf("%s now sets DATABASE_URL but no LITELLM_SALT_KEY, so a new master key could make LiteLLM's stored credentials unreadable; "+
			"first set LITELLM_SALT_KEY to the current master key's value (see %s); nothing was changed", path, guide14)
	}
	return nil
}

// agentPlistOurs re-reads wt's LaunchAgent plist when a step writes or
// removes it: absent (present false) or managed by wt (the marker and the
// label) is fine; anything else, or a file that cannot be read, fails the
// step before anything is written or removed.
func agentPlistOurs(env Env, path string) (present bool, err error) {
	data, err := env.FS.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("read %s: %w; nothing was written or removed", path, err)
	}
	if p, perr := ParseAgentPlist(data); perr != nil || !p.Managed {
		return true, fmt.Errorf("%s is not marked \"managed by wt\" with the label %s (it changed since the plan), so wt neither overwrites nor removes it; nothing was written or removed: check again", path, AgentLabel)
	}
	return true, nil
}

// PlistReadError words an error from reading the LiteLLM plist without its
// text when that text could quote the file (an XML decoder error can quote a
// fragment after an &, and the plist holds secrets): a file-system error and
// the binary-plist error name only paths; anything else is a fixed message.
func PlistReadError(path string, err error) string {
	var pe *fs.PathError
	switch {
	case errors.Is(err, litellm.ErrBinaryPlist):
		return err.Error()
	case errors.As(err, &pe):
		return pe.Op + " " + pe.Path + ": " + pe.Err.Error()
	}
	return path + " is not a well-formed XML plist"
}

// readBackKey re-reads path and checks it carries key as its one master key.
func readBackKey(env Env, path, key string) error {
	back, err := env.FS.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s back: %w", path, err)
	}
	sc, err := scanEnv(back)
	if err != nil || sc.masters != 1 || subtle.ConstantTimeCompare([]byte(sc.vars[masterKeyName]), []byte(key)) != 1 {
		return fmt.Errorf("%s does not read back with the new master key", path)
	}
	return nil
}

// applyNewPair writes the key this Apply generated (create or rotate) to
// wt-model's pair. With no verified key it fails and writes nothing.
func applyNewPair(env Env, s Step, run *applyRun) error {
	url := s.Value
	if url == "" {
		return errors.New("the pair step names no url")
	}
	run.pairURL = url
	key := run.newKey.Reveal()
	if key == "" {
		return errors.New("no new master key was written to the LiteLLM plist in this apply; the pair is unchanged")
	}
	if err := env.Config.SetPair(&url, &key); err != nil {
		return fmt.Errorf("write wt-model's LiteLLM pair: %w", err)
	}
	run.pairSet = true
	return nil
}

// waitLiteLLM checks LiteLLM's /health/liveliness under Step.Value every
// LiteLLMReadyPoll until it answers 200 or LiteLLMReadyTimeout has passed.
func waitLiteLLM(ctx context.Context, env Env, s Step) error {
	if s.Value == "" {
		return errors.New("the wait step names no LiteLLM url")
	}
	url := strings.TrimRight(s.Value, "/") + "/health/liveliness"
	deadline := env.Host.Now().Add(LiteLLMReadyTimeout)
	for {
		status, _, err := env.Net.Get(ctx, url, "")
		if err == nil && status == 200 {
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !env.Host.Now().Before(deadline) {
			why := fmt.Sprintf("the last answer was HTTP %d", status)
			if err != nil {
				why = "it did not answer"
			}
			home, _ := env.Host.Home()
			return fmt.Errorf("LiteLLM did not answer at %s within %s (%s); see %s", url, LiteLLMReadyTimeout, why, filepath.Join(home, ".litellm.err.log"))
		}
		if err := env.Host.Sleep(ctx, LiteLLMReadyPoll); err != nil {
			return err
		}
	}
}

// ownListener checks that what answered is the job a create step just
// loaded: the pid listening on the url's port must be the pid launchd gives
// local.litellm.proxy. A LiteLLM run by hand would otherwise answer the
// checks while the new job crash-loops on bind, and the pair would be set to
// a key that process rejects.
func ownListener(ctx context.Context, env Env, s Step) error {
	u, err := neturl.Parse(s.Value)
	if err != nil {
		return fmt.Errorf("the wait step's url %q does not parse", s.Value)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return fmt.Errorf("the wait step's url %q names no port", s.Value)
	}
	job, err := env.Launchd.Print(ctx, litellm.ProxyLabel)
	if err != nil {
		return fmt.Errorf("ask launchd about %s: %w", litellm.ProxyLabel, err)
	}
	pid, ok, err := env.Net.ListenerPID(ctx, port)
	if err != nil {
		return fmt.Errorf("look for the listener on port %d: %w", port, err)
	}
	if ok && job.Running && job.PID != 0 && !descendsFrom(ctx, env, pid, job.PID) {
		ok = false
	}
	if !ok || !job.Running || job.PID == 0 {
		return fmt.Errorf("LiteLLM answered on port %d, but the listener there (pid %d) is not the new %s job (pid %d): another process holds the port; the pair was not written",
			port, pid, litellm.ProxyLabel, job.PID)
	}
	return nil
}

// listenerAncestors bounds the walk from a listener up to the job's pid: a
// wrapper script, a worker pool and a spawn each add one level.
const listenerAncestors = 4

// descendsFrom reports whether pid is job or a descendant of it, found by
// walking up at most listenerAncestors parents. The job launchd supervises is
// not always the process that listens (a litellm that is a shell wrapper
// forking Python without exec, or one with several workers): a process with
// no path to the job's pid is another process's, which is the case the check
// exists for. A parent that cannot be read ends the walk with "no" — the
// check fails closed (#641).
func descendsFrom(ctx context.Context, env Env, pid, job int) bool {
	for i := 0; i <= listenerAncestors; i++ {
		if pid == job {
			return true
		}
		if pid <= 1 {
			return false
		}
		parent, err := env.Net.ParentPID(ctx, pid)
		if err != nil {
			return false
		}
		pid = parent
	}
	return false
}

// keyAccepted makes one authenticated request (GET /v1/models, the key only
// as the bearer header, never in text) with the master key this Apply wrote,
// so the pair is set, and wt-model restarted, only for a key LiteLLM takes.
func keyAccepted(ctx context.Context, env Env, s Step, run *applyRun) error {
	key := run.newKey.Reveal()
	if key == "" {
		return nil
	}
	url := strings.TrimRight(s.Value, "/") + "/v1/models"
	status, _, err := env.Net.Get(ctx, url, key)
	switch {
	case err != nil:
		return fmt.Errorf("LiteLLM did not answer the key check at %s", url)
	case status == 401 || status == 403:
		return fmt.Errorf("LiteLLM at %s rejected the new master key (HTTP %d)", url, status)
	case status != 200:
		return fmt.Errorf("LiteLLM at %s did not accept the new master key (HTTP %d)", url, status)
	}
	return nil
}

// recovery says, after a failed step, where the LiteLLM change stands and
// what to run by hand; "" when the LiteLLM plist was never touched or the
// failure left nothing out of step. It never holds a key.
func (run *applyRun) recovery(env Env, p Plan, failed *Step) string {
	domain := "gui/" + strconv.Itoa(env.Host.UID())
	url := run.pairURL
	if url == "" {
		url = "http://localhost:" + strconv.Itoa(DefaultLiteLLMPort)
	}
	restartWt := "`launchctl kickstart -k " + domain + "/" + AgentLabel + "`"
	switch {
	case run.rotating && !run.edited:
		if run.backup != "" {
			return run.plistOrNone() + " and wt-model's pair are unchanged (the backup " + run.backup + " is not needed)"
		}
		// Not "nothing was changed": an earlier token step of the same plan
		// may have written server.token.
		return run.plistOrNone() + " and wt-model's pair were not touched; no backup was kept"
	case run.edited && !run.pairSet:
		return "the plist " + run.plist + " may hold a new master key while wt-model's pair and the running LiteLLM still use the old one: " +
			"put the old plist back with `cp -p " + run.backup + " " + run.plist + " && chmod 600 " + run.plist + "` (nothing else was changed), then apply again"
	case run.created && !run.pairSet:
		return "the new plist " + run.plist + " is in place but wt-model's pair was not set: once LiteLLM answers (see its log), " +
			"load it if it is not loaded with `launchctl bootstrap " + domain + " " + run.plist + "`, then set the pair with " +
			"`wt-model litellm set --url " + url + " --api-key <the LITELLM_MASTER_KEY in " + run.plist + ">` and run " + restartWt +
			"; to abandon it instead, run `launchctl bootout " + domain + "/" + litellm.ProxyLabel + "` and remove " + run.plist + " by hand"
	case run.pairSet && !run.litellmReady && run.litellmRestarted:
		home, _ := env.Host.Home()
		return "the plist " + run.plist + " and wt-model's pair both hold the new master key and LiteLLM was restarted, but it did not answer with the new key (see " +
			filepath.Join(home, ".litellm.err.log") + "): once it does, run " + restartWt
	case run.pairSet && !run.litellmReady:
		// A kickstart would restart the OLD loaded definition, which holds the
		// old key: the edited plist has to be bootstrapped (#681).
		state := "LiteLLM still runs with the old one"
		unload := "`launchctl bootout " + domain + "/" + litellm.ProxyLabel + "` (skip it if the job is already gone), then "
		if run.litellmUnloaded {
			state = "LiteLLM was unloaded and not loaded again"
			unload = ""
		}
		return "the plist " + run.plist + " and wt-model's pair both hold the new master key, but " + state + ": run " + unload +
			"`launchctl bootstrap " + domain + " " + run.plist + "`, then " + restartWt
	case run.pairSet && !run.wtRestarted && failed != nil && (failed.Kind == StepBootout || failed.Kind == StepBootstrap):
		// A kickstart would restart the OLD definition (or nothing is loaded):
		// the new agent plist has to be bootstrapped.
		agentPlist := failed.Path // the plan's bootout/bootstrap steps always carry it
		return "the plist " + run.plist + ", wt-model's pair and LiteLLM all use the new master key, but wt-model's agent was not reloaded (the old definition may still be loaded): run " +
			"`launchctl bootout " + domain + "/" + AgentLabel + "` (skip it if the job is already gone), then `launchctl bootstrap " + domain + " " + agentPlist + "`"
	case run.pairSet && !run.wtRestarted:
		return "the plist " + run.plist + ", wt-model's pair and LiteLLM all use the new master key, but wt-model has not been restarted to read it: run " + restartWt
	}
	return ""
}

func (run *applyRun) plistOrNone() string {
	if run.plist == "" {
		return "the LiteLLM plist"
	}
	return "the plist " + run.plist
}
