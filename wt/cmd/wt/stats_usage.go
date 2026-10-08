// The usage half of `wt stats`: launches from usage.jsonl joined with the
// LiteLLM proxy's spend log, one row per model. Split from stats.go so the
// survey report and the usage report can each be read whole.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// usageRow is one model in the usage table. Spend is nil when the report
// has no spend data at all (every status but spendOK); with spend data, a
// model the proxy logged nothing for has a zero Row, not nil.
type usageRow struct {
	Model    string
	Family   string
	Launches int
	Spend    *spend.Row
	// AlsoLoggedAs lists the other spellings of Model whose spend rows were
	// folded into Spend (see foldTarget), sorted; nil when there are none.
	AlsoLoggedAs []string
}

// familyFor is a model's family: the registry's, else the id's provider
// prefix (the part before the first "/"), else "unknown". The order is
// modelman's (usage/reconcile.py _family_for), so a model that has left the
// registry still answers to --family. Unlike modelman, an empty registry
// family or an empty prefix ("/x") falls through to the next rule: no row
// gets a family --family cannot name.
func familyFor(id string, families map[string]string) string {
	if f, ok := families[id]; ok && f != "" {
		return f
	}
	if prefix, _, ok := strings.Cut(id, "/"); ok && prefix != "" {
		return prefix
	}
	return "unknown"
}

// launchesIn picks the count for the report's window.
func launchesIn(c usage.UsageCounts, window time.Duration) int {
	switch window {
	case survey.Window1d:
		return c.OneDay
	case survey.Window7d:
		return c.SevenDay
	}
	return c.ThirtyDay
}

// foldTarget is the wt id a spend row logged as s belongs to, when s is
// another spelling of one. wt spells a "/" inside a model's name as "--"
// (mtplx/Org--Name, an MTPLX directory name), and LiteLLM logged some
// requests as provider/model_name with the "/" kept (mtplx/Org/Name), so one
// model reached the join under two ids. A wrong fold moves money to the
// wrong model, so all of this must hold:
//
//   - s is not a known id itself. Both spellings can be real, distinct ids
//     (a registry holds openrouter/z-ai/glm-5.3-flash as well as ids like
//     openrouter/google--gemma-4-31b-it), and a known id is never renamed.
//   - s has a provider prefix, and a "/" after it.
//   - s with every "/" after the provider prefix (the text up to and
//     including the first "/") written "--" is a known id. The prefix is
//     compared as it is, so nothing folds across providers.
func foldTarget(s string, known func(string) bool) (string, bool) {
	if known(s) {
		return "", false
	}
	i := strings.Index(s, "/")
	if i <= 0 || !strings.Contains(s[i+1:], "/") {
		return "", false
	}
	k := s[:i+1] + strings.ReplaceAll(s[i+1:], "/", "--")
	return k, known(k)
}

