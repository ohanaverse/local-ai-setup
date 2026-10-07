// Tests for the usage half of the `wt stats` command: both tables, the
// degraded modes, the notes and the filters. No test here reaches a
// database: TestMain replaces querySpend, and stubSpend is the only way a
// test gets spend rows.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/spend"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// spendCall records what a stubbed querySpend was asked.
type spendCall struct {
	n          int
	start, end time.Time
}

// stubSpend makes querySpend answer res, err for the rest of the test.
func stubSpend(t *testing.T, res spend.Result, err error) *spendCall {
	t.Helper()
	call := &spendCall{}
	old := querySpend
	querySpend = func(_ context.Context, start, end time.Time) (spend.Result, error) {
		call.n++
		call.start, call.end = start, end
		return res, err
	}
	t.Cleanup(func() { querySpend = old })
	return call
}

// launch is one usage.jsonl line: a model launched by an agent, age ago.
type launch struct {
	model, agent string
	age          time.Duration
}

// seedLaunches writes <tmp>/agent-wt/usage.jsonl, where newTestApp's
// XDG_CONFIG_HOME points the usage store.
func seedLaunches(t *testing.T, tmp string, launches ...launch) {
	t.Helper()
	var data []byte
	for _, l := range launches {
		line, err := json.Marshal(map[string]any{
			"model_id": l.model, "agent": l.agent, "timestamp": time.Now().UTC().Add(-l.age),
		})
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(tmp, "agent-wt", "usage.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// runStats runs `wt stats` with args and returns stdout and stderr apart.
func runStats(t *testing.T, a *app, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := statsCmd(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("wt stats %v: %v", args, err)
	}
	return out.String(), errOut.String()
}

// TestStatsCmdPrintsTheUsageTable verifies the whole command with spend
// available: the survey table is still there, the usage table follows it
// after a blank line with launches and spend joined per model, nothing is
// written to stderr, and the database is asked for exactly the --window
// ending now. This is `modelman usage report`'s replacement.
func TestStatsCmdPrintsTheUsageTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/gemma4:9b", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
	})
	seedLaunches(t, tmp,
		launch{"ollama/gemma4:9b", "claude", time.Hour},
		launch{"ollama/gemma4:9b", "codex", 3 * 24 * time.Hour},
		launch{"ollama/old", "claude", 10 * 24 * time.Hour},
	)
	call := stubSpend(t, spend.Result{Rows: []spend.Row{
		{Model: "ollama/gemma4:9b", Requests: 1204, PromptTokens: 1234567, CompletionTokens: 630, Spend: 0},
		{Model: "openrouter/qwen/qwen3.8-27b", Requests: 5, PromptTokens: 517, CompletionTokens: 3135, Spend: 0.0085},
	}}, nil)
	asOf := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	oldNow := statsNow
	statsNow = func() time.Time { return asOf }
	t.Cleanup(func() { statsNow = oldNow })

	stdout, stderr := runStats(t, a, "--window", "7d")

	wantUsage := "\n\n" +
		"MODEL                        LAUNCHES  REQUESTS     PROMPT  COMPLETION    SPEND\n" +
		"ollama/gemma4:9b                    2     1,204  1,234,567         630  $0.0000\n" +
		"openrouter/qwen/qwen3.8-27b         0         5        517       3,135  $0.0085\n"
	if !strings.HasSuffix(stdout, wantUsage) {
		t.Errorf("stdout =\n%s\nwant it to end with a blank line and\n%s", stdout, wantUsage)
	}
	if !strings.Contains(stdout, "(all)") || strings.Contains(stdout, "no survey data") {
		t.Errorf("stdout =\n%s\nwant the survey table kept above the usage table", stdout)
	}
	if strings.Contains(stdout, "ollama/old") {
		t.Errorf("stdout lists ollama/old, whose only launch is outside the 7d window:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing when spend is available", stderr)
	}
	if call.n != 1 || !call.end.Equal(asOf) || !call.start.Equal(asOf.Add(-7*24*time.Hour)) {
		t.Errorf("querySpend called %d times for %s..%s, want once for the 7 days ending %s", call.n, call.start, call.end, asOf)
	}
}

