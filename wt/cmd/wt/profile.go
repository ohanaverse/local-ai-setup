// wt profile — inspect and toggle the local-model profile layer
// (internal/profiles). See docs/wt-agents/profiles.md.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

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
	case "model":
		return "model=" + p.Model
	default:
		return fmt.Sprintf("match=%q (invalid)", p.Match)
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
			// A load error means profiles.toml is malformed/unreadable —
			// a.profiles is NOT real data (it's the zero-value Store{}), so
			// rendering it as "(no profiles defined)" would misreport a
			// broken file as an empty one. Report the real error instead.
			if a.profilesLoadErr != nil {
				return fmt.Errorf("profiles.toml: %w", a.profilesLoadErr)
			}
			if len(a.profiles.Profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "(no profiles defined)")
				return nil
			}
			for _, p := range a.profiles.Profiles {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: match=%s %s\n", p.Agent, p.Match, matchValueForDisplay(p))
			}
			if a.profilesValidateErr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: profiles.toml failed validation — profiles are disabled for every launch: %v\n", a.profilesValidateErr)
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
			if a.profilesLoadErr != nil {
				return fmt.Errorf("profiles.toml: %w", a.profilesLoadErr)
			}
			if a.profilesValidateErr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "(profiles disabled for every launch — profiles.toml failed validation: %v)\n", a.profilesValidateErr)
				return nil
			}
			// -M is optional (design spec: `wt profile show -A <agent> [-M
			// <model>]`): with none given, resolve with a zero Model
			// instead of erroring — no tier can match without a model
			// (Resolve degrades to reporting no match), which still lets an
			// agent-only run confirm the agent has no profile at all rather
			// than failing outright.
			var m config.Model
			if showModel != "" {
				var err error
				m, err = findModelByID(a.cfg, showModel)
				if err != nil {
					return err
				}
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
			if a.profilesLoadErr != nil {
				return fmt.Errorf("profiles.toml: %w", a.profilesLoadErr)
			}
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
				// A load error means profiles.toml failed to parse: writing
				// now would re-encode whatever a.profiles currently holds
				// (the zero-value Store{}), silently destroying every
				// existing [[profiles]] entry and comment in the real file.
				// Refuse and tell the user to fix the file first. A
				// validation-only error is safe to toggle — the file
				// parsed correctly, so store.Profiles is real data.
				if a.profilesLoadErr != nil {
					return fmt.Errorf("profiles.toml: %w (fix the file before toggling — 'on'/'off' refuses to write over a file it couldn't parse)", a.profilesLoadErr)
				}
				if err := writeProfilesEnabled(enabled); err != nil {
					return err
				}
				a.profiles.Enabled = enabled
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

// topLevelEnabledLineRe matches a top-level `enabled = true|false` line.
// Profile's Go struct (internal/profiles.Profile) has no field named
// "enabled", so this can only ever match the file's own top-level toggle —
// but the search is still scoped to the text BEFORE the first
// "[[profiles]]" table header (see setEnabledLine) so a future field
// addition, or a profile entry with stray top-level-looking text in a
// string value, can never be mistaken for the toggle line.
var topLevelEnabledLineRe = regexp.MustCompile(`(?m)^enabled\s*=\s*(true|false)\s*$`)

// setEnabledLine returns content with its top-level `enabled = ...` line
// set to want, editing only that one line (or inserting one at the top
// when absent) — everything else in the file (comments, [[profiles]]
// entries, their own formatting) is byte-for-byte untouched. The search
// for an existing line is scoped to the text before the first
// "[[profiles]]" occurrence, so a hypothetical profile field that also
// happened to be named "enabled" inside a table body could never be
// mismatched (Profile has no such field today; this is defense in depth).
func setEnabledLine(content string, want bool) string {
	newLine := fmt.Sprintf("enabled = %t", want)
	head, tail := content, ""
	if i := strings.Index(content, "[[profiles]]"); i >= 0 {
		head, tail = content[:i], content[i:]
	}
	if loc := topLevelEnabledLineRe.FindStringIndex(head); loc != nil {
		return head[:loc[0]] + newLine + head[loc[1]:] + tail
	}
	return newLine + "\n\n" + head + tail
}

// writeProfilesEnabled sets profiles.toml's top-level `enabled` flag via a
// surgical, line-level text edit: it replaces (or inserts) only the
// `enabled = ...` line and leaves the rest of the file — comments, unknown
// keys, [[profiles]] entries and their formatting — byte-for-byte
// untouched. This is deliberately NOT a full profiles.Save() re-encode:
// Profile's Go struct doesn't preserve TOML comments, and the TOML
// encoder's output formatting differs from arbitrary hand-written TOML, so
// re-encoding the whole Store on every `on`/`off` toggle would silently
// strip a user's comments and reformat their file. When profiles.toml does
// not exist yet (the very first toggle, nothing on disk to preserve), this
// falls back to profiles.Save's whole-file encode.
func writeProfilesEnabled(enabled bool) error {
	data, err := os.ReadFile(profiles.Path())
	if os.IsNotExist(err) {
		return profiles.Save(profiles.Store{Enabled: enabled})
	}
	if err != nil {
		return err
	}
	newContent := setEnabledLine(string(data), enabled)
	return config.WriteFileAtomic(profiles.Path(), []byte(newContent), 0o644)
}
