package config

import (
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ModelServerState is the hand-edited [model-server] table of config.toml:
// where the wt-model server is and which file holds its bearer token.
type ModelServerState struct {
	// Mode is native, local or remote (empty is unset: a launch refuses, or on a
	// terminal opens the first-run gate; native is only the explicit value).
	Mode      string `toml:"mode,omitempty"`
	URL       string `toml:"url,omitempty"`
	TokenFile string `toml:"token_file,omitempty"`
	// Token is never used for auth, but its presence is not inert: a save
	// writes config.toml 0600 when it is set, and no save loosens an
	// existing stricter mode. A value here is also reported as a problem so
	// the user moves it into a 0600 token_file.
	Token string `toml:"token,omitempty"`
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
	switch strings.ToLower(strings.TrimSpace(s.Mode)) {
	case "", "native", "local", "remote":
	default:
		out = append(out, "[model-server] mode "+strconv.Quote(s.Mode)+" is not native, local or remote")
	}
	if s.Token != "" {
		out = append(out, "[model-server] token is not used for auth — while it is set wt writes config.toml 0600, and no save loosens an existing stricter mode — put the token in a file with mode 0600 and set token_file")
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
	// The proxy pair is host state a session never reads (the spec's
	// "No session-behavior change" anti-goal): no wt-model home is opened
	// here, so a broken home or key file can never block a launch. The
	// legacy table is copied as it always was, inert.
	if cfg.LitellmTable != nil {
		cfg.litellm = *cfg.LitellmTable
	}
	return cfg, nil
}

// ModelServerFile is a tolerant read of config.toml's [model-server] table.
// Path is always set; ParseErr is a syntax error (nothing else is read);
// TableErr is a decode error inside the table itself; RestErr one elsewhere
// in the file, which never hides the table but fails a write (the writer
// re-encodes the whole file).
type ModelServerFile struct {
	Path     string
	Exists   bool
	Table    *ModelServerState
	ParseErr error
	TableErr error
	RestErr  error
}

// ReadModelServerFile never errors. Syntax is checked by decoding into a
// generic map; the table is decoded alone, so a problem elsewhere in the
// file (a wrong-typed agent key) never hides it.
func ReadModelServerFile() ModelServerFile {
	f := ModelServerFile{Path: Path()}
	data, err := os.ReadFile(f.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			f.ParseErr = err
			f.Exists = true
		}
		return f
	}
	f.Exists = true
	var generic map[string]any
	if _, err := toml.Decode(string(data), &generic); err != nil {
		f.ParseErr = err
		return f
	}
	var only struct {
		ModelServer *ModelServerState `toml:"model-server"`
	}
	if _, err := toml.Decode(string(data), &only); err != nil {
		f.TableErr = err
		return f
	}
	f.Table = only.ModelServer
	if _, err := toml.Decode(string(data), &Config{DefaultTag: "code"}); err != nil {
		f.RestErr = err
	}
	return f
}

// UpdateModelServer rewrites the [model-server] table under the config lock.
// Any decode error (syntax, or a wrong-typed key elsewhere) is returned and
// nothing is written, because the writer re-encodes the whole struct and
// would drop what it could not decode.
func UpdateModelServer(change func(cur ModelServerState) (ModelServerState, error)) error {
	return WithLock(func() error {
		cfg, _, err := readConfigFile()
		if err != nil {
			return err
		}
		next, err := change(cfg.ModelServer())
		if err != nil {
			return err
		}
		if next == (ModelServerState{}) {
			cfg.ModelServerTable = nil
		} else {
			cfg.ModelServerTable = &next
		}
		return writeConfigFile(cfg)
	})
}
