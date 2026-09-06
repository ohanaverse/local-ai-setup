package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
type modelmanState struct {
	ModelState map[string]struct {
		LitellmExposed bool `toml:"litellm_exposed"`
		Ready          bool `toml:"ready"`
	} `toml:"model_state"`
}

// loadModelmanState reads modelman.toml and returns a map of exposed model ids
// with their ready state. A missing file returns an empty map (every non-native
// model is unexposed).
func loadModelmanState() (map[string]struct {
	LitellmExposed bool
	Ready          bool
}, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]struct {
			LitellmExposed bool
			Ready          bool
		}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]struct {
		LitellmExposed bool
		Ready          bool
	}, len(s.ModelState))
	for id, st := range s.ModelState {
		out[id] = struct {
			LitellmExposed bool
			Ready          bool
		}{LitellmExposed: st.LitellmExposed, Ready: st.Ready}
	}
	return out, nil
}
