package config

import (
	"slices"
)

// ModelServerState is the hand-edited [model-server] table of config.toml:
// where the wt-model server is and which file holds its bearer token.
type ModelServerState struct {
	URL       string `toml:"url"`
	TokenFile string `toml:"token_file"`
	// Token is never read: config.toml is not private. A value here is only
	// reported as a problem so the user moves it into a 0600 file.
	Token string `toml:"token"`
}

// ModelServer returns the [model-server] table, zero when it is absent.
func (c *Config) ModelServer() ModelServerState {
	if c == nil || c.ModelServerTable == nil {
		return ModelServerState{}
	}
	return *c.ModelServerTable
}

// Problems lists what is wrong with the table beyond a missing value.
func (s ModelServerState) Problems() []string {
	var out []string
	if s.Token != "" {
		out = append(out, "[model-server] token is not read from config.toml, which is not private — put the token in a file with mode 0600 and set token_file")
	}
	return out
}

// IgnoredLitellmKeys names the [litellm] keys wt tolerates but no longer
// reads: routing is the model server's job, so enabled = true does nothing.
func (c *Config) IgnoredLitellmKeys() []string {
	if c != nil && c.LitellmTable != nil && c.LitellmTable.Enabled {
		return []string{"[litellm] enabled"}
	}
	return nil
}

// LoadSession reads config.toml only, for the session client: Agents,
// DefaultTag, [launch], [litellm] (inert) and [model-server]. It needs no
// registry and does not validate supported_providers. The agent reference
// migration runs in memory (the file is untouched), and each name in
// driverNames with no agent entry gets an in-memory agent that accepts every
// provider (AllProviders); an explicit entry is kept as written. Nothing
// synthesized is ever written: saves re-read the file (PatchSave).
func LoadSession(driverNames ...string) (*Config, error) {
	cfg, _, err := readConfigFile()
	if err != nil {
		return nil, err
	}
	migrateAgentRefs(cfg)
	for _, name := range driverNames {
		if slices.ContainsFunc(cfg.Agents, func(a Agent) bool { return a.Name == name }) {
			continue
		}
		cfg.Agents = append(cfg.Agents, Agent{Name: name, AllProviders: true})
	}
	applyProxyState(cfg)
	return cfg, nil
}
