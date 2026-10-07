// Tests for the usage table's rows: the launch × spend join, the family
// rule and the filters. Pure functions, no I/O.
package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// counts builds a UsageCounts from its 1d, 7d and 30d values.
func counts(d1, d7, d30 int) usage.UsageCounts {
	return usage.UsageCounts{OneDay: d1, SevenDay: d7, ThirtyDay: d30}
}

// TestBuildUsageRowsJoinsLaunchesAndSpend verifies the join modelman's
// reconcile did, in one table: a model with both launches and spend, one
// with launches only (zero spend, not missing spend), and one with spend
// only (zero launches), sorted by model id. The three cases are the three
// things the report exists to show — traffic through the proxy, launches
// that bypassed it, and proxy use that did not come from wt.
func TestBuildUsageRowsJoinsLaunchesAndSpend(t *testing.T) {
	sp := &spend.Result{Rows: []spend.Row{
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 0.0085},
		{Model: "ollama/both", Requests: 4, PromptTokens: 152, CompletionTokens: 630},
	}}
	rows := buildUsageRows(map[string]usage.UsageCounts{
		"ollama/both":        counts(0, 4, 5),
		"ollama/launch-only": counts(7, 15, 17),
	}, survey.Window7d, sp, nil, "", "")

	if len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3", rows)
	}
	want := []struct {
		model    string
		launches int
		requests int64
	}{
		{"ollama/both", 4, 4},
		{"ollama/launch-only", 15, 0},
		{"openrouter/qwen/qwen3.8-27b", 0, 5},
	}
	for i, w := range want {
		r := rows[i]
		if r.Model != w.model || r.Launches != w.launches {
			t.Errorf("rows[%d] = %s with %d launches, want %s with %d", i, r.Model, r.Launches, w.model, w.launches)
		}
		if r.Spend == nil || r.Spend.Requests != w.requests {
			t.Errorf("rows[%d].Spend = %+v, want %d requests (zero, never nil, when there is spend data)", i, r.Spend, w.requests)
		}
	}
	if got := rows[2].Spend.Spend; got != 0.0085 {
		t.Errorf("spend-only row's spend = %v, want 0.0085", got)
	}
}

// TestBuildUsageRowsWithoutSpendData verifies that with no spend data (nil)
// every launched model still gets a row, with a nil Spend the table renders
// as "-". The spec's degradation rule: launch counts print whatever
// happened to the database.
func TestBuildUsageRowsWithoutSpendData(t *testing.T) {
	rows := buildUsageRows(map[string]usage.UsageCounts{"ollama/a": counts(1, 2, 3)}, survey.Window30d, nil, nil, "", "")
	if len(rows) != 1 || rows[0].Launches != 3 || rows[0].Spend != nil {
		t.Fatalf("rows = %+v, want one row with 3 launches and nil Spend", rows)
	}
}

// TestBuildUsageRowsUsesTheWindowsLaunchBucket verifies the launch count is
// the one bucket matching --window, and that a model with no launch in that
// window and no spend has no row. modelman showed all three buckets beside
// a spend window of a different length; here launches and spend cover the
// same period, so a row reading "0 launches, 5 requests" means what it says.
func TestBuildUsageRowsUsesTheWindowsLaunchBucket(t *testing.T) {
	c := map[string]usage.UsageCounts{"m": counts(0, 2, 5)}
	for _, tc := range []struct {
		window time.Duration
		want   int
	}{{survey.Window1d, -1}, {survey.Window7d, 2}, {survey.Window30d, 5}} {
		rows := buildUsageRows(c, tc.window, nil, nil, "", "")
		switch {
		case tc.want < 0 && len(rows) != 0:
			t.Errorf("window %s: rows = %+v, want none (no launch in the window)", tc.window, rows)
		case tc.want >= 0 && (len(rows) != 1 || rows[0].Launches != tc.want):
			t.Errorf("window %s: rows = %+v, want %d launches", tc.window, rows, tc.want)
		}
	}
}