// TestStatsCmdDegradesWhenSpendIsUnavailable verifies the spec's
// degradation for each cause — no psql, no reachable database, no
// configured URL: the launch counts print, the spend cells show "-",
// exactly one note goes to stderr, stdout carries no note, and the command
// succeeds. `wt stats` is a report; a laptop away from its database must
// not turn it into an error.
func TestStatsCmdDegradesWhenSpendIsUnavailable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		note string
	}{
		{"no psql", spend.ErrNoPsql, "wt: spend unavailable: psql not found on PATH\n"},
		{"database down", fmt.Errorf("%w at 127.0.0.1:5432: Connection refused", spend.ErrUnreachable),
			"wt: spend unavailable: cannot reach the LiteLLM database at 127.0.0.1:5432: Connection refused\n"},
		{"no url", fmt.Errorf("%w: /x/config.yaml has no general_settings.database_url (set WT_LITELLM_DATABASE_URL)", litellm.ErrNoDatabase),
			"wt: spend unavailable: no LiteLLM database configured: /x/config.yaml has no general_settings.database_url (set WT_LITELLM_DATABASE_URL)\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, tmp := newTestApp(t)
			seedLaunches(t, tmp, launch{"ollama/gemma4:9b", "claude", time.Hour})
			stubSpend(t, spend.Result{}, c.err)

			stdout, stderr := runStats(t, a)

			want := "no survey data\n\n" +
				"MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
				"ollama/gemma4:9b         1         -       -           -      -\n"
			if stdout != want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
			}
			if stderr != c.note {
				t.Errorf("stderr = %q\nwant     %q (exactly one note)", stderr, c.note)
			}
		})
	}
}

// TestCollectUsageClassifiesMissingSpend verifies the report's spend status
// for each outcome: ok, not_configured (nothing names a database),
// unavailable (anything else that failed) and skipped (--agent). The status
// is what --json consumers branch on; "not configured" must not be lumped
// with "the database is down".
func TestCollectUsageClassifiesMissingSpend(t *testing.T) {
	a, _ := newTestApp(t)
	asOf := time.Now().UTC()
	for _, tc := range []struct {
		name, agent string
		err         error
		want        string
	}{
		{"ok", "", nil, spendOK},
		{"no database", "", fmt.Errorf("%w: reason", litellm.ErrNoDatabase), spendNotConfigured},
		{"bad config.yaml", "", fmt.Errorf("%w: reason", litellm.ErrInvalid), spendUnavailable},
		{"no psql", "", spend.ErrNoPsql, spendUnavailable},
		{"agent", "claude", nil, spendSkipped},
	} {
		stubSpend(t, spend.Result{Unattributed: 2}, tc.err)
		rep := collectUsage(context.Background(), a.cfg, survey.Window30d, asOf, "", "", tc.agent)
		if rep.SpendStatus != tc.want {
			t.Errorf("%s: SpendStatus = %q, want %q", tc.name, rep.SpendStatus, tc.want)
		}
		if (rep.SpendReason == "") != (tc.want == spendOK) {
			t.Errorf("%s: SpendReason = %q, want a reason exactly when spend is missing", tc.name, rep.SpendReason)
		}
		if wantUn := int64(0); tc.want == spendOK {
			wantUn = 2
			if rep.Unattributed != wantUn {
				t.Errorf("%s: Unattributed = %d, want %d", tc.name, rep.Unattributed, wantUn)
			}
		} else if rep.Unattributed != 0 {
			t.Errorf("%s: Unattributed = %d, want 0 without spend data", tc.name, rep.Unattributed)
		}
	}
}

