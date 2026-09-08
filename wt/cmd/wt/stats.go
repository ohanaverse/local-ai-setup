// wt stats — a read-only report over survey.jsonl (wt collects, wt
// reports). Never touches the modelman catalog: model ids are read
// straight from the survey events, so the command works even for a model
// that has since been removed from registry.toml.
package main

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/spf13/cobra"
)

// statsAllAgents labels the per-model aggregate row (all agents combined),
// distinct from any real agent name.
const statsAllAgents = "(all)"

// statsRow is one rendered row of `wt stats`: either a per-model "(all)"
// aggregate (Agent == statsAllAgents) or one (agent, model) combo.
type statsRow struct {
	ModelID string
	Agent   string
	Stats   survey.Stats
}

// statsCmd returns the `wt stats` command. It never affects the exit code
// on a missing/empty store — only a malformed --window value is an error.
func statsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report accumulated session survey stats",
		Long: "Report accumulated post-session survey stats (worked%, quality, speed)\n" +
			"per model and per agent×model combo, collected by the post-session\n" +
			"survey prompt.",
		RunE: func(cmd *cobra.Command, args []string) error {
			window, err := parseStatsWindow(mustGetString(cmd, "window"))
			if err != nil {
				return err
			}
			modelFilter := mustGetString(cmd, "model")
			agentFilter := mustGetString(cmd, "agent")

			events := survey.NewStore().Events()
			asOf := time.Now().UTC()
			rows := buildStatsRows(events, window, asOf, modelFilter, agentFilter)

			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no survey data")
				return nil
			}

			headers := []string{"MODEL", "AGENT", "WORKED%", "QUALITY", "SPEED", "N", "SKIPPED"}
			tableRows := make([][]string, 0, len(rows))
			for _, r := range rows {
				quality, qok := r.Stats.QualityAvg()
				speed, sok := r.Stats.SpeedAvg()
				tableRows = append(tableRows, []string{
					r.ModelID,
					r.Agent,
					formatPctCell(r.Stats),
					formatAvgCell(quality, qok),
					formatAvgCell(speed, sok),
					strconv.Itoa(r.Stats.Answered),
					strconv.Itoa(r.Stats.Skipped),
				})
			}
			fmt.Fprintln(cmd.OutOrStdout(), renderTable(headers, tableRows, a.theme))
			return nil
		},
	}
	cmd.Flags().String("window", "30d", "Stats window: 1d, 7d, or 30d")
	cmd.Flags().String("model", "", "Filter to one model id")
	cmd.Flags().String("agent", "", "Filter to one agent")
	return cmd
}

// parseStatsWindow maps a --window flag value to its duration.
func parseStatsWindow(s string) (time.Duration, error) {
	switch s {
	case "1d":
		return survey.Window1d, nil
	case "7d":
		return survey.Window7d, nil
	case "30d", "":
		return survey.Window30d, nil
	}
	return 0, fmt.Errorf("invalid --window %q: want 1d, 7d, or 30d", s)
}

// buildStatsRows merges the per-model "(all)" aggregate with every
// per-(agent,model) combo into one row list, applies the --model/--agent
// filters, drops rows with zero answered and zero skipped in the window,
// and sorts by model id (the "(all)" row first within a model, then agent
// name).
func buildStatsRows(events []survey.Event, window time.Duration, asOf time.Time, modelFilter, agentFilter string) []statsRow {
	modelStats := survey.ModelStats(events, window, asOf)
	combos := survey.AllAgentModelStats(events, window, asOf)

	modelIDs := make(map[string]bool, len(modelStats))
	for id := range modelStats {
		modelIDs[id] = true
	}
	for _, c := range combos {
		modelIDs[c.ModelID] = true
	}

	var rows []statsRow
	for id := range modelIDs {
		if modelFilter != "" && id != modelFilter {
			continue
		}
		if agentFilter == "" {
			if s := modelStats[id]; s.Answered > 0 || s.Skipped > 0 {
				rows = append(rows, statsRow{ModelID: id, Agent: statsAllAgents, Stats: s})
			}
		}
	}
	for _, c := range combos {
		if modelFilter != "" && c.ModelID != modelFilter {
			continue
		}
		if agentFilter != "" && c.Agent != agentFilter {
			continue
		}
		if c.Stats.Answered == 0 && c.Stats.Skipped == 0 {
			continue
		}
		rows = append(rows, statsRow{ModelID: c.ModelID, Agent: c.Agent, Stats: c.Stats})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Agent != rows[j].Agent {
			return rows[i].Agent < rows[j].Agent
		}
		return rows[i].ModelID < rows[j].ModelID
	})
	return rows
}

// formatPctCell renders WorkedPct as "N%", or "-" when unanswered.
func formatPctCell(s survey.Stats) string {
	pct, ok := s.WorkedPct()
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%d%%", int(pct+0.5))
}

// formatAvgCell renders a rating average to one decimal, or "-" when unrated.
func formatAvgCell(avg float64, ok bool) string {
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.1f", avg)
}