// TestBuildUsageRowsFamilyAndFilters verifies family resolution and both
// filters, ported from modelman's reconcile tests: the registry's family,
// else the id's provider prefix, else "unknown" (an empty family or prefix
// counts as none, so every row has a family --family can name); --model and
// --family are exact and apply to ids the registry does not know. It also pins the one
// place wt differs from modelman on purpose: a launched model that has left
// the registry keeps its row (modelman dropped it unless it had spend).
func TestBuildUsageRowsFamilyAndFilters(t *testing.T) {
	families := map[string]string{"ollama/gemma4:9b": "gemma4"}
	c := map[string]usage.UsageCounts{
		"ollama/gemma4:9b":  counts(1, 1, 1),
		"ollama/gone:cloud": counts(1, 1, 1),
		"bare-id":           counts(1, 1, 1),
		"/no-prefix":        counts(1, 1, 1),
		"ollama/blank":      counts(1, 1, 1),
	}
	families["ollama/blank"] = "" // a registry entry with no family
	sp := &spend.Result{Rows: []spend.Row{{Model: "openrouter/x/y", Requests: 1}}}

	got := map[string]string{}
	for _, r := range buildUsageRows(c, survey.Window30d, sp, families, "", "") {
		got[r.Model] = r.Family
	}
	want := map[string]string{
		"ollama/gemma4:9b":  "gemma4",
		"ollama/gone:cloud": "ollama",
		"bare-id":           "unknown",
		"/no-prefix":        "unknown",
		"ollama/blank":      "ollama",
		"openrouter/x/y":    "openrouter",
	}
	if len(got) != len(want) {
		t.Fatalf("families = %v, want %v", got, want)
	}
	for id, f := range want {
		if got[id] != f {
			t.Errorf("family of %s = %q, want %q", id, got[id], f)
		}
	}

	ids := func(model, family string) string {
		var out []string
		for _, r := range buildUsageRows(c, survey.Window30d, sp, families, model, family) {
			out = append(out, r.Model)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ model, family, want string }{
		{"ollama/gone:cloud", "", "ollama/gone:cloud"},
		{"", "ollama", "ollama/blank,ollama/gone:cloud"},
		{"", "gemma4", "ollama/gemma4:9b"},
		{"", "openrouter", "openrouter/x/y"},
		{"", "gemma", ""},                     // exact, not a prefix
		{"", "unknown", "/no-prefix,bare-id"}, // the fallback family is a value --family takes
		{"ollama/gemma4:9b", "ollama", ""},
	} {
		if got := ids(tc.model, tc.family); got != tc.want {
			t.Errorf("--model %q --family %q: rows = %q, want %q", tc.model, tc.family, got, tc.want)
		}
	}
}

// TestBuildUsageRowsNeverMakesANamelessRow verifies an empty model id gets
// no row, whether it arrives as a launch count or as a spend row. usage.jsonl
// is a plain file: a hand-edited line with no model_id used to print as a
// row with a blank MODEL cell and LAUNCHES 1, which no --model value could
// select and which read as a rendering bug. The named models beside it are
// untouched.
func TestBuildUsageRowsNeverMakesANamelessRow(t *testing.T) {
	c := map[string]usage.UsageCounts{"": counts(1, 1, 1), "ollama/a": counts(2, 2, 2)}
	sp := &spend.Result{Rows: []spend.Row{{Model: "", Requests: 9}, {Model: "ollama/a", Requests: 4}}}
	for name, s := range map[string]*spend.Result{"with spend": sp, "without spend": nil} {
		rows := buildUsageRows(c, survey.Window30d, s, nil, "", "")
		if len(rows) != 1 || rows[0].Model != "ollama/a" || rows[0].Launches != 2 {
			t.Errorf("%s: rows = %+v, want the one ollama/a row and none with an empty id", name, rows)
		}
	}
	if rows := buildUsageRows(c, survey.Window30d, sp, nil, "", "unknown"); len(rows) != 0 {
		t.Errorf("--family unknown: rows = %+v, want none (the empty id has no family row either)", rows)
	}
}

// TestBuildUsageRowsFoldsTheSlashSpelling verifies a spend row logged under
// the other spelling of a wt id lands on that id's row. wt writes a "/"
// inside a model's name as "--" (mtplx/Org--Name), LiteLLM logged some
// requests as provider/model_name with the "/" kept (mtplx/Org/Name), and an
// exact join then printed one model twice: launches on one line, requests on
// another. The rule is narrow because a wrong fold moves money to the wrong
// model: the logged id must not be a known id itself, the "--" spelling must
// be one (in the registry, or launched), and only the "/" after the provider
// prefix are rewritten.
func TestBuildUsageRowsFoldsTheSlashSpelling(t *testing.T) {
	type want struct {
		model    string
		launches int
		requests int64
		also     string // AlsoLoggedAs, comma-joined
	}
	for _, tc := range []struct {
		name     string
		launched []string // ids with one launch each
		registry []string // registry ids, with no launch
		spend    []spend.Row
		want     []want
	}{
		{
			name:     "the slash spelling folds into the launched id",
			launched: []string{"mtplx/Org--Name"},
			spend:    []spend.Row{{Model: "mtplx/Org--Name", Requests: 4}, {Model: "mtplx/Org/Name", Requests: 5}},
			want:     []want{{"mtplx/Org--Name", 1, 9, "mtplx/Org/Name"}},
		},
		{
			name:     "a registry id with no launch is known too",
			registry: []string{"mtplx/Org--Name"},
			spend:    []spend.Row{{Model: "mtplx/Org/Name", Requests: 5}},
			want:     []want{{"mtplx/Org--Name", 0, 5, "mtplx/Org/Name"}},
		},
		{
			name:     "no fold when the logged id is itself known",
			launched: []string{"openrouter/z-ai--glm"},
			registry: []string{"openrouter/z-ai/glm"},
			spend:    []spend.Row{{Model: "openrouter/z-ai/glm", Requests: 5}},
			want:     []want{{"openrouter/z-ai--glm", 1, 0, ""}, {"openrouter/z-ai/glm", 0, 5, ""}},
		},
		{
			name:  "no fold when the -- spelling is not known",
			spend: []spend.Row{{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5}},
			want:  []want{{"openrouter/qwen/qwen3.8-27b", 0, 5, ""}},
		},
		{
			name:     "no fold across providers",
			launched: []string{"omlx/Org--Name"},
			spend:    []spend.Row{{Model: "mtplx/Org/Name", Requests: 5}},
			want:     []want{{"mtplx/Org/Name", 0, 5, ""}, {"omlx/Org--Name", 1, 0, ""}},
		},
		{
			name:     "the provider prefix's own slash is never rewritten",
			launched: []string{"mtplx--Org--Name", "mtplx--Name"},
			spend:    []spend.Row{{Model: "mtplx/Org/Name", Requests: 5}, {Model: "mtplx/Name", Requests: 2}},
			want: []want{
				{"mtplx--Name", 1, 0, ""}, {"mtplx--Org--Name", 1, 0, ""},
				{"mtplx/Name", 0, 2, ""}, {"mtplx/Org/Name", 0, 5, ""},
			},
		},
		{
			name:     "every slash after the prefix is rewritten, or none",
			launched: []string{"mtplx/a--b--c", "mtplx/x/y--z"},
			spend:    []spend.Row{{Model: "mtplx/a/b/c", Requests: 3}, {Model: "mtplx/x/y/z", Requests: 7}},
			want: []want{
				{"mtplx/a--b--c", 1, 3, "mtplx/a/b/c"},
				{"mtplx/x/y--z", 1, 0, ""}, {"mtplx/x/y/z", 0, 7, ""},
			},
		},
		{
			name:     "a mixed spelling folds as well",
			launched: []string{"mtplx/a--b--c"},
			spend:    []spend.Row{{Model: "mtplx/a/b--c", Requests: 2}},
			want:     []want{{"mtplx/a--b--c", 1, 2, "mtplx/a/b--c"}},
		},
		{
			name:     "an id with no provider prefix never folds",
			launched: []string{"/Org--Name"},
			spend:    []spend.Row{{Model: "/Org/Name", Requests: 5}},
			want:     []want{{"/Org--Name", 1, 0, ""}, {"/Org/Name", 0, 5, ""}},
		},
	} {
		c := map[string]usage.UsageCounts{}
		for _, id := range tc.launched {
			c[id] = counts(1, 1, 1)
		}
		families := map[string]string{}
		for _, id := range tc.registry {
			families[id] = "fam"
		}
		rows := buildUsageRows(c, survey.Window30d, &spend.Result{Rows: tc.spend}, families, "", "")
		if len(rows) != len(tc.want) {
			t.Errorf("%s: rows = %+v, want %d", tc.name, rows, len(tc.want))
			continue
		}
		for i, w := range tc.want {
			r := rows[i]
			got := want{r.Model, r.Launches, r.Spend.Requests, strings.Join(r.AlsoLoggedAs, ",")}
			if got != w {
				t.Errorf("%s: rows[%d] = %+v, want %+v", tc.name, i, got, w)
			}
			if r.Spend.Model != r.Model {
				t.Errorf("%s: rows[%d].Spend.Model = %q, want the row's id %q", tc.name, i, r.Spend.Model, r.Model)
			}
		}
	}
}

// TestBuildUsageRowsSumsFoldedSpellings verifies that when several logged
// spellings fold into one id, every figure adds up — requests, both token
// counts and the spend — whatever order the database returned them in, and
// the row lists the spellings sorted. A fold that kept only the last row
// would silently drop money from the report. The folded row takes the wt
// id's family (the registry's), not the logged spelling's.
func TestBuildUsageRowsSumsFoldedSpellings(t *testing.T) {
	c := map[string]usage.UsageCounts{"mtplx/a--b--c": counts(2, 2, 2)}
	families := map[string]string{"mtplx/a--b--c": "qwen"}
	in := []spend.Row{
		{Model: "mtplx/a/b/c", Requests: 5, PromptTokens: 100, CompletionTokens: 10, Spend: 0.5},
		{Model: "mtplx/a--b--c", Requests: 4, PromptTokens: 20, CompletionTokens: 2, Spend: 0.25},
		{Model: "mtplx/a--b/c", Requests: 1, PromptTokens: 3, CompletionTokens: 1, Spend: 0.125},
	}
	for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
		sp := &spend.Result{}
		for _, i := range order {
			sp.Rows = append(sp.Rows, in[i])
		}
		rows := buildUsageRows(c, survey.Window30d, sp, families, "", "")
		if len(rows) != 1 {
			t.Fatalf("order %v: rows = %+v, want one", order, rows)
		}
		r := rows[0]
		wantSpend := spend.Row{Model: "mtplx/a--b--c", Requests: 10, PromptTokens: 123, CompletionTokens: 13, Spend: 0.875}
		if r.Model != "mtplx/a--b--c" || r.Family != "qwen" || r.Launches != 2 || *r.Spend != wantSpend {
			t.Errorf("order %v: row = %+v with spend %+v, want family qwen, 2 launches and %+v", order, r, *r.Spend, wantSpend)
		}
		if got := strings.Join(r.AlsoLoggedAs, ","); got != "mtplx/a--b/c,mtplx/a/b/c" {
			t.Errorf("order %v: AlsoLoggedAs = %q, want both spellings, sorted", order, got)
		}
	}
	if in[0].Requests != 5 || in[1].Requests != 4 {
		t.Errorf("the caller's spend rows were changed: %+v", in)
	}
}

