package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionHome points the config home at a scratch dir holding the given
// config.toml (and no registry) and returns the config.toml path.
func sessionHome(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "agent-wt", "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadSessionNeedsNoRegistry proves the session client loads with only a
// config.toml: no registry, no provider validation of an unknown id.
func TestLoadSessionNeedsNoRegistry(t *testing.T) {
	sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"

[model-server]
url = "http://127.0.0.1:9"
token_file = "~/tok"
`)
	cfg, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 0 || len(cfg.Providers) != 0 {
		t.Errorf("models/providers not empty: %d/%d", len(cfg.Models), len(cfg.Providers))
	}
	ms := cfg.ModelServer()
	if ms.URL != "http://127.0.0.1:9" || ms.TokenFile != "~/tok" {
		t.Errorf("model server = %+v", ms)
	}
	a, err := cfg.AgentByName("claude")
	if err != nil || a.Name != "claude" {
		t.Errorf("claude agent = %+v, %v", a, err)
	}
}

// TestLoadSessionSynthesizesDriverAgents proves a driver with no entry gets an
// in-memory synthesized agent, an explicit entry is kept as written, and a
// PatchSave never writes a synthesized agent.
func TestLoadSessionSynthesizesDriverAgents(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"

[model-server]
url = "http://127.0.0.1:9"
token_file = "/t"
`)
	cfg, err := LoadSession("claude", "pi")
	if err != nil {
		t.Fatal(err)
	}
	pi, err := cfg.AgentByName("pi")
	if err != nil || !pi.Synthesized {
		t.Fatalf("pi = %+v, %v", pi, err)
	}
	cl, err := cfg.AgentByName("claude")
	if err != nil || cl.Synthesized {
		t.Errorf("claude widened or lost: %+v, %v", cl, err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `name = "pi"`) || strings.Contains(string(raw), `name = 'pi'`) {
		t.Errorf("synthesized agent was written:\n%s", raw)
	}
}

// TestPatchSaveNeverWritesASynthesizedAgent proves an editor save that hands
// back every in-memory agent (the Synthesized ones included)
// writes only the agents config.toml held, so a session save never turns a
// synthesized agent into a real entry.
func TestPatchSaveNeverWritesASynthesizedAgent(t *testing.T) {
	path := sessionHome(t, "default_tag = \"code\"\n\n[[agents]]\nname = \"codex\"\n")
	cfg, err := LoadSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	if a, err := cfg.AgentByName("claude"); err != nil || !a.Synthesized {
		t.Fatalf("claude = %+v, %v", a, err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.Agents = cfg.Agents }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `name = "codex"`) {
		t.Errorf("codex agent lost:\n%s", raw)
	}
	if strings.Contains(string(raw), `name = "claude"`) || strings.Contains(string(raw), `name = 'claude'`) {
		t.Errorf("synthesized agent was written:\n%s", raw)
	}
}

// TestModelServerTableSurvivesAnUnrelatedSave proves an unrelated PatchSave
// keeps the [model-server] table intact, like [launch]: struct-equal when
// read back and still present as a table in the file.
func TestModelServerTableSurvivesAnUnrelatedSave(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[model-server]
url = "http://127.0.0.1:9"
token_file = "~/tok"
`)
	cfg, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}
	again, _, err := readConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if again.ModelServerTable == nil || *again.ModelServerTable != *cfg.ModelServerTable {
		t.Errorf("table = %+v, want %+v", again.ModelServerTable, cfg.ModelServerTable)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"[model-server]", `url = "http://127.0.0.1:9"`, `token_file = "~/tok"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%q missing:\n%s", want, raw)
		}
	}
}

// TestWholeFileSaveNeverWritesSynthesizedAgents proves Save of a LoadSession
// config drops the in-memory synthesized agents (R13) while keeping an
// explicit entry, so a whole-file save cannot persist them.
func TestWholeFileSaveNeverWritesSynthesizedAgents(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"

[model-server]
url = "http://127.0.0.1:9"
token_file = "/t"
`)
	cfg, err := LoadSession("claude", "pi")
	if err != nil {
		t.Fatal(err)
	}
	before := len(cfg.Agents)
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"pi"`) || strings.Contains(string(raw), `'pi'`) {
		t.Errorf("synthesized agent was written:\n%s", raw)
	}
	if !strings.Contains(string(raw), `name = "claude"`) {
		t.Errorf("explicit agent lost:\n%s", raw)
	}
	if len(cfg.Agents) != before {
		t.Errorf("Save mutated the caller's agents: %d", len(cfg.Agents))
	}
}

