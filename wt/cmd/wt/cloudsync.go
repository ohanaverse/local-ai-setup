// wt cloud-sync — refresh what public services publish into registry.toml.
// One flow so far: OpenRouter's prices. The planning is internal/cloudsync's;
// this file owns the fetch, the confirmation, the one registry write, the
// route sync and the exit code.
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
// reaches the network or a terminal.
var (
	// cloudFetch GETs one public page: OpenRouter's model list.
	cloudFetch = realCloudFetch
	// confirmCloudSync asks the one question that covers both printed plans.
	confirmCloudSync = promptCloudSync
	// cloudSyncNow is the clock the stamps and the saved-page name read.
	cloudSyncNow = time.Now
)

// cloudSyncFlows are the flows `--only` selects among, in the order they are
// planned and reported.
var cloudSyncFlows = []string{"prices"}

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
	prices      bool
	dryRun, yes bool
}

// selectFlows reads --only: a comma list of flow names, every flow when it
// is empty.
func selectFlows(only string, o *cloudSyncOpts) error {
	if strings.TrimSpace(only) == "" {
		o.prices = true
		return nil
	}
	for _, name := range strings.Split(only, ",") {
		switch name = strings.TrimSpace(name); name {
		case "prices":
			o.prices = true
		default:
			return fmt.Errorf("--only: unknown flow %q (valid: %s)", name, strings.Join(cloudSyncFlows, ", "))
		}
	}
	return nil
}

func cloudSyncCmd(a *app) *cobra.Command {
	var (
		o    cloudSyncOpts
		only string
	)
	cmd := &cobra.Command{
		Use:   "cloud-sync",
		Short: "Refresh cloud prices (OpenRouter) into registry.toml",
		Long: "Bring registry.toml's prices up to date with what OpenRouter publishes, for\n" +
			"the models priced by OpenRouter.\n\n" +
			"The plan is printed first, each line prefixed with its flow (prices:).\n" +
			"--dry-run stops there. Otherwise one confirmation is asked on the terminal\n" +
			"(--yes skips it), the registry is written once, and the LiteLLM routes are\n" +
			"synced once if a price changed.\n\n" +
			"Exit status: 0 when the flow finished or had nothing to do, 1 when a step\n" +
			"failed or on a usage error.",
		Example: "  wt cloud-sync --dry-run\n" +
			"  wt cloud-sync --yes",
		Args: cobra.NoArgs,
		// A failed fetch or a refused plan is not a usage mistake, and main
		// prints the one error line.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := selectFlows(only, &o); err != nil {
				return err
			}
			// Only a config that could not be loaded stops this: a.cfg is
			// then an empty default, and a sync planned against it would
			// find nothing to keep. A validation gap does not.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runCloudSync(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, o)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "Run only these flows: a comma list of "+strings.Join(cloudSyncFlows, ", ")+" (default: all)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the plan and change nothing")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Apply without asking")
	return cmd
}

// cloudSyncOutcome collects what the exit status is made of.
type cloudSyncOutcome struct {
	// failed: a step failed.
	failed bool
}

// err is the command's result: nil, or an exitCodeError.
func (r *cloudSyncOutcome) err() error {
	if r.failed {
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
// returns nil when there is nothing to apply: no OpenRouter-priced model (no
// fetch is made), or a fetch that failed (reported, and res.failed set).
func planPricesFlow(ctx context.Context, out, errOut io.Writer, entries []cloudsync.Entry, providers []cloudsync.Provider, res *cloudSyncOutcome) *pricesRun {
	if !slices.ContainsFunc(entries, func(e cloudsync.Entry) bool { return cloudsync.OpenRouterPriced(e, providers) }) {
		fmt.Fprintln(out, "prices: no OpenRouter-priced model in the registry; nothing to refresh")
		return nil
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
	return run
}

// cloudSyncApplied is what the one registry write did. The apply function
// assigns it afresh on every run.
type cloudSyncApplied struct {
	prices      cloudsync.PricesApplied
	pricesStale bool
}

// runCloudSync is `wt cloud-sync`: plan with no lock held, print the plan,
// confirm, apply in one registry write, sync the routes once.
func runCloudSync(ctx context.Context, out, errOut io.Writer, _ *config.Config, o cloudSyncOpts) error {
	doc, err := config.ReadRegistryDoc()
	if err != nil {
		return withRegistryHint(err)
	}
	entries, providers := cloudsync.Entries(doc.Models()), cloudsync.Providers(doc.Providers())

	var res cloudSyncOutcome
	var prices *pricesRun
	if o.prices {
		prices = planPricesFlow(ctx, out, errOut, entries, providers, &res)
	}
	if o.dryRun {
		return res.err()
	}

	// What is left to apply. A plan with nothing in it is not asked about.
	if prices != nil && !prices.plan.HasWork() {
		prices = nil
	}
	if prices == nil {
		return res.err()
	}
	// The flows about to be applied, for the lines that concern all of them.
	pending := []string{"prices"}
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
	var applied cloudSyncApplied
	_, err = config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Afresh on every run: apply may run more than once. The plan is
		// made again from the file as it is under the lock, and applied only
		// if it is still the plan that was printed.
		applied = cloudSyncApplied{}
		entries, providers := cloudsync.Entries(d.Models()), cloudsync.Providers(d.Providers())
		fresh := cloudsync.PlanPrices(entries, providers, prices.api)
		if applied.pricesStale = fresh.Format() != prices.printed; !applied.pricesStale {
			done, err := fresh.Apply(d, now)
			if err != nil {
				return err
			}
			applied.prices = done
		}
		return nil
	})
	if err != nil {
		notApplied(errOut, "error: registry.toml was not changed: %v", withRegistryHint(err))
		res.failed = true
		return res.err()
	}

	if applied.pricesStale {
		fmt.Fprintln(errOut, "prices: error: the registry changed after the plan was printed; no price was changed — run it again")
		res.failed = true
	} else {
		fmt.Fprintf(out, "prices: refreshed %d model(s); %d price(s) changed\n", applied.prices.Stamped, applied.prices.Changed)
	}

	// A run that only stamped models changed no route: skip the sync, which
	// could restart the proxy for nothing.
	if applied.prices.Changed > 0 {
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

// withRegistryHint is err with the repair config names for it, when it names
// one.
func withRegistryHint(err error) error {
	if hint := config.RegistryFixHint(err); hint != "" {
		return fmt.Errorf("%w (%s)", err, hint)
	}
	return err
}
