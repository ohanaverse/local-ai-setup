package litellm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

type OutcomeJSON struct {
	ID     string `json:"id"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

type ResultJSON struct {
	Outcomes []OutcomeJSON `json:"outcomes"`
	Changed  bool          `json:"changed"`
	Warnings []string      `json:"warnings"`
}

var ErrIDFailed = errors.New("one or more models could not be applied")

// Report prints a Result (text or JSON) and returns ErrIDFailed
// when any id was rejected, so the process exits 1 while the per-id detail is
// still printed for callers that parse it.
func Report(out, errOut io.Writer, res Result, asJSON bool) error {
	failed := false
	doc := ResultJSON{Outcomes: []OutcomeJSON{}, Changed: res.Changed, Warnings: append([]string{}, res.Warnings...)}
	for _, o := range res.Outcomes {
		j := OutcomeJSON{ID: o.ID, Action: o.Action}
		if o.Err != nil {
			j.Action, j.Error, failed = "", o.Err.Error(), true
		}
		doc.Outcomes = append(doc.Outcomes, j)
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(doc); err != nil {
			return err
		}
	} else {
		for _, j := range doc.Outcomes {
			if j.Error != "" {
				fmt.Fprintf(errOut, "%s: %s\n", j.ID, j.Error)
			} else {
				fmt.Fprintf(out, "%s: %s\n", j.ID, j.Action)
			}
		}
		for _, w := range doc.Warnings {
			fmt.Fprintf(errOut, "warning: %s\n", w)
		}
	}
	if failed {
		return ErrIDFailed
	}
	return nil
}

type SyncPlanJSON struct {
	DryRun bool `json:"dry_run"`
	Plan   struct {
		Add     []string      `json:"add"`
		Adopt   []string      `json:"adopt"`
		Rewrite []string      `json:"rewrite"`
		Remove  []string      `json:"remove"`
		Repair  []string      `json:"repair"`
		Errors  []OutcomeJSON `json:"errors"`
		// Move, EnableDB and Restart: see SyncPlan (#598). Added beside the
		// older keys; none of those changed meaning.
		Move     []string `json:"move"`
		EnableDB bool     `json:"enable_db"`
		Restart  bool     `json:"restart"`
		// Aliases: SyncPlan.Aliases (#596), always present. Added beside the
		// older keys like move; a skipped target's reason is a warning, never
		// a key here.
		Aliases []AliasPlanJSON `json:"aliases"`
	} `json:"plan"`
	Warnings []string `json:"warnings"`
}

// AliasPlanJSON is one AliasPlan in the dry run's JSON.
type AliasPlanJSON struct {
	ID      string          `json:"id"`
	Action  string          `json:"action"`
	Live    []AliasLiveJSON `json:"live"`
	Skipped []AliasSkipJSON `json:"skipped"`
}

// AliasLiveJSON is one AliasLive: a target that gets a row, and its order.
type AliasLiveJSON struct {
	Target string `json:"target"`
	Order  int    `json:"order"`
}

// AliasSkipJSON is one AliasSkip without its reason (a warning line says it).
type AliasSkipJSON struct {
	Target string `json:"target"`
	Frozen bool   `json:"frozen"`
}

// ReportSyncPlan prints a dry-run plan plus the probe warnings the real sync
// would report, in both renderings; exit status 1 when any desired id could
// not be built, like a real sync.
func ReportSyncPlan(out, errOut io.Writer, plan SyncPlan, warnings []string, asJSON bool) error {
	doc := SyncPlanJSON{DryRun: true, Warnings: append([]string{}, warnings...)}
	doc.Plan.Add = append([]string{}, plan.Add...)
	doc.Plan.Adopt = append([]string{}, plan.Adopt...)
	doc.Plan.Rewrite = append([]string{}, plan.Rewrite...)
	doc.Plan.Remove = append([]string{}, plan.Remove...)
	doc.Plan.Repair = append([]string{}, plan.Repair...)
	doc.Plan.Move = append([]string{}, plan.Move...)
	doc.Plan.EnableDB, doc.Plan.Restart = plan.EnableDB, plan.Restart
	doc.Plan.Aliases = []AliasPlanJSON{}
	for _, a := range plan.Aliases {
		j := AliasPlanJSON{ID: a.ID, Action: a.Action, Live: []AliasLiveJSON{}, Skipped: []AliasSkipJSON{}}
		for _, l := range a.Live {
			j.Live = append(j.Live, AliasLiveJSON{Target: l.Target, Order: l.Order})
		}
		for _, sk := range a.Skipped {
			j.Skipped = append(j.Skipped, AliasSkipJSON{Target: sk.Target, Frozen: sk.Frozen})
		}
		doc.Plan.Aliases = append(doc.Plan.Aliases, j)
	}
	doc.Plan.Errors = []OutcomeJSON{}
	for _, e := range plan.Errors {
		doc.Plan.Errors = append(doc.Plan.Errors, OutcomeJSON{ID: e.ID, Error: e.Err.Error()})
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(doc); err != nil {
			return err
		}
	} else {
		for _, id := range plan.Add {
			verb := "route"
			switch {
			case slices.Contains(plan.Adopt, id):
				verb = "adopt"
			case slices.Contains(plan.Rewrite, id):
				verb = "rewrite"
			}
			fmt.Fprintf(out, "%s: would %s\n", id, verb)
		}
		for _, id := range plan.Remove {
			fmt.Fprintf(out, "%s: would unroute\n", id)
		}
		for _, id := range plan.Repair {
			fmt.Fprintf(out, "%s: would set api_base\n", id)
		}
		for _, id := range plan.Move {
			fmt.Fprintf(out, "%s: would move to the database\n", id)
		}
		// Aliases (#596): only the ones the sync would change; a skipped one
		// is a warning line below.
		for _, a := range plan.Aliases {
			switch a.Action {
			case AliasActionRoute, AliasActionRewrite, AliasActionUnroute:
				targets := make([]string, 0, len(a.Live))
				for _, l := range a.Live {
					targets = append(targets, fmt.Sprintf("%s order %d", l.Target, l.Order))
				}
				line := fmt.Sprintf("%s: would %s", a.ID, a.Action)
				if len(targets) > 0 {
					line += " (" + strings.Join(targets, ", ") + ")"
				}
				fmt.Fprintln(out, line)
			}
		}
		// The two database cases cost a restart a reader would not expect
		// from the lines above, so the plan says so (#598).
		switch {
		case plan.EnableDB:
			fmt.Fprintln(out, "config.yaml: would set general_settings.store_model_in_db: true; the proxy would restart once, and local routes move to the database on the sync after")
		case len(plan.Move) > 0 && plan.Restart:
			fmt.Fprintln(out, "config.yaml: the moved rows would leave it; the proxy would restart once")
		}
		for _, e := range doc.Plan.Errors {
			fmt.Fprintf(errOut, "%s: %s\n", e.ID, e.Error)
		}
		for _, w := range doc.Warnings {
			fmt.Fprintf(errOut, "warning: %s\n", w)
		}
	}
	if len(plan.Errors) > 0 {
		return ErrIDFailed
	}
	return nil
}

// RunSync is `wt-model litellm sync` (and its --dry-run): SyncInputs over
// probe, then Sync or PlanSync, then the report. admin is Options.Admin (nil:
// every route in config.yaml, as before #598).
func RunSync(out, errOut io.Writer, cfg *config.Config, probe Probe, admin *AdminClient, asJSON, dryRun bool) error {
	desired, o, probeWarns := SyncInputs(cfg, probe)
	o.Admin = admin
	if dryRun {
		plan, err := PlanSync(cfg, desired, o)
		if err != nil {
			return err
		}
		// Same order as the real sync: what the write would leave behind,
		// then the probe warnings.
		return ReportSyncPlan(out, errOut, plan, append(slices.Clone(plan.Warnings), probeWarns...), asJSON)
	}
	res, err := Sync(cfg, desired, o)
	if err != nil {
		return err
	}
	res.Warnings = append(res.Warnings, probeWarns...)
	return Report(out, errOut, res, asJSON)
}

// SyncAfterWrite reloads the config (the registry just changed) and
// reconciles the routes exactly as `wt-model litellm sync` does. It never fails the
// command that wrote the registry: the write succeeded, and a sync that could
// not run is a warning. No LiteLLM setup at all (no config.yaml) is not even
// that. A redirected registry with no WT_LITELLM_CONFIG is refused by the
// sync (ErrRegistryRedirected), and the refusal is the warning. admin builds
// Options.Admin from the reloaded config (nil: no database routes).
func SyncAfterWrite(out, errOut io.Writer, probe Probe, admin func(*config.Config) *AdminClient) string {
	if _, err := os.Stat(DefaultPath()); os.IsNotExist(err) {
		// No LiteLLM here: nothing to sync, and no reason to probe providers.
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return "LiteLLM routes not synced: " + err.Error()
	}
	err = RunSync(out, errOut, cfg, probe, adminOf(admin, cfg), false, false)
	switch {
	case err == nil, errors.Is(err, ErrMissing):
		return ""
	case errors.Is(err, ErrRegistryRedirected):
		return err.Error()
	}
	return "LiteLLM routes not synced: " + err.Error()
}

// SyncAndWarn runs the one route sync that follows a registry write, prints
// its lines, and prints every warning on stderr. It never fails: the write
// succeeded, so the command exits 0.
func SyncAndWarn(out, errOut io.Writer, warnings []string, sync func(out, errOut io.Writer) string) error {
	var syncOut bytes.Buffer
	if w := sync(&syncOut, errOut); w != "" {
		warnings = append(warnings, w)
	}
	_, _ = io.Copy(out, &syncOut)
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	return nil
}

// adminOf is admin(cfg), or nil when there is no admin builder.
func adminOf(admin func(*config.Config) *AdminClient, cfg *config.Config) *AdminClient {
	if admin == nil {
		return nil
	}
	return admin(cfg)
}
