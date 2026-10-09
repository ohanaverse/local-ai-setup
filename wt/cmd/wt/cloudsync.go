// wt cloud-sync — refresh what two public services publish into
// registry.toml: OpenRouter's prices (the prices flow) and ollama.com's cloud
// catalog (the catalog flow, cloudsync_catalog.go). The planning is
// internal/cloudsync's; this file owns the fetches, the confirmation, the one
// registry write, the route sync and the exit code.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// Test seams. cmd/wt's TestMain makes all of them fail closed, so no test
// reaches the network, a terminal or the developer's ollama (the ollama CLI's
// own seam is in cloudsync_ollama.go).
var (
	// cloudFetch GETs one public page: OpenRouter's model list,
	// ollama.com/pricing, an ollama.com/library tags page.
	cloudFetch = realCloudFetch
	// confirmCloudSync asks the one question that covers both printed plans.
	confirmCloudSync = promptCloudSync
	// cloudSyncNow is the clock the stamps and the saved-page name read.
	cloudSyncNow = time.Now
)

// cloudSyncFlows are the flows `--only` selects among, in the order they are
// planned and reported.
var cloudSyncFlows = []string{"prices", "catalog"}

var cloudHTTP = &http.Client{Timeout: 30 * time.Second}

// cloudFetchLimit bounds one response. OpenRouter's model list, the largest,
// is under 1 MiB.
const cloudFetchLimit = 32 << 20

func realCloudFetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cloudHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, cloudFetchLimit))
}

