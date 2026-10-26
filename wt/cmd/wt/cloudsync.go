// wt cloud-sync — refresh what two public services publish into
// registry.toml: OpenRouter's prices (the openrouter flow) and ollama.com's cloud
// catalog (the ollama flow, cloudsync_catalog.go). The planning is
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

// The flows' names: --only's spelling and the prefix of every line a flow
// prints. Each is named for the provider it syncs (held to a provider of the
// table by TestCloudFlowsMatchTheirProviders), but a flow name is not a
// provider id: code that looks a provider up uses provider.ID*.
const (
	flowOpenRouter = "openrouter"
	flowOllama     = "ollama"
)

// cloudFlow is what the command knows of a flow before it plans one: its
// name (also the id of the registry provider it syncs) and a way to reach the --only choice
// for it in cloudSyncOpts. The planners, gates and applies are typed per flow
// (openRouterRun, ollamaRun) and stay in their files; this table is where a
// flow is declared to the command line, so --only, its error text, the --only
// flag's help, the order of the flows and the order of the "not applied" lines
// (pendingFlows) come from one place (#453, #475). It does not drive
// cloudSyncOutcome, its err() (whose exit-code priority is semantic) or the
// flow descriptions in the command's Long text, which the docs and the skill
// quote: those stay hand-written until a third flow exists.
// TestCloudFlowsMatchTheirProviders holds each flow to a provider of the table
// in internal/provider.
type cloudFlow struct {
	name     string
	selected func(*cloudSyncOpts) *bool
}

// cloudFlows are the flows in the order they are planned and reported.
var cloudFlows = []cloudFlow{
	{flowOpenRouter, func(o *cloudSyncOpts) *bool { return &o.openrouter }},
	{flowOllama, func(o *cloudSyncOpts) *bool { return &o.ollama }},
}

// cloudSyncFlows are the flows' names, in cloudFlows' order: the flows
// `--only` selects among.
var cloudSyncFlows = func() []string {
	names := make([]string, len(cloudFlows))
	for i, f := range cloudFlows {
		names[i] = f.name
	}
	return names
}()

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
	openrouter, ollama bool
	dryRun, yes        bool
	// force and approve gate the removals of both flows; htmlFile is the
	// ollama flow's alone.
	force    bool
	htmlFile string
	approve  string
}

