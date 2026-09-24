package profiles

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Mechanism names one way a profile can change a launch. Drivers declare
// which ones they accept via ProfileCapable; Validate rejects a profile
// using one its agent does not declare.
type Mechanism string

const (
	MechanismEnv        Mechanism = "env"
	MechanismArgs       Mechanism = "args"
	MechanismConfigFile Mechanism = "config_file"
	MechanismWrapper    Mechanism = "wrapper"
)

// ProfileCapable is implemented by agents.Driver values that accept
// profile overlays. Defined here — not in internal/agents — so this
// package never imports internal/agents; the dependency runs the other
// way (a driver imports profiles for the Mechanism type).
type ProfileCapable interface {
	ProfileMechanisms() []Mechanism
}

// WrapperSpec replaces the launched binary with Binary, substituting the
// original argv into ArgsTemplate wherever the literal element "{{args}}"
// appears (see apply.go).
type WrapperSpec struct {
	Binary       string   `toml:"binary"`
	ArgsTemplate []string `toml:"args_template"`
}

// Profile is one entry in profiles.toml. Match selects which of
// Location/Provider/Model is the tier condition; the other two are
// ignored. See the design spec §1 for the on-disk shape.
type Profile struct {
	Agent         string            `toml:"agent"`
	Match         string            `toml:"match"` // "location" | "provider" | "model"
	Location      string            `toml:"location,omitempty"`
	Provider      string            `toml:"provider,omitempty"`
	Model         string            `toml:"model,omitempty"`
	Env           map[string]string `toml:"env,omitempty"`
	Args          []string          `toml:"args,omitempty"`
	ConfigContent map[string]any    `toml:"config_content,omitempty"`
	Wrapper       *WrapperSpec      `toml:"wrapper,omitempty"`
}

// fileSchema is the raw on-disk shape; Enabled is a pointer so an absent
// key defaults to true (profiles on) while an explicit `enabled = false`
// is distinguishable from "not set".
type fileSchema struct {
	Enabled  *bool     `toml:"enabled"`
	Profiles []Profile `toml:"profiles"`
}

// Store is the in-memory result of Load.
type Store struct {
	Enabled  bool
	Profiles []Profile
}

// Path returns the profiles.toml location, alongside config.Dir()'s
// config.toml.
func Path() string { return filepath.Join(config.Dir(), "profiles.toml") }

// Load reads profiles.toml. A missing file is not an error: it returns
// Store{Enabled: true} (profiles on, none defined yet), matching
// config.Load's "empty Config if config.toml does not exist" convention.
func Load() (Store, error) {
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return Store{Enabled: true}, nil
	}
	if err != nil {
		return Store{}, err
	}
	var fs fileSchema
	if _, err := toml.Decode(string(data), &fs); err != nil {
		return Store{}, fmt.Errorf("parse profiles.toml: %w", err)
	}
	enabled := true
	if fs.Enabled != nil {
		enabled = *fs.Enabled
	}
	return Store{Enabled: enabled, Profiles: fs.Profiles}, nil
}