// promptCloudSync asks on the controlling terminal and defaults to No, like
// promptStop: piped input can never approve a sync. Without a terminal it
// names the flag that applies without asking. The terminal is opened through
// openTTY (launch.go), the seam a test takes it away with.
func promptCloudSync(question string) (bool, error) {
	f, err := openTTY()
	if err != nil {
		return false, errors.New("there is no terminal to confirm on — rerun with --yes to apply without asking")
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return askYesNo(f)
}

// cloudSyncOpts is the parsed command line.
type cloudSyncOpts struct {
	prices, catalog bool
	dryRun, yes     bool
	// Catalog flow only.
	force    bool
	htmlFile string
	approve  string
}

// selectFlows reads --only: a comma list of flow names, and every flow when
// the flag was not given. Given with nothing in it (`--only "$FLOWS"` with
// the variable unset) it is an error, never "every flow".
func selectFlows(only string, given bool, o *cloudSyncOpts) error {
	if !given {
		o.prices, o.catalog = true, true
		return nil
	}
	if strings.TrimSpace(only) == "" {
		return fmt.Errorf("--only: no flow named (valid: %s)", strings.Join(cloudSyncFlows, ", "))
	}
	for _, name := range strings.Split(only, ",") {
		switch name = strings.TrimSpace(name); name {
		case "prices":
			o.prices = true
		case "catalog":
			o.catalog = true
		default:
			return fmt.Errorf("--only: unknown flow %q (valid: %s)", name, strings.Join(cloudSyncFlows, ", "))
		}
	}
	return nil
}

// selectedFlows names the flows --only picked, in cloudSyncFlows' order.
func selectedFlows(o cloudSyncOpts) []string {
	picked := map[string]bool{"prices": o.prices, "catalog": o.catalog}
	var names []string
	for _, flow := range cloudSyncFlows {
		if picked[flow] {
			names = append(names, flow)
		}
	}
	return names
}

func cloudSyncCmd(a *app) *cobra.Command {
	var (
		o    cloudSyncOpts
		only string
	)
	cmd := &cobra.Command{
		Use:   "cloud-sync",
		Short: "Refresh cloud prices (OpenRouter) and the ollama cloud catalog into registry.toml",
		Long: "Bring registry.toml up to date with what two public services publish. Two\n" +
			"independent flows, both run unless --only picks one:\n\n" +
			"  prices   OpenRouter's per-token prices, for the models priced by OpenRouter\n" +
			"  catalog  https://ollama.com/pricing, mirrored: prices (off-peak included),\n" +
			"           new cloud models added and pulled, models the page no longer\n" +
			"           lists removed from the registry and from ollama\n\n" +
			"Both plans are printed first, each line prefixed with its flow. --dry-run\n" +
			"stops there. Otherwise one confirmation covers both (asked on the terminal;\n" +
			"--yes skips it), the registry is written once, the catalog's pulls and\n" +
			"removals run, and the LiteLLM routes are synced once if a price, the set of\n" +
			"models or a pulled tag changed.\n\n" +
			"--yes never deletes on its own: a catalog plan that removes anything is\n" +
			"applied only with --approve-removals and the digest a dry run printed.\n" +
			"Local models are never touched.\n\n" +
			"A flow whose service the registry does not use is skipped, never an error,\n" +
			"however it was asked for: with no OpenRouter-priced model the prices flow\n" +
			"says so, and with no ollama provider row the catalog flow says so. Each\n" +
			"prints one line, fetches nothing and counts as finished.\n\n" +
			"Exit status:\n" +
			"  0  every selected flow finished, or had nothing to do\n" +
			"  1  a step failed in either flow, or a usage error\n" +
			"  2  catalog changed nothing: the page, --html or `ollama list` could not\n" +
			"     be read, no cloud tag resolved, or the ollama row's base_url names no\n" +
			"     daemon\n" +
			"  3  catalog changed nothing: the pricing page changed shape (its HTML is\n" +
			"     saved, and the path printed)\n" +
			"  4  catalog changed nothing: it would remove more than half the ollama\n" +
			"     cloud entries, and --force was not given\n" +
			"  5  catalog changed nothing: its removals were not approved, or the\n" +
			"     registry changed after its plan was printed\n" +
			"2 to 5 are about the catalog flow only and win over 1; the prices flow may\n" +
			"have been applied in the same run.",
		Example: "  wt cloud-sync --dry-run\n" +
			"  wt cloud-sync --yes --approve-removals 0bcc560b956e\n" +
			"  wt cloud-sync --only prices --yes",
		Args: cobra.NoArgs,
		// A failed fetch or a refused plan is not a usage mistake, and main
		// prints the one error line.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := selectFlows(only, cmd.Flags().Changed("only"), &o); err != nil {
				return err
			}
			// A catalog flag with the catalog flow left out would be ignored
			// in silence, and an ignored --force or digest is a gate the
			// user believes was passed.
			for _, flag := range []string{"html", "approve-removals", "force"} {
				if cmd.Flags().Changed(flag) && !o.catalog {
					// The flows --only selected, not the spelling it was
					// given: this line is meant to be read back and re-run.
					return fmt.Errorf("--%s is for the catalog flow, which --only %s leaves out", flag, strings.Join(selectedFlows(o), ", "))
				}
			}
			// Only a config that could not be loaded stops this: a.cfg is
			// then an empty default, and a sync planned against it would
			// find nothing to keep. A validation gap does not.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			if a.cfg == nil {
				return errors.New("config not loaded")
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runCloudSync(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, o)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "Run only these flows: a comma list of "+strings.Join(cloudSyncFlows, ", ")+" (default: all)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the plans and change nothing")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Apply without asking (removals still need --approve-removals)")
	cmd.Flags().StringVar(&o.htmlFile, "html", "", "catalog: parse this saved pricing page instead of fetching it (cloud tags are still looked up on ollama.com/library)")
	cmd.Flags().StringVar(&o.approve, "approve-removals", "", "catalog, with --yes: the removal digest a reviewed --dry-run printed")
	cmd.Flags().BoolVar(&o.force, "force", false, "catalog: apply even if more than half the ollama cloud entries would be removed")
	return cmd
}

// cloudSyncOutcome collects what the exit status is made of.
type cloudSyncOutcome struct {
	// failed: a step failed in either flow.
	failed bool
	// catalogCode is 2 to 5 when the catalog flow changed nothing, else 0.
	catalogCode int
	// catalogWhy, when set, is the reason given for catalogCode in place of
	// catalogCodeMeaning's: exit 5 is also a plan that was approved and then
	// found changed under the registry lock.
	catalogWhy string
}