// approvedDigests reads --approve-removals: a comma list of removal digests,
// spaces and empty elements ignored. A flow's removals apply under its own
// digest, so one --yes run can name both.
func approvedDigests(flag string) []string {
	var out []string
	for _, d := range strings.Split(flag, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// selectFlows reads --only: a comma list of flow names, and every flow when
// the flag was not given. Given with nothing in it (`--only "$FLOWS"` with
// the variable unset) it is an error, never "every flow".
func selectFlows(only string, given bool, o *cloudSyncOpts) error {
	if !given {
		for _, f := range cloudFlows {
			*f.selected(o) = true
		}
		return nil
	}
	if strings.TrimSpace(only) == "" {
		return fmt.Errorf("--only: no flow named (valid: %s)", strings.Join(cloudSyncFlows, ", "))
	}
	for _, name := range strings.Split(only, ",") {
		name = strings.TrimSpace(name)
		i := slices.Index(cloudSyncFlows, name)
		if i < 0 {
			return fmt.Errorf("--only: unknown flow %q (valid: %s)", name, strings.Join(cloudSyncFlows, ", "))
		}
		*cloudFlows[i].selected(o) = true
	}
	return nil
}

// selectedFlows names the flows --only picked, in cloudSyncFlows' order.
func selectedFlows(o cloudSyncOpts) []string {
	var names []string
	for _, f := range cloudFlows {
		if *f.selected(&o) {
			names = append(names, f.name)
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
			"  openrouter  OpenRouter's per-token prices, for the models of the openrouter\n" +
			"              provider; models OpenRouter no longer lists are removed\n" +
			"  ollama      https://ollama.com/pricing, mirrored: prices (off-peak included),\n" +
			"              new cloud models added and pulled, models the page no longer\n" +
			"              lists removed from the registry and from ollama\n\n" +
			"Both plans are printed first, each line prefixed with its flow. --dry-run\n" +
			"stops there. Otherwise one confirmation covers both (asked on the terminal;\n" +
			"--yes skips it), the registry is written once, the ollama flow's pulls and\n" +
			"removals run, and the LiteLLM routes are synced once if a price, the set of\n" +
			"models or a pulled tag changed.\n\n" +
			"--yes never deletes on its own: a plan that removes anything (either flow)\n" +
			"is applied only with --approve-removals and the digest a dry run printed;\n" +
			"name both, comma-separated, when both flows remove. A model added with\n" +
			"`wt model add` is marked manual and kept until its source lists it.\n" +
			"Local models are never touched.\n\n" +
			"A flow whose service the registry does not use is skipped, never an error,\n" +
			"however it was asked for: with no OpenRouter-priced model the openrouter flow\n" +
			"says so, and with no ollama provider row the ollama flow says so. Each\n" +
			"prints one line, fetches nothing and counts as finished.\n\n" +
			"Exit status:\n" +
			"  0  every selected flow finished, or had nothing to do\n" +
			"  1  a step failed in either flow, or a usage error\n" +
			"  2  ollama changed nothing: the page, --html or `ollama list` could not\n" +
			"     be read, no cloud tag resolved, or the ollama row's base_url names no\n" +
			"     daemon\n" +
			"  3  ollama changed nothing: the pricing page changed shape (its HTML is\n" +
			"     saved, and the path printed)\n" +
			"  4  a flow changed nothing: it would remove more than half its entries,\n" +
			"     and --force was not given\n" +
			"  5  a flow changed nothing: its removals were not approved, or (ollama)\n" +
			"     the registry changed after its plan was printed\n" +
			"2 to 5 name a flow that changed nothing and win over 1 (the ollama flow's\n" +
			"over the openrouter flow's); the other flow may have been applied in the\n" +
			"same run.",
		Example: "  wt cloud-sync --dry-run\n" +
			"  wt cloud-sync --yes --approve-removals 0bcc560b956e\n" +
			"  wt cloud-sync --only openrouter --yes",
		Args: cobra.NoArgs,
		// A failed fetch or a refused plan is not a usage mistake, and main
		// prints the one error line.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := selectFlows(only, cmd.Flags().Changed("only"), &o); err != nil {
				return err
			}
			// An ollama-flow flag with the ollama flow left out would be ignored
			// in silence. (--approve-removals and --force gate both flows.)
			if cmd.Flags().Changed("html") && !o.ollama {
				// The flows --only selected, not the spelling it was
				// given: this line is meant to be read back and re-run.
				return fmt.Errorf("--html is for the ollama flow, which --only %s leaves out", strings.Join(selectedFlows(o), ", "))
			}
			// Only a config that could not be loaded stops this: a.cfg is
			// then an empty default, and a sync planned against it would
			// find nothing to keep. A validation gap does not.
			if a.loadErr != nil {
				return configError(a.loadErr)
			}
			return runCloudSync(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, o)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "Run only these flows: a comma list of "+strings.Join(cloudSyncFlows, ", ")+" (default: all)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Print the plans and change nothing")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Apply without asking (removals still need --approve-removals)")
	cmd.Flags().StringVar(&o.htmlFile, "html", "", "ollama: parse this saved pricing page instead of fetching it (cloud tags are still looked up on ollama.com/library)")
	cmd.Flags().StringVar(&o.approve, "approve-removals", "", "with --yes: the removal digest(s) a reviewed --dry-run printed, comma-separated when both flows remove")
	cmd.Flags().BoolVar(&o.force, "force", false, "apply even if more than half a flow's entries would be removed")
	return cmd
}

// flowOutcome is why a flow changed nothing: code is 2 to 5, or 0 when it did
// not stop that way. why, when set, is the reason given in place of
// cloudCodeMeaning's: exit 5 is also a plan that was approved and then found
// changed under the registry lock (the ollama flow).
type flowOutcome struct {
	code int
	why  string
}

// cloudSyncOutcome collects what the exit status is made of.
type cloudSyncOutcome struct {
	// failed: a step failed in either flow.
	failed bool
	// ollama is 2 to 5 when the ollama flow changed nothing (2 and 3 are
	// ollama's alone), else zero.
	ollama flowOutcome
	// openrouter is 4 or 5 when the openrouter flow changed nothing because
	// its removals were refused, else zero.
	openrouter flowOutcome
}

// cloudCodeMeaning is what each exit code 2 to 5 says about a flow that
// changed nothing, for either flow.
var cloudCodeMeaning = map[int]string{
	2: "an input could not be read",
	3: "the pricing page changed shape",
	4: "mass removal refused",
	5: "removals not approved",
}

// exitError is the command's result for a flow that changed nothing.
func (o flowOutcome) exitError(flow string) error {
	why := o.why
	if why == "" {
		why = cloudCodeMeaning[o.code]
	}
	return &exitCodeError{code: o.code, err: fmt.Errorf("cloud-sync: the %s flow changed nothing: %s", flow, why)}
}

// err is the command's result: nil, or an exitCodeError. An ollama-flow code wins
// over an openrouter one, and both over a failed step, as the codes are documented.
func (r *cloudSyncOutcome) err() error {
	switch {
	case r.ollama.code != 0:
		return r.ollama.exitError(flowOllama)
	case r.openrouter.code != 0:
		return r.openrouter.exitError(flowOpenRouter)
	case r.failed:
		return &exitCodeError{code: 1, err: errors.New("cloud-sync: a step failed; see the error lines above")}
	}
	return nil
}

// graduatedClause is the summary's "; graduated N manual model(s) to curated"
// for a flow that graduated any, else "".
func graduatedClause(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; graduated %d manual model(s) to curated", n)
}

// prefixLines writes text to w with every line prefixed by "<flow>: ".
func prefixLines(w io.Writer, flow, text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		fmt.Fprintf(w, "%s: %s\n", flow, line)
	}
}

// openRouterRun is the openrouter flow between its plan and its apply.
type openRouterRun struct {
	api     map[string]cloudsync.APIPrice
	plan    *cloudsync.PricePlan
	printed string
}

// planOpenRouterFlow fetches OpenRouter's list and prints the price plan. It
// returns nil when there is nothing to apply: no OpenRouter-priced model (one
// line, no fetch is made, res.failed stays false), a fetch that failed, or a
// plan the write would refuse (both reported, and res.failed set).
func planOpenRouterFlow(ctx context.Context, out, errOut io.Writer, doc *config.RegistryDoc, res *cloudSyncOutcome) *openRouterRun {
	entries := cloudsync.Entries(doc.Models())
	if !slices.ContainsFunc(entries, cloudsync.OpenRouterPriced) {
		fmt.Fprintln(out, "openrouter: no OpenRouter-priced model in the registry; nothing to refresh")
		return nil
	}
	// Check for duplicate model IDs before fetching: the write refuses an id
	// that appears more than once, so a plan with such a model can never be
	// applied. Fail fast to avoid a wasted network request.
	for _, e := range entries {
		if !cloudsync.OpenRouterPriced(e) {
			continue
		}
		if _, err := doc.Model(e.ID); err != nil {
			fmt.Fprintf(errOut, "openrouter: error: no price was changed: %v\n", err)
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
		fmt.Fprintf(errOut, "openrouter: error: could not read OpenRouter's prices: %v; no price was changed\n", err)
		res.failed = true
		return nil
	}
	run := &openRouterRun{api: api, plan: cloudsync.PlanPrices(entries, api)}
	run.printed = run.plan.Format()
	prefixLines(out, flowOpenRouter, run.printed)
	if run.plan.Withheld != "" {
		// Not part of run.printed: it explains an absence from the plan.
		fmt.Fprintln(out, run.plan.Withheld)
	}
	if run.plan.Candidates > 0 && len(run.plan.Matched) == 0 && len(run.plan.Removals) == 0 {
		// Not part of run.printed: it is advice, not the plan. With nothing
		// matched nothing is stamped, so the stale-pricing notice (which
		// names this command) is not cleared by running it again. Each
		// candidate has a warning in the plan above that says why.
		fmt.Fprintln(out, "openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; "+
			"the warnings above say why for each model")
	}
	return run
}

// removalPlan is the part of a price or catalog plan the removal gate reads.
type removalPlan interface {
	EntriesGone() int
	MassRemoval() bool
	RemovalDigest() string
}

// removalGateWording carries the per-flow text the shared removal gate
// prints. The mass-removal refusal reads "N of massTotal massNoun would be
// removed massNote — check massCheck": massNoun names what is counted,
// massTotal is the count the plan weighs its removals against, massNote is an
// aside about that count (the ollama flow does not count re-tagged entries),
// and massCheck is what to verify before forcing. reason is the unapproved
// removals' explanation.
type removalGateWording struct {
	massNoun  string
	massTotal int
	massNote  string
	massCheck string
	reason    string
}

// removalGate decides whether a printed removal plan may be applied: not a
// mass removal without --force, and under --yes not a plan that removes
// anything without the digest of a reviewed dry run. The openrouter and ollama
// flows share this decision and the exit codes it sets; the flow names the
// error lines and the code the refusal sets, and the wording carries what
// differs between flows.
func removalGate(errOut io.Writer, plan removalPlan, flow string, o cloudSyncOpts, setCode func(code int), w removalGateWording) bool {
	if plan.MassRemoval() && !o.force {
		fmt.Fprintf(errOut, "%s: error: %d of %d %s would be removed%s — check %s, then re-run with --force. Nothing was changed by the %s flow.\n",
			flow, plan.EntriesGone(), w.massTotal, w.massNoun, w.massNote, w.massCheck, flow)
		setCode(4)
		return false
	}
	if digest := plan.RemovalDigest(); o.yes && digest != "" && !slices.Contains(approvedDigests(o.approve), digest) {
		why := w.reason
		if o.approve != "" {
			why = "the removals are not the ones digest " + o.approve + " approved"
		}
		fmt.Fprintf(errOut, "%s: error: %s — review a --dry-run, then re-run with `--yes --approve-removals %s`. Nothing was changed by the %s flow.\n",
			flow, why, digest, flow)
		setCode(5)
		return false
	}
	return true
}

// openrouterGate decides whether a printed OpenRouter plan may be applied,
// as ollamaGate does for the ollama flow: not a mass removal without
// --force, and under --yes not a plan that removes anything without the
// digest of a reviewed dry run. It reports false, with the reason printed
// and res.openrouter.code set, when it may not.
func openrouterGate(errOut io.Writer, r *openRouterRun, o cloudSyncOpts, res *cloudSyncOutcome) bool {
	return removalGate(errOut, r.plan, flowOpenRouter, o, func(code int) { res.openrouter.code = code }, removalGateWording{
		massNoun:  "OpenRouter-priced models",
		massTotal: r.plan.RemovableCandidates,
		massCheck: "OpenRouter's list is complete",
		reason:    "the plan removes models",
	})
}

// cloudSyncApplied is what the registry write did. The apply function
// assigns it afresh on every run.
type cloudSyncApplied struct {
	openrouter cloudsync.PricesApplied
	ollama     cloudsync.CatalogApplied
	// openrouterStale, ollamaStale: the flow was to be applied, and its plan
	// made again under the registry lock was not the plan that was printed,
	// so nothing of it was applied.
	openrouterStale, ollamaStale bool
}

// errPlanChanged is what the apply function returns when no plan made under
// the registry lock is the one that was printed. It is an error, and not a
// write of nothing, so that config.UpdateRegistry stops there: an apply that
// succeeds creates a registry that is missing, and a refused plan must leave
// a registry removed since the plan as absent as it found it.
var errPlanChanged = errors.New("the registry changed after the plan was printed")

// pendingFlows names the flows in active, in cloudFlows' order: the order of
// the lines that concern all the flows about to be applied.
func pendingFlows(active map[string]bool) []string {
	var names []string
	for _, f := range cloudFlows {
		if active[f.name] {
			names = append(names, f.name)
		}
	}
	return names
}

// runCloudSync is `wt cloud-sync`: plan both flows with no lock held, print
// both plans, gate, apply both in one registry write, run the ollama flow's
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
	var openrouter *openRouterRun
	if o.openrouter {
		openrouter = planOpenRouterFlow(ctx, out, errOut, doc, &res)
	}
	var ollama *ollamaRun
	if o.ollama {
		ollama = planOllamaFlow(ctx, out, errOut, cfg, o, doc, &res)
	}
	if openrouter != nil && ollama != nil {
		if or, ol := openrouter.plan.RemovalDigest(), ollama.plan.RemovalDigest(); or != "" && ol != "" {
			fmt.Fprintf(out, "removals: approve both with --yes --approve-removals %s,%s\n", or, ol)
		}
	}
	if o.dryRun {
		return res.err()
	}

	// What is left to apply. A plan with nothing in it is not asked about.
	if openrouter != nil && (!openrouter.plan.HasWork() || !openrouterGate(errOut, openrouter, o, &res)) {
		openrouter = nil
	}
	if ollama != nil && (!ollama.plan.HasWork() || !ollamaGate(errOut, ollama, o, &res)) {
		ollama = nil
	}
	if openrouter == nil && ollama == nil {
		return res.err()
	}
	// The flows about to be applied, for the lines that concern all of them.
	pending := pendingFlows(map[string]bool{flowOpenRouter: openrouter != nil, flowOllama: ollama != nil})
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
	applied, err := writeCloudSync(openrouter, ollama, now)
	switch {
	case err == nil:
	case openrouter != nil && ollama != nil && errors.Is(err, config.ErrRegistryInvalid):
		// A row one flow must change would not load, and the writer refused
		// the write whole. The flows are independent: write each alone, so
		// the broken row holds up only the flow that owns it.
		alone, perr := writeCloudSync(openrouter, nil, now)
		applied.openrouter, applied.openrouterStale = alone.openrouter, alone.openrouterStale
		if perr != nil {
			fmt.Fprintf(errOut, "openrouter: error: the price changes were not written: %v\n", withRegistryHint(perr))
			res.failed, openrouter = true, nil
		}
		alone, cerr := writeCloudSync(nil, ollama, now)
		applied.ollama, applied.ollamaStale = alone.ollama, alone.ollamaStale
		if cerr != nil {
			fmt.Fprintf(errOut, "ollama: error: the ollama flow's changes were not written: %v\n", withRegistryHint(cerr))
			res.failed, ollama = true, nil
		}
	default:
		notApplied(errOut, "error: registry.toml was not changed: %v", withRegistryHint(err))
		res.failed = true
		return res.err()
	}

	if openrouter != nil {
		if applied.openrouterStale {
			fmt.Fprintln(errOut, "openrouter: error: the registry changed after the plan was printed; no price was changed — run it again")
			res.failed = true
		} else {
			fmt.Fprintf(out, "openrouter: refreshed %d model(s); %d price(s) changed", applied.openrouter.Stamped, applied.openrouter.Changed)
			fmt.Fprint(out, graduatedClause(applied.openrouter.Graduated))
			if applied.openrouter.Removed > 0 {
				fmt.Fprintf(out, "; removed %d", applied.openrouter.Removed)
			}
			fmt.Fprintln(out)
		}
	}
	if ollama != nil {
		if applied.ollamaStale {
			fmt.Fprintln(errOut, "ollama: error: the registry changed after the plan was printed, so this is no longer the plan that was approved; nothing was changed — run it again")
			res.ollama = flowOutcome{code: 5, why: errPlanChanged.Error()}
		} else {
			fmt.Fprintf(out, "ollama: updated %d, added %d and removed %d model(s)", applied.ollama.Updated, applied.ollama.Added, applied.ollama.Removed)
			fmt.Fprint(out, graduatedClause(applied.ollama.Graduated))
			fmt.Fprintln(out)
			runOllamaWork(ctx, out, errOut, ollama.origin, applied.ollama, &res)
		}
	}

	// A run that only stamped models changed no route: skip the sync, which
	// could restart the proxy for nothing. A run with ollama work syncs even
	// when the registry did not change: it may be finishing a run that was
	// interrupted after its registry write and before its sync, and the
	// registry alone no longer shows that the routes are behind.
	ollamaWork := len(applied.ollama.Pulls)+len(applied.ollama.Removes) > 0
	if applied.openrouter.Changed > 0 || applied.openrouter.Removed > 0 || applied.ollama.Changed() || ollamaWork {
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
// an openrouter plan with work in it never prints the same (it had a model), and
// an ollama plan is stale by rule when the ollama provider row is gone, even
// if it was all additions and so reads the same. A stale flow beside one
// that is applied therefore always means the file is still there.
func writeCloudSync(openrouter *openRouterRun, ollama *ollamaRun, now time.Time) (cloudSyncApplied, error) {
	var applied cloudSyncApplied
	_, err := config.UpdateRegistry(func(d *config.RegistryDoc) error {
		// Afresh on every run: apply may run more than once.
		applied = cloudSyncApplied{}
		entries, providers := cloudsync.Entries(d.Models()), cloudsync.Providers(d.Providers())
		var freshOpenRouter *cloudsync.PricePlan
		if openrouter != nil {
			freshOpenRouter = cloudsync.PlanPrices(entries, openrouter.api)
			applied.openrouterStale = freshOpenRouter.Format() != openrouter.printed
		}
		var freshOllama *cloudsync.CatalogPlan
		if ollama != nil {
			freshOllama = ollama.replan(entries)
			applied.ollamaStale = !hasOllamaProvider(providers) || freshOllama.Format() != ollama.printed
		}
		if (openrouter == nil || applied.openrouterStale) && (ollama == nil || applied.ollamaStale) {
			return errPlanChanged
		}
		if openrouter != nil && !applied.openrouterStale {
			done, err := freshOpenRouter.Apply(d, now)
			if err != nil {
				return err
			}
			applied.openrouter = done
		}
		if ollama != nil && !applied.ollamaStale {
			done, err := freshOllama.Apply(d, ollama.tags, now)
			if err != nil {
				return err
			}
			applied.ollama = done
		}
		return nil
	})
	if errors.Is(err, errPlanChanged) {
		return cloudSyncApplied{openrouterStale: openrouter != nil, ollamaStale: ollama != nil}, nil
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
