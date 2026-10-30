// `wt profile slots add|set|rm`: edit slot-profiles.toml in place through
// internal/slotedit, which backs the file up and refuses a write that would
// leave a profile unusable. See docs/wt-agents/slot-profiles.md.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/candidates"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
	"github.com/ohanaverse/local-ai-setup/wt/internal/slotedit"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
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
	c.AddCommand(slotsAddCmd(a), slotsSetCmd(a), slotsRmCmd(a), slotsUsageCmd(a), slotsCandidatesCmd(a))
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
	a.slotChecks, a.slotUnknown, a.slotLoadErr = loadSlotState()
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
			if len(slots) == 0 && len(env) == 0 && len(unsetFlags) == 0 && !cmd.Flags().Changed("default") {
				return fmt.Errorf("nothing to change: give at least one of --slot, --default, --env, --unset-env")
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

// slotUsageJSON is one `wt profile slots usage --json` element.
type slotUsageJSON struct {
	Name        string            `json:"name"`
	Launches30d int               `json:"launches_30d"`
	LastLaunch  *string           `json:"last_launch"`
	Comment     string            `json:"comment"`
	Slots       map[string]string `json:"slots"`
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func slotsUsageCmd(a *app) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use: "usage", Short: "Show launches of each slot profile over the last 1, 7 and 30 days", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			store, err := loadSlotStore()
			if err != nil {
				return err
			}
			var comments map[string]string
			if src, rerr := os.ReadFile(profiles.SlotPath()); rerr == nil {
				comments = slotedit.LeadingComments(src)
			}
			asOf := time.Now()
			us := usage.NewStore()
			counts, last := us.AllCounts("", asOf), us.LastLaunch("", asOf)
			// Profiles in the file drive the rows: ids of a renamed or removed
			// profile linger in usage.jsonl and are not reported.
			var rows []slotUsageJSON
			var all []usage.UsageCounts
			for _, p := range store.Profiles {
				id := p.ID()
				r := slotUsageJSON{Name: p.Name, Launches30d: counts[id].ThirtyDay, Comment: comments[p.Name], Slots: p.Slots}
				if r.Slots == nil {
					r.Slots = map[string]string{}
				}
				if t, ok := last[id]; ok {
					s := t.UTC().Format(time.RFC3339)
					r.LastLaunch = &s
				}
				rows = append(rows, r)
				all = append(all, counts[id])
			}
			if asJSON {
				if rows == nil {
					rows = []slotUsageJSON{}
				}
				return writeJSON(w, rows)
			}
			if len(rows) == 0 {
				fmt.Fprintln(w, "slot profiles: (none defined)")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "PROFILE\t1D\t7D\t30D\tLAST LAUNCH\tCOMMENT")
			for i, r := range rows {
				lastS := "-"
				if r.LastLaunch != nil {
					lastS = *r.LastLaunch
				}
				fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\t%s\n", r.Name, all[i].OneDay, all[i].SevenDay, all[i].ThirtyDay, lastS,
					strings.ReplaceAll(r.Comment, "\n", " "))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return c
}

// candidateJSON is one candidate in `wt profile slots candidates --json`.
type candidateJSON struct {
	ID                 string   `json:"id"`
	Provider           string   `json:"provider"`
	Location           string   `json:"location"`
	Tier               string   `json:"tier"`
	Confidence         string   `json:"confidence"`
	Score              *float64 `json:"score"`
	Rated              bool     `json:"rated"`
	OpenWeights        bool     `json:"open_weights"`
	Context            int      `json:"context"`
	OutputPerMillion   *float64 `json:"output_per_million"`
	Subscription       *float64 `json:"subscription"`
	SubscriptionPeriod string   `json:"subscription_period"`
	Flags              []string `json:"flags"`
}

func toCandidateJSON(c candidates.Candidate) candidateJSON {
	j := candidateJSON{
		ID: c.ID, Provider: c.Provider, Location: c.Location, Tier: c.Tier, Confidence: c.Confidence,
		Rated: c.Rated, OpenWeights: c.OpenWeights, Context: c.Context, OutputPerMillion: c.OutputPerMillion,
		Subscription: c.Subscription, SubscriptionPeriod: c.SubscriptionPeriod, Flags: c.Flags,
	}
	if j.Flags == nil {
		j.Flags = []string{}
	}
	if c.HasScore {
		s := c.Score
		j.Score = &s
	}
	return j
}

func fmtPrice(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%g", *p)
}

// ageDays renders an age in whole days, so the table never shows a Go
// duration such as 719h37m0s.
func ageDays(d time.Duration) string {
	switch days := int(d / (24 * time.Hour)); {
	case days < 1:
		return "less than a day"
	case days == 1:
		return "1 day"
	default:
		return fmt.Sprintf("%d days", days)
	}
}

func slotsCandidatesCmd(a *app) *cobra.Command {
	var asJSON, openOnly bool
	var provider, near string
	var perProvider int
	c := &cobra.Command{
		Use: "candidates", Short: "Rank registry models by tier score and price for a slot", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			if cmd.Flags().Changed("per-provider") && near == "" {
				return fmt.Errorf("--per-provider needs --near")
			}
			if a.cfg == nil {
				return fmt.Errorf("no config loaded")
			}
			q := candidates.Query{OpenOnly: openOnly, Near: near, PerProvider: perProvider}
			for _, p := range strings.Split(provider, ",") {
				if p = strings.TrimSpace(p); p != "" {
					q.Providers = append(q.Providers, p)
				}
			}
			cs := candidates.Rank(a.cfg.Models, tiers.Load(), q)
			fresh, haveFresh := tiers.ReadFreshness()
			if asJSON {
				out := struct {
					GeneratedAt *string         `json:"tiers_generated_at"`
					Stale       []string        `json:"stale_sources"`
					Candidates  []candidateJSON `json:"candidates"`
				}{Stale: []string{}, Candidates: []candidateJSON{}}
				if haveFresh {
					s := fresh.GeneratedAt.UTC().Format(time.RFC3339)
					out.GeneratedAt = &s
					if len(fresh.StaleSources) > 0 {
						out.Stale = fresh.StaleSources
					}
				}
				for _, c := range cs {
					out.Candidates = append(out.Candidates, toCandidateJSON(c))
				}
				return writeJSON(w, out)
			}
			if haveFresh {
				fmt.Fprintf(w, "tiers.toml: %s old\n", ageDays(time.Since(fresh.GeneratedAt)))
				if len(fresh.StaleSources) > 0 {
					fmt.Fprintf(w, "stale sources: %s\n", strings.Join(fresh.StaleSources, ", "))
				}
			} else {
				fmt.Fprintln(w, "tiers.toml: not found")
			}
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "MODEL\tTIER\tSCORE\tOPEN\tCONTEXT\tOUT $/M\tFLAGS")
			for _, c := range cs {
				score := "-"
				if c.HasScore {
					score = fmt.Sprintf("%.1f", c.Score)
				}
				open := ""
				if c.OpenWeights {
					open = "open"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", c.ID, c.Tier, score, open, c.Context, fmtPrice(c.OutputPerMillion), strings.Join(c.Flags, ","))
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	c.Flags().BoolVar(&openOnly, "open-only", false, "only open-weights models")
	c.Flags().StringVar(&provider, "provider", "", "comma-separated provider ids")
	c.Flags().StringVar(&near, "near", "", "registry model id: list the nearest-scoring models per provider")
	c.Flags().IntVar(&perProvider, "per-provider", 0, "with --near, models per provider (default 3)")
	return c
}
