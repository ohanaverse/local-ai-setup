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
	} `toml:"model_state"`
}

// loadModelmanState reads modelman.toml and returns a set of exposed model ids.
// A missing file returns an empty set (every non-native model is unexposed).
func loadModelmanState() (map[string]bool, error) {
	path := ModelmanPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]bool, len(s.ModelState))
	for id, st := range s.ModelState {
		if st.LitellmExposed {
			out[id] = true
		}
	}
	return out, nil
}