// TestStatsCmdNotesRequestsWithNoModel verifies requests the proxy logged
// with an empty model group are counted in a stderr note rather than shown
// as a nameless row or dropped in silence (modelman dropped them without a
// word). The count explains a REQUESTS total that is lower than LiteLLM's
// own. Under --model or --family the note is not printed: the count is not
// about the rows shown.
func TestStatsCmdNotesRequestsWithNoModel(t *testing.T) {
	for n, want := range map[int64]string{
		1:    "wt: 1 request had no model and is not shown\n",
		1234: "wt: 1,234 requests had no model and are not shown\n",
	} {
		a, _ := newTestApp(t)
		stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "m", Requests: 2}}, Unattributed: n}, nil)
		stdout, stderr := runStats(t, a)
		if stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		if strings.Contains(stdout, "no model") {
			t.Errorf("stdout carries the note; notes belong on stderr:\n%s", stdout)
		}
	}

	// Under --model or --family the count is still the whole window's (the
	// query is never filtered), so beside one model's rows the note would
	// read as that model's. It is left out.
	a, _ := newTestApp(t)
	stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "ollama/m", Requests: 2}}, Unattributed: 7}, nil)
	for _, args := range [][]string{{"--model", "ollama/m"}, {"--family", "ollama"}} {
		stdout, stderr := runStats(t, a, args...)
		if stderr != "" {
			t.Errorf("wt stats %v: stderr = %q, want no note about a window-wide count beside a filtered table", args, stderr)
		}
		if !strings.Contains(stdout, "ollama/m") {
			t.Errorf("wt stats %v: stdout =\n%s\nwant the ollama/m row", args, stdout)
		}
	}
}

// TestStatsCmdAgentFilterSkipsSpend verifies --agent narrows the launch
// counts to that agent, does not query the database at all, and says why in
// one note. The spend log has no agent column; showing every agent's spend
// beside one agent's launches would be a number that means nothing.
func TestStatsCmdAgentFilterSkipsSpend(t *testing.T) {
	a, tmp := newTestApp(t)
	seedLaunches(t, tmp,
		launch{"ollama/gemma4:9b", "claude", time.Hour},
		launch{"ollama/gemma4:9b", "codex", time.Hour},
		launch{"ollama/gemma4:9b", "codex", 2 * time.Hour},
		launch{"ollama/other", "claude", time.Hour},
	)
	call := stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "ollama/gemma4:9b", Requests: 9}}}, nil)

	stdout, stderr := runStats(t, a, "--agent", "codex")

	want := "no survey data\n\n" +
		"MODEL             LAUNCHES  REQUESTS  PROMPT  COMPLETION  SPEND\n" +
		"ollama/gemma4:9b         2         -       -           -      -\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if call.n != 0 {
		t.Errorf("querySpend called %d times, want 0 with --agent", call.n)
	}
	if stderr != "wt: --agent narrows launches only; LiteLLM does not log which agent sent a request, so spend is not shown\n" {
		t.Errorf("stderr = %q, want the one --agent note", stderr)
	}
}

// TestStatsCmdFamilyFiltersOnlyTheUsageTable verifies --family narrows the
// usage table and leaves the survey table alone, and that --model narrows
// both. The survey rows carry no family, so a --family that emptied the
// survey table would hide answers the user did not ask to hide.
func TestStatsCmdFamilyFiltersOnlyTheUsageTable(t *testing.T) {
	a, tmp := newTestApp(t)
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "claude", ModelID: "ollama/a", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "openrouter/b", Timestamp: time.Now().Add(-time.Hour), Worked: boolPtr(true)},
	})
	seedLaunches(t, tmp, launch{"ollama/a", "claude", time.Hour}, launch{"openrouter/b", "claude", time.Hour})
	stubSpend(t, spend.Result{}, nil)

	split := func(args ...string) (surveyPart, usagePart string) {
		stdout, _ := runStats(t, a, args...)
		surveyPart, usagePart, ok := strings.Cut(stdout, "\n\n")
		if !ok {
			t.Fatalf("wt stats %v: no blank line between the tables:\n%s", args, stdout)
		}
		return surveyPart, usagePart
	}

	s, u := split("--family", "openrouter")
	if !strings.Contains(s, "ollama/a") || !strings.Contains(s, "openrouter/b") {
		t.Errorf("--family changed the survey table:\n%s", s)
	}
	if strings.Contains(u, "ollama/a") || !strings.Contains(u, "openrouter/b") {
		t.Errorf("--family openrouter usage table =\n%s\nwant only openrouter/b", u)
	}

	s, u = split("--model", "ollama/a")
	if strings.Contains(s, "openrouter/b") || strings.Contains(u, "openrouter/b") {
		t.Errorf("--model ollama/a left openrouter/b in a table:\n%s\n\n%s", s, u)
	}

	_, u = split("--family", "nope")
	if u != "no usage data\n" {
		t.Errorf("--family nope usage part = %q, want \"no usage data\"", u)
	}
}

