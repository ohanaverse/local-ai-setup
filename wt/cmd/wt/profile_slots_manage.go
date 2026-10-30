// `wt profile slots add|set|rm`: edit slot-profiles.toml in place through
// internal/slotedit, which backs the file up and refuses a write that would
// leave a profile unusable. See docs/wt-agents/slot-profiles.md.
package main

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/slotedit"
	"github.com/spf13/cobra"
)

// slotAgent is the agent `wt profile slots add` writes profiles for.
const slotAgent = "claude"

var slotProfileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// slotsCmd is `wt profile slots`. Subcommands are registered here, in one
// place.
func slotsCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "slots",
		Short: "Add, change and remove slot profiles in slot-profiles.toml",
	}
	c.AddCommand(slotsAddCmd(a), slotsSetCmd(a), slotsRmCmd(a))
	return c
}

// parseKV splits a repeated "key=value" flag into pairs, in order.
func parseKV(flag string, vals []string) ([][2]string, error) {
	out := make([][2]string, 0, len(vals))
	for _, v := range vals {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--%s %q: want KEY=VALUE", flag, v)
		}
		out = append(out, [2]string{k, val})
	}
	return out, nil
}

// parseSlotFlags turns repeated --slot name=id values into a map, refusing a
// slot the agent does not declare and a slot given twice.
func parseSlotFlags(vals []string) (map[string]string, error) {
	pairs, err := parseKV("slot", vals)
	if err != nil {
		return nil, err
	}
	declared := agents.SlotDrivers().Slots(slotAgent)
	slots := map[string]string{}
	for _, kv := range pairs {
		if !slices.Contains(declared, kv[0]) {
			return nil, fmt.Errorf("--slot %s=%s: %s declares the slots %s", kv[0], kv[1], slotAgent, strings.Join(declared, ", "))
		}
		if _, dup := slots[kv[0]]; dup {
			return nil, fmt.Errorf("--slot %s given twice", kv[0])
		}
		slots[kv[0]] = kv[1]
	}
	return slots, nil
}

func parseEnvFlags(flag string, vals []string) (map[string]string, error) {
	pairs, err := parseKV(flag, vals)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range pairs {
		env[kv[0]] = kv[1]
	}
	return env, nil
}

// slotEditOptions builds the Apply options: the registry's model ids and the
// profile that must be usable afterwards ("" for a removal).
func slotEditOptions(a *app, target string) (slotedit.Options, error) {
	if a.cfg == nil {
		if a.cfgErr != nil {
			return slotedit.Options{}, fmt.Errorf("no config loaded: %w", a.cfgErr)
		}
		return slotedit.Options{}, fmt.Errorf("no config loaded")
	}
	ids := make([]string, 0, len(a.cfg.Models))
	for _, m := range a.cfg.Models {
		ids = append(ids, m.ID)
	}
	return slotedit.Options{Drivers: agents.SlotDrivers(), ModelIDs: ids, Target: target}, nil
}

// applySlotEdit runs edit through slotedit.Apply on slot-profiles.toml, then
// reloads the app's slot state from disk and prints the backup path (or
// "created") followed by the `wt profile list` slot section.
func applySlotEdit(w io.Writer, a *app, edit func([]byte) ([]byte, error), o slotedit.Options) error {
	backup, err := slotedit.Apply(profiles.SlotPath(), edit, o)
	if err != nil {
		return err
	}
	if backup == "" {
		fmt.Fprintf(w, "created %s\n", profiles.SlotPath())
	} else {
		fmt.Fprintf(w, "backup: %s\n", backup)
	}
	// The write made the app's cached slot state stale.
	store, loadErr := loadSlotStore()
	a.slotLoadErr, a.slotChecks, a.slotUnknown = loadErr, nil, nil
	if loadErr == nil {
		if len(store.Profiles) > 0 {
			a.slotChecks = profiles.ValidateSlots(store, agents.SlotDrivers())
		}
		a.slotUnknown = store.Unknown
	}
	return writeSlotProfileList(w, a)
}

func slotsAddCmd(a *app) *cobra.Command {
	var slotFlags, envFlags []string
	var def, comment string
	c := &cobra.Command{
		Use: "add <name>", Short: "Add a slot profile for claude", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !slotProfileNameRe.MatchString(name) {
				return fmt.Errorf("profile name %q: use lowercase letters, digits and hyphens, starting with a letter or digit", name)
			}
			slots, err := parseSlotFlags(slotFlags)
			if err != nil {
				return err
			}
			env, err := parseEnvFlags("env", envFlags)
			if err != nil {
				return err
			}
			o, err := slotEditOptions(a, name)
			if err != nil {
				return err
			}
			p := profiles.SlotProfile{Name: name, Agent: slotAgent, Default: def, Slots: slots}
			if len(env) > 0 {
				p.Env = env
			}
			return applySlotEdit(cmd.OutOrStdout(), a, func(src []byte) ([]byte, error) {
				return slotedit.Add(src, p, comment)
			}, o)
		},
	}
	c.Flags().StringArrayVar(&slotFlags, "slot", nil, "slot=registry-id (repeatable)")
	c.Flags().StringVar(&def, "default", "sonnet", "the default slot")
	c.Flags().StringArrayVar(&envFlags, "env", nil, "KEY=VALUE env entry (repeatable)")
	c.Flags().StringVar(&comment, "comment", "", "comment written above the entry")
	return c
}

func slotsSetCmd(a *app) *cobra.Command {
	var slotFlags, envFlags, unsetFlags []string
	var def string
	c := &cobra.Command{
		Use: "set <name>", Short: "Change slots, default or env of a slot profile", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			slots, err := parseSlotFlags(slotFlags)
			if err != nil {
				return err
			}
			env, err := parseEnvFlags("env", envFlags)
			if err != nil {
				return err
			}
			patch := slotedit.Patch{UnsetEnv: unsetFlags}
			if len(slots) > 0 {
				patch.Slots = slots
			}
			if len(env) > 0 {
				patch.SetEnv = env
			}
			if cmd.Flags().Changed("default") {
				patch.Default = &def
			}
			o, err := slotEditOptions(a, name)
			if err != nil {
				return err
			}
			return applySlotEdit(cmd.OutOrStdout(), a, func(src []byte) ([]byte, error) {
				return slotedit.Set(src, name, patch)
			}, o)
		},
	}
	c.Flags().StringArrayVar(&slotFlags, "slot", nil, "slot=registry-id to set (repeatable)")
	c.Flags().StringVar(&def, "default", "", "new default slot")
	c.Flags().StringArrayVar(&envFlags, "env", nil, "KEY=VALUE env entry to set (repeatable)")
	c.Flags().StringArrayVar(&unsetFlags, "unset-env", nil, "env KEY to remove (repeatable)")
	return c
}

func slotsRmCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "rm <name>", Short: "Remove a slot profile", Args: cobra.ExactArgs(1), SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			o, err := slotEditOptions(a, "")
			if err != nil {
				return err
			}
			return applySlotEdit(cmd.OutOrStdout(), a, func(src []byte) ([]byte, error) {
				return slotedit.Remove(src, name)
			}, o)
		},
	}
}
