package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/spf13/cobra"
)

// registryDiscover is the live-discovery entry point used by modelsCmd. It is
// a package-level var so tests can stub it and avoid shelling out to ollama
// or making HTTP requests.
var registryDiscover = registry.Discover

func rotateCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:    "rotate <tag>",
		Short:  "Print the model after the last-launched in a tag group (debug)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := args[0]
			models := a.cfg.ModelsWithTag(tag)
			if len(models) == 0 {
				return fmt.Errorf("no models tagged %q", tag)
			}
			// Legacy CLI shape: rotate is scoped by tag only. We use an empty
			// agent/family so the state-file name matches legacy behavior
			// (rotation-<tag>.state via the back-compat read path).
			slot := rotation.Slot{Agent: "-", Tag: tag, Family: "-"}
			r := rotation.NewForSlot(slot, models, "")
			last, ok := r.LastLaunched()
			if !ok {
				// No prior launch; print the first model.
				fmt.Println(models[0].ID)
				return nil
			}
			next, _ := rotation.FirstAfter(models, last)
			fmt.Println(next.ID)
			return nil
		},
	}
	return c
}

// modelsCmd returns the `wt models` subcommand, which prints the merged
// registry of curated config.toml models plus live-discovered Ollama and
// OpenRouter models.
func modelsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List the merged model registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			merged := registryDiscover(a.cfg)
			sort.Slice(merged, func(i, j int) bool { return merged[i].ID < merged[j].ID })
			rows := make([][]string, 0, len(merged))
			for _, m := range merged {
				loc := string(m.Location)
				if loc == "" {
					loc = "-"
				}
				rows = append(rows, []string{
					m.ID,
					m.ProviderID,
					m.Family,
					strings.ToUpper(loc),
					string(m.Source),
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), renderTable(
				[]string{"ID", "PROVIDER", "FAMILY", "LOCATION", "SOURCE"},
				rows,
				a.theme,
			))
			return nil
		},
	}
}

// agentsCmd returns the `wt agents` subcommand, which prints every configured
// agent plus every registered driver. Command agents (like shell) are
// marked as such.
func agentsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List configured agents and registered drivers",
		RunE: func(cmd *cobra.Command, args []string) error {
			seen := map[string]bool{}
			var rows [][]string
			add := func(name string) {
				if seen[name] {
					return
				}
				seen[name] = true
				ag, err := a.cfg.AgentByName(name)
				configured := err == nil
				if !configured {
					ag = &config.Agent{Name: name}
				}
				row := []string{
					ag.Name,
					"agent",
					strings.Join(ag.SupportedProviders, ", "),
					ag.DefaultProvider,
				}
				if agents.IsCommand(ag.Name) {
					row[1] = "command"
					row[2] = "-"
					row[3] = "-"
				} else if !configured {
					row[2] = "-"
					row[3] = "not configured"
				}
				rows = append(rows, row)
			}
			for _, a := range a.cfg.Agents {
				add(a.Name)
			}
			for _, n := range agents.Names() {
				add(n)
			}
			sort.Slice(rows, func(i, j int) bool {
				if rows[i][1] != rows[j][1] {
					return rows[i][1] == "command"
				}
				return rows[i][0] < rows[j][0]
			})
			fmt.Fprintln(cmd.OutOrStdout(), renderTable(
				[]string{"NAME", "TYPE", "PROVIDERS", "DEFAULT"},
				rows,
				a.theme,
			))
			return nil
		},
	}
}
