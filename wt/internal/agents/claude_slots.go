package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
// an env key and a modelOverrides key, when present, are tables, every
// modelOverrides target is a string, and the content can be encoded as the
// --settings JSON. They are claudeConfigEnv's, claudeModelOverrides' and
// claudeEncodable's rules, the ones the slot build applies, so validation and
// the build cannot disagree.
func (claudeDriver) CheckConfigContent(cc map[string]any) error {
	if _, err := claudeConfigEnv(cc); err != nil {
		return err
	}
	if _, err := claudeModelOverrides(cc); err != nil {
		return err
	}
	return claudeEncodable(cc)
}

// claudeModelOverrides returns the modelOverrides table of a profile's
// config_content as key to target, nil when it has none. Claude Code's modelOverrides setting
// maps model ids to model ids (#412), so one that is not a table is an error,
// and so is a target that is not a string (#418): the first such key in key
// order is named, its value never echoed. Its keys are Claude Code's ids,
// which wt cannot know, and are not judged; an empty-string target is a
// string, and ConfigContentWarnings warns about it.
func claudeModelOverrides(cc map[string]any) (map[string]string, error) {
	raw, ok := cc["modelOverrides"]
	if !ok {
		return nil, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("config_content.modelOverrides must be a table")
	}
	targets := make(map[string]string, len(table))
	for _, key := range slices.Sorted(maps.Keys(table)) {
		target, ok := table[key].(string)
		if !ok {
			return nil, fmt.Errorf("config_content.modelOverrides[%q] must be a model id string", key)
		}
		targets[key] = target
	}
	return targets, nil
}

// ConfigContentWarnings is what claude says, without blocking, about a
// profile's config_content.modelOverrides (#412): one line per target that
// is not one of slotModels, in key order. wt makes sure only the slot models
// are routed at a launch; an override may still name an id the user routes
// another way, so it is a warning. A modelOverrides that is not a table, or
// that has a target that is not a string, says nothing here:
// CheckConfigContent blocks it (claudeModelOverrides, #418).
func (claudeDriver) ConfigContentWarnings(cc map[string]any, slotModels []string) []string {
	targets, err := claudeModelOverrides(cc)
	if err != nil {
		return nil
	}
	var out []string
	for _, key := range slices.Sorted(maps.Keys(targets)) {
		if target := targets[key]; !slices.Contains(slotModels, target) {
			out = append(out, fmt.Sprintf("config_content.modelOverrides[%q] targets %q, which is not one of the profile's slot models, so wt does not make sure it is routed", key, target))
		}
	}
	return out
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

// claudeOpusplan is Claude Code's opusplan model alias: the opus slot's
// model in plan mode, the sonnet slot's model otherwise. Claude Code reads
// ANTHROPIC_DEFAULT_OPUS_MODEL and ANTHROPIC_DEFAULT_SONNET_MODEL for it
// (checked live with Claude Code 2.1.299 on 2026-10-14, #391), so a profile
// may name it as its default only when it fills both slots.
const claudeOpusplan = "opusplan"

// DefaultAlias reports claude's one alias for a profile's default, opusplan.
func (claudeDriver) DefaultAlias(name string) (profiles.SlotAlias, bool) {
	if name != claudeOpusplan {
		return profiles.SlotAlias{}, false
	}
	return profiles.SlotAlias{
		Requires: []string{"opus", "sonnet"},
		Rest:     "sonnet",
		Doc:      "the opus slot in plan mode, the sonnet slot otherwise",
	}, true
}

// RefusedSlotPassthrough names the flags that, passed after `--`, would
// replace the slot mapping: --settings carries it and --model picks the slot.
func (claudeDriver) RefusedSlotPassthrough() []string { return []string{"--settings", "--model"} }

// claudeHeaderModeClear are the inherited variables a header-mode launch
// removes (#597): each would replace the stored claude.ai login that was
// checked with another credential. wt sets ANTHROPIC_API_KEY empty itself;
// clearing the inherited one leaves that entry the only one.
var claudeHeaderModeClear = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}

// claudeHeaderModeClearPrefixes are the inherited variable families a
// header-mode launch removes: CLAUDE_CODE_USE_* (Bedrock, Vertex, Foundry and
// any later one) would send the session, and the token header with it, to a
// cloud provider instead of the gateway.
func claudeHeaderModeClearPrefixes() []string { return []string{"CLAUDE_CODE_USE_"} }

// claudeHeaderModeRefused are the keys a header-mode profile may not set in
// env or config_content.env, with why. A slot profile's land in the
// --settings env and a launch profile's config_content in a settings file,
// both of which outrank the process env; a launch profile's env is appended
// after wt's own and wins. ANTHROPIC_CUSTOM_HEADERS would replace the merged
// header (an opaque 401), the tokens and the key would displace the checked
// login, the base url would redirect the session. ANTHROPIC_BASE_URL,
// ANTHROPIC_AUTH_TOKEN and ANTHROPIC_API_KEY are reserved for slot profiles
// anyway (static validation refuses them); they are asked again for a caller
// that reached the build another way.
var claudeHeaderModeRefused = []struct{ key, why string }{
	{profiles.CustomHeadersVar, "it would replace the header wt sends the model-server token in"},
	{"ANTHROPIC_BASE_URL", "wt sets it to the model server's gateway"},
	{"ANTHROPIC_API_KEY", "it would displace your Claude login"},
	{"ANTHROPIC_AUTH_TOKEN", "it would displace your Claude login"},
	{"CLAUDE_CODE_OAUTH_TOKEN", "it would displace your Claude login"},
}