// TestStatsCmdWorksWithoutARegistry verifies the report needs no registry,
// in the three ways a machine really has none: registry.toml missing,
// unparseable, or a dangling symlink. newApp then carries an empty model
// list and a load error, and `wt stats` still prints launches and spend
// with each family taken from the id's provider prefix — so --family ollama
// still selects, and one note says prefixes are what it matched (without
// it, `--family gemma4` would print "no usage data" and no hint why).
// modelman's report exited 1 on each of these; a report about the past
// must not depend on today's catalog.
func TestStatsCmdWorksWithoutARegistry(t *testing.T) {
	breakRegistry := map[string]func(path string) error{
		"missing": os.Remove,
		"unparseable": func(path string) error {
			return os.WriteFile(path, []byte("models = [unclosed\n"), 0o644)
		},
		"dangling symlink": func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Symlink(filepath.Join(filepath.Dir(path), "nowhere.toml"), path)
		},
	}
	for name, breakIt := range breakRegistry {
		t.Run(name, func(t *testing.T) {
			_, tmp := newTestApp(t)
			if err := breakIt(filepath.Join(tmp, "local-ai", "registry.toml")); err != nil {
				t.Fatal(err)
			}
			// The app as production builds it for this machine.
			a, err := newApp()
			if err != nil || a.loadErr == nil || a.cfg == nil || len(a.cfg.Models) != 0 {
				t.Fatalf("newApp() = cfg %+v, loadErr %v, err %v; want an empty config, a load error and no fatal error", a.cfg, a.loadErr, err)
			}
			seedLaunches(t, tmp, launch{"ollama/gone:cloud", "claude", time.Hour}, launch{"openrouter/x", "claude", time.Hour})
			stubSpend(t, spend.Result{Rows: []spend.Row{{Model: "ollama/gone:cloud", Requests: 3}}}, nil)

			stdout, stderr := runStats(t, a, "--family", "ollama")
			want := "no survey data\n\n" +
				"MODEL              LAUNCHES  REQUESTS  PROMPT  COMPLETION    SPEND\n" +
				"ollama/gone:cloud         1         3       0           0  $0.0000\n"
			if stdout != want {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
			}
			if stderr != "wt: "+registryNote+"\n" {
				t.Errorf("stderr = %q, want the one note that --family matched prefixes", stderr)
			}

			// Without --family nothing depends on the registry: both rows, no note.
			stdout, stderr = runStats(t, a)
			if !strings.Contains(stdout, "ollama/gone:cloud") || !strings.Contains(stdout, "openrouter/x") || stderr != "" {
				t.Errorf("without --family: stdout =\n%s\nstderr = %q\nwant both rows and no note", stdout, stderr)
			}
		})
	}
}

