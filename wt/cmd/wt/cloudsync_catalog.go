// The catalog flow of wt cloud-sync: mirror https://ollama.com/pricing into
// the registry's ollama cloud entries and into ollama itself. Everything that
// decides what changes is internal/cloudsync's; this file reads the page and
// `ollama list`, gates the plan, and runs the pulls and removals the registry
// write leaves (through cloudsync_ollama.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// saveFailedHTML keeps a page ParsePricing refused, so the parser can be
// repaired against exactly what ollama served. The file is always a new one,
// mode 0600: its name is predictable and TMPDIR may be shared, so whatever
// already has that name (a page saved in the same second, a planted link) is
// removed, never written into or through.
func saveFailedHTML(page string, now time.Time) (string, error) {
	path := filepath.Join(os.TempDir(), "ollama-pricing-"+now.Format("20060102-150405")+".html")
	create := func() (*os.File, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	f, err := create()
	if errors.Is(err, os.ErrExist) {
		if err = os.Remove(path); err == nil {
			f, err = create()
		}
	}
	if err != nil {
		return path, err
	}
	if _, err := f.WriteString(page); err != nil {
		f.Close()
		return path, err
	}
	return path, f.Close()
}

// catalogRun is the catalog flow between its plan and its apply: everything
// the plan was made from, so it can be made again under the registry lock.
type catalogRun struct {
	origin      string
	catalog     cloudsync.Catalog
	tags        []string
	resolved    map[string]string
	tagWarnings []string
	plan        *cloudsync.CatalogPlan
	printed     string
}

// replan makes the plan again from entries, with the same page, tags and
// resolved names.
func (c *catalogRun) replan(entries []cloudsync.Entry) *cloudsync.CatalogPlan {
	plan := cloudsync.PlanCatalog(entries, c.catalog, c.tags, c.resolved)
	plan.Warnings = append(plan.Warnings, c.tagWarnings...)
	return plan
}

// hasOllamaProvider reports whether the registry has an ollama provider row:
// the row every catalog entry names, and the one that says which daemon the
// ollama CLI is pinned to.
func hasOllamaProvider(providers []cloudsync.Provider) bool {
	return slices.ContainsFunc(providers, func(p cloudsync.Provider) bool { return p.ID == "ollama" })
}

// planCatalogFlow reads the page and `ollama list`, resolves the cloud tags
// and prints the catalog plan. It returns nil when there is nothing to apply:
// a registry with no ollama provider row (one line, nothing fetched,
// res untouched), or a flow that cannot go on, with res saying why. In every
// such case it has changed nothing.
func planCatalogFlow(ctx context.Context, out, errOut io.Writer, cfg *config.Config, o cloudSyncOpts,
	doc *config.RegistryDoc, res *cloudSyncOutcome) *catalogRun {
	stop := func(code int, format string, args ...any) *catalogRun {
		fmt.Fprintf(errOut, "catalog: error: "+format+"\n", args...)
		if code == 1 {
			res.failed = true
		} else {
			res.catalogCode = code
		}
		return nil
	}
	entries, providers := cloudsync.Entries(doc.Models()), cloudsync.Providers(doc.Providers())
	// A registry that does not use ollama has no catalog to mirror, however
	// the flow was asked for (--only catalog and the catalog's own flags
	// included): it is skipped, never an error. A new entry would be a row
	// with provider_id "ollama", which the registry writer refuses without
	// the provider row, and there is no daemon address to pin the CLI to.
	// `wt cloud-sync` never seeds the row.
	if !hasOllamaProvider(providers) {
		fmt.Fprintln(out, "catalog: no ollama provider in the registry; nothing to mirror")
		return nil
	}
	// Every ollama command is pinned to this address, and ollama reads an
	// empty or unusable OLLAMA_HOST as its default daemon: a base_url that
	// names no daemon ("/v1", a blank, no scheme) would send the pulls and
	// removals somewhere the registry does not say, in silence. (A row with
	// no base_url at all has wt's documented default, which is an address.)
	origin, _ := localmodels.FamilyOrigin(cfg, "ollama")
	if u, err := url.Parse(origin); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return stop(2, "the ollama provider row's base_url names no daemon (it reads as %q): set auth.base_url to http://host:port; nothing was changed", origin)
	}

	var page string
	if o.htmlFile != "" {
		data, err := os.ReadFile(o.htmlFile)
		if err != nil {
			return stop(2, "cannot read %s: %v; nothing was changed", o.htmlFile, err)
		}
		page = string(data)
	} else {
		data, err := cloudFetch(ctx, cloudsync.PricingURL)
		if err != nil {
			return stop(2, "could not fetch ollama.com/pricing: %v; nothing was changed", err)
		}
		page = string(data)
	}
	catalog, err := cloudsync.ParsePricing(page)
	if err != nil {
		saved, saveErr := saveFailedHTML(page, cloudSyncNow())
		where := "raw HTML saved to " + saved
		if saveErr != nil {
			where = "the raw HTML could not be saved: " + saveErr.Error()
		}
		return stop(3, "could not parse ollama.com/pricing: %v\ncatalog: %s — the parser to update is wt/internal/cloudsync/pricingpage.go; nothing was changed", err, where)
	}

	run := &catalogRun{catalog: catalog, origin: origin}
	// Without it, what to pull and what to rm is unknowable: refuse instead
	// of mirroring half the plan.
	if run.tags, err = ollamaTags(ctx, run.origin); err != nil {
		hint := " (is the ollama daemon up?)"
		if errors.Is(err, errOllamaNotInstalled) {
			hint = ""
		}
		return stop(2, "could not run `ollama list` against %s%s: %v; nothing was changed", run.origin, hint, err)
	}
	names := make([]string, len(catalog.Models))
	for i, m := range catalog.Models {
		names[i] = m.Name
	}
	run.resolved, run.tagWarnings = cloudsync.ResolveCloudTags(ctx, cloudFetch, names, cloudsync.VerifiedTags(entries, run.tags))
	if !slices.ContainsFunc(names, func(n string) bool { return run.resolved[n] != "" }) {
		for _, w := range run.tagWarnings {
			fmt.Fprintf(errOut, "catalog:   %s\n", w)
		}
		return stop(2, "could not resolve a cloud tag for any model on ollama.com/library; nothing was changed")
	}

	run.plan = run.replan(entries)
	// The write addresses a row by its id and refuses an id that is there
	// more than once, so a plan that changes, clones or removes such a row
	// can never be applied. Say so now, in a dry run too, instead of printing
	// a plan and a digest that the apply then refuses.
	for _, id := range catalogTouchedIDs(run.plan) {
		if _, err := doc.Model(id); err != nil {
			return stop(1, "nothing was changed: %v", err)
		}
	}
	run.printed = run.plan.Format()
	prefixLines(out, "catalog", run.printed)
	return run
}