// claudeHeaderModePrefixRefused are the refused variable families:
// CLAUDE_CODE_USE_* (Bedrock, Vertex, Foundry and any later one) would send
// the session, and the token header with it, to a cloud provider.
var claudeHeaderModePrefixRefused = []struct{ prefix, why string }{
	{"CLAUDE_CODE_USE_", "it would send the session to another provider instead of the gateway"},
}

// claudeKeyHelper is Claude Code's settings key naming a command that prints
// an API key: in header mode it would displace the checked login.
const claudeKeyHelper = "apiKeyHelper"

// headerModeKeyWhy is why a header-mode profile may not set name, "" when it
// may.
func headerModeKeyWhy(name string) string {
	for _, r := range claudeHeaderModeRefused {
		if name == r.key {
			return r.why
		}
	}
	for _, r := range claudeHeaderModePrefixRefused {
		if strings.HasPrefix(name, r.prefix) {
			return r.why
		}
	}
	return ""
}

// HeaderModeRefusedKey reports whether a profile key (an env name, a
// config_content env name or a top-level config_content key) is one a claude
// header-mode launch refuses in a profile (HeaderModeProfileConflict's keys).
func HeaderModeRefusedKey(name string) bool {
	return name == claudeKeyHelper || headerModeKeyWhy(name) != ""
}

// HeaderModeProfileConflict refuses a profile that would undo a claude
// header-mode launch (#597, decision 7): an env, config_content.env or
// top-level config_content key that a header-mode launch sets or clears
// itself (headerModeKeyWhy), or a top-level apiKeyHelper. The error names
// where and the key, never a value; keys are judged in sorted order so the
// answer is stable. A config_content.env that is not a table is left to the
// callers that read it. Both the slot build (BuildSlots) and the
// launch-profile appliers (cmd/wt-session) ask it.
func HeaderModeProfileConflict(env map[string]string, configContent map[string]any) error {
	refuse := func(where, key, why string) error {
		return fmt.Errorf("%s sets %s, which a profile using your Claude subscription may not set: %s", where, key, why)
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if why := headerModeKeyWhy(k); why != "" {
			return refuse("env", k, why)
		}
	}
	if table, err := claudeConfigEnv(configContent); err == nil {
		for _, k := range slices.Sorted(maps.Keys(table)) {
			if why := headerModeKeyWhy(k); why != "" {
				return refuse("config_content.env", k, why)
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(configContent)) {
		if k == claudeKeyHelper {
			return refuse("config_content", k, "it would displace your Claude login")
		}
		if why := headerModeKeyWhy(k); why != "" {
			return refuse("config_content", k, why)
		}
	}
	return nil
}

// claudeHeaderModeErr refuses a header-mode slot profile whose env or
// config_content would undo header mode (HeaderModeProfileConflict).
func claudeHeaderModeErr(in profiles.SlotBuildInput) error {
	return HeaderModeProfileConflict(in.Env, in.ConfigContent)
}

// BuildSlots builds the command of a slot-profile launch. The slot mapping
// travels in one inline --settings JSON, so wt writes no settings file. The
// proxy key is set in the process environment only: argv is visible in the
// process list, and the key must not be.
//
// With in.Subscription (any slot model on the owner's Claude subscription)
// the whole session runs in header mode (#597, decision 7): no
// ANTHROPIC_AUTH_TOKEN, an empty ANTHROPIC_API_KEY, the stray credentials
// cleared, and the gateway token handed back only as HeaderToken, which the
// env assembler turns into ANTHROPIC_CUSTOM_HEADERS. The token is refused when
// a header line cannot carry it (ErrBadHeaderToken), and so is a profile
// whose settings env would override the header or the login.
func (claudeDriver) BuildSlots(in profiles.SlotBuildInput) (profiles.SlotCommand, error) {
	if !slices.Contains(claudeSlots, in.Default) && in.Default != claudeOpusplan {
		return profiles.SlotCommand{}, fmt.Errorf("default slot %q is not a claude slot", in.Default)
	}
	if in.Subscription {
		if err := checkHeaderToken(in.ProxyKey); err != nil {
			return profiles.SlotCommand{}, err
		}
		if err := claudeHeaderModeErr(in); err != nil {
			return profiles.SlotCommand{}, err
		}
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
	sc := profiles.SlotCommand{
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
	}
	if in.Subscription {
		sc.Env = []string{"ANTHROPIC_BASE_URL=" + in.ProxyOrigin, "ANTHROPIC_API_KEY="}
		sc.ClearEnv = append(sc.ClearEnv, claudeHeaderModeClear...)
		sc.ClearEnvPrefixes = append(sc.ClearEnvPrefixes, claudeHeaderModeClearPrefixes()...)
		sc.HeaderToken = in.ProxyKey
	}
	return sc, nil
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
	// The same for a modelOverrides that is not a table, or has a target
	// that is not a string (claudeModelOverrides).
	if _, err := claudeModelOverrides(in.ConfigContent); err != nil {
		return "", err
	}
	for _, slot := range claudeSlots {
		m, ok := in.Models[slot]
		if !ok {
			return "", fmt.Errorf("slot %s has no model", slot)
		}
		for _, v := range claudeSlotVars(slot, gatewayID(m), m) {
			env[v.Key] = v.Value
		}
	}
	// In plan mode Claude Code promotes opusplan's opus model to its [1m]
	// variant and assumes a 1M-token window for it, so a slot model with a
	// smaller window would be compacted far too late (#391). wt's value wins
	// over the profile's own.
	if in.Default == claudeOpusplan {
		env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] = "1"
	}
	// Claude Code does not know the default slot's model and would assume a
	// 200K window: declare the one tiers.toml records, unless the profile or
	// the user named the variable themselves (#405).
	if w, ok := claudeWindowSetting(env, in); ok {
		env[claudeContextVar] = w
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
