package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/smoke"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// warnIgnoredLaunchKeys prints one note naming each legacy
// config key (config.IgnoredLaunchKeys) that wt tolerates but no longer
// reads — the retired [launch] exit_prompt today. R305a: such a key is
// tolerated and ignored, never a validation error. w is the caller's output
// seam (newApp passes osStderr).
func warnIgnoredLaunchKeys(w io.Writer, cfg *config.Config) {
	keys := cfg.IgnoredLaunchKeys()
	if len(keys) == 0 {
		return
	}
	for _, key := range keys {
		fmt.Fprintf(w, "wt: note: config.toml: %s is no longer used and is ignored\n", key)
	}
}

// ignoredLitellmNoted is set once this process has printed the note for a
// retired [litellm] key: the note is printed once per process, however many
// command trees the process builds (tests build many).
var ignoredLitellmNoted atomic.Bool

// warnIgnoredLitellmKeys prints, once per process, one note naming each
// [litellm] key wt tolerates but no longer reads (config.IgnoredLitellmKeys:
// enabled, since every session routes through the model server). The url
// and api_key it leaves alone: they are host state `wt-model serve` reads
// (R4), and they round-trip every save.
func warnIgnoredLitellmKeys(w io.Writer, cfg *config.Config) {
	keys := cfg.IgnoredLitellmKeys()
	if len(keys) == 0 || ignoredLitellmNoted.Swap(true) {
		return
	}
	for _, key := range keys {
		fmt.Fprintf(w, "wt: note: config.toml: %s is no longer used and is ignored\n", key)
	}
}

// hooks are the per-app replacements for the work a test must not do for
// real: build and start an agent, dial a model server, read the terminal,
// wait between polls. The zero value is the real behavior and is what
// newApp (so production) builds; a test sets a field on the app it runs. A
// *modelSession points at the app's hooks, so the code that
// holds only a session (openClientSession, the stop picker, a cancel poll)
// reaches them without the app.
type hooks struct {
	// buildGateway builds a launch against the gateway; nil is
	// agents.BuildLaunchCmdGateway.
	buildGateway func(agent string, m config.Model, worktreePath string, yolo bool, gw agents.Gateway, models []config.Model, extraArgs []string) (*exec.Cmd, agents.LaunchInfo, error)
	// buildSlotGateway is the slot build; nil is agents.BuildSlotLaunchCmdGateway.
	buildSlotGateway func(sl profiles.SlotLaunch, worktreePath string, yolo bool, gw agents.Gateway, extraArgs []string) (*exec.Cmd, error)
	// newClient builds the client for an endpoint; nil is modelclient.New (in a
	// test binary, a client that dials loopback only).
	newClient func(ep modelclient.Endpoint) *modelclient.Client
	// beatInterval is the client session's heartbeat interval; 0 is
	// modelclient's default (20 s).
	beatInterval time.Duration
	// stopIn is where the stop picker reads its answer; nil is os.Stdin.
	stopIn func() io.Reader
	// jobSleep waits between the polls of a cancel; nil waits on a timer.
	jobSleep func(ctx context.Context, d time.Duration) error
	// smokeRun replaces the agent run of a smoke row; the zero value runs it.
	smokeRun smoke.RowOptions
}

// build is the gateway launch builder.
func (h hooks) build(agent string, m config.Model, worktreePath string, yolo bool, gw agents.Gateway, models []config.Model, extraArgs []string) (*exec.Cmd, agents.LaunchInfo, error) {
	if h.buildGateway != nil {
		return h.buildGateway(agent, m, worktreePath, yolo, gw, models, extraArgs)
	}
	return agents.BuildLaunchCmdGateway(agent, m, worktreePath, yolo, gw, models, extraArgs)
}

// buildSlot is the slot launch builder.
func (h hooks) buildSlot(sl profiles.SlotLaunch, worktreePath string, yolo bool, gw agents.Gateway, extraArgs []string) (*exec.Cmd, error) {
	if h.buildSlotGateway != nil {
		return h.buildSlotGateway(sl, worktreePath, yolo, gw, extraArgs)
	}
	return agents.BuildSlotLaunchCmdGateway(sl, worktreePath, yolo, gw, extraArgs)
}

// client builds the model-server client for ep.
func (h hooks) client(ep modelclient.Endpoint) *modelclient.Client {
	if h.newClient != nil {
		return h.newClient(ep)
	}
	if testing.Testing() {
		return loopbackOnlyClient(ep)
	}
	return modelclient.New(ep)
}

