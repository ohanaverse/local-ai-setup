// wt model list — every registry model and every discovered local model,
// with live status. Read-only.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
	"github.com/spf13/cobra"
)

// modelListColumns are the text listing's columns. SIZE and PATH are the two
// a narrow terminal gives up (fitModelList).
var modelListColumns = []plainColumn{
	{head: "MODEL"}, {head: "FAMILY"}, {head: "LOC"}, {head: "STATUS"}, {head: "RUNNING"},
	{head: "SIZE", right: true}, {head: "PATH"},
}

func modelListCmd(a *app) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List every registry model and every local model found on this machine",
		Long: "List every model in the registry, and every local model the providers have\n" +
			"that the registry does not (STATUS new), from one live probe.\n\n" +
			"  STATUS   ok       a cloud model, or local weights the probe found\n" +
			"           missing  a registered local model that is not on disk\n" +
			"           unknown  the provider could not be asked\n" +
			"           new      on disk or pulled, not in the registry\n" +
			"           -        an mlx_lm_server pairing (never enumerated)\n" +
			"  RUNNING  run, load (omlx is still loading it), blank, or ? (could not tell)\n\n" +
			"SIZE and PATH are shown when the terminal is wide enough for them; --json\n" +
			"always has them (size_bytes, path).\n\n" +
			"A model whose fetch or draft is malformed in the registry (not a table, or a\n" +
			"repo or local_path that is not a string) is still listed, with that value\n" +
			"read as absent; a line on stderr names the row and the problem, and --json\n" +
			"has the same in each model's \"malformed\" array. A cost.time_prices row that\n" +
			"breaks the registry's rules (an unknown timezone, a time that is not HH:MM)\n" +
			"is named the same way: the model picker does not apply such a row.",
		Example:      "  wt model list\n  wt model list --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The load error only: a registry with a gap in it (a model
			// whose provider has no row) is exactly what this list is for.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			return runModelList(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, asJSON, stdoutWidth())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "machine-readable output, with size_bytes and path for every row")
	return c
}

// modelListJSON is `wt model list --json`'s one document.
type modelListJSON struct {
	Registry string         `json:"registry"`
	Models   []modelListRow `json:"models"`
	// Providers is each probed provider family's probe status: ok, partial
	// (running state not known) or unreachable.
	Providers map[string]string `json:"providers"`
}

type modelListRow struct {
	ID         string   `json:"id"`
	Family     string   `json:"family"`
	ProviderID string   `json:"provider_id"`
	ModelName  string   `json:"model_name"`
	Location   string   `json:"location"`
	Tags       []string `json:"tags"`
	Registered bool     `json:"registered"`
	Status     string   `json:"status"`
	// Running is "run", "load", "" or "?", as the text table prints it.
	Running string `json:"running"`
	// SizeBytes and Path are null when wt does not know them.
	SizeBytes *int64  `json:"size_bytes"`
	Path      *string `json:"path"`
	Target    string  `json:"target,omitempty"`
	Draft     string  `json:"draft,omitempty"`
	// Malformed names each fetch or draft value the loader read as absent
	// (config.Model.Malformed). Always an array, empty when there is none.
	Malformed []string `json:"malformed"`
}