// TestBuildUsageRowsFiltersApplyAfterFolding verifies --model and --family
// see the folded rows: --model <wt id> includes the spend logged under the
// other spelling, --model <other spelling> matches nothing (that id has no
// row any more), and --family matches the wt id's family, not the logged
// spelling's prefix. Filtering first would bring back the split the fold
// removes. A spelling that is itself known is still a row --model can name.
func TestBuildUsageRowsFiltersApplyAfterFolding(t *testing.T) {
	c := map[string]usage.UsageCounts{"mtplx/Org--Name": counts(1, 1, 1)}
	families := map[string]string{"mtplx/Org--Name": "qwen", "openrouter/z-ai/glm": "glm"}
	sp := &spend.Result{Rows: []spend.Row{
		{Model: "mtplx/Org--Name", Requests: 4},
		{Model: "mtplx/Org/Name", Requests: 5},
		{Model: "openrouter/z-ai/glm", Requests: 2},
	}}
	for _, tc := range []struct {
		model, family string
		want          string // "id:requests", comma-joined
	}{
		{"mtplx/Org--Name", "", "mtplx/Org--Name:9"},
		{"mtplx/Org/Name", "", ""},
		{"", "qwen", "mtplx/Org--Name:9"},
		{"", "mtplx", ""},
		{"mtplx/Org--Name", "qwen", "mtplx/Org--Name:9"},
		{"openrouter/z-ai/glm", "", "openrouter/z-ai/glm:2"},
	} {
		var got []string
		for _, r := range buildUsageRows(c, survey.Window30d, sp, families, tc.model, tc.family) {
			got = append(got, r.Model+":"+formatCount(r.Spend.Requests))
		}
		if g := strings.Join(got, ","); g != tc.want {
			t.Errorf("--model %q --family %q: rows = %q, want %q", tc.model, tc.family, g, tc.want)
		}
	}
}

