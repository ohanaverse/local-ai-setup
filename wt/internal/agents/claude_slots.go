package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// claudeSlots are Claude Code's model slots, in display order.
var claudeSlots = []string{"fable", "opus", "sonnet", "haiku"}

// claudeSlotModelKey is the variable Claude Code reads for one slot's model:
// "ANTHROPIC_DEFAULT_" + upper(slot) + "_MODEL". The same name with "_NAME"
// appended is the label Claude Code's own /model picker shows for it.
func claudeSlotModelKey(slot string) string {
	return "ANTHROPIC_DEFAULT_" + strings.ToUpper(slot) + "_MODEL"
}

// SlotNames returns a copy, so a caller cannot reorder the driver's list.
func (claudeDriver) SlotNames() []string { return slices.Clone(claudeSlots) }

// ReservedEnvKeys are the variables wt sets for a slot-profile launch. A
// profile may not set them, in env or under config_content.env.
func (claudeDriver) ReservedEnvKeys() []string {
	keys := []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}
	for _, slot := range claudeSlots {
		keys = append(keys, claudeSlotModelKey(slot), claudeSlotModelKey(slot)+"_NAME")
	}
	return keys
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
	if raw, ok := in.ConfigContent["env"]; ok {
		table, ok := raw.(map[string]any)
		if !ok {
			return "", errors.New("config_content.env must be a table")
		}
		for k, v := range table {
			env[k] = v
		}
	}
	for _, slot := range claudeSlots {
		m, ok := in.Models[slot]
		if !ok {
			return "", fmt.Errorf("slot %s has no model", slot)
		}
		env[claudeSlotModelKey(slot)] = m.ID
		env[claudeSlotModelKey(slot)+"_NAME"] = m.ModelName
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
		return "", fmt.Errorf("encode --settings: %w", err)
	}
	return string(payload), nil
}