func runModelList(out, errOut io.Writer, cfg *config.Config, asJSON bool, width int) error {
	snap := probeInventory(cfg)
	rows := modeladmin.Rows(cfg, snap)
	if asJSON {
		doc := modelListJSON{Registry: config.RegistryPath(), Models: []modelListRow{}, Providers: map[string]string{}}
		for f, st := range snap.Providers {
			doc.Providers[f] = string(st)
		}
		for _, r := range rows {
			jr := modelListRow{
				ID: r.ID, Family: r.Family, ProviderID: r.ProviderID, ModelName: r.ModelName, Location: r.Location,
				Tags: append([]string{}, r.Tags...), Registered: r.Registered, Status: string(r.Status), Running: r.Running,
				Target: r.Target, Draft: r.Draft, Malformed: append([]string{}, r.Malformed...),
			}
			if r.Size > 0 {
				jr.SizeBytes = &r.Size
			}
			if r.Path != "" {
				jr.Path = &r.Path
			}
			doc.Models = append(doc.Models, jr)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return err
		}
		noteMalformed(errOut, cfg)
		return nil
	}
	if len(rows) == 0 {
		// Only a provider the registry calls local is probed: with none,
		// nothing was looked for, and "none found" would say otherwise.
		if len(snap.Providers) == 0 {
			fmt.Fprintln(out, "no models: the registry has none, and no local provider row to look for models with (`wt model init` adds them; `wt model add` registers a model)")
			return nil
		}
		fmt.Fprintln(out, "no models: the registry has none, and no local model was found on this machine (add one with `wt model add`)")
		return nil
	}
	cols, cells, dropped := fitModelList(rows, width)
	fmt.Fprintln(out, renderPlainTable(cols, cells, width))
	if dropped != "" {
		fmt.Fprintf(errOut, "(%s not shown at this width; `wt model list --json` has every column)\n", dropped)
	}
	noteMalformed(errOut, cfg)
	return nil
}

// noteMalformed prints one line for each registry row whose fetch or draft
// the loader read as absent because it is malformed (config.Model.Malformed),
// in the registry's own order — the order the user meets them in the file,
// not the table's. The load tolerates such a value so that a hand edit cannot
// stop wt, and this listing says so for every row (the Models tab says it for
// the selected one, from the same phrases): without the line a
// `fetch = "~/models/x"` meant as a local_path only makes the row read
// "missing". The id is escaped as the table escapes it.
func noteMalformed(errOut io.Writer, cfg *config.Config) {
	// A malformed fetch is a bad registry entry, so the repair is worded by
	// the one function that words those.
	hint := config.RegistryFixHint(config.ErrRegistryEntry)
	for _, m := range cfg.Models {
		if problems := m.Malformed(); len(problems) > 0 {
			fmt.Fprintf(errOut, "%s: %s; wt reads it as absent (%s)\n", visibleID(m.ID), strings.Join(problems, ", "), hint)
		}
	}
}

// modelListMinKey is the least width the MODEL column is given before SIZE
// is dropped to make room: enough for a short id beside its cells. A longer
// id goes on a line of its own (renderPlainTable), never cut.
const modelListMinKey = 20

// fitModelList picks the columns that fit width (0: no limit) and returns
// them with the rows' cells. PATH is shown only when every row fits on one
// line with it. SIZE stays as long as the other columns leave MODEL at least
// modelListMinKey columns, or all it needs when every id is shorter than
// that. The five columns that say what a row is are never
// dropped. dropped names what was left out, "" when nothing was.
func fitModelList(rows []modeladmin.Row, width int) (cols []plainColumn, cells [][]string, dropped string) {
	full := make([][]string, len(rows))
	for i, r := range rows {
		// The family is the registry's text and the path a directory's name:
		// like the id, neither may carry a control sequence to the terminal.
		full[i] = []string{
			visibleID(r.ID), dash(visibleID(r.Family)), dash(r.Location), string(r.Status), r.Running,
			modeladmin.FormatSize(r.Size), dash(visibleID(r.Path)),
		}
	}
	take := func(keep int) {
		cols = modelListColumns[:keep]
		cells = make([][]string, len(full))
		for i := range full {
			cells[i] = full[i][:keep]
		}
	}
	take(7)
	if width <= 0 {
		return cols, cells, ""
	}
	if key, rest := plainTableWidth(cols, cells); key+rest <= width {
		return cols, cells, ""
	}
	take(6)
	if key, rest := plainTableWidth(cols, cells); min(key, modelListMinKey)+rest <= width {
		return cols, cells, "PATH"
	}
	take(5)
	return cols, cells, "SIZE and PATH"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
