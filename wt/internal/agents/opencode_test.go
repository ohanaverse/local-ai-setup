package agents

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// stubOpenCodeQuery swaps the opencode `db` seam for one test and records the
// SQL it was handed. Production shells out to the opencode binary itself;
// stubbing here keeps these tests independent of what happens to be installed
// on the host, which is what let the old sqlite3-based coverage skip in CI.
func stubOpenCodeQuery(t *testing.T, out string, err error) *[]string {
	t.Helper()
	var got []string
	prev := opencodeDBQuery
	opencodeDBQuery = func(sql string) ([]byte, error) {
		got = append(got, sql)
		return []byte(out), err
	}
	t.Cleanup(func() { opencodeDBQuery = prev })
	return &got
}

// TestOpenCodeLatestSessionParsesNewestRow pins issue #162's contract: the
// newest top-level, unarchived session for the exact path is returned, with
// time_updated (epoch ms) converted to MTime.
func TestOpenCodeLatestSessionParsesNewestRow(t *testing.T) {
	stubOpenCodeQuery(t, `[{"id":"ses_new","time_updated":2000}]`, nil)

	s, err := opencodeDriver{}.LatestSession("/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.ID != "ses_new" {
		t.Fatalf("LatestSession = %+v, want ses_new", s)
	}
	if want := time.UnixMilli(2_000); !s.MTime.Equal(want) {
		t.Errorf("MTime = %v, want %v (time_updated is epoch ms)", s.MTime, want)
	}
}

// TestOpenCodeLatestSessionQueryShape asserts the SQL still carries every #162
// filter: exact directory match, top-level sessions only (parent_id is null),
// unarchived only, newest first. Dropping any one of them makes resume offer
// the wrong conversation — a sub-agent session or an archived one.
func TestOpenCodeLatestSessionQueryShape(t *testing.T) {
	got := stubOpenCodeQuery(t, `[]`, nil)

	if _, err := (opencodeDriver{}).LatestSession("/work/repo"); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("expected exactly 1 query, got %d: %v", len(*got), *got)
	}
	q := strings.ToLower((*got)[0])
	for _, want := range []string{
		"from session",
		"directory = '/work/repo'",
		"parent_id is null",
		"time_archived is null",
		"order by time_updated desc",
		"limit 1",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q is missing %q", (*got)[0], want)
		}
	}
}

// TestOpenCodeLatestSessionEscapesPathQuotes asserts a worktree path holding a
// single quote is matched literally rather than closing the SQL literal and
// changing the query.
func TestOpenCodeLatestSessionEscapesPathQuotes(t *testing.T) {
	got := stubOpenCodeQuery(t, `[]`, nil)

	if _, err := (opencodeDriver{}).LatestSession("/work/it's a repo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*got)[0], `'/work/it''s a repo'`) {
		t.Errorf("query %q does not escape the quote in the path", (*got)[0])
	}
}

