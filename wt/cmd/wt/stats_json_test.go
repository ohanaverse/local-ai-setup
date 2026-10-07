package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// pinStatsNow fixes the report's as-of instant for the test.
func pinStatsNow(t *testing.T, at time.Time) {
	t.Helper()
	old := statsNow
	statsNow = func() time.Time { return at }
	t.Cleanup(func() { statsNow = old })
}

// TestStatsJSONDocument pins the --json document byte for byte with spend
// available: one line holding window, as_of, survey and usage; the
// all-agents survey row has a null agent; averages with nothing rated are
// null; a launch-only model has zero spend fields, not null; also_logged_as
// is [] on a row nothing was folded into. Scripts and
// the archived `wt stats --json >> usage.jsonl` habit parse exactly this.
func TestStatsJSONDocument(t *testing.T) {
	a, tmp := newTestApp(t)
	asOf := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	pinStatsNow(t, asOf)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: asOf.Add(-time.Hour), Worked: boolPtr(true), Speed: intPtr(4), Quality: intPtr(5)},
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: asOf.Add(-2 * time.Hour), Worked: boolPtr(false)},
	})
	seedLaunches(t, tmp,
		launch{"ollama/gemma4:9b", "claude", time.Hour},
		launch{"ollama/launch-only", "claude", time.Hour},
	)
	stubSpend(t, spend.Result{Unattributed: 3, Rows: []spend.Row{
		{Model: "ollama/gemma4:9b", Requests: 4, PromptTokens: 152, CompletionTokens: 630, Spend: 0},
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 0.0085},
	}}, nil)

	stdout, stderr := runStats(t, a, "--json", "--window", "7d")

	want := `{"window":"7d","as_of":"2026-10-07T12:00:00Z",` +
		`"survey":[` +
		`{"model":"ollama/gemma4:9b","agent":null,"answered":2,"worked":1,"failed":1,"skipped":0,"worked_pct":50,"quality_avg":5,"speed_avg":4},` +
		`{"model":"ollama/gemma4:9b","agent":"claude","answered":2,"worked":1,"failed":1,"skipped":0,"worked_pct":50,"quality_avg":5,"speed_avg":4}],` +
		`"usage":{"spend_status":"ok","spend_reason":"","unattributed_requests":3,"rows":[` +
		`{"model":"ollama/gemma4:9b","family":"ollama","launches":1,"requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0,"also_logged_as":[]},` +
		`{"model":"ollama/launch-only","family":"ollama","launches":1,"requests":0,"prompt_tokens":0,"completion_tokens":0,"spend":0,"also_logged_as":[]},` +
		`{"model":"openrouter/qwen/qwen3.8-27b","family":"openrouter","launches":0,"requests":5,"prompt_tokens":517,"completion_tokens":3135,"spend":0.0085,"also_logged_as":[]}]}}` + "\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if stderr != "wt: 3 requests had no model and are not shown\n" {
		t.Errorf("stderr = %q, want the unattributed note there and not in the document's place", stderr)
	}
}

