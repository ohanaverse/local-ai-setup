package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// LitellmState is wt-owned since 2026-09-21; it mirrors `[litellm]` in wt's
// config.toml. modelman.toml's `[litellm]` is a legacy read-only fallback.
// It controls whether agents dial providers directly or route through the
// LiteLLM proxy.
type LitellmState struct {
	Enabled bool   `toml:"enabled"`
	URL     string `toml:"url"`
	APIKey  string `toml:"api_key"`
}

// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
//
// wt reads no per-model state at all (#179 Phase B): what is on disk and
// what is running come from its live inventory, never from modelman's
// `ready`/`downloaded`, `running` or retired `exposed` flags.
type modelmanState struct {
	// price_refresh_last_run is modelman's global "token pricing last
	// refreshed" date (YYYY-MM-DD), written by `modelman refresh-prices`.
	// wt reads it post-launch to print a stale-pricing notice.
	PriceRefreshLastRun string        `toml:"price_refresh_last_run"`
	Litellm             *LitellmState `toml:"litellm"`
}

// loadModelmanState reads modelman.toml and returns its legacy [litellm]
// routing state (nil when the table or the file is absent).
func loadModelmanState() (*LitellmState, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	return s.Litellm, nil
}

// PriceRefreshLastRun returns modelman's global token-pricing refresh
// date (price_refresh_last_run in ~/.config/local-ai/modelman.toml) as
// (value, present). present is false when the file or key is missing or
// the file cannot be read/parsed — the notice must stay silent on errors,
// matching how loadModelmanState tolerates a missing file. wt is a
// read-only consumer; modelman owns the key.
func PriceRefreshLastRun() (string, bool) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return "", false
	}
	if s.PriceRefreshLastRun == "" {
		return "", false
	}
	return s.PriceRefreshLastRun, true
}
