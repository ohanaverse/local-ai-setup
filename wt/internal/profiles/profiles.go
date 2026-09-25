package profiles

import (
	"bytes"
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

// MechanismRequirer is implemented by agents whose ProfileMechanisms
// includes a mechanism that only has an effect when a second mechanism is
// also present on the SAME profile entry — composition across match tiers
// does not satisfy it, since Resolve can merge two profiles that never
// both apply to the same launch. pi's env mechanism is the motivating
// case: env lands on the launched process before a wrapper (if any)
// replaces it, and bare pi has no env lever of its own — only a wrapper
// like little-coder reads it. Validate uses this to reject a profile that
// sets env with no wrapper alongside it.
type MechanismRequirer interface {
	// RequiredMechanism reports the mechanism that must also be set on any
	// profile entry using m, or ok=false when m has no such requirement.
	RequiredMechanism(m Mechanism) (required Mechanism, ok bool)
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

// Save writes store to profiles.toml as a whole-file atomic write
// (config.WriteFileAtomic), the same convention config.Save uses for
// config.toml. Used by `wt profile on|off`.
func Save(store Store) error {
	var buf bytes.Buffer
	fs := fileSchema{Enabled: &store.Enabled, Profiles: store.Profiles}
	if err := toml.NewEncoder(&buf).Encode(&fs); err != nil {
		return err
	}
	return config.WriteFileAtomic(Path(), buf.Bytes(), 0o644)
}
