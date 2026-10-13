package profiles

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// SlotDrivers is how ValidateSlots asks about agents without importing
// internal/agents. Known, Slots, ReservedEnv and Mechanisms must be set.
// Required may be nil: no mechanism then needs a partner. ConfigContent may
// be nil too, and that fails closed: a profile that sets config_content is
// refused, since nothing can say whether its agent accepts the content.
type SlotDrivers struct {
	Known       func(agent string) bool
	Slots       func(agent string) []string // nil: the agent has no slot support
	ReservedEnv func(agent string) []string
	Mechanisms  func(agent string) []Mechanism
	Required    func(agent string, m Mechanism) (Mechanism, bool)
	// ConfigContent is the agent's own rule for a profile's config_content
	// (SlotCapable.CheckConfigContent): nil when the agent accepts cc, else
	// the error that blocks the profile. It is asked only about a profile
	// that sets config_content.
	ConfigContent func(agent string, cc map[string]any) error
}

// ErrSlotDuplicate is the static error of every profile that shares its
// name with another profile of the same agent.
var ErrSlotDuplicate = errors.New("duplicate profile name")

// SlotCheck is one profile's static verdict.
type SlotCheck struct {
	Index     int // 0-based position in the file
	Profile   SlotProfile
	Usable    bool     // the name is well formed and the agent is known and slot-capable
	Duplicate bool     // Usable, and another Usable profile has the same Agent and Name
	Declared  []string // the agent's slot names in display order; nil when !Usable
	Err       error    // the first failing static rule; nil when the profile passes
}

// Label names the profile in a message: Profile.ID() for a Usable check,
// else "slot-profiles.toml[<Index>]", since an unusable profile has no id a
// row or a pin could carry.
func (c SlotCheck) Label() string {
	if c.Usable {
		return c.Profile.ID()
	}
	return fmt.Sprintf("slot-profiles.toml[%d]", c.Index)
}

// slotNameRE is a well-formed profile name: lowercase letters, digits and
// hyphens, starting with a letter or digit so `-M profile/<name>` can never
// read as a flag.
var slotNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidateSlots returns one SlotCheck per profile, in file order. It reads
// the file's content and the drivers' declarations only: whether a slot's
// model exists, is routable or is running is resolved validation, done
// where rows are built. One profile's error never changes another's
// verdict, except that profiles sharing a name for one agent are all
// duplicates.
func ValidateSlots(store SlotStore, d SlotDrivers) []SlotCheck {
	type nameKey struct{ agent, name string }
	checks := make([]SlotCheck, len(store.Profiles))
	sharing := map[nameKey]int{}
	for i, p := range store.Profiles {
		c := SlotCheck{Index: i, Profile: p}
		if err := slotIdentityErr(p, d); err != nil {
			c.Err = err
		} else {
			c.Usable = true
			c.Declared = slices.Clone(d.Slots(p.Agent))
			sharing[nameKey{p.Agent, p.Name}]++
		}
		checks[i] = c
	}
	for i := range checks {
		c := &checks[i]
		if !c.Usable {
			continue
		}
		if sharing[nameKey{c.Profile.Agent, c.Profile.Name}] > 1 {
			c.Duplicate = true
			c.Err = ErrSlotDuplicate
			continue
		}
		c.Err = slotBodyErr(c.Profile, c.Declared, d)
	}
	return checks
}

// slotIdentityErr applies rules 1 to 5, the ones that decide whether a
// profile has a usable name and agent at all.
func slotIdentityErr(p SlotProfile, d SlotDrivers) error {
	switch {
	case p.Name == "":
		return errors.New("name is missing")
	case !slotNameRE.MatchString(p.Name):
		return fmt.Errorf("name %q is malformed: use lowercase letters, digits and hyphens", p.Name)
	case p.Agent == "":
		return errors.New("agent is missing")
	case !d.Known(p.Agent):
		return fmt.Errorf("agent %q is unknown", p.Agent)
	case len(d.Slots(p.Agent)) == 0:
		return fmt.Errorf("agent %q does not support slot profiles", p.Agent)
	}
	return nil
}

// slotBodyErr applies rules 7 to 16 to a profile whose name and agent are
// usable, and returns the first that fails. Map keys are walked in sorted
// order, so the same file always reports the same error.
func slotBodyErr(p SlotProfile, declared []string, d SlotDrivers) error {
	if len(p.Slots) == 0 {
		return errors.New("slots is empty: assign at least one slot")
	}
	slotNames := slices.Sorted(maps.Keys(p.Slots))
	for _, slot := range slotNames {
		if !slices.Contains(declared, slot) {
			return fmt.Errorf("slot %q is not a slot of agent %q (slots: %s)", slot, p.Agent, strings.Join(declared, ", "))
		}
	}
	for _, slot := range slotNames {
		if p.Slots[slot] == "" {
			return fmt.Errorf("slot %q names no model", slot)
		}
	}
	if p.Default == "" {
		return errors.New("default is missing")
	}
	if _, ok := p.Slots[p.Default]; !ok {
		return fmt.Errorf("default %q is not a slot this profile fills", p.Default)
	}

	// The same four fields, judged "used" exactly as an automatic profile's are.
	used := usedMechanisms(Profile{Env: p.Env, Args: p.Args, ConfigContent: p.ConfigContent, Wrapper: p.Wrapper})
	allowed := d.Mechanisms(p.Agent)
	for _, mech := range used {
		if !slices.Contains(allowed, mech) {
			return fmt.Errorf("agent %q does not support mechanism %q", p.Agent, mech)
		}
	}
	if d.Required != nil {
		for _, mech := range used {
			if req, ok := d.Required(p.Agent, mech); ok && !slices.Contains(used, req) {
				return fmt.Errorf("mechanism %q has no effect without %q on the same profile", mech, req)
			}
		}
	}

	reserved := d.ReservedEnv(p.Agent)
	for _, key := range slices.Sorted(maps.Keys(p.Env)) {
		if slices.Contains(reserved, key) {
			return fmt.Errorf("env sets %s, which wt sets for the launch", key)
		}
	}
	// What config_content may hold is the agent's rule, not this package's:
	// claude's, for one, is that an env key is a table.
	if len(p.ConfigContent) > 0 {
		if d.ConfigContent == nil {
			return fmt.Errorf("config_content cannot be checked for agent %q", p.Agent)
		}
		if err := d.ConfigContent(p.Agent, p.ConfigContent); err != nil {
			return err
		}
	}
	if env, ok := p.ConfigContent["env"].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(env)) {
			if slices.Contains(reserved, key) {
				return fmt.Errorf("config_content.env sets %s, which wt sets for the launch", key)
			}
		}
	}

	if p.Wrapper != nil && !slices.Contains(p.Wrapper.ArgsTemplate, "{{args}}") {
		return fmt.Errorf("wrapper args_template %v has no \"{{args}}\" placeholder", p.Wrapper.ArgsTemplate)
	}
	return nil
}