// TestStatsFamilyShadowsTheRootFlag pins which --family `wt stats` has: its
// own, one exact value, with no -F shorthand — the way its --model already
// shadows the root's -M/--model. The root's persistent -F/--family is a
// comma list for the picker; inherited, `--family a,b` would mean two
// different things on one command. So -F is rejected on stats, before or
// after the word, and a comma is part of the family's name.
func TestStatsFamilyShadowsTheRootFlag(t *testing.T) {
	_, tmp := newTestApp(t) // rootCmd builds its own app from this throwaway home
	seedLaunches(t, tmp, launch{"ollama/a", "claude", time.Hour}, launch{"omlx/b", "claude", time.Hour})
	run := func(args ...string) (string, error) {
		root := rootCmd()
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(args)
		err := root.Execute()
		return out.String(), err
	}
	for _, args := range [][]string{{"stats", "-F", "ollama"}, {"-F", "ollama", "stats"}} {
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), "unknown shorthand flag: 'F'") {
			t.Errorf("wt %v: err = %v, want -F rejected on stats", args, err)
		}
	}
	if out, err := run("stats", "--family", "ollama"); err != nil || !strings.Contains(out, "ollama/a") || strings.Contains(out, "omlx/b") {
		t.Errorf("wt stats --family ollama = %q, %v; want only ollama/a", out, err)
	}
	if out, err := run("stats", "--family", "ollama,omlx"); err != nil || !strings.HasSuffix(out, "no usage data\n") {
		t.Errorf("wt stats --family ollama,omlx = %q, %v; want no usage data (one family, not a list)", out, err)
	}
}

// TestStatsCmdUsesTheTerminalWidth verifies the command hands the usage
// table the terminal's width: at 80 columns a 51-character id moves to its
// own line and no line of the usage table is wider than the terminal.
// Without this the width-aware table is only ever tested, never used. Only
// the usage table is measured: the survey table above it keeps its bordered
// renderer, whose width this step does not change.
func TestStatsCmdUsesTheTerminalWidth(t *testing.T) {
	a, tmp := newTestApp(t)
	const long = "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"
	seedLaunches(t, tmp, launch{long, "claude", time.Hour})
	stubSpend(t, spend.Result{Rows: []spend.Row{{Model: long, Requests: 1204, PromptTokens: 12345678, CompletionTokens: 1234567}}}, nil)
	old := stdoutWidth
	stdoutWidth = func() int { return 80 }
	t.Cleanup(func() { stdoutWidth = old })

	stdout, _ := runStats(t, a)
	_, usagePart, _ := strings.Cut(stdout, "\n\n")
	if !strings.HasPrefix(usagePart, "MODEL") || !strings.Contains(usagePart, "\n"+long+"\n") {
		t.Fatalf("usage table =\n%s\nwant the long id on a line of its own at 80 columns", usagePart)
	}
	for _, line := range strings.Split(usagePart, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("usage table line is %d columns wide at an 80-column terminal: %q", w, line)
		}
	}
}

// TestStatsNeverReachesADatabaseByDefault pins both guards between a
// cmd/wt test and the developer's LiteLLM database. First, TestMain's
// querySpend stub: an unstubbed `wt stats` reports "not stubbed" instead of
// querying. Second, the real seam itself under this package's throwaway
// config home: the registry is redirected and nothing names config.yaml, so
// litellm.DatabaseURL refuses before it reads config.yaml or the proxy's
// LaunchAgent plist, and before psql is looked for. PATH is emptied and the
// plist path pointed at nothing for that half so that, if the guard ever
// regresses, the worst this test can do is fail — it still cannot read the
// real plist or run psql.
func TestStatsNeverReachesADatabaseByDefault(t *testing.T) {
	a, _ := newTestApp(t)
	_, stderr := runStats(t, a)
	if stderr != "wt: spend unavailable: querySpend not stubbed in this test\n" {
		t.Errorf("stderr = %q, want TestMain's stub to have answered", stderr)
	}

	t.Setenv("PATH", t.TempDir())
	t.Setenv("WT_LITELLM_PLIST", filepath.Join(t.TempDir(), "no-such.plist"))
	for _, k := range []string{"WT_LITELLM_DATABASE_URL", "MODELMAN_LITELLM_DATABASE_URL", "WT_LITELLM_CONFIG", "MODELMAN_LITELLM_CONFIG", "DATABASE_URL"} {
		t.Setenv(k, "")
	}
	_, err := realQuerySpend(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if !errors.Is(err, litellm.ErrNoDatabase) {
		t.Fatalf("realQuerySpend err = %v, want litellm.ErrNoDatabase (redirected registry, config.yaml unnamed)", err)
	}
}
