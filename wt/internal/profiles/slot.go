package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// SlotIDPrefix prefixes a slot profile's row id: "profile/<name>".
const SlotIDPrefix = "profile/"

// SlotReservedProvider is the registry provider id whose model ids would
// collide with SlotIDPrefix.
const SlotReservedProvider = "profile"

// SlotProfile is one [[profile]] entry of slot-profiles.toml: a named,
// hand-written assignment of registry model ids to an agent's model slots.
// It is a different thing from Profile, an automatic profiles.toml entry
// matched to a launch; the two share only the four mechanism fields.
type SlotProfile struct {
	Name          string            `toml:"name"`
	Agent         string            `toml:"agent"`
	Default       string            `toml:"default"`
	Slots         map[string]string `toml:"slots"`
	Env           map[string]string `toml:"env,omitempty"`
	Args          []string          `toml:"args,omitempty"`
	ConfigContent map[string]any    `toml:"config_content,omitempty"`
	Wrapper       *WrapperSpec      `toml:"wrapper,omitempty"`
}

// ID is the profile's row id, SlotIDPrefix + Name.
func (p SlotProfile) ID() string { return SlotIDPrefix + p.Name }

// FilledOrder lists the slots p fills that are in declared: Default first
// (when it is filled and declared), then the rest in declared's order.
// A slot p fills that the agent does not declare is left out.
func (p SlotProfile) FilledOrder(declared []string) []string {
	filled := func(slot string) bool {
		_, ok := p.Slots[slot]
		return ok
	}
	var out []string
	if filled(p.Default) && slices.Contains(declared, p.Default) {
		out = append(out, p.Default)
	}
	for _, slot := range declared {
		if slot != p.Default && filled(slot) {
			out = append(out, slot)
		}
	}
	return out
}

// slotFileSchema is the on-disk shape.
type slotFileSchema struct {
	Profiles []SlotProfile `toml:"profile"`
}

// SlotStore is slot-profiles.toml as parsed, in file order.
type SlotStore struct {
	Profiles []SlotProfile
}

// SlotPath returns the slot-profiles.toml location, beside profiles.toml
// in config.Dir(). wt reads the file and never writes it.
func SlotPath() string { return filepath.Join(config.Dir(), "slot-profiles.toml") }

// ParseSlots decodes the file's bytes. It checks nothing but the TOML and
// the field types; ValidateSlots judges each profile. A key the schema
// does not name is ignored, as Load ignores one in profiles.toml.
func ParseSlots(data []byte) (SlotStore, error) {
	var fs slotFileSchema
	if _, err := toml.Decode(string(data), &fs); err != nil {
		return SlotStore{}, fmt.Errorf("parse slot-profiles.toml: %w", err)
	}
	return SlotStore{Profiles: fs.Profiles}, nil
}

// LoadSlots reads SlotPath(). A missing file is not an error: it is an
// empty store, so a machine with no slot profiles shows no profile rows.
func LoadSlots() (SlotStore, error) {
	data, err := os.ReadFile(SlotPath())
	if os.IsNotExist(err) {
		return SlotStore{}, nil
	}
	if err != nil {
		return SlotStore{}, err
	}
	return ParseSlots(data)
}

// SlotLaunch is a resolved slot profile, ready for an agent's slot build.
type SlotLaunch struct {
	Agent         string
	Models        map[string]config.Model // every slot the agent declares → its model; an unfilled slot holds the default slot's model
	Default       string                  // the default slot's name
	Env           map[string]string       // the profile's env
	ConfigContent map[string]any          // the profile's config_content
}

// SlotBuildInput is what a driver's slot build receives.
type SlotBuildInput struct {
	SlotLaunch
	ProxyOrigin string // the [litellm] url, no trailing slash
	ProxyKey    string // the [litellm] api key
	Yolo        bool
}

// SlotCommand is what a driver's slot build returns: a command before the
// binary is looked up and the environment is assembled.
type SlotCommand struct {
	Bin              string
	Args             []string
	Env              []string // "KEY=VALUE", appended after the filtered inherited environment
	ClearEnv         []string // exact names removed from the inherited environment
	ClearEnvPrefixes []string // names with one of these prefixes are removed from the inherited environment
}

// SlotCapable is implemented by agents.Driver values that support slot
// profiles. It is declared here, beside ProfileCapable, so this package
// never imports internal/agents. An agent whose driver does not implement
// it cannot have slot profiles.
type SlotCapable interface {
	// SlotNames are the slots the agent accepts, in display order.
	SlotNames() []string
	// ReservedEnvKeys are the env keys wt owns for a slot-profile launch.
	ReservedEnvKeys() []string
	// BuildSlots builds the command for one resolved profile.
	BuildSlots(in SlotBuildInput) (SlotCommand, error)
}

// MergeConfigContent merges src into dst by the rule automatic profiles
// use (mergeInto): tables merge key by key, recursively; a scalar or an
// array replaces the value whole. src is not changed. A table that dst
// lacks is stored in dst as src's own map, not a copy, so a caller that
// goes on to write into dst's tables copies first.
func MergeConfigContent(dst, src map[string]any) { mergeInto(dst, src) }
