package main

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg *config.Config
}

// newApp loads and validates the config. Live model discovery is deferred to
// the `models` subcommand so flag-only paths (--version, --init, -w, --cwd)
// don't shell out to ollama or hit the OpenRouter API.
func newApp() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &app{cfg: cfg}, nil
}
