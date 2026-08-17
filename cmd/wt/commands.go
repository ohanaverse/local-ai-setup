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
	var crossTag string
	c := &cobra.Command{
		Use:    "rotate <tag>",
		Short:  "Print the next model in a tag group's rotation (debug)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := rotation.ForTag(a.cfg, args[0])
			m, ok := r.Next(crossTag)
			if !ok {
				return fmt.Errorf("no models tagged %q", args[0])
			}
			fmt.Println(m.ID)
			return nil
		},
	}
	c.Flags().StringVar(&crossTag, "cross-tag", "", "skip models used by this tag")
	return c
}
