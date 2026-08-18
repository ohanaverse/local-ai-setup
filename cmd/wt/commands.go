package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/registry"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
	"github.com/spf13/cobra"
)

func modelsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "models",
		Aliases: []string{"model"},
		Short:   "Browse and manage the model registry",
		Example: "  wt models          # list the registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Providers table
			provRows := make([][]string, 0, len(a.cfg.Providers))
			for _, p := range a.cfg.Providers {
				provRows = append(provRows, []string{
					p.ID,
					string(p.Location),
					p.Auth.Type,
					p.Auth.BaseURL,
				})
			}
			fmt.Println("Providers:")
			fmt.Println(renderTable(
				[]string{"ID", "LOCATION", "AUTH", "BASE_URL"},
				provRows,
			))

			// Models table — sort by provider, then ID
			models := registry.Discover(a.cfg)
			sort.Slice(models, func(i, j int) bool {
				if models[i].ProviderID != models[j].ProviderID {
					return models[i].ProviderID < models[j].ProviderID
				}
				return models[i].ID < models[j].ID
			})

			modelRows := make([][]string, 0, len(models))
			for _, m := range models {
				loc, _ := a.cfg.ResolveLocation(m)
				modelRows = append(modelRows, []string{
					m.ID,
					m.Family,
					m.ProviderID,
					string(loc),
					strings.Join(m.Tags, ", "),
				})
			}
			fmt.Println("Models:")
			fmt.Println(renderTable(
				[]string{"ID", "FAMILY", "PROVIDER", "LOCATION", "TAGS"},
				modelRows,
			))

			// Agents table
			agentRows := make([][]string, 0, len(a.cfg.Agents))
			for _, ag := range a.cfg.Agents {
				agentRows = append(agentRows, []string{
					ag.Name,
					strings.Join(ag.SupportedProviders, ", "),
					ag.DefaultProvider,
				})
			}
			fmt.Println("Agents:")
			fmt.Println(renderTable(
				[]string{"NAME", "PROVIDERS", "DEFAULT"},
				agentRows,
			))

			return nil
		},
	}
}

func agentsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "agents",
		Short:   "List installed agents and set defaults",
		Example: "  wt agents",
		Run: func(cmd *cobra.Command, args []string) {
			names := agents.Names()
			sort.Strings(names)
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				d := agents.ByName(n)
				installed := "no"
				if agents.Installed(n) {
					installed = "yes"
				}
				rows = append(rows, []string{n, installed, d.YoloFlag()})
			}
			fmt.Println("Agents:")
			fmt.Println(renderTable(
				[]string{"NAME", "INSTALLED", "YOLO_FLAG"},
				rows,
			))
		},
	}
}

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
			r := rotation.New(tag, models, "")
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
