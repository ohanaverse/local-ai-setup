// wt stats — a read-only report: the survey table over survey.jsonl (wt
// collects, wt reports), then the usage table (stats_usage.go) joining
// usage.jsonl's launches with the LiteLLM proxy's spend log. Model ids are
// read straight from the events and the spend rows, so both tables work
// for a model that has since been removed from registry.toml; the registry
// is consulted only for a model's family.
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
// aggregate (Aggregate) or one (agent, model) combo. Agent is the label the
// table prints, and statsAllAgents is only that label: an agent may be
// configured under the name "(all)", so the string cannot answer whether a
// row is the aggregate; Aggregate can.
type statsRow struct {
	ModelID string
	Agent   string
	Stats   survey.Stats
	// Aggregate marks the per-model all-agents row built from ModelStats.
	Aggregate bool
}

// statsCmd returns the `wt stats` command. It is a report, never a gate:
// an empty store, a missing psql and an unreachable database all exit 0.
// Only a malformed --window value is an error.
func statsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Report survey stats, launches and LiteLLM spend per model",
		Long: "Report two tables for one window.\n\n" +
			"The survey table: accumulated post-session survey stats (worked%, quality,\n" +
			"speed) per model and per agent×model combo.\n\n" +
			"The usage table: per model, wt launches from usage.jsonl beside the\n" +
			"requests, prompt and completion tokens and spend the LiteLLM proxy logged.\n" +
			"Spend is read with psql from the proxy's database. Its connection string\n" +
			"is the first of: WT_LITELLM_DATABASE_URL; MODELMAN_LITELLM_DATABASE_URL\n" +
			"(the legacy alias); general_settings.database_url in LiteLLM's config.yaml;\n" +
			"and DATABASE_URL, when config.yaml names none. A config.yaml value written\n" +
			"os.environ/NAME, and DATABASE_URL, are looked up in wt's environment and\n" +
			"then in the EnvironmentVariables of the proxy's LaunchAgent plist.\n\n" +
			"Without psql, a reachable database or a configured URL, the launches\n" +
			"still print, the spend cells show \"-\", and one note on stderr says why.",
		RunE: func(cmd *cobra.Command, args []string) error {
			windowName := mustGetString(cmd, "window")
			window, err := parseStatsWindow(windowName)
			if err != nil {
				return err
			}
			modelFilter := mustGetString(cmd, "model")
			agentFilter := mustGetString(cmd, "agent")
			familyFilter := mustGetString(cmd, "family")

			asOf := statsNow()
			rows := buildStatsRows(survey.NewStore().Events(), window, asOf, modelFilter, agentFilter)

			out := cmd.OutOrStdout()
			asJSON, _ := cmd.Flags().GetBool("json")
			var rep usageReport
			if asJSON {
				if windowName == "" {
					windowName = "30d"
				}
				// One JSON document: writeStatsJSON writes nothing until it is
				// complete, so there is no first table to show before the spend
				// query: query, then write.
				rep = collectUsage(cmd.Context(), a.cfg, window, asOf, modelFilter, familyFilter, agentFilter)
				if err := writeStatsJSON(out, buildStatsJSON(windowName, asOf, rows, rep)); err != nil {
					return err
				}
			} else {
				if table := surveyTableRows(rows); len(table) == 0 {
					fmt.Fprintln(out, "no survey data")
				} else {
					fmt.Fprintln(out, renderTable(surveyHeaders, table, a.theme))
				}
				fmt.Fprintln(out)

				// After the survey table: collectUsage runs the spend query (psql,
				// up to a few seconds against a down or hanging database), and
				// the survey table does not need it — printing first keeps the
				// first table off that critical path. (--json waits anyway: one
				// document prints only when complete.)
				rep = collectUsage(cmd.Context(), a.cfg, window, asOf, modelFilter, familyFilter, agentFilter)
				if len(rep.Rows) == 0 {
					fmt.Fprintln(out, "no usage data")
				} else {
					fmt.Fprintln(out, renderUsageTable(rep.Rows, stdoutWidth()))
				}
			}
			notes := rep.notes()
			if familyFilter != "" && a.loadErr != nil {
				notes = append(notes, registryNote(a.loadErr))
			}
			for _, note := range notes {
				fmt.Fprintf(cmd.ErrOrStderr(), "wt: %s\n", note)
			}
			return nil
		},
	}
	cmd.Flags().String("window", "30d", "Window for both tables: 1d, 7d, or 30d")
	cmd.Flags().String("model", "", "Filter both tables to one model id")
	cmd.Flags().String("agent", "", "Filter to one agent (survey and launches; spend is then not shown)")
	// Local, so it shadows the root's persistent -F/--family (a comma list
	// for the picker) on this command, as --model above shadows -M/--model.
	cmd.Flags().String("family", "", "Filter the usage table to one model family")
	cmd.Flags().Bool("json", false, "Print one JSON document (window, as_of, survey, usage) instead of the tables")
	return cmd
}

// statsNow is the report's "as of" instant. It is read once per run, and
// every window in the report is measured back from that one value: the
// survey rows, the launch counts (usage's AllCounts takes it), the spend
// query, and the as_of a --json document carries. A seam so a test can pin
// the whole report.
var statsNow = func() time.Time { return time.Now().UTC() }

var surveyHeaders = []string{"MODEL", "AGENT", "WORKED%", "QUALITY", "SPEED", "N", "SKIPPED"}

// surveyTableRows renders the survey rows as table cells.
func surveyTableRows(rows []statsRow) [][]string {
	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		quality, qok := r.Stats.QualityAvg()
		speed, sok := r.Stats.SpeedAvg()

		// Exclude rows with no data: rows where both Answered == 0 and
		// Skipped == 0 have no survey information at all. Rows with
		// Answered == 0 but Skipped > 0 ("all skipped") are kept to show
		// that the agent×model combo was tried but never produced data.
		// buildStatsRows applies the same rule.
		if r.Stats.Answered == 0 && r.Stats.Skipped == 0 {
			continue
		}

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
	return tableRows
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
// and sorts by agent name (the "(all)" aggregate rows first, then model
// id). The aggregate-first placement is enforced explicitly rather than
// by lexicographic luck: agent names are user-configured and may sort
// before "(" (digits, "-", non-ASCII).
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
				rows = append(rows, statsRow{ModelID: id, Agent: statsAllAgents, Aggregate: true, Stats: s})
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

	sort.Slice(rows, func(i, j int) bool {
		// Aggregate rows sort before real-agent rows regardless of how
		// a configured agent name compares to "(all)" byte-wise — including
		// an agent named "(all)", which is a real-agent row.
		if rows[i].Aggregate != rows[j].Aggregate {
			return rows[i].Aggregate
		}
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