// loopbackOnlyClient is the client a test binary gets when no hook names one:
// its transport refuses to dial any address that is not loopback, so the
// httptest fake is the one network a test reaches. Never used by a production
// binary (testing.Testing() is false there). opts are extra client options a
// test adds (fastModelClient's no-sleep poll).
func loopbackOnlyClient(ep modelclient.Endpoint, opts ...modelclient.Option) *modelclient.Client {
	d := &net.Dialer{}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			return nil, fmt.Errorf("cmd/wt-session tests dial loopback only, not %q", addr)
		}
		return d.DialContext(ctx, network, addr)
	}}
	return modelclient.New(ep, append([]modelclient.Option{modelclient.WithHTTPClient(&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})}, opts...)...)
}

// stopReader is where the stop picker reads from.
func (h hooks) stopReader() io.Reader {
	if h.stopIn != nil {
		return h.stopIn()
	}
	return os.Stdin
}

// sleep waits d between the polls of a cancel, or until ctx ends.
func (h hooks) sleep(ctx context.Context, d time.Duration) error {
	if h.jobSleep != nil {
		return h.jobSleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// app holds shared dependencies loaded once at startup.
type app struct {
	// hooks replace the work a test must not do for real; the zero value is
	// the real behavior.
	hooks    hooks
	theme    themes.Theme // active theme; populated by newApp()
	profiles profiles.Store
	// profilesLoadErr is set ONLY when profiles.Load() itself fails (a
	// genuine parse/IO failure — the file is malformed or unreadable). When
	// set, a.profiles is the zero-value Store{} (empty, Enabled: false),
	// NOT real data, so `wt profile on|off` must refuse to write (it would
	// silently overwrite the malformed file with just "enabled = false",
	// destroying every hand-authored profile and comment) and
	// list/show/status must report the error instead of rendering an
	// empty/default profiles view. Mirrors how hostLoadErr/hostCfgErr
	// separate "config.Load failed" from "config validation failed".
	profilesLoadErr error
	// profilesValidateErr is set ONLY when profiles.Validate() fails, which
	// only runs when Load succeeded — a.profiles therefore still holds the
	// file's real, correctly-parsed data (some profile just names a
	// mechanism its agent doesn't declare support for). This is safe to
	// toggle/list/show since nothing was lost; it only means that specific
	// profile can never actually apply at launch time (applyProfileForLaunch
	// also runs Validate and degrades gracefully on failure).
	profilesValidateErr error
	// slotChecks is slot-profiles.toml after static validation
	// (profiles.ValidateSlots), in file order; nil when the file is missing
	// or did not load. A check that is not Usable is kept: `wt profile list`
	// reports it by its position and a launch warns about it once.
	slotChecks []profiles.SlotCheck
	// slotUnknown is the keys of slot-profiles.toml the schema does not name
	// (profiles.SlotStore.Unknown). The file loaded, and what such a key
	// holds was not read: a launch warns about each once, and so does
	// `wt profile list`. It can be set while slotChecks is nil.
	slotUnknown []string
	// slotLoadErr is profiles.LoadSlots' error: a parse or read failure.
	// It never stops wt; a launch prints it once and shows no profile rows.
	slotLoadErr error

	// The session's config and model-server handle (models.go), both read on
	// first use and cached.
	sessCfg    *config.Config
	sessCfgErr error
	sessCfgSet bool
	models     *modelSession
	// built, builtErr and builtSet cache buildModels' one read of the
	// model-server settings (a failure included).
	built    *modelSession
	builtErr error
	builtSet bool

	// The host's config (hostConfig): config.toml and registry.toml through
	// config.Load, read on first use by a host command and cached. hostCfg is
	// never nil once hostSet (an empty default when the load failed);
	// hostLoadErr is config.Load's error alone (parse, I/O, a missing
	// registry), which protectedPaths and noRegistry read; hostCfgErr is that
	// error or else the validation error, for the commands that need a valid
	// registry.
	hostCfg     *config.Config
	hostLoadErr error
	hostCfgErr  error
	hostSet     bool
}

// hostConfig is the host's config: config.Load (config.toml and the
// registry), read once, on first use, and cached; the error is config.Load's
// alone. Only the host commands read it (R3:
// `served`, `warm`, `stop <provider>|--all`, `cloud-sync`, `litellm`, `wt
// config`, `stats`' registry note) and protectedPaths
// (R10): a session path reads sessionCfg, never the registry. The returned
// config is never nil: an empty default stands in when the load failed, as
// newApp's did before the flip, so a command that tolerates a load error
// (the config editor) still has a value to hand on. The cache is
// unsynchronized: the main goroutine only.
func (a *app) hostConfig() (*config.Config, error) {
	a.loadHost()
	return a.hostCfg, a.hostLoadErr
}

// hostConfigErr is hostConfig's load error, or else the loaded config's
// validation error: what a host command that needs a valid registry (`wt
// stop <provider>`, the config editor's banner) refuses on.
func (a *app) hostConfigErr() error {
	a.loadHost()
	return a.hostCfgErr
}

// loadHost reads the host's config once.
func (a *app) loadHost() {
	if a.hostSet {
		return
	}
	cfg, err := config.Load()
	if cfg == nil {
		cfg = &config.Config{DefaultTag: "code"}
	}
	verr := err
	if verr == nil {
		verr = cfg.Validate()
	}
	a.hostCfg, a.hostLoadErr, a.hostCfgErr, a.hostSet = cfg, err, verr, true
}

// profileState packages a's already-loaded profiles.toml state for
// applyProfileForLaunch (via runAgentCmd's pp parameter), so a launch reuses
// newApp()'s single Load/Validate instead of repeating both.
func (a *app) profileState() *precomputedProfiles {
	return &precomputedProfiles{store: a.profiles, loadErr: a.profilesLoadErr, validateErr: a.profilesValidateErr}
}

// agentProfileMechanisms looks up the profile mechanisms agent's driver
// declares via profiles.ProfileCapable, or nil for an unregistered agent
// or one that implements no mechanisms at all (copilot, shell).
func agentProfileMechanisms(agent string) []profiles.Mechanism {
	d := agents.ByName(agent)
	if d == nil {
		return nil
	}
	pc, ok := d.(profiles.ProfileCapable)
	if !ok {
		return nil
	}
	return pc.ProfileMechanisms()
}

// agentRequiredMechanism looks up, via profiles.MechanismRequirer, the
// mechanism agent's driver requires alongside m on the same profile entry,
// or ok=false for an unregistered agent or one that declares no such
// coupling (every agent but pi, currently).
func agentRequiredMechanism(agent string, m profiles.Mechanism) (profiles.Mechanism, bool) {
	d := agents.ByName(agent)
	if d == nil {
		return "", false
	}
	mr, ok := d.(profiles.MechanismRequirer)
	if !ok {
		return "", false
	}
	return mr.RequiredMechanism(m)
}

// loadSlotStore is a seam for tests: production reads
// ~/.config/agent-wt/slot-profiles.toml (profiles.LoadSlots).
var loadSlotStore = profiles.LoadSlots

// loadSlotState reads slot-profiles.toml and returns what the app caches
// from it: the static checks (nil when the file is missing, empty or did not
// load), the unknown keys, and the load error.
func loadSlotState() (checks []profiles.SlotCheck, unknown []string, err error) {
	store, err := loadSlotStore()
	if err != nil {
		return nil, nil, err
	}
	if len(store.Profiles) > 0 {
		checks = profiles.ValidateSlots(store, agents.SlotDrivers())
	}
	return checks, store.Unknown, nil
}

// newApp reads the session's config (config.toml only, best-effort), the
// active theme, and the profiles store. It reads no registry: a host command
// reads it through hostConfig when it runs, so a session machine with no
// registry starts wt like any other. Config and profiles errors are stored
// in the returned app rather than returned as a fatal error, so `wt config`/
// `wt profile` can still launch and let the user repair a broken file.
// newApp does no live model discovery, so flag-only paths (--version,
// --init, -W, --cwd) don't shell out to ollama or hit the OpenRouter API.
func newApp() (*app, error) {
	a := &app{}
	// A legacy [launch] exit_prompt and the retired [litellm] enabled are
	// tolerated and ignored (R305a, R4), never a load failure; noticed
	// instead. A config.toml that does not load is said by the command that
	// needs it.
	if scfg, err := a.sessionCfg(); err == nil {
		warnIgnoredLaunchKeys(osStderr, scfg)
		warnIgnoredLitellmKeys(osStderr, scfg)
	}
	// Load the active theme. I/O and TOML parse errors are hard failures.
	// An unknown or empty theme name falls back to Default so a typo in
	// themes.toml never crashes the launcher — the user can still run
	// `wt config theme set` or `wt config theme unset` to repair it.
	theme, _, err := themes.Load()
	if err != nil && !themes.IsThemeNameError(err) {
		return nil, err
	}
	store, profilesLoadErr := profiles.Load()
	var profilesValidateErr error
	if profilesLoadErr == nil {
		profilesValidateErr = profiles.Validate(store, agentProfileMechanisms, agentRequiredMechanism)
	}
	// Slot profiles load independently of profiles.toml: an error in either
	// file never hides the other.
	slotChecks, slotUnknown, slotLoadErr := loadSlotState()
	a.theme = theme
	a.profiles, a.profilesLoadErr, a.profilesValidateErr = store, profilesLoadErr, profilesValidateErr
	a.slotChecks, a.slotUnknown, a.slotLoadErr = slotChecks, slotUnknown, slotLoadErr
	return a, nil
}
