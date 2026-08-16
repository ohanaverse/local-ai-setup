package main

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg    *config.Config
	models []config.Model // curated + discovered registry
}

// newApp loads and validates the config, then discovers live models.
func newApp() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &app{cfg: cfg, models: registry.Discover(cfg)}, nil
}
