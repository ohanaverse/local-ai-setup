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

// modelmanState mirrors the one table of ~/.config/local-ai/modelman.toml
// that wt still reads. The file is modelman's.
//
// wt reads no per-model state at all (#179 Phase B): what is on disk and
// what is running come from its live inventory, never from modelman's
// `ready`/`downloaded`, `running` or retired `exposed` flags. Nor does it
// read price_refresh_last_run any more: the stale-price notice takes its
// date from the registry's pricing_updated_at stamps (agents.LastPriceRefresh).
type modelmanState struct {
	Litellm *LitellmState `toml:"litellm"`
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