var catalogCodeMeaning = map[int]string{
	2: "an input could not be read",
	3: "the pricing page changed shape",
	4: "mass removal refused",
	5: "removals not approved",
}

// err is the command's result: nil, or an exitCodeError. A catalog code wins
// over a failed step, as the codes are documented.
func (r *cloudSyncOutcome) err() error {
	switch {
	case r.catalogCode != 0:
		why := r.catalogWhy
		if why == "" {
			why = catalogCodeMeaning[r.catalogCode]
		}
		return &exitCodeError{code: r.catalogCode, err: fmt.Errorf("cloud-sync: the catalog flow changed nothing: %s", why)}
	case r.failed:
		return &exitCodeError{code: 1, err: errors.New("cloud-sync: a step failed; see the error lines above")}
	}
	return nil
}

// prefixLines writes text to w with every line prefixed by "<flow>: ".
func prefixLines(w io.Writer, flow, text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(w, "%s: %s\n", flow, line)
	}
}

// pricesRun is the prices flow between its plan and its apply.
type pricesRun struct {
	api     map[string]cloudsync.APIPrice
	plan    *cloudsync.PricePlan
	printed string
}

// planPricesFlow fetches OpenRouter's list and prints the price plan. It
// returns nil when there is nothing to apply: no OpenRouter-priced model (one
// line, no fetch is made, res.failed stays false), a fetch that failed, or a
// plan the write would refuse (both reported, and res.failed set).
func planPricesFlow(ctx context.Context, out, errOut io.Writer, doc *config.RegistryDoc, res *cloudSyncOutcome) *pricesRun {
	entries, providers := cloudsync.Entries(doc.Models()), cloudsync.Providers(doc.Providers())
	if !slices.ContainsFunc(entries, func(e cloudsync.Entry) bool { return cloudsync.OpenRouterPriced(e, providers) }) {
		fmt.Fprintln(out, "prices: no OpenRouter-priced model in the registry; nothing to refresh")
		return nil
	}
	// Check for duplicate model IDs before fetching: the write refuses an id
	// that appears more than once, so a plan with such a model can never be
	// applied. Fail fast to avoid a wasted network request.
	for _, e := range entries {
		if !cloudsync.OpenRouterPriced(e, providers) {
			continue
		}
		if _, err := doc.Model(e.ID); err != nil {
			fmt.Fprintf(errOut, "prices: error: no price was changed: %v\n", err)
			res.failed = true
			return nil
		}
	}
	body, err := cloudFetch(ctx, cloudsync.OpenRouterModelsURL)
	var api map[string]cloudsync.APIPrice
	if err == nil {
		api, err = cloudsync.ParseOpenRouter(body)
	}
	if err != nil {
		fmt.Fprintf(errOut, "prices: error: could not read OpenRouter's prices: %v; no price was changed\n", err)
		res.failed = true
		return nil
	}
	run := &pricesRun{api: api, plan: cloudsync.PlanPrices(entries, providers, api)}
	run.printed = run.plan.Format()
	prefixLines(out, "prices", run.printed)
	if run.plan.Candidates > 0 && len(run.plan.Matched) == 0 {
		// Not part of run.printed: it is advice, not the plan. With nothing
		// matched nothing is stamped, so the stale-pricing notice (which
		// names this command) is not cleared by running it again.
		fmt.Fprintln(out, "prices: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; "+
			"set openrouter_priced = false on a provider whose model names are not OpenRouter ids")
	}
	return run
}

// cloudSyncApplied is what the registry write did. The apply function
// assigns it afresh on every run.
type cloudSyncApplied struct {
	prices  cloudsync.PricesApplied
	catalog cloudsync.CatalogApplied
	// pricesStale, catalogStale: the flow was to be applied, and its plan
	// made again under the registry lock was not the plan that was printed,
	// so nothing of it was applied.
	pricesStale, catalogStale bool
}