// TestStatsJSONIsOneDocumentInEveryMode verifies that whatever happened to
// spend — unavailable, not configured, skipped by --agent — and with both
// stores empty, stdout is exactly one JSON document: the status and reason
// are fields, the spend fields and unattributed_requests are null, both
// lists are [] rather than null, and the note still goes to stderr. A
// stray line on stdout breaks `wt stats --json | jq` precisely when the
// database is down.
func TestStatsJSONIsOneDocumentInEveryMode(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		err        error
		status     string
		wantReason string
	}{
		{"unavailable", nil, spend.ErrNoPsql, "unavailable", "spend unavailable: psql not found on PATH"},
		{"not configured", nil, fmt.Errorf("%w: nothing names one", litellm.ErrNoDatabase), "not_configured",
			"spend unavailable: no LiteLLM database configured: nothing names one"},
		{"skipped", []string{"--agent", "claude"}, nil, "skipped",
			"--agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, tmp := newTestApp(t)
			seedLaunches(t, tmp, launch{"ollama/gemma4:9b", "claude", time.Hour})
			stubSpend(t, spend.Result{}, c.err)

			stdout, stderr := runStats(t, a, append([]string{"--json"}, c.args...)...)

			if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "}\n") {
				t.Fatalf("stdout = %q, want exactly one line holding one object", stdout)
			}
			var doc statsJSON
			dec := json.NewDecoder(strings.NewReader(stdout))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&doc); err != nil {
				t.Fatalf("stdout is not the document: %v\n%s", err, stdout)
			}
			if doc.Window != "30d" || doc.Usage.SpendStatus != c.status || doc.Usage.SpendReason != c.wantReason {
				t.Errorf("window %q, status %q, reason %q; want 30d, %q, %q", doc.Window, doc.Usage.SpendStatus, doc.Usage.SpendReason, c.status, c.wantReason)
			}
			if doc.Usage.UnattributedRequests != nil {
				t.Errorf("unattributed_requests = %d, want null without spend data", *doc.Usage.UnattributedRequests)
			}
			if len(doc.Usage.Rows) != 1 || doc.Usage.Rows[0].Launches != 1 || doc.Usage.Rows[0].Requests != nil || doc.Usage.Rows[0].Spend != nil {
				t.Errorf("rows = %+v, want one row with 1 launch and null spend fields", doc.Usage.Rows)
			}
			if !strings.Contains(stdout, `"survey":[]`) {
				t.Errorf("stdout = %s, want \"survey\":[] for an empty survey store", stdout)
			}
			if stderr != "wt: "+c.wantReason+"\n" {
				t.Errorf("stderr = %q, want the one note", stderr)
			}
		})
	}

	t.Run("nothing at all", func(t *testing.T) {
		a, _ := newTestApp(t)
		stubSpend(t, spend.Result{}, nil)
		pinStatsNow(t, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
		stdout, stderr := runStats(t, a, "--json")
		want := `{"window":"30d","as_of":"2026-10-07T12:00:00Z","survey":[],` +
			`"usage":{"spend_status":"ok","spend_reason":"","unattributed_requests":0,"rows":[]}}` + "\n"
		if stdout != want || stderr != "" {
			t.Errorf("stdout = %s stderr = %q\nwant %s and no stderr", stdout, stderr, want)
		}
	})
}

// TestStatsJSONNullsWhereTheTableShowsADash verifies the survey fields
// that have no value are null, not 0: worked_pct for a model whose every
// prompt was skipped, and quality_avg and speed_avg for one that was
// answered but never rated. The table prints "-" in those cells; a 0 in the
// document would read as "never worked" and "rated zero" to whatever parses
// the archive.
func TestStatsJSONNullsWhereTheTableShowsADash(t *testing.T) {
	a, tmp := newTestApp(t)
	asOf := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	pinStatsNow(t, asOf)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "m/skipped", Timestamp: asOf.Add(-time.Hour), Skipped: true},
		{Agent: "claude", ModelID: "m/unrated", Timestamp: asOf.Add(-time.Hour), Worked: boolPtr(true)},
	})
	stubSpend(t, spend.Result{}, nil)

	stdout, _ := runStats(t, a, "--json", "--agent", "claude")
	var doc statsJSON
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not the document: %v\n%s", err, stdout)
	}
	if len(doc.Survey) != 2 || doc.Survey[0].Model != "m/skipped" || doc.Survey[1].Model != "m/unrated" {
		t.Fatalf("survey = %+v, want the m/skipped and m/unrated rows", doc.Survey)
	}
	if r := doc.Survey[0]; r.Skipped != 1 || r.Answered != 0 || r.WorkedPct != nil || r.QualityAvg != nil || r.SpeedAvg != nil {
		t.Errorf("all-skipped row = %+v, want skipped 1 and three nulls", r)
	}
	if r := doc.Survey[1]; r.WorkedPct == nil || *r.WorkedPct != 100 || r.QualityAvg != nil || r.SpeedAvg != nil {
		t.Errorf("unrated row = %+v, want worked_pct 100 and null quality_avg and speed_avg", r)
	}
	for _, field := range []string{`"worked_pct":null`, `"quality_avg":null`, `"speed_avg":null`} {
		if !strings.Contains(stdout, field) {
			t.Errorf("stdout = %s\nwant %s spelled as null", stdout, field)
		}
	}
}

// TestStatsJSONListsFoldedSpellings verifies a --json row whose spend
// includes requests the proxy logged under another spelling of the id names
// that spelling in also_logged_as, carries the summed figures under the wt
// id, and that no row has the logged spelling as its model. An archived
// document must be able to say why a model's requests exceed what the
// database shows for its id alone; the text table has no room for it.
func TestStatsJSONListsFoldedSpellings(t *testing.T) {
	a, tmp := newTestApp(t)
	seedLaunches(t, tmp, launch{"mtplx/Org--Name", "claude", time.Hour})
	stubSpend(t, spend.Result{Rows: []spend.Row{
		{Model: "mtplx/Org--Name", Requests: 4, PromptTokens: 100, CompletionTokens: 10},
		{Model: "mtplx/Org/Name", Requests: 5, PromptTokens: 23, CompletionTokens: 5, Spend: 0.5},
		{Model: "openrouter/x/y", Requests: 1},
	}}, nil)

	stdout, _ := runStats(t, a, "--json")

	wantRows := `"rows":[` +
		`{"model":"mtplx/Org--Name","family":"mtplx","launches":1,"requests":9,"prompt_tokens":123,"completion_tokens":15,"spend":0.5,"also_logged_as":["mtplx/Org/Name"]},` +
		`{"model":"openrouter/x/y","family":"openrouter","launches":0,"requests":1,"prompt_tokens":0,"completion_tokens":0,"spend":0,"also_logged_as":[]}]}}` + "\n"
	if !strings.HasSuffix(stdout, wantRows) {
		t.Errorf("stdout =\n%s\nwant it to end with\n%s", stdout, wantRows)
	}
}

