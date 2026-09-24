// wt profile — inspect and toggle the local-model profile layer
// (internal/profiles). See docs/wt-agents/profiles.md.
package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/spf13/cobra"
)

func matchValueForDisplay(p profiles.Profile) string {
	switch p.Match {
	case "location":
		return "location=" + p.Location
	case "provider":
		return "provider=" + p.Provider
	default:
		return "model=" + p.Model
	}
}

func profileCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "profile",
		Short: "Inspect and toggle local-model launch profiles (internal/profiles)",
	}

	listC := &cobra.Command{
		Use: "list", Short: "List every defined profile", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(a.profiles.Profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no profiles defined)")
				return nil
			}
			for _, p := range a.profiles.Profiles {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: match=%s %s\n", p.Agent, p.Match, matchValueForDisplay(p))
			}
			return nil
		},
	}

	var showAgent, showModel string
	showC := &cobra.Command{
		Use: "show", Short: "Dry-run profile resolution for an agent/model, without launching", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showAgent == "" {
				return fmt.Errorf("-A/--agent is required")
			}
			if a.profilesErr != nil {
				return fmt.Errorf("profiles.toml error: %w", a.profilesErr)
			}
			m, err := findModelByID(a.cfg, showModel)
			if err != nil {
				return err
			}
			rp := profiles.Resolve(a.profiles, showAgent, a.cfg, m)
			if rp.Empty() {
				fmt.Fprintln(cmd.OutOrStdout(), "(no matching profile)")
				return nil
			}
			for k, v := range rp.Env {
				fmt.Fprintf(cmd.OutOrStdout(), "env: %s=%s\n", k, v)
			}
			if len(rp.ExtraArgs) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "args: %v\n", rp.ExtraArgs)
			}
			if len(rp.ConfigContent) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "config_content: %v\n", rp.ConfigContent)
			}
			if rp.Wrapper != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "wrapper: %s %v\n", rp.Wrapper.Binary, rp.Wrapper.ArgsTemplate)
			}
			return nil
		},
	}
	showC.Flags().StringVarP(&showAgent, "agent", "A", "", "agent to resolve for")
	showC.Flags().StringVarP(&showModel, "model", "M", "", "model id to resolve for")

	statusC := &cobra.Command{
		Use: "status", Short: "Show whether the profile layer is enabled", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.profiles.Enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "profiles: on")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "profiles: off")
			}
			return nil
		},
	}

	toggle := func(use, short string, enabled bool) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.NoArgs, SilenceUsage: true,
			RunE: func(cmd *cobra.Command, _ []string) error {
				store := a.profiles
				store.Enabled = enabled
				if err := writeProfilesEnabled(store); err != nil {
					return err
				}
				a.profiles = store
				fmt.Fprintf(cmd.OutOrStdout(), "profiles: %s\n", map[bool]string{true: "on", false: "off"}[enabled])
				return nil
			},
		}
	}

	c.AddCommand(listC, showC, statusC, toggle("on", "Enable the profile layer", true), toggle("off", "Disable the profile layer", false))
	return c
}

// findModelByID looks m up in cfg.Models by exact id, for `wt profile
// show -M`. Unlike the launch path this does not consult live/discovered
// inventory — it is a dry-run tool over the registry only.
func findModelByID(cfg *config.Config, id string) (config.Model, error) {
	if cfg == nil {
		return config.Model{}, fmt.Errorf("no config loaded")
	}
	for _, m := range cfg.Models {
		if m.ID == id {
			return m, nil
		}
	}
	return config.Model{}, fmt.Errorf("model %q not found in registry", id)
}

// writeProfilesEnabled rewrites the top-level `enabled` key in
// profiles.toml, preserving every existing [[profiles]] entry exactly —
// it re-encodes the whole Store, matching config.Save's whole-file
// atomic-write convention (config.WriteFileAtomic) rather than a
// partial/line-level edit.
func writeProfilesEnabled(store profiles.Store) error {
	return profiles.Save(store)
}