// errPlanChanged is what the apply function returns when no plan made under
// the registry lock is the one that was printed. It is an error, and not a
// write of nothing, so that config.UpdateRegistry stops there: an apply that
// succeeds creates a registry that is missing, and a refused plan must leave
// a registry removed since the plan as absent as it found it.
var errPlanChanged = errors.New("the registry changed after the plan was printed")

// runCloudSync is `wt cloud-sync`: plan both flows with no lock held, print
// both plans, gate, apply both in one registry write, run the catalog's
// ollama work, sync the routes once. The flows are independent: one that
// fails or is refused does not stop the other. (That is also the one case
// with a second write: when the shared write is refused because a row would
// not load, each flow is written alone, so only the flow that owns the row
// is held up.)
func runCloudSync(ctx context.Context, out, errOut io.Writer, cfg *config.Config, o cloudSyncOpts) error {
	doc, err := config.ReadRegistryDoc()
	if err != nil {
		return withRegistryHint(err)
	}

	var res cloudSyncOutcome
	var prices *pricesRun
	if o.prices {
		prices = planPricesFlow(ctx, out, errOut, doc, &res)
	}
	var catalog *catalogRun
	if o.catalog {
		catalog = planCatalogFlow(ctx, out, errOut, cfg, o, doc, &res)
	}
	if o.dryRun {
		return res.err()
	}

	// What is left to apply. A plan with nothing in it is not asked about.
	if prices != nil && !prices.plan.HasWork() {
		prices = nil
	}
	if catalog != nil && (!catalog.plan.HasWork() || !catalogGate(errOut, catalog, o, &res)) {
		catalog = nil
	}
	if prices == nil && catalog == nil {
		return res.err()
	}
	// The flows about to be applied, for the lines that concern all of them.
	var pending []string
	if prices != nil {
		pending = append(pending, "prices")
	}
	if catalog != nil {
		pending = append(pending, "catalog")
	}
	notApplied := func(w io.Writer, format string, args ...any) {
		for _, flow := range pending {
			fmt.Fprintf(w, "%s: %s\n", flow, fmt.Sprintf(format, args...))
		}
	}
	if !o.yes {
		ok, err := confirmCloudSync("Apply these changes?")
		if err != nil {
			notApplied(errOut, "error: not applied: %v", err)
			res.failed = true
			return res.err()
		}
		if !ok {
			notApplied(out, "not applied (declined)")
			return res.err()
		}
	}

	now := cloudSyncNow()
	applied, err := writeCloudSync(prices, catalog, now)
	switch {
	case err == nil:
	case prices != nil && catalog != nil && errors.Is(err, config.ErrRegistryInvalid):
		// A row one flow must change would not load, and the writer refused
		// the write whole. The flows are independent: write each alone, so
		// the broken row holds up only the flow that owns it.
		alone, perr := writeCloudSync(prices, nil, now)
		applied.prices, applied.pricesStale = alone.prices, alone.pricesStale
		if perr != nil {
			fmt.Fprintf(errOut, "prices: error: the price changes were not written: %v\n", withRegistryHint(perr))
			res.failed, prices = true, nil
		}
		alone, cerr := writeCloudSync(nil, catalog, now)
		applied.catalog, applied.catalogStale = alone.catalog, alone.catalogStale
		if cerr != nil {
			fmt.Fprintf(errOut, "catalog: error: the catalog's changes were not written: %v\n", withRegistryHint(cerr))
			res.failed, catalog = true, nil
		}
	default:
		notApplied(errOut, "error: registry.toml was not changed: %v", withRegistryHint(err))
		res.failed = true
		return res.err()
	}

	if prices != nil {
		if applied.pricesStale {
			fmt.Fprintln(errOut, "prices: error: the registry changed after the plan was printed; no price was changed — run it again")
			res.failed = true
		} else {
			fmt.Fprintf(out, "prices: refreshed %d model(s); %d price(s) changed\n", applied.prices.Stamped, applied.prices.Changed)
		}
	}
	if catalog != nil {
		if applied.catalogStale {
			fmt.Fprintln(errOut, "catalog: error: the registry changed after the plan was printed, so this is no longer the plan that was approved; nothing was changed — run it again")
			res.catalogCode, res.catalogWhy = 5, errPlanChanged.Error()
		} else {
			fmt.Fprintf(out, "catalog: updated %d, added %d and removed %d model(s)\n", applied.catalog.Updated, applied.catalog.Added, applied.catalog.Removed)
			runOllamaWork(ctx, out, errOut, catalog.origin, applied.catalog, &res)
		}
	}

	// A run that only stamped models changed no route: skip the sync, which
	// could restart the proxy for nothing. A run with ollama work syncs even
	// when the registry did not change: it may be finishing a run that was
	// interrupted after its registry write and before its sync, and the
	// registry alone no longer shows that the routes are behind.
	ollamaWork := len(applied.catalog.Pulls)+len(applied.catalog.Removes) > 0
	if applied.prices.Changed > 0 || applied.catalog.Changed() || ollamaWork {
		var routes, routeErrs bytes.Buffer
		warning := syncRoutesAfterWrite(&routes, &routeErrs)
		if routes.Len() > 0 {
			prefixLines(out, "routes", routes.String())
		}
		if routeErrs.Len() > 0 {
			prefixLines(errOut, "routes", routeErrs.String())
		}
		if warning != "" {
			fmt.Fprintf(errOut, "routes: warning: %s\n", warning)
		}
	}
	return res.err()
}

