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
