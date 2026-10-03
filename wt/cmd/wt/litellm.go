// wt litellm — the primitives that manage LiteLLM's config.yaml and the proxy.
// wt owns this since the 2026-09-21 ownership move; modelman shells out to
// these commands (see docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/spf13/cobra"
)

type litellmOutcomeJSON struct {
	ID     string `json:"id"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

type litellmResultJSON struct {
	Outcomes []litellmOutcomeJSON `json:"outcomes"`
	Changed  bool                 `json:"changed"`
	Warnings []string             `json:"warnings"`
}

var errLitellmIDFailed = errors.New("one or more models could not be applied")

// reportLitellm prints a Result (text or JSON) and returns errLitellmIDFailed
// when any id was rejected, so the process exits 1 while the per-id detail is
// still printed for callers that parse it.
func reportLitellm(out, errOut io.Writer, res litellm.Result, asJSON bool) error {
	failed := false
	doc := litellmResultJSON{Outcomes: []litellmOutcomeJSON{}, Changed: res.Changed, Warnings: append([]string{}, res.Warnings...)}
	for _, o := range res.Outcomes {
		j := litellmOutcomeJSON{ID: o.ID, Action: o.Action}
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
		return errLitellmIDFailed
	}
	return nil
}

type syncPlanJSON struct {
	DryRun bool `json:"dry_run"`
	Plan   struct {
		Add     []string             `json:"add"`
		Adopt   []string             `json:"adopt"`
		Rewrite []string             `json:"rewrite"`
		Remove  []string             `json:"remove"`
		Errors  []litellmOutcomeJSON `json:"errors"`
	} `json:"plan"`
	Warnings []string `json:"warnings"`
}

// reportSyncPlan prints a dry-run plan plus the probe warnings the real sync
// would report, in both renderings; exit status 1 when any desired id could
// not be built, like a real sync.
func reportSyncPlan(out, errOut io.Writer, plan litellm.SyncPlan, warnings []string, asJSON bool) error {
	doc := syncPlanJSON{DryRun: true, Warnings: append([]string{}, warnings...)}
	doc.Plan.Add = append([]string{}, plan.Add...)
	doc.Plan.Adopt = append([]string{}, plan.Adopt...)
	doc.Plan.Rewrite = append([]string{}, plan.Rewrite...)
	doc.Plan.Remove = append([]string{}, plan.Remove...)
	doc.Plan.Errors = []litellmOutcomeJSON{}
	for _, e := range plan.Errors {
		doc.Plan.Errors = append(doc.Plan.Errors, litellmOutcomeJSON{ID: e.ID, Error: e.Err.Error()})
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
		for _, e := range doc.Plan.Errors {
			fmt.Fprintf(errOut, "%s: %s\n", e.ID, e.Error)
		}
		for _, w := range doc.Warnings {
			fmt.Fprintf(errOut, "warning: %s\n", w)
		}
	}
	if len(plan.Errors) > 0 {
		return errLitellmIDFailed
	}
	return nil
}

func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error {
	snap := probeInventory(cfg)
	// The local models to route: running ones, plus pulled registry ollama
	// models (desiredLocalIDs). That set is untrustworthy for a family whose
	// probe did not fully succeed; leave its models' routes exactly as they
	// are. The exception is a server that refused the connection: nothing is
	// listening, so nothing is running, and its routes are stale and must go
	// (the case a provider stopped outside wt leaves behind).
	desired := desiredLocalIDs(snap)
	untouched, probeWarns := syncUntouchedAndWarnings(cfg, snap, routedIDSet())
	o := litellm.Options{
		Untouched: untouched,
		// The probe above predates the config.yaml lock; a start that lands
		// in between must not lose its route, so removals are re-verified
		// under the lock.
		Recheck: func() []string { return desiredLocalIDs(probeInventory(cfg)) },
	}
	if dryRun {
		plan, err := litellm.PlanSync(cfg, desired, o)
		if err != nil {
			return err
		}
		return reportSyncPlan(out, errOut, plan, probeWarns, asJSON)
	}
	res, err := litellm.Sync(cfg, desired, o)
	if err != nil {
		return err
	}
	res.Warnings = append(res.Warnings, probeWarns...)
	return reportLitellm(out, errOut, res, asJSON)
}

// syncUntouchedAndWarnings derives both sync modes' view of the provider
// probes: the ids whose routes must not move (families the probe could not
// vouch for), and the warnings the run reports. One helper for the real sync
// and the dry run, so the two always report the same warnings — the dry run
// used to differ from the real run's output, promising a clean run that then
// arrived degraded.
//
// A family whose server REFUSED the connection gets no untouched entries
// (nothing is listening, so nothing is running and its local routes are
// stale). It gets a warning only when that matters: a local route of the
// family is in config.yaml (routed holds its ids) and is about to go, or the
// family serves registry cloud models, which sync still routes though they
// dial the same refused daemon — a failure that must be reported, not
// swallowed. A stopped provider with neither is the everyday case and stays
// silent.
func syncUntouchedAndWarnings(cfg *config.Config, snap localmodels.Snapshot, routed map[string]bool) (untouched, warnings []string) {
	skipped := map[string]bool{}
	for _, m := range litellm.LocalModels(cfg) {
		fam := localmodels.Family(m.ProviderID)
		if fam == "" {
			// No probe exists for this provider (retired llamacpp), so
			// nothing can vouch for its state: leave it, with no probe warning.
			untouched = append(untouched, m.ID)
			continue
		}
		if snap.Providers[fam] != localmodels.StatusOK && !snap.Down[fam] {
			untouched = append(untouched, m.ID)
			skipped[fam] = true
		}
	}
	fams := make([]string, 0, len(skipped))
	for f := range skipped {
		fams = append(fams, f)
	}
	sort.Strings(fams)
	for _, f := range fams {
		if snap.Ambiguous[f] {
			// The probe answered; it is the served model that cannot be
			// tied to one registered pairing, not a probe failure. Worded for
			// both shapes (a foreign id, and an empty list): "is serving"
			// would be a lie in the second.
			warnings = append(warnings, fmt.Sprintf("provider %q answered, but wt cannot tell which of its registered models it is serving (status %q); its model routes were left unchanged", f, snap.Providers[f]))
			continue
		}
		warnings = append(warnings, fmt.Sprintf("provider %q probe did not succeed (status %q); its model routes were left unchanged", f, snap.Providers[f]))
	}
	downs := make([]string, 0, len(snap.Down))
	for f := range snap.Down {
		if snap.Down[f] {
			downs = append(downs, f)
		}
	}
	sort.Strings(downs)
	inFamily := func(f string, ms []config.Model, keep func(config.Model) bool) bool {
		return slices.ContainsFunc(ms, func(m config.Model) bool { return localmodels.Family(m.ProviderID) == f && keep(m) })
	}
	for _, f := range downs {
		local := inFamily(f, litellm.LocalModels(cfg), func(m config.Model) bool { return routed[m.ID] })
		cloud := inFamily(f, litellm.CloudModels(cfg), func(config.Model) bool { return true })
		if !local && !cloud {
			continue
		}
		msg := fmt.Sprintf("provider %q refused the probe connection: nothing is listening there", f)
		if local {
			msg += ", so its local routes are treated as stale"
		}
		if cloud {
			msg += "; its cloud models stay routed but fail until it is back up"
		}
		warnings = append(warnings, msg)
	}
	return untouched, warnings
}

// routedIDSet is the set of model_name ids in config.yaml, for the probe
// warnings. An unreadable file yields an empty set: sync itself then refuses
// the file with the real error.
func routedIDSet() map[string]bool {
	out := map[string]bool{}
	f, err := litellm.Open(litellm.DefaultPath())
	if err != nil {
		return out
	}
	for _, r := range f.Rows() {
		out[r.ID] = true
	}
	return out
}

// desiredLocalIDs lists the registered local model ids sync should route:
// every one a probe found running, plus every registered model of a family
// whose routes follow its artifact (ollama) that the probe saw pulled, loaded
// or not — ollama serves a pulled model on request (#179). Pulled counts only
// when the family's probe is fully OK: a partial probe (e.g. /api/ps refused
// after /api/tags answered — the daemon died) cannot vouch that anything is
// serving. An unknown artifact (ArtifactKnown false) never counts as pulled.
// The real sync, its dry run and the under-lock Recheck all call this one
// function so they cannot drift.
func desiredLocalIDs(snap localmodels.Snapshot) []string {
	var desired []string
	for _, e := range snap.Entries {
		if !e.Registered {
			continue
		}
		fam := localmodels.Family(e.ProviderID)
		pulled := e.ArtifactKnown && e.Artifact != "" &&
			localmodels.RoutesFollowArtifact(fam) &&
			snap.Providers[fam] == localmodels.StatusOK
		if e.Running || pulled {
			desired = append(desired, e.ModelID)
		}
	}
	return desired
}

func runLitellmList(out io.Writer, asJSON bool) error {
	f, err := litellm.Open(litellm.DefaultPath())
	if err != nil {
		return err
	}
	rows := f.Rows()
	ids := make([]string, 0, len(rows))
	type rowJSON struct {
		ID      string `json:"id"`
		Managed bool   `json:"managed"`
	}
	jrows := make([]rowJSON, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
		jrows = append(jrows, rowJSON{r.ID, r.Managed})
	}
	if asJSON {
		return json.NewEncoder(out).Encode(map[string]any{"routed": ids, "rows": jrows})
	}
	// A wt row prints as its bare id, as list always has, so `grep -x <id>`
	// keeps working; only a hand-written row carries a marker.
	for _, r := range rows {
		if r.Managed {
			fmt.Fprintln(out, r.ID)
		} else {
			fmt.Fprintf(out, "%s\t(hand-written)\n", r.ID)
		}
	}
	return nil
}

func runLitellmProviders(out io.Writer, asJSON bool) error {
	provs := litellm.Providers()
	if asJSON {
		doc := map[string]map[string]map[string]bool{"providers": {}}
		for id, cloud := range provs {
			doc["providers"][id] = map[string]bool{"cloud": cloud}
		}
		return json.NewEncoder(out).Encode(doc)
	}
	ids := make([]string, 0, len(provs))
	for id := range provs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(out, "%s cloud=%v\n", id, provs[id])
	}
	return nil
}

func runLitellmStatus(out io.Writer, cfg *config.Config, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(map[string]any{
			"enabled": cfg.IsLitellm(), "url": cfg.LitellmBaseURL(), "api_key_set": cfg.LitellmAPIKey() != "",
		})
	}
	mode := "off"
	if cfg.IsLitellm() {
		mode = "on"
	}
	key := "(unset)"
	if k := cfg.LitellmAPIKey(); k != "" {
		key = "***"
		if len(k) > 4 { // a short key's last-4 would be the whole secret
			key += k[len(k)-4:]
		}
	}
	url := cfg.LitellmBaseURL()
	if url == "" {
		url = "(unset)"
	}
	fmt.Fprintf(out, "litellm: %s\n  url: %s\n  api_key: %s\n", mode, url, key)
	return nil
}

func runLitellmToggle(out, errOut io.Writer, cfg *config.Config, on bool) error {
	if err := cfg.UpdateLitellm(func(s *config.LitellmState) { s.Enabled = on }); err != nil {
		return err
	}
	if on {
		fmt.Fprintln(out, "litellm: on")
		if !cfg.LitellmConfigured() {
			fmt.Fprintln(errOut, "warning: litellm.url or litellm.api_key is not set — wt will fail at launch time; run 'wt litellm set --url ... --api-key ...'")
		}
		return nil
	}
	fmt.Fprintln(out, "litellm: off")
	if !cfg.LitellmConfigured() {
		fmt.Fprintln(errOut, "warning: litellm.url or litellm.api_key is not set — agents whose protocols force LiteLLM will still fail to launch")
	}
	return nil
}

func runLitellmSet(out io.Writer, cfg *config.Config, url, key *string) error {
	if err := cfg.UpdateLitellm(func(s *config.LitellmState) {
		if url != nil {
			s.URL = *url
		}
		if key != nil {
			s.APIKey = *key
		}
	}); err != nil {
		return err
	}
	fmt.Fprintln(out, "litellm: updated")
	return nil
}

func litellmCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "litellm",
		Short: "Manage LiteLLM routes and routing state for registry models",
	}
	var syncJSON, listJSON, provJSON, syncDryRun bool
	syncC := &cobra.Command{
		Use: "sync", Short: "Make LiteLLM routes match the registry's cloud models and the running (or pulled ollama) local models", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.loadErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.loadErr)
			}
			return runLitellmSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, syncJSON, syncDryRun)
		},
	}
	syncC.Flags().BoolVar(&syncJSON, "json", false, "machine-readable output")
	syncC.Flags().BoolVar(&syncDryRun, "dry-run", false, "print the route plan and the probe warnings the real sync would report; change nothing (does not report litellm_settings fixes)")
	listC := &cobra.Command{
		Use: "list", Short: "List routed model ids from config.yaml", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLitellmList(cmd.OutOrStdout(), listJSON) },
	}
	listC.Flags().BoolVar(&listJSON, "json", false, "machine-readable output")
	provC := &cobra.Command{
		Use: "providers", Short: "List providers with a LiteLLM mapping", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLitellmProviders(cmd.OutOrStdout(), provJSON) },
	}
	provC.Flags().BoolVar(&provJSON, "json", false, "machine-readable output")
	cfgGuard := func(run func(cmd *cobra.Command) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			// Gate on the LOAD error only: these commands touch just wt's own
			// [litellm] state, so a registry validation gap must not lock the
			// user out. A genuine load failure leaves a default cfg that Save
			// would write over config.toml.
			if a.loadErr != nil {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.loadErr)
			}
			return run(cmd)
		}
	}
	var statusJSON bool
	statusC := &cobra.Command{
		Use: "status", Short: "Show the LiteLLM routing state (key is never printed)", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: cfgGuard(func(cmd *cobra.Command) error { return runLitellmStatus(cmd.OutOrStdout(), a.cfg, statusJSON) }),
	}
	statusC.Flags().BoolVar(&statusJSON, "json", false, "machine-readable output")
	onC := &cobra.Command{
		Use: "on", Short: "Route agents through LiteLLM", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: cfgGuard(func(cmd *cobra.Command) error {
			return runLitellmToggle(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, true)
		}),
	}
	offC := &cobra.Command{
		Use: "off", Short: "Stop routing agents through LiteLLM (proxy untouched)", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: cfgGuard(func(cmd *cobra.Command) error {
			return runLitellmToggle(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, false)
		}),
	}
	var setURL, setKey string
	setC := &cobra.Command{
		Use: "set", Short: "Set the LiteLLM proxy url and/or api key", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: cfgGuard(func(cmd *cobra.Command) error {
			var u, k *string
			if cmd.Flags().Changed("url") {
				u = &setURL
			}
			if cmd.Flags().Changed("api-key") {
				k = &setKey
			}
			if u == nil && k == nil {
				return errors.New("nothing to set: pass --url and/or --api-key")
			}
			return runLitellmSet(cmd.OutOrStdout(), a.cfg, u, k)
		}),
	}
	setC.Flags().StringVar(&setURL, "url", "", "LiteLLM proxy base URL")
	setC.Flags().StringVar(&setKey, "api-key", "", "LiteLLM proxy API key")
	c.AddCommand(
		syncC, listC, provC, statusC, onC, offC, setC,
		removedLitellmCmd("expose"), removedLitellmCmd("unexpose"),
	)
	return c
}

// removedCmdAnnotation marks a hidden stub that only reports a removed
// command; its value names the issue that removed it.
const removedCmdAnnotation = "wt-removed"

// removedLitellmCmd is a hidden stub for a subcommand #179 removed. Without
// it cobra prints the parent's help and exits 0 for the unknown name, so a
// script running `wt litellm expose X && ...` would "succeed" having written
// nothing. Flag parsing is off so legacy flags (--json, --dry-run,
// --skip-ready-gate) reach the same pointer to sync instead of "unknown flag".
// The removedCmdAnnotation marks the Hidden as deliberate for
// TestNoWtCommandIsHidden.
func removedLitellmCmd(name string) *cobra.Command {
	return &cobra.Command{
		Use: name, Hidden: true, DisableFlagParsing: true, SilenceUsage: true,
		Annotations: map[string]string{removedCmdAnnotation: "#179"},
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("`wt litellm %s` was removed (#179): configured cloud models and running local models are routed automatically — run `wt litellm sync`", name)
		},
	}
}
