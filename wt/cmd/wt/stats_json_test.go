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
// null; a launch-only model has zero spend fields, not null. Scripts and
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
		`{"model":"ollama/gemma4:9b","family":"ollama","launches":1,"requests":4,"prompt_tokens":152,"completion_tokens":630,"spend":0},` +
		`{"model":"ollama/launch-only","family":"ollama","launches":1,"requests":0,"prompt_tokens":0,"completion_tokens":0,"spend":0},` +
		`{"model":"openrouter/qwen/qwen3.8-27b","family":"openrouter","launches":0,"requests":5,"prompt_tokens":517,"completion_tokens":3135,"spend":0.0085}]}}` + "\n"
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

// TestStatsJSONAgreesWithTheTable verifies the document and the text
// tables are two renderings of one report: the same filters select the same
// models in both. A --json that ignored --family or --model would archive
// numbers the user did not ask for.
func TestStatsJSONAgreesWithTheTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedLaunches(t, tmp, launch{"ollama/a", "claude", time.Hour}, launch{"openrouter/b", "claude", time.Hour})
	stubSpend(t, spend.Result{}, nil)

	for _, args := range [][]string{{"--family", "openrouter"}, {"--model", "openrouter/b"}} {
		text, _ := runStats(t, a, args...)
		stdout, _ := runStats(t, a, append([]string{"--json"}, args...)...)
		var doc statsJSON
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Usage.Rows) != 1 || doc.Usage.Rows[0].Model != "openrouter/b" {
			t.Errorf("%v --json rows = %+v, want only openrouter/b", args, doc.Usage.Rows)
		}
		if strings.Contains(text, "ollama/a") || !strings.Contains(text, "openrouter/b") {
			t.Errorf("%v text =\n%s\nwant only openrouter/b", args, text)
		}
	}
}