// TestInlineTokenIsAProblem proves an inline token is reported (it is not
// used for auth) and a clean table reports none.
func TestInlineTokenIsAProblem(t *testing.T) {
	got := ModelServerState{URL: "u", TokenFile: "f", Token: "secret"}.Problems()
	want := "[model-server] token is not used for auth — while it is set wt writes config.toml 0600, and no save loosens an existing stricter mode — put the token in a file with mode 0600 and set token_file"
	if len(got) != 1 || got[0] != want {
		t.Errorf("problems = %q", got)
	}
	if p := (ModelServerState{URL: "u", TokenFile: "f"}).Problems(); len(p) != 0 {
		t.Errorf("clean table problems = %q", p)
	}
}

// TestIgnoredLitellmKeys proves a [litellm] enabled = true is named as ignored.
func TestIgnoredLitellmKeys(t *testing.T) {
	c := &Config{}
	if k := c.IgnoredLitellmKeys(); len(k) != 0 {
		t.Errorf("empty = %q", k)
	}
	c.LitellmTable = &LitellmState{Enabled: true}
	if k := c.IgnoredLitellmKeys(); len(k) != 1 || k[0] != "[litellm] enabled" {
		t.Errorf("enabled = %q", k)
	}
	c.LitellmTable = &LitellmState{}
	if k := c.IgnoredLitellmKeys(); len(k) != 0 {
		t.Errorf("disabled = %q", k)
	}
}

// TestLegacyLitellmKeysSurviveASave pins R4 across the flip: the [litellm]
// table is host state (`wt-model serve` reads its url and api_key as the
// read-only fallback of the wt-model home) that the session leaves alone, so
// every save path a session or host command takes (an unrelated PatchSave of
// a LoadSession config, a whole-file Save) keeps url, api_key and the retired
// enabled key exactly as written. A save that dropped them would cut the
// model server off from LiteLLM, and one that dropped enabled would rewrite a
// file wt only promised to ignore.
func TestLegacyLitellmKeysSurviveASave(t *testing.T) {
	const body = `default_tag = "code"

[litellm]
enabled = true
url = "http://127.0.0.1:4000"
api_key = "sk-host"

[model-server]
url = "http://127.0.0.1:9"
token_file = "/t"
`
	want := LitellmState{Enabled: true, URL: "http://127.0.0.1:4000", APIKey: "sk-host"}
	check := func(t *testing.T, step string) {
		t.Helper()
		again, _, err := readConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		if again.LitellmTable == nil || *again.LitellmTable != want {
			t.Errorf("%s: [litellm] = %+v, want %+v", step, again.LitellmTable, want)
		}
	}
	sessionHome(t, body)
	cfg, err := LoadSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PatchSave(func(fresh *Config) { fresh.DefaultTag = "design" }); err != nil {
		t.Fatal(err)
	}
	check(t, "PatchSave")
	cfg, err = LoadSession("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(t, "Save")
}

// TestReadModelServerFileMissingFile proves a missing config.toml reads as
// not existing with no errors, so the config editor can offer a first save.
func TestReadModelServerFileMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := ReadModelServerFile()
	if f.Exists || f.ParseErr != nil || f.TableErr != nil || f.Table != nil {
		t.Errorf("file = %+v, want a clean absent file", f)
	}
}

// TestReadModelServerFileSyntaxError proves a syntax-broken file reports
// ParseErr and no table, so callers can refuse to act on guesses.
func TestReadModelServerFileSyntaxError(t *testing.T) {
	sessionHome(t, "default_tag = \n[model-server\n")
	f := ReadModelServerFile()
	if !f.Exists || f.ParseErr == nil || f.Table != nil {
		t.Errorf("file = %+v, want ParseErr set and no table", f)
	}
}

// TestReadModelServerFileSemanticErrorElsewhereStillReadsTheTable proves a
// wrong-typed key outside [model-server] never hides the table: the reader
// decodes the table on its own.
func TestReadModelServerFileSemanticErrorElsewhereStillReadsTheTable(t *testing.T) {
	sessionHome(t, `[[agents]]
name = 5

[model-server]
mode = "remote"
url = "http://127.0.0.1:9"
`)
	f := ReadModelServerFile()
	if f.ParseErr != nil || f.TableErr != nil {
		t.Fatalf("errors = %v / %v", f.ParseErr, f.TableErr)
	}
	if f.Table == nil || f.Table.Mode != "remote" {
		t.Errorf("table = %+v, want mode remote", f.Table)
	}
}

