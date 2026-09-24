package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg         *config.Config
	cfgErr      error        // config load/validation error, if any; surfaced by commands that can repair it
	loadErr     error        // config.Load error ONLY (parse/IO/registry missing); gates commands that just rewrite wt's own [litellm] state
	theme       themes.Theme // active theme; populated by newApp()
	profiles    profiles.Store
	profilesErr error // profiles.toml load or Validate error, if any; surfaced by `wt profile` commands
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

// newApp loads the config (best-effort), the active theme, and the
// profiles store. Config and profiles validation errors are stored in the
// returned app rather than returned as a fatal error, so `wt config`/
// `wt profile` can still launch and let the user repair a broken file.
// Live model discovery is deferred to the `models` subcommand so
// flag-only paths (--version, --init, -w, --cwd) don't shell out to
// ollama or hit the OpenRouter API.
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
	store, profilesErr := profiles.Load()
	if profilesErr == nil {
		profilesErr = profiles.Validate(store, agentProfileMechanisms)
	}
	return &app{
		cfg: cfg, cfgErr: cfgErr, loadErr: loadErr, theme: theme,
		profiles: store, profilesErr: profilesErr,
	}, nil
}