// TestOpenCodeLatestSessionNoSessions asserts a machine where opencode never
// ran — the query returns an empty array — yields no session and no error.
func TestOpenCodeLatestSessionNoSessions(t *testing.T) {
	stubOpenCodeQuery(t, `[]`, nil)

	s, err := opencodeDriver{}.LatestSession("/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
}

// TestOpenCodeLatestSessionQueryError asserts a failed query surfaces an error
// rather than silently reporting "no sessions" — the failure mode that hid
// #162 for so long.
func TestOpenCodeLatestSessionQueryError(t *testing.T) {
	stubOpenCodeQuery(t, "", errors.New("opencode: not found"))

	if _, err := (opencodeDriver{}).LatestSession("/work/repo"); err == nil {
		t.Fatal("expected an error when the query fails")
	}
}

// TestOpenCodeLatestSessionMalformedOutput asserts unexpected output is
// reported instead of being turned into a bogus session id that wt would then
// hand to `opencode --session`.
func TestOpenCodeLatestSessionMalformedOutput(t *testing.T) {
	stubOpenCodeQuery(t, "not json at all", nil)

	if _, err := (opencodeDriver{}).LatestSession("/work/repo"); err == nil {
		t.Fatal("expected an error for unparseable query output")
	}
}

// TestRealOpenCodeDBQueryRunsOpenCodeDB pins the command the real seam runs:
// `opencode db <sql> --format json`. Going through opencode's own binary is
// what makes the database location (OPENCODE_DB, the per-channel filename) and
// WAL handling opencode's business rather than wt's — the previous bare
// `sqlite3` invocation made a missing CLI block every opencode launch. A fake
// `opencode` script on PATH keeps this test off the real binary and off
// sqlite3, so it runs on any host.
func TestRealOpenCodeDBQueryRunsOpenCodeDB(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\nprintf '[]'\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := realOpenCodeDBQuery("select 1"); err != nil {
		t.Fatalf("realOpenCodeDBQuery: %v", err)
	}
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(strings.Fields(string(raw)), " "); got != "db select 1 --format json" {
		t.Errorf("opencode argv = %q, want %q", got, "db select 1 --format json")
	}
}

// TestOpenCodeBuildLitellm asserts that in gateway mode opencode routes
// through a wt-declared @ai-sdk/openai-compatible provider pointed at the
// LiteLLM gateway. The builtin "openai" provider cannot be used: opencode
// resolves its models through its own catalog, so an unknown registry id
// errors with "Model not found", and the models.dev-listed openai path uses
// the responses API whose bridged stream opencode cannot map ("text part not
// found"). The explicit models map registers the registry id; small_model is
// pinned to the same gateway model so background summarization (default
// gpt-5-nano) does not query the proxy with nonexistent model names.
func TestOpenCodeBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := opencodeDriver{}.Build(m, false, routeFor(m, gw))
	content := envValue(t, lc.Env, "OPENCODE_CONFIG_CONTENT")
	if !strings.Contains(content, `"model":"`+opencodeGatewayProviderID+`/ollama/qwen3.8:27b-mlx"`) {
		t.Errorf("config content = %s, want model %s/ollama/qwen3.8:27b-mlx", content, opencodeGatewayProviderID)
	}
	if !strings.Contains(content, `"small_model":"`+opencodeGatewayProviderID+`/ollama/qwen3.8:27b-mlx"`) {
		t.Errorf("config content = %s, want small_model pinned to the gateway (default gpt-5-nano is unknown to the proxy)", content)
	}
	if !strings.Contains(content, `"npm":"@ai-sdk/openai-compatible"`) {
		t.Errorf("config content = %s, want @ai-sdk/openai-compatible provider (chat wire, no responses bridging)", content)
	}
	if !strings.Contains(content, `"baseURL":"http://localhost:4000/v1"`) {
		t.Errorf("config content = %s, want baseURL http://localhost:4000/v1", content)
	}
	if !strings.Contains(content, `"apiKey":"sk-litellm"`) {
		t.Errorf("config content = %s, want apiKey sk-litellm", content)
	}
	if !strings.Contains(content, `"models":{"ollama/qwen3.8:27b-mlx":{`) {
		t.Errorf("config content = %s, want registry id declared in provider models map (opencode rejects catalog-unknown model ids)", content)
	}
}

// envValue returns the value of key in env, failing the test if absent.
func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	t.Fatalf("env %s not found in %v", key, env)
	return ""
}

// OpenCode was the one driver whose Build ignored the Native model bit —
// opencode is ollama-only in normal use, so it never received a Native
// model before the unconfigured-agent passthrough sentinel (issue #147).
// Without this guard, a native launch would still emit
// OPENCODE_CONFIG_CONTENT pointing at an empty gateway base URL instead of a
// bare `opencode` command.
func TestOpenCodeNativeBypass(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	m := nativeModel("opencode")
	lc := d.Build(m, false, directRoute(m))
	if lc.Bin != "opencode" || len(lc.Args) != 0 || len(lc.Env) != 0 {
		t.Errorf("native build = %+v, want bare opencode (no args, no env)", lc)
	}
}

// TestOpenCodeDriverDeclaresEnvAndConfigFileProfileMechanisms verifies
// opencode accepts env and config_file — config_file mechanically merges
// into the OPENCODE_CONFIG_CONTENT env entry (internal/profiles handles
// that dispatch), but from the capability-declaration side it is still
// the config_file mechanism the compaction-tuning profile uses.
func TestOpenCodeDriverDeclaresEnvAndConfigFileProfileMechanisms(t *testing.T) {
	mechs := opencodeDriver{}.ProfileMechanisms()
	want := map[profiles.Mechanism]bool{profiles.MechanismEnv: true, profiles.MechanismConfigFile: true}
	if len(mechs) != len(want) {
		t.Fatalf("ProfileMechanisms() = %v, want exactly %v", mechs, want)
	}
	for _, m := range mechs {
		if !want[m] {
			t.Errorf("unexpected mechanism %q", m)
		}
	}
}