// TestReadModelServerFileTableTypeError proves a wrong-typed key inside
// [model-server] is reported as TableErr, not ParseErr (the syntax is fine).
func TestReadModelServerFileTableTypeError(t *testing.T) {
	sessionHome(t, "[model-server]\nurl = 5\n")
	f := ReadModelServerFile()
	if f.ParseErr != nil || f.TableErr == nil {
		t.Errorf("file = %+v, want TableErr only", f)
	}
}

// TestUpdateModelServerPreservesOtherTables proves a model-server update
// keeps agents and [launch] intact while writing the new table.
func TestUpdateModelServerPreservesOtherTables(t *testing.T) {
	path := sessionHome(t, `default_tag = "design"

[[agents]]
name = "claude"

[launch]
exit_prompt = "never"
`)
	err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.Mode = "remote"
		cur.URL = "http://127.0.0.1:9"
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`default_tag = "design"`, `name = "claude"`, "[launch]", `exit_prompt = "never"`, "[model-server]", `mode = "remote"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%q missing:\n%s", want, raw)
		}
	}
}

// TestUpdateModelServerRefusesASyntaxBrokenFileAndLeavesItUntouched proves a
// file that does not parse is never rewritten: the writer would drop what it
// could not read.
func TestUpdateModelServerRefusesASyntaxBrokenFileAndLeavesItUntouched(t *testing.T) {
	body := "default_tag = \n[model-server\n"
	path := sessionHome(t, body)
	err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.Mode = "native"
		return cur, nil
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if raw, _ := os.ReadFile(path); string(raw) != body {
		t.Errorf("file changed:\n%s", raw)
	}
}

// TestUpdateModelServerRefusesATypeErrorElsewhere proves a wrong-typed key
// outside [model-server] also refuses the write (the re-encode would drop
// it), leaves the bytes alone and names the key.
func TestUpdateModelServerRefusesATypeErrorElsewhere(t *testing.T) {
	body := "default_tag = 5\n"
	path := sessionHome(t, body)
	err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.Mode = "native"
		return cur, nil
	})
	if err == nil || !strings.Contains(err.Error(), "default_tag") {
		t.Fatalf("err = %v, want one naming default_tag", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != body {
		t.Errorf("file changed:\n%s", raw)
	}
}

// TestUpdateModelServerWorksWhenConfigFailsValidationButParses proves a file
// that decodes but fails validation (a repeated agent name) is still written,
// so a broken agent never blocks fixing the server.
func TestUpdateModelServerWorksWhenConfigFailsValidationButParses(t *testing.T) {
	path := sessionHome(t, `default_tag = "code"

[[agents]]
name = "claude"

[[agents]]
name = "claude"
`)
	pre, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if pre.ValidateSession() == nil {
		t.Fatal("fixture should fail ValidateSession (a repeated agent name)")
	}
	err = UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.Mode = "local"
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `mode = "local"`) {
		t.Errorf("mode missing:\n%s", raw)
	}
}

// TestUpdateModelServerKeepsAnExistingStricterMode proves an update never
// loosens a 0600 config.toml.
func TestUpdateModelServerKeepsAnExistingStricterMode(t *testing.T) {
	path := sessionHome(t, "default_tag = \"code\"\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.Mode = "native"
		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", st.Mode().Perm())
	}
}

// TestNativeModeWritesOnlyTheModeKey proves a native-only table is written
// as just mode = "native": empty url/token fields are omitted, not stored.
func TestNativeModeWritesOnlyTheModeKey(t *testing.T) {
	path := sessionHome(t, "default_tag = \"code\"\n")
	err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		return ModelServerState{Mode: "native"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	s := string(raw)
	if !strings.Contains(s, `mode = "native"`) {
		t.Errorf("mode missing:\n%s", s)
	}
	for _, bad := range []string{"url =", "token_file =", "token ="} {
		if strings.Contains(s, bad) {
			t.Errorf("%q present:\n%s", bad, s)
		}
	}
}

// TestUpdateModelServerChangeErrorWritesNothing proves an error from the
// change callback aborts the update and leaves the file as it was.
func TestUpdateModelServerChangeErrorWritesNothing(t *testing.T) {
	body := "default_tag = \"code\"\n"
	path := sessionHome(t, body)
	want := errors.New("nope")
	err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		return ModelServerState{Mode: "remote"}, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if raw, _ := os.ReadFile(path); string(raw) != body {
		t.Errorf("file changed:\n%s", raw)
	}
}

// TestReadModelServerFileNeverQuotesASecretValue proves a syntax error on a
// token or api_key line (a base64 token typed without quotes) is reported by
// line and key name only: the parser's own message can quote the value, and
// the config editor shows ParseErr on screen.
func TestReadModelServerFileNeverQuotesASecretValue(t *testing.T) {
	for _, body := range []string{
		"[model-server]\nmode = \"remote\"\ntoken = abcSECRETxyz+/==\n",
		"[litellm]\napi_key = sk-SECRET-0123456789\n",
		"[model-server]\ntoken = \"abcSECRET\nmode = \"remote\"\n",
		// A url typed without quotes may hold credentials (#640).
		"[model-server]\nurl = admin:SECRET@gpu:8787\n",
		"[litellm]\nurl = SECRET@litellm:4000\n",
	} {
		sessionHome(t, body)
		f := ReadModelServerFile()
		if f.ParseErr == nil {
			t.Fatalf("no ParseErr for %q", body)
		}
		msg := f.ParseErr.Error()
		if strings.Contains(msg, "SECRET") {
			t.Errorf("ParseErr quotes the secret: %q", msg)
		}
		if !strings.Contains(msg, "line ") || !(strings.Contains(msg, "token") || strings.Contains(msg, "api_key") || strings.Contains(msg, "url")) {
			t.Errorf("ParseErr %q does not name the line and the key", msg)
		}
	}
	// Any other syntax error keeps the parser's message.
	sessionHome(t, "default_tag = \n")
	if f := ReadModelServerFile(); f.ParseErr == nil || strings.Contains(f.ParseErr.Error(), "not shown") {
		t.Errorf("ParseErr = %v, want the parser's own message", f.ParseErr)
	}
}

// TestReadModelServerFileReportsADecodeErrorElsewhere proves a wrong-typed
// key outside [model-server] (which every config.toml writer refuses on) is
// reported as RestErr, so a setup plan can refuse before its first step
// rather than fail at the last, while the table itself still reads.
func TestReadModelServerFileReportsADecodeErrorElsewhere(t *testing.T) {
	sessionHome(t, "[[agents]]\nname = 5\n\n[model-server]\nmode = \"remote\"\n")
	f := ReadModelServerFile()
	if f.RestErr == nil || f.TableErr != nil || f.Table == nil {
		t.Errorf("file = %+v, want RestErr with the table read", f)
	}
	sessionHome(t, "[model-server]\nmode = \"remote\"\n")
	if f := ReadModelServerFile(); f.RestErr != nil {
		t.Errorf("RestErr = %v for a clean file", f.RestErr)
	}
}

// TestReadModelServerFileReportsAKeyElsewhereAsRestErr pins that a
// wrong-typed key outside [model-server] still leaves the table readable but
// sets RestErr, which a plan refuses on (the writer re-encodes the whole file).
func TestReadModelServerFileReportsAKeyElsewhereAsRestErr(t *testing.T) {
	sessionHome(t, "[[agents]]\nname = 5\n\n[model-server]\nmode = \"remote\"\n")
	f := ReadModelServerFile()
	if f.RestErr == nil || f.Table == nil {
		t.Errorf("file = %+v, want RestErr set and the table still read", f)
	}
}

// TestModelServerSubscriptionKeyRoundTrips proves the session-side opt-in
// `[model-server] subscription = true` (#597, decision 12) loads, survives an
// UpdateModelServer that changes another key, and is written only when true.
func TestModelServerSubscriptionKeyRoundTrips(t *testing.T) {
	path := sessionHome(t, "[model-server]\nmode = \"local\"\nsubscription = true\n")
	cfg, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ModelServer().Subscription {
		t.Fatal("subscription = true did not load")
	}
	if err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.URL = "http://127.0.0.1:9"
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "subscription = true") {
		t.Errorf("the opt-in was dropped:\n%s", raw)
	}
	if err := UpdateModelServer(func(cur ModelServerState) (ModelServerState, error) {
		cur.Subscription = false
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "subscription") {
		t.Errorf("a false opt-in was written:\n%s", raw)
	}
}