// TestStatsJSONAgreesWithTheTable verifies the document and the text
// tables are two renderings of one report: the same filters select the same
// models in both, and each model carries the same launches, requests,
// tokens and spend in both. A --json that ignored --family or --model, or
// read its numbers from somewhere the table does not, would archive
// figures the user never saw on screen.
func TestStatsJSONAgreesWithTheTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedLaunches(t, tmp,
		launch{"ollama/a", "claude", time.Hour},
		launch{"openrouter/b", "claude", time.Hour}, launch{"openrouter/b", "codex", 2 * time.Hour},
	)
	stubSpend(t, spend.Result{Rows: []spend.Row{
		{Model: "ollama/a", Requests: 7, PromptTokens: 70, CompletionTokens: 700, Spend: 0},
		{Model: "openrouter/b", Requests: 1204, PromptTokens: 51700, CompletionTokens: 3135, Spend: 0.0085},
	}}, nil)

	// tableRows reads the usage table back: model id -> its five cells.
	tableRows := func(text string) map[string][]string {
		_, usagePart, _ := strings.Cut(text, "\n\n")
		out := map[string][]string{}
		for _, line := range strings.Split(strings.TrimSpace(usagePart), "\n")[1:] {
			f := strings.Fields(line)
			out[f[0]] = f[1:]
		}
		return out
	}
	for _, c := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"ollama/a", "openrouter/b"}},
		{[]string{"--family", "openrouter"}, []string{"openrouter/b"}},
		{[]string{"--model", "openrouter/b"}, []string{"openrouter/b"}},
	} {
		text, _ := runStats(t, a, c.args...)
		stdout, _ := runStats(t, a, append([]string{"--json"}, c.args...)...)
		var doc statsJSON
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatal(err)
		}
		table := tableRows(text)
		if len(doc.Usage.Rows) != len(c.want) || len(table) != len(c.want) {
			t.Fatalf("%v: --json rows = %+v, table rows = %v; want %v in both", c.args, doc.Usage.Rows, table, c.want)
		}
		for i, r := range doc.Usage.Rows {
			if r.Model != c.want[i] {
				t.Errorf("%v: --json row %d is %s, want %s", c.args, i, r.Model, c.want[i])
			}
			fromJSON := []string{
				formatCount(int64(r.Launches)), formatCount(*r.Requests), formatCount(*r.PromptTokens),
				formatCount(*r.CompletionTokens), fmt.Sprintf("$%.4f", *r.Spend),
			}
			if got := table[r.Model]; strings.Join(got, " ") != strings.Join(fromJSON, " ") {
				t.Errorf("%v: %s is %v in the table and %v in --json", c.args, r.Model, got, fromJSON)
			}
		}
	}
}

// TestStatsJSONKeepsAnAgentNamedAll verifies a null agent field means "the
// model's all-agents aggregate" and nothing else: an agent that is
// configured under the name "(all)" is a real agent, and its rows keep that
// name in the document. Reading the flag off the printed label would mark
// the archive's rows with an aggregate that was never computed — the text
// table can only show the collision (both rows read "(all)"), the document
// has to resolve it.
func TestStatsJSONKeepsAnAgentNamedAll(t *testing.T) {
	a, tmp := newTestApp(t)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "(all)", ModelID: "m/one", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
	})
	stubSpend(t, spend.Result{}, nil)

	stdout, _ := runStats(t, a, "--json")
	var doc statsJSON
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not the document: %v\n%s", err, stdout)
	}
	var aggregate, named int
	for _, r := range doc.Survey {
		switch {
		case r.Agent == nil:
			aggregate++
		case *r.Agent == statsAllAgents:
			named++
		}
	}
	if len(doc.Survey) != 2 || aggregate != 1 || named != 1 {
		t.Errorf("survey = %+v, want two rows: one null agent (the aggregate) and one agent %q", doc.Survey, statsAllAgents)
	}
}
