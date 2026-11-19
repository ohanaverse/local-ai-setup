package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ModelServerState is the [model-server] table of config.toml (written by the
// wt config Mode tab or wt-model setup, and still hand-editable):
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
// TableErr is a decode error inside the table itself; RestErr is a decode
// error anywhere else in the file (a wrong-typed agent key), which every
// writer of config.toml (UpdateModelServer included) refuses on.
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
		f.ParseErr = secretSafe(err, data)
		return f
	}
	var only struct {
		ModelServer *ModelServerState `toml:"model-server"`
	}
	if _, err := toml.Decode(string(data), &only); err != nil {
		f.TableErr = secretSafe(err, data)
		return f
	}
	f.Table = only.ModelServer
	// What every writer of the file (UpdateModelServer included) decodes.
	var whole Config
	if _, err := toml.Decode(string(data), &whole); err != nil {
		f.RestErr = secretSafe(err, data)
	}
	return f
}

// secretSafe keeps a TOML error from quoting a secret: when the error sits on
// a line whose key is a token, a key or a url (token, api_key, *_token,
// *_key, url, *_url: a url typed without quotes may hold credentials, and the
// parser quotes the token in front of its '@', #640), or the last key parsed
// is one, the parser's message (which can quote the
// value, say a base64 token typed without quotes) is replaced by the line
// number and the key name. Any other error is returned as it is.
func secretSafe(err error, data []byte) error {
	var pe toml.ParseError
	if !errors.As(err, &pe) {
		return err
	}
	line := pe.Position.Line
	if line == 0 {
		line = pe.Line
	}
	key := ""
	if lines := strings.Split(string(data), "\n"); line >= 1 && line <= len(lines) {
		if k, _, ok := strings.Cut(lines[line-1], "="); ok {
			key = strings.Trim(strings.TrimSpace(k), `"'`)
		}
	}
	for _, k := range []string{key, pe.LastKey} {
		parts := strings.Split(k, ".")
		name := strings.Trim(strings.TrimSpace(parts[len(parts)-1]), `"'`)
		if secretKeyName(name) {
			return fmt.Errorf("toml: line %d: the value of %s does not parse (not shown: it may hold a secret)", line, name)
		}
	}
	return err
}

// secretKeyName reports whether a config key's value is, or may hold, a
// secret. A url counts: one can carry userinfo (#640).
func secretKeyName(k string) bool {
	k = strings.ToLower(k)
	return k == "token" || k == "api_key" || strings.HasSuffix(k, "_token") || strings.HasSuffix(k, "_key") ||
		k == "url" || strings.HasSuffix(k, "_url")
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
