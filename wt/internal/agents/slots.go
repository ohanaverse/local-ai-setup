package agents

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// SlotPassthroughRefuser is an optional capability of a slot-capable
// driver: the flags that, passed after `--`, would replace the slot mapping.
type SlotPassthroughRefuser interface {
	RefusedSlotPassthrough() []string
}

// SlotConfigWarner is an optional capability of a slot-capable driver: what
// a profile's config_content holds that the agent accepts but the user should
// hear about. Unlike CheckConfigContent it never blocks a profile.
// slotModels are the distinct registry ids the profile fills its slots with,
// the ones a launch makes sure are routed.
type SlotConfigWarner interface {
	ConfigContentWarnings(cc map[string]any, slotModels []string) []string
}

// SlotConfigWarnings asks c's driver what to say about the profile's
// config_content (SlotConfigWarner), with the profile's slot models. It says
// nothing about a profile that is not Usable or failed validation: that one
// already carries its error. nil also when the agent's driver has no such
// capability.
func SlotConfigWarnings(c profiles.SlotCheck) []string {
	if !c.Usable || c.Err != nil || len(c.Profile.ConfigContent) == 0 {
		return nil
	}
	w, ok := ByName(c.Profile.Agent).(SlotConfigWarner)
	if !ok {
		return nil
	}
	// The default's model is one of these: the default names a filled slot,
	// or an alias whose slots must be filled.
	models := slices.Sorted(maps.Values(c.Profile.Slots))
	return w.ConfigContentWarnings(c.Profile.ConfigContent, slices.Compact(models))
}

// SlotsFor returns the agent's slot support. ok is false for an unknown
// agent and for a driver without profiles.SlotCapable.
func SlotsFor(agent string) (profiles.SlotCapable, bool) {
	d := ByName(agent)
	if d == nil {
		return nil, false
	}
	sc, ok := d.(profiles.SlotCapable)
	return sc, ok
}

// SlotDrivers is the profiles.SlotDrivers backed by the registered drivers.
// internal/profiles cannot import this package, so static validation asks
// about agents through these callbacks.
func SlotDrivers() profiles.SlotDrivers {
	return profiles.SlotDrivers{
		Known: func(agent string) bool { return ByName(agent) != nil },
		Slots: func(agent string) []string {
			sc, ok := SlotsFor(agent)
			if !ok {
				return nil
			}
			return sc.SlotNames()
		},
		ReservedEnv: func(agent string) []string {
			sc, ok := SlotsFor(agent)
			if !ok {
				return nil
			}
			return sc.ReservedEnvKeys()
		},
		Mechanisms: func(agent string) []profiles.Mechanism {
			pc, ok := ByName(agent).(profiles.ProfileCapable)
			if !ok {
				return nil
			}
			return pc.ProfileMechanisms()
		},
		ConfigContent: func(agent string, cc map[string]any) error {
			sc, ok := SlotsFor(agent)
			if !ok {
				return nil
			}
			return sc.CheckConfigContent(cc)
		},
		DefaultAlias: func(agent, name string) (profiles.SlotAlias, bool) {
			sc, ok := SlotsFor(agent)
			if !ok {
				return profiles.SlotAlias{}, false
			}
			return sc.DefaultAlias(name)
		},
		Required: func(agent string, m profiles.Mechanism) (profiles.Mechanism, bool) {
			mr, ok := ByName(agent).(profiles.MechanismRequirer)
			if !ok {
				return "", false
			}
			return mr.RequiredMechanism(m)
		},
	}
}

// CheckSlotPassthrough returns an error naming the first argument of
// extraArgs that is one of the agent's refused flags, either alone
// ("--model") or in its "=" form ("--model=opus"). It returns nil when there
// is none, or when the driver refuses nothing.
func CheckSlotPassthrough(agent string, extraArgs []string) error {
	refuser, ok := ByName(agent).(SlotPassthroughRefuser)
	if !ok {
		return nil
	}
	refused := refuser.RefusedSlotPassthrough()
	for _, arg := range extraArgs {
		for _, flag := range refused {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return fmt.Errorf("a slot profile sets %s itself: remove it from the arguments after --", flag)
			}
		}
	}
	return nil
}

// BuildSlotCommand runs the agent's slot build and nothing else: no binary
// lookup, no environment. `wt profile show` calls it with a masked key.
func BuildSlotCommand(sl profiles.SlotLaunch, yolo bool, proxyOrigin, proxyKey string) (profiles.SlotCommand, error) {
	sc, ok := SlotsFor(sl.Agent)
	if !ok {
		return profiles.SlotCommand{}, fmt.Errorf("agent %q does not support slot profiles", sl.Agent)
	}
	return sc.BuildSlots(profiles.SlotBuildInput{
		SlotLaunch:  sl,
		ProxyOrigin: proxyOrigin,
		ProxyKey:    proxyKey,
		Yolo:        yolo,
	})
}

// BuildSlotLaunchCmd builds the exec.Cmd of a slot-profile launch, giving the
// driver's slot build the treatment BuildLaunchCmdInfo gives a single-model
// launch: binary lookup, the inherited environment filtered, the worktree
// directory and PWD, and the user's passthrough args appended unchanged.
//
// It reads the [litellm] url and key directly and never consults
// cfg.IsLitellm(): a slot profile uses the proxy whatever the toggle says. It
// resolves no route, runs no Syncer and applies no automatic profile.
func BuildSlotLaunchCmd(sl profiles.SlotLaunch, worktreePath string, yolo bool, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	if err := CheckSlotPassthrough(sl.Agent, extraArgs); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	origin, key := cfg.LitellmBaseURL(), cfg.LitellmAPIKey()
	if origin == "" {
		return nil, fmt.Errorf(
			"a slot profile always uses the LiteLLM proxy, but no URL is configured — "+
				"run 'wt litellm set --url ... --api-key ...': %w",
			config.ErrLitellmUnconfigured)
	}
	if key == "" {
		return nil, fmt.Errorf(
			"a slot profile always uses the LiteLLM proxy, but no API key is configured — "+
				"run 'wt litellm set --url ... --api-key ...': %w",
			config.ErrLitellmUnconfigured)
	}
	sc, err := BuildSlotCommand(sl, yolo, origin, key)
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath(sc.Bin)
	if err != nil {
		return nil, fmt.Errorf("agent %s not installed", sc.Bin)
	}
	cmd := exec.Command(bin, sc.Args...)
	cmd.Env = append(filterEnvPrefixes(filterEnv(os.Environ(), sc.ClearEnv), sc.ClearEnvPrefixes), sc.Env...)
	SetDir(cmd, worktreePath)
	cmd.Args = append(cmd.Args, extraArgs...)
	return cmd, nil
}

// filterEnvPrefixes returns env without the entries whose key starts with
// one of prefixes. filterEnv matches whole names only; this removes a family
// of variables (every ANTHROPIC_DEFAULT_*) whose members wt cannot list.
func filterEnvPrefixes(env, prefixes []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		drop := false
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}
