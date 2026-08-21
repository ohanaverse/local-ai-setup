package main

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg   *config.Config
	theme themes.Theme // active theme; populated by newApp()
}

// newApp loads and validates the config and loads the active theme. Live
// model discovery is deferred to the `models` subcommand so flag-only paths
// (--version, --init, -w, --cwd) don't shell out to ollama or hit the
// OpenRouter API.
func newApp() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Load the active theme. I/O and TOML parse errors are hard failures.
	// An unknown or empty theme name falls back to Default so a typo in
	// themes.toml never crashes the launcher — the user can still run
	// `wt config theme set` or `wt config theme unset` to repair it.
	theme, _, err := themes.Load()
	if err != nil && !themes.IsThemeNameError(err) {
		return nil, err
	}
	return &app{cfg: cfg, theme: theme}, nil
}