// buildUsageRows joins launch counts with spend rows on the model id — the
// LiteLLM route's model_name is the registry id, so the proxy's model_group
// and usage.jsonl's model_id are the same string, except for the other
// spelling foldTarget recognises: such a spend row is added to its wt id's
// row and its id is listed in that row's AlsoLoggedAs. The known ids a row
// can fold into are the keys of families (every registry id, whatever its
// family) and of counts (every id launched in the last 30 days, not only in
// this window). A model gets a row when it has a launch in the window or a
// spend row; sp is nil when there is no spend data. f.model and f.family are
// exact matches, apply to every observed id, registered or not, and apply
// after folding: a folded spelling has no row of its own to match. f.agent
// is not read here: the launch counts arrive already narrowed to it, and a
// spend row has no agent. Rows are sorted by model id.
//
// An empty id never gets a row, from either side: it would print as a line
// with a blank MODEL cell that no --model value can name. usage.AllCounts
// and spend.Query both leave such entries out already (spend counts them as
// Unattributed); this is the join's own guarantee, for whatever else fills
// the two inputs.
func buildUsageRows(counts map[string]usage.UsageCounts, window time.Duration, sp *spend.Result, families map[string]string, f statsFilter) []usageRow {
	byModel := map[string]*usageRow{}
	row := func(id string) *usageRow {
		r, ok := byModel[id]
		if !ok {
			r = &usageRow{Model: id, Family: familyFor(id, families)}
			if sp != nil {
				r.Spend = &spend.Row{Model: id}
			}
			byModel[id] = r
		}
		return r
	}
	for id, c := range counts {
		if n := launchesIn(c, window); n > 0 && id != "" {
			row(id).Launches = n
		}
	}
	if sp != nil {
		known := func(id string) bool {
			if id == "" {
				return false
			}
			_, registered := families[id]
			_, launched := counts[id]
			return registered || launched
		}
		for _, s := range sp.Rows {
			if s.Model == "" {
				continue
			}
			id := s.Model
			if k, ok := foldTarget(id, known); ok {
				id = k
			}
			r := row(id)
			if id != s.Model {
				r.AlsoLoggedAs = append(r.AlsoLoggedAs, s.Model)
			}
			r.Spend.Requests += s.Requests
			r.Spend.PromptTokens += s.PromptTokens
			r.Spend.CompletionTokens += s.CompletionTokens
			r.Spend.Spend += s.Spend
		}
		for _, r := range byModel {
			sort.Strings(r.AlsoLoggedAs)
		}
	}

	rows := make([]usageRow, 0, len(byModel))
	for _, r := range byModel {
		if f.model != "" && r.Model != f.model {
			continue
		}
		if f.family != "" && r.Family != f.family {
			continue
		}
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	return rows
}

// The four answers to "is there spend data in this report".
const (
	spendOK            = "ok"
	spendNotConfigured = "not_configured" // nothing names a database
	spendUnavailable   = "unavailable"    // psql missing, database down, query failed
	spendSkipped       = "skipped"        // --agent: the spend log has no agent
)

// usageReport is everything the usage table and its notes are built from.
type usageReport struct {
	Rows         []usageRow
	SpendStatus  string
	SpendReason  string // why spend is missing; "" when SpendStatus is spendOK
	Unattributed int64  // requests the proxy logged with no model, in the whole window
	Narrowed     bool   // --model or --family was given: Rows is part of the window
}

// registryNote is the extra note for --family when wt's configuration did
// not load: there are then no registry families, every family is an id's
// provider prefix, and a registry family (gemma4) matches nothing. loadErr
// is config.Load's error, which is about config.toml (unparseable, a failed
// migration) as often as about registry.toml, so the note quotes it rather
// than name a file itself. The error names files and keys, never a secret;
// it is folded onto one line because a TOML parse error can span several.
// The repair hint mirrors configError's (helpers.go): `wt config` cannot
// repair a registry problem (a link to fix, a location to move, a missing
// registry to seed), so those name the working repair.
func registryNote(loadErr error) string {
	hint := "run `wt config` to repair"
	if errors.Is(loadErr, config.ErrRegistryMissing) {
		hint = "seed the registry with `modelman migrate`"
	} else if h := config.RegistryFixHint(loadErr); h != "" {
		hint = h
	}
	return fmt.Sprintf("wt's configuration did not load (%s; %s), so --family matched each id's provider prefix",
		strings.Join(strings.Fields(loadErr.Error()), " "), hint)
}

// querySpend asks the LiteLLM database for per-model totals after start and
// up to and including end (spend.InWindow). A seam: cmd/wt's TestMain replaces it, so no test resolves a
// connection string or runs psql.
var querySpend = realQuerySpend

func realQuerySpend(ctx context.Context, start, end time.Time) (spend.Result, error) {
	dsn, err := litellm.DatabaseURL()
	if err != nil {
		return spend.Result{}, err
	}
	return spend.Query(ctx, dsn, start, end)
}

// stdoutWidth is the terminal's width in columns, or 0 when stdout is not
// a terminal (a pipe or a file has no width to fit). A seam so tests do not
// depend on where `go test` was run.
var stdoutWidth = realStdoutWidth

func realStdoutWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil {
		return 0
	}
	return w
}

// collectUsage builds the usage report for one window ending at asOf,
// (asOf-window, asOf]: the spend query covers it, and the launches are the
// ones usage.jsonl dates inside that same window. Both halves leave out an
// event dated exactly asOf-window and count one dated exactly asOf (#298); a
// launch after asOf is in neither half. It never fails: every way of having
// no spend data is a status and a reason, and the launch counts are reported
// regardless.
func collectUsage(ctx context.Context, cfg *config.Config, window time.Duration, asOf time.Time, f statsFilter) usageReport {
	rep := usageReport{SpendStatus: spendOK, Narrowed: f.model != "" || f.family != ""}
	var sp *spend.Result
	if f.agent != "" {
		rep.SpendStatus = spendSkipped
		rep.SpendReason = "--agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown"
	} else if res, err := querySpend(ctx, asOf.Add(-window), asOf); err != nil {
		rep.SpendStatus = spendUnavailable
		// spend.ErrNoConnectionString is the same answer from the other
		// package: DatabaseURL never returns a blank string, but a querySpend
		// that got one some other way was refused, not failed.
		if errors.Is(err, litellm.ErrNoDatabase) || errors.Is(err, spend.ErrNoConnectionString) {
			rep.SpendStatus = spendNotConfigured
		}
		rep.SpendReason = "spend unavailable: " + err.Error()
	} else {
		sp = &res
		rep.Unattributed = res.Unattributed
	}
	// The same asOf the spend query ends at: launches are bucketed against
	// the report's instant, never against a second read of the clock.
	rep.Rows = buildUsageRows(usage.NewStore().AllCounts(f.agent, asOf), window, sp, registryFamilies(cfg), f)
	return rep
}

// notes are the lines `wt stats` writes to stderr after the tables: at
// most one about missing spend, and one counting requests with no model.
// The count is the whole window's — the query is never filtered — so it is
// left out when --model or --family narrowed the rows, where it would read
// as a fact about the model shown.
func (r usageReport) notes() []string {
	var out []string
	if r.SpendReason != "" {
		out = append(out, r.SpendReason)
	}
	switch {
	case r.Narrowed:
	case r.Unattributed == 1:
		out = append(out, "1 request had no model and is not shown")
	case r.Unattributed > 1:
		out = append(out, formatCount(r.Unattributed)+" requests had no model and are not shown")
	}
	return out
}

// registryFamilies maps each registry model id to its family. cfg is never
// nil (newApp substitutes an empty config when the registry does not load),
// and an empty model list simply yields no families: familyFor then falls
// back to each id's prefix.
func registryFamilies(cfg *config.Config) map[string]string {
	out := map[string]string{}
	for _, m := range cfg.Models {
		out[m.ID] = m.Family
	}
	return out
}
