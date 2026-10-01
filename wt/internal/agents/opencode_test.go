package agents

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// writeOpenCodeDB creates an opencode.db at <dataHome>/opencode/ with the
// columns LatestSession reads from opencode's session table (the real table
// has many more). Each row is {id, directory, parent_id, time_updated ms,
// time_archived ms}; "" / 0 store NULL.
func writeOpenCodeDB(t *testing.T, dataHome string, rows [][5]any) {
	t.Helper()
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not on PATH")
	}
	dir := filepath.Join(dataHome, "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	null := func(v any) string {
		switch v := v.(type) {
		case string:
			if v == "" {
				return "NULL"
			}
			return "'" + strings.ReplaceAll(v, "'", "''") + "'"
		case int:
			if v == 0 {
				return "NULL"
			}
			return fmt.Sprint(v)
		}
		t.Fatalf("unsupported value %#v", v)
		return ""
	}
	sql := "create table session (id text primary key, directory text not null, parent_id text, time_updated integer not null, time_archived integer);"
	for _, r := range rows {
		sql += fmt.Sprintf("insert into session values (%s, %s, %s, %d, %s);",
			null(r[0]), null(r[1]), null(r[2]), r[3], null(r[4]))
	}
	if out, err := exec.Command("sqlite3", filepath.Join(dir, "opencode.db"), sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v\n%s", err, out)
	}
}

// TestOpenCodeLatestSession pins issue #162: opencode keeps sessions in
// SQLite (opencode.db), keyed by the session's directory. The newest
// top-level, unarchived session for the exact path wins; child (sub-agent)
// sessions, archived sessions and other directories are ignored.
func TestOpenCodeLatestSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	repo := "/work/repo"
	writeOpenCodeDB(t, filepath.Join(home, ".local", "share"), [][5]any{
		{"ses_old", repo, "", 1_000, 0},
		{"ses_new", repo, "", 2_000, 0},
		{"ses_child", repo, "ses_new", 3_000, 0},
		{"ses_archived", repo, "", 4_000, 4_500},
		{"ses_other_dir", repo + "/.worktrees/x", "", 5_000, 0},
	})

	s, err := opencodeDriver{}.LatestSession(repo)
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

// TestOpenCodeLatestSessionXDGDataHome asserts the db is found under
// $XDG_DATA_HOME when set (opencode follows the XDG base-dir spec), and that
// a path containing a single quote is matched literally.
func TestOpenCodeLatestSessionXDGDataHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	repo := "/work/it's a repo"
	writeOpenCodeDB(t, xdg, [][5]any{{"ses_q", repo, "", 1_000, 0}})

	s, err := opencodeDriver{}.LatestSession(repo)
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.ID != "ses_q" {
		t.Fatalf("LatestSession = %+v, want ses_q", s)
	}
}

// TestOpenCodeLatestSessionNoDB asserts a machine where opencode never ran
// (no opencode.db) yields no session and no error.
func TestOpenCodeLatestSessionNoDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")

	s, err := opencodeDriver{}.LatestSession("/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatalf("expected nil session, got %+v", s)
	}
}

// TestOpenCodeLatestSessionUnreadableDBErrors asserts a db that exists but
// cannot be queried surfaces an error instead of silently reporting "no
// sessions" (the failure mode that hid #162).
func TestOpenCodeLatestSessionUnreadableDBErrors(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	dir := filepath.Join(home, ".local", "share", "opencode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.db"), []byte("not a database, just text padding it out"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := (opencodeDriver{}).LatestSession("/work/repo"); err == nil {
		t.Fatal("expected an error for an unreadable opencode.db")
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
