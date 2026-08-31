package main

import (
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg    *config.Config
	cfgErr error        // config load/validation error, if any; surfaced by commands that can repair it
	theme  themes.Theme // active theme; populated by newApp()
}

// newApp loads the config (best-effort) and the active theme. Config
// validation errors are stored in the returned app rather than returned as
// a fatal error, so `wt config` can still launch and let the user repair a
// broken config.toml. Live model discovery is deferred to the `models`
// subcommand so flag-only paths (--version, --init, -w, --cwd) don't shell
// out to ollama or hit the OpenRouter API.
func newApp() (*app, error) {
	cfg, cfgErr := config.Load()
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
	return &app{cfg: cfg, cfgErr: cfgErr, theme: theme}, nil
}
