package litellm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

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
	} `json:"plan"`
	Warnings []string `json:"warnings"`
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

func RunSync(out, errOut io.Writer, cfg *config.Config, probe Probe, asJSON, dryRun bool) error {
	desired, o, probeWarns := SyncInputs(cfg, probe)
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
// reconciles the routes exactly as `wt litellm sync` does. It never fails the
// command that wrote the registry: the write succeeded, and a sync that could
// not run is a warning. No LiteLLM setup at all (no config.yaml) is not even
// that. A redirected registry with no WT_LITELLM_CONFIG is refused by the
// sync (ErrRegistryRedirected), and the refusal is the warning.
func SyncAfterWrite(out, errOut io.Writer, probe Probe) string {
	if _, err := os.Stat(DefaultPath()); os.IsNotExist(err) {
		// No LiteLLM here: nothing to sync, and no reason to probe providers.
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		return "LiteLLM routes not synced: " + err.Error()
	}
	err = RunSync(out, errOut, cfg, probe, false, false)
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
