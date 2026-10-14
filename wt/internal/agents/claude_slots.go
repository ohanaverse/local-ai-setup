package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// claudeSlots are Claude Code's model slots, in display order.
var claudeSlots = []string{"fable", "opus", "sonnet", "haiku"}

// claudeSlotVar is one variable Claude Code reads for a model slot.
type claudeSlotVar struct{ Key, Value string }

// claudeSlotVars returns the two variables that map one slot to a model:
// "ANTHROPIC_DEFAULT_" + upper(slot) + "_MODEL", the reference Claude Code
// sends for the slot, and the same name with "_NAME" appended, the label its
// own /model picker shows, which is the model's provider-side name. ref is
// what the endpoint routes: a registry id through the proxy, the provider's
// own name on a direct route. The slot-profile build and the single-model
// build both take the names and the label rule from here.
func claudeSlotVars(slot, ref string, m config.Model) [2]claudeSlotVar {
	key := "ANTHROPIC_DEFAULT_" + strings.ToUpper(slot) + "_MODEL"
	return [2]claudeSlotVar{{key, ref}, {key + "_NAME", m.ModelName}}
}

// claudeSlotKeys lists the two variable names of every slot, in slot order.
func claudeSlotKeys() []string {
	keys := make([]string, 0, 2*len(claudeSlots))
	for _, slot := range claudeSlots {
		for _, v := range claudeSlotVars(slot, "", config.Model{}) {
			keys = append(keys, v.Key)
		}
	}
	return keys
}

// claudeSingleModelSlotEnv maps every slot to the one model of a single-model
// launch, as "KEY=VALUE" entries. Claude Code uses its tiers unasked
// (background requests on haiku, built-in subagents defined with a tier
// alias), and an unmapped tier resolves to an Anthropic id the endpoint
// rejects.
func claudeSingleModelSlotEnv(ref string, m config.Model) []string {
	env := make([]string, 0, 2*len(claudeSlots))
	for _, slot := range claudeSlots {
		for _, v := range claudeSlotVars(slot, ref, m) {
			env = append(env, v.Key+"="+v.Value)
		}
	}
	return env
}

// SlotNames returns a copy, so a caller cannot reorder the driver's list.
func (claudeDriver) SlotNames() []string { return slices.Clone(claudeSlots) }

// ReservedEnvKeys are the variables wt sets for a slot-profile launch. A
// profile may not set them, in env or under config_content.env.
func (claudeDriver) ReservedEnvKeys() []string {
	return append([]string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}, claudeSlotKeys()...)
}

// CheckConfigContent is claude's static rule for a profile's config_content:
// an env key, when present, is a table, and the content can be encoded as
// the --settings JSON. They are claudeConfigEnv's and claudeEncodable's
// rules, the ones the slot build applies, so validation and the build cannot
// disagree.
func (claudeDriver) CheckConfigContent(cc map[string]any) error {
	if _, err := claudeConfigEnv(cc); err != nil {
		return err
	}
	return claudeEncodable(cc)
}

// claudeEncodable refuses a config_content the --settings JSON cannot hold.
// A TOML nan or inf decodes to a float json.Marshal refuses, and only the
// build's encode would find that out — at the launch, after the routes were
// written. json.Marshal is the one judge of it: static validation asks it
// here, and the build's encode re-wraps its own failure with the same
// wording (encodeContentErr) as a second line of defence.
func claudeEncodable(cc map[string]any) error {
	if _, err := json.Marshal(cc); err != nil {
		return encodeContentErr(err)
	}
	return nil
}

// encodeContentErr words one failure of encoding a config_content into the
// --settings JSON. One wording, for static validation (claudeEncodable) and
// for the slot build (claudeSlotSettings), so the two cannot disagree.
func encodeContentErr(err error) error {
	return fmt.Errorf("config_content cannot be encoded as the --settings JSON: %w", err)
}

// claudeConfigEnv returns the env table of a profile's config_content, nil
// when it has none. A config_content.env that is not a table is an error:
// Claude Code's settings take env as an object, and wt merges its own
// variables into it. It is the one statement of that rule, for static
// validation (CheckConfigContent) and for the slot build (claudeSlotSettings).
func claudeConfigEnv(cc map[string]any) (map[string]any, error) {
	raw, ok := cc["env"]
	if !ok {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("config_content.env must be a table")
	}
	return table, nil
}

// RefusedSlotPassthrough names the flags that, passed after `--`, would
// replace the slot mapping: --settings carries it and --model picks the slot.
func (claudeDriver) RefusedSlotPassthrough() []string { return []string{"--settings", "--model"} }

// BuildSlots builds the command of a slot-profile launch. The slot mapping
// travels in one inline --settings JSON, so wt writes no settings file. The
// proxy key is set in the process environment only: argv is visible in the
// process list, and the key must not be.
func (claudeDriver) BuildSlots(in profiles.SlotBuildInput) (profiles.SlotCommand, error) {
	if !slices.Contains(claudeSlots, in.Default) {
		return profiles.SlotCommand{}, fmt.Errorf("default slot %q is not a claude slot", in.Default)
	}
	payload, err := claudeSlotSettings(in)
	if err != nil {
		return profiles.SlotCommand{}, err
	}
	var args []string
	if in.Yolo {
		args = append(args, claudeDriver{}.YoloFlag())
	}
	args = append(args, "--model", in.Default, "--settings", payload)
	return profiles.SlotCommand{
		Bin:  "claude",
		Args: args,
		Env: []string{
			"ANTHROPIC_BASE_URL=" + in.ProxyOrigin,
			"ANTHROPIC_AUTH_TOKEN=" + in.ProxyKey,
			"ANTHROPIC_API_KEY=",
		},
		// A stray shell export of any of these could send a tier to an id
		// the profile never chose.
		ClearEnv:         []string{"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"},
		ClearEnvPrefixes: []string{"ANTHROPIC_DEFAULT_"},
	}, nil
}

// claudeSlotSettings returns the --settings payload: the profile's
// config_content, with an env block whose precedence, low to high, is the
// profile's env, config_content.env, then wt's own variables. It never holds
// ANTHROPIC_AUTH_TOKEN, and it mutates neither in.Env nor in.ConfigContent.
func claudeSlotSettings(in profiles.SlotBuildInput) (string, error) {
	env := map[string]any{}
	for k, v := range in.Env {
		env[k] = v
	}
	// Static validation already refused a config_content.env that is not a
	// table (CheckConfigContent); the build asks again, for a caller that
	// reached it another way.
	table, err := claudeConfigEnv(in.ConfigContent)
	if err != nil {
		return "", err
	}
	for k, v := range table {
		env[k] = v
	}
	for _, slot := range claudeSlots {
		m, ok := in.Models[slot]
		if !ok {
			return "", fmt.Errorf("slot %s has no model", slot)
		}
		for _, v := range claudeSlotVars(slot, m.ID, m) {
			env[v.Key] = v.Value
		}
	}
	// Repeated here so an env block in the user's own settings files cannot
	// redirect the session.
	env["ANTHROPIC_BASE_URL"] = in.ProxyOrigin
	env["ANTHROPIC_API_KEY"] = ""

	obj := map[string]any{}
	profiles.MergeConfigContent(obj, in.ConfigContent)
	// obj["env"] is replaced, not written into: after the merge it is the
	// profile's own config_content.env table.
	obj["env"] = env
	payload, err := json.Marshal(obj)
	if err != nil {
		// Static validation already refused a config_content that cannot be
		// encoded (claudeEncodable); the build asks again, for a caller that
		// reached it another way.
		return "", encodeContentErr(err)
	}
	return string(payload), nil
}