// writeCloudSync is the registry write: one config.UpdateRegistry for the
// flows it is given (either may be nil, not both). Each plan is made again
// from the file as it is under the lock, and applied only if it is still the
// plan that was printed; a flow whose plan changed comes back marked stale,
// and does not stop the other flow's write.
//
// When no flow is left to apply, the apply function returns errPlanChanged
// and nothing is written, which is what keeps a registry that was removed
// after the plan from being created empty: made again from no file at all,
// a prices plan with work in it never prints the same (it had a model), and
// a catalog plan is stale by rule when the ollama provider row is gone, even
// if it was all additions and so reads the same. A stale flow beside one
// that is applied therefore always means the file is still there.
func writeCloudSync(prices *pricesRun, catalog *catalogRun, now time.Time) (cloudSyncApplied, error) {
	var applied cloudSyncApplied
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Afresh on every run: apply may run more than once.
		applied = cloudSyncApplied{}
		entries, providers := cloudsync.Entries(d.Models()), cloudsync.Providers(d.Providers())
		var freshPrices *cloudsync.PricePlan
		if prices != nil {
			freshPrices = cloudsync.PlanPrices(entries, providers, prices.api)
			applied.pricesStale = freshPrices.Format() != prices.printed
		}
		var freshCatalog *cloudsync.CatalogPlan
		if catalog != nil {
			freshCatalog = catalog.replan(entries)
			applied.catalogStale = !hasOllamaProvider(providers) || freshCatalog.Format() != catalog.printed
		}
		if (prices == nil || applied.pricesStale) && (catalog == nil || applied.catalogStale) {
			return errPlanChanged
		}
		if prices != nil && !applied.pricesStale {
			done, err := freshPrices.Apply(d, now)
			if err != nil {
				return err
			}
			applied.prices = done
		}
		if catalog != nil && !applied.catalogStale {
			done, err := freshCatalog.Apply(d, catalog.tags, now)
			if err != nil {
				return err
			}
			applied.catalog = done
		}
		return nil
	})
	if errors.Is(err, errPlanChanged) {
		return cloudSyncApplied{pricesStale: prices != nil, catalogStale: catalog != nil}, nil
	}
	if err != nil {
		return cloudSyncApplied{}, err
	}
	return applied, nil
}

// withRegistryHint is err with the repair config names for it, when it names
// one.
func withRegistryHint(err error) error {
	if hint := config.RegistryFixHint(err); hint != "" {
		return fmt.Errorf("%w (%s)", err, hint)
	}
	return err
}
