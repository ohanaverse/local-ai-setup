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

// ExposureEntry is the decoded-in-memory representation of a single model
// state entry for wt's exposure predicate.
type ExposureEntry struct {
	Exposed bool
	Ready   bool
}

// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
//
// `downloaded` is the legacy spelling of `ready` (modelman/state.py still
// accepts `downloaded` as a read-side fallback for pre-registry files).
// wt materializes both keys into a single Ready bool so the exposure
// predicate treats legacy entries consistently with modelman.
//
// `litellm_exposed` is the legacy spelling of `exposed`; wt ORs the two so
// a pre-rename file keeps working.
type modelmanState struct {
	// price_refresh_last_run is modelman's global "token pricing last
	// refreshed" date (YYYY-MM-DD), written by `modelman refresh-prices`.
	// wt reads it post-launch to print a stale-pricing notice.
	PriceRefreshLastRun string `toml:"price_refresh_last_run"`
	ModelState          map[string]struct {
		Exposed        bool `toml:"exposed"`
		LitellmExposed bool `toml:"litellm_exposed"` // back-compat read
		Ready          bool `toml:"ready"`
		Downloaded     bool `toml:"downloaded"`
	} `toml:"model_state"`
	Litellm *LitellmState `toml:"litellm"`
}

// loadModelmanState reads modelman.toml and returns the exposure map and
// the legacy [litellm] routing state (nil when the table is absent). A missing
// file returns an empty map and nil.
func loadModelmanState() (map[string]ExposureEntry, *LitellmState, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]ExposureEntry{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]ExposureEntry, len(s.ModelState))
	for id, st := range s.ModelState {
		out[id] = ExposureEntry{
			Exposed: st.Exposed || st.LitellmExposed,
			Ready:   st.Ready || st.Downloaded,
		}
	}
	return out, s.Litellm, nil
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
