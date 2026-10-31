package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg      *config.Config
	cfgErr   error        // config load/validation error, if any; surfaced by commands that can repair it
	loadErr  error        // config.Load error ONLY (parse/IO/registry missing); gates commands that just rewrite wt's own [litellm] state
	theme    themes.Theme // active theme; populated by newApp()
	profiles profiles.Store
	// profilesLoadErr is set ONLY when profiles.Load() itself fails (a
	// genuine parse/IO failure — the file is malformed or unreadable). When
	// set, a.profiles is the zero-value Store{} (empty, Enabled: false),
	// NOT real data, so `wt profile on|off` must refuse to write (it would
	// silently overwrite the malformed file with just "enabled = false",
	// destroying every hand-authored profile and comment) and
	// list/show/status must report the error instead of rendering an
	// empty/default profiles view. Mirrors how cfg/cfgErr/loadErr already
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

// newApp loads the config (best-effort), the active theme, and the
// profiles store. Config and profiles validation errors are stored in the
// returned app rather than returned as a fatal error, so `wt config`/
// `wt profile` can still launch and let the user repair a broken file.
// newApp does no live model discovery, so flag-only paths (--version,
// --init, -W, --cwd) don't shell out to ollama or hit the OpenRouter API.
func newApp() (*app, error) {
	cfg, cfgErr := config.Load()
	loadErr := cfgErr
	if cfg == nil {
		cfg = &config.Config{DefaultTag: "code"}
	}
	if cfgErr == nil {
		cfgErr = cfg.Validate()
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
	return &app{
		cfg: cfg, cfgErr: cfgErr, loadErr: loadErr, theme: theme,
		profiles: store, profilesLoadErr: profilesLoadErr, profilesValidateErr: profilesValidateErr,
		slotChecks: slotChecks, slotUnknown: slotUnknown, slotLoadErr: slotLoadErr,
	}, nil
}
