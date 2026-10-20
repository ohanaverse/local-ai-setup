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

// filledOrderFrom lists the slots p fills that are in declared: first
// (when it is filled and declared) first, then the rest in declared's order.
// A slot p fills that the agent does not declare is left out.
func (p SlotProfile) filledOrderFrom(declared []string, first string) []string {
	filled := func(slot string) bool {
		_, ok := p.Slots[slot]
		return ok
	}
	var out []string
	if filled(first) && slices.Contains(declared, first) {
		out = append(out, first)
	}
	for _, slot := range declared {
		if slot != first && filled(slot) {
			out = append(out, slot)
		}
	}
	return out
}

// SlotAlias is a default that names one of the agent's own modes rather
// than a slot: Claude Code's opusplan, which plans on the opus slot and does
// everything else on the sonnet slot. The profile must fill every slot in
// Requires. Rest is the slot the session starts and works on: the one the
// row is priced as, listed first and marked. Doc says what the alias does,
// in a few words, for `wt profile show`.
type SlotAlias struct {
	Requires []string
	Rest     string
	Doc      string
}

// slotFileSchema is the on-disk shape.
type slotFileSchema struct {
	Profiles []SlotProfile `toml:"profile"`
}

// SlotStore is slot-profiles.toml as parsed, in file order.
type SlotStore struct {
	Profiles []SlotProfile
	// Unknown lists the keys of the file the schema does not name, as dotted
	// paths in file order, each once: "profiles" for a [[profiles]] array,
	// "profile.envs" for a misspelt table of a profile. Such a key is not an
	// error, and what it holds is not read; wt warns about each.
	Unknown []string
}

// SlotPath returns the slot-profiles.toml location, beside profiles.toml
// in config.Dir(). wt reads the file and never writes it.
func SlotPath() string { return filepath.Join(config.Dir(), "slot-profiles.toml") }

// ParseSlots decodes the file's bytes. It checks nothing but the TOML and
// the field types; ValidateSlots judges each profile. A value of the wrong
// type anywhere fails the whole file. A key the schema does not name fails
// nothing: it is listed in the store's Unknown (unknownSlotKeys).
func ParseSlots(data []byte) (SlotStore, error) {
	var fs slotFileSchema
	md, err := toml.Decode(string(data), &fs)
	if err != nil {
		return SlotStore{}, fmt.Errorf("parse slot-profiles.toml: %w", err)
	}
	return SlotStore{Profiles: fs.Profiles, Unknown: unknownSlotKeys(md.Undecoded())}, nil
}

// unknownSlotKeys turns the decoder's undecoded keys into SlotStore.Unknown.
// The decoder names an unknown table and then every key under it, names a
// key once per [[profile]] entry that has it, and names the nested tables of
// config_content although that table is free-form and was decoded whole. So
// a key under config_content is dropped, as is a key under one already
// listed and a key listed before.
func unknownSlotKeys(undecoded []toml.Key) []string {
	freeForm := toml.Key{"profile", "config_content"}
	under := func(k, parent toml.Key) bool {
		return len(k) > len(parent) && slices.Equal(k[:len(parent)], parent)
	}
	var kept []toml.Key
	var out []string
next:
	for _, k := range undecoded {
		if under(k, freeForm) {
			continue
		}
		for _, have := range kept {
			if slices.Equal(k, have) || under(k, have) {
				continue next
			}
		}
		kept = append(kept, k)
		out = append(out, k.String())
	}
	return out
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
	// CheckConfigContent is the agent's own static rule for a profile's
	// config_content: nil when the slot build would accept cc, else the
	// error it would return. Static validation asks it, so a profile the
	// build would refuse is a blocked row and never a launch row.
	CheckConfigContent(cc map[string]any) error
	// DefaultAlias reports whether name, as a profile's default, is one of
	// the agent's aliases over its slots (claude's "opusplan"), and what it
	// needs. A default that is neither a slot nor an alias is refused.
	DefaultAlias(name string) (SlotAlias, bool)
	// BuildSlots builds the command for one resolved profile.
	BuildSlots(in SlotBuildInput) (SlotCommand, error)
}

// MergeConfigContent merges src into dst by the rule automatic profiles
// use (mergeInto): tables merge key by key, recursively; a scalar or an
// array replaces the value whole. src is not changed. A table that dst
// lacks is stored in dst as src's own map, not a copy, so a caller that
// goes on to write into dst's tables copies first.
func MergeConfigContent(dst, src map[string]any) { mergeInto(dst, src) }