// catalogTouchedIDs are the ids of the existing rows a catalog plan
// addresses: those it re-prices, those it removes, and those it clones a
// re-tagged entry from.
func catalogTouchedIDs(plan *cloudsync.CatalogPlan) []string {
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, u := range plan.Updates {
		add(u.ModelID)
	}
	for _, a := range plan.Additions {
		add(a.CloneOf)
	}
	for _, id := range plan.Removals {
		add(id)
	}
	return ids
}

// catalogGate decides whether a printed catalog plan may be applied: not a
// mass removal without --force, and under --yes not a plan that deletes
// anything without the digest of a reviewed dry run. It reports false, with
// the reason printed and res.catalogCode set, when it may not.
func catalogGate(errOut io.Writer, c *catalogRun, o cloudSyncOpts, res *cloudSyncOutcome) bool {
	if c.plan.MassRemoval() && !o.force {
		fmt.Fprintf(errOut, "catalog: error: %d of %d ollama cloud entries would be removed — check the page parsed correctly, then re-run with --force. Nothing was changed for the catalog.\n",
			len(c.plan.Removals), c.plan.CloudEntries)
		res.catalogCode = 4
		return false
	}
	if digest := c.plan.RemovalDigest(); o.yes && digest != "" && o.approve != digest {
		reason := "the plan deletes models"
		if o.approve != "" {
			reason = "the removals are not the ones digest " + o.approve + " approved"
		}
		fmt.Fprintf(errOut, "catalog: error: %s — review a --dry-run, then re-run with `--yes --approve-removals %s`. Nothing was changed for the catalog.\n", reason, digest)
		res.catalogCode = 5
		return false
	}
	return true
}

// runOllamaWork does what the registry write left for ollama: the pulls,
// then the removals. Each failure is reported and makes the run exit 1, and
// the rest still run. A tag whose `ollama rm` failed is by now a stray (its
// entry is gone from the registry), which the next run removes; that run's
// removals are not this run's, so the failure line says a new digest is
// needed. A tag whose pull failed has its entry in the registry all the
// same, unrouted until the tag is there; the next run plans that pull again,
// and a pull needs no digest.
//
// The daemon is named once, first: it is where the pulls and removals land,
// and no other line of a run that works says so.
func runOllamaWork(ctx context.Context, out, errOut io.Writer, origin string, done cloudsync.CatalogApplied, res *cloudSyncOutcome) {
	if len(done.Pulls)+len(done.Removes) > 0 {
		fmt.Fprintf(out, "catalog: ollama at %s\n", origin)
	}
	for _, tag := range done.Pulls {
		if err := ollamaPull(ctx, origin, tag); err != nil {
			fmt.Fprintf(errOut, "catalog: error: %v\n", err)
			fmt.Fprintf(errOut, "catalog:   %s is in the registry but not pulled, so it is not routed; run `wt cloud-sync` again to retry the pull\n", tag)
			res.failed = true
			continue
		}
		fmt.Fprintf(out, "catalog: pulled %s\n", tag)
	}
	for _, tag := range done.Removes {
		if err := ollamaRemove(ctx, origin, tag); err != nil {
			fmt.Fprintf(errOut, "catalog: error: %v\n", err)
			fmt.Fprintf(errOut, "catalog:   %s is left pulled; the next run lists it as a stray tag — start again from --dry-run, since the removal digest may have changed\n", tag)
			res.failed = true
			continue
		}
		fmt.Fprintf(out, "catalog: removed %s\n", tag)
	}
}