// TestBuildUsageRowsFoldsIntoAnIdLaunchedOutsideTheWindow verifies a launched
// id counts as known for the whole 30 days usage.jsonl covers, not only
// inside --window: a model launched three days ago and not in the registry
// still takes the spend logged under its slash spelling in a 1-day report,
// as one row with zero launches. Deciding "known" from the window's launch
// count instead would bring the second row back for exactly the reports
// (--window 1d, 7d) where a launch is most likely to fall outside.
func TestBuildUsageRowsFoldsIntoAnIdLaunchedOutsideTheWindow(t *testing.T) {
	c := map[string]usage.UsageCounts{"mtplx/Org--Name": counts(0, 0, 1)}
	sp := &spend.Result{Rows: []spend.Row{{Model: "mtplx/Org/Name", Requests: 5}}}
	for _, window := range []time.Duration{survey.Window1d, survey.Window7d} {
		rows := buildUsageRows(c, window, sp, nil, "", "")
		if len(rows) != 1 {
			t.Fatalf("window %v: rows = %+v, want one", window, rows)
		}
		r := rows[0]
		if r.Model != "mtplx/Org--Name" || r.Launches != 0 || r.Spend == nil || r.Spend.Requests != 5 || strings.Join(r.AlsoLoggedAs, ",") != "mtplx/Org/Name" {
			t.Errorf("window %v: row = %+v with spend %+v, want mtplx/Org--Name with 0 launches, the 5 folded requests and the slash spelling in AlsoLoggedAs", window, r, r.Spend)
		}
	}
}
