// Tests for the wt config cobra wiring. Verifies subcommand dispatch,
// error messages, and the atomic-write guarantee (unknown theme names
// never leave a broken file). Uses the themes test seam to redirect
// themes.toml to a temp dir.

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessioneditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// newTestApp returns an app with both config and theme loaded from a temp
// dir. Tests that need to assert file state use the returned tmp path.
//
// We point themes' dirFunc at the tmp dir and set XDG_CONFIG_HOME to tmp so
// config.Load reads a minimal config.toml (agents + default tag) and a
// registry.toml (empty providers/models) from the temp dir.
// The seam is restored via t.Cleanup.
func newTestApp(t *testing.T) (*app, string) {
	t.Helper()
	tmp := t.TempDir()
	origDirFunc := themes.DirFuncForTest()
	themes.SetDirFuncForTest(func() string { return tmp })
	t.Cleanup(func() { themes.SetDirFuncForTest(origDirFunc) })
	t.Setenv("XDG_CONFIG_HOME", tmp)
	cfgDir := filepath.Join(tmp, "agent-wt")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Load fail-closes without a registry.toml.
	regDir := filepath.Join(tmp, "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("providers = []\nmodels = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	return a, tmp
}

// editorCall records one run of the config editor.
type editorCall struct {
	called bool
	cfg    *config.Config
	cfgErr error
	opts   sessioneditor.Options
}

// stubConfigEditor replaces the editor (which needs a terminal) with one that
// records how it was opened and returns res and err.
func stubConfigEditor(t *testing.T, res sessioneditor.Result, err error) *editorCall {
	t.Helper()
	call := &editorCall{}
	old := sessioneditorRun
	sessioneditorRun = func(_ themes.Theme, cfg *config.Config, cfgErr error, o sessioneditor.Options) (sessioneditor.Result, error) {
		*call = editorCall{called: true, cfg: cfg, cfgErr: cfgErr, opts: o}
		return res, err
	}
	t.Cleanup(func() { sessioneditorRun = old })
	return call
}

// TestConfigCmd_NoSubcommand_LaunchesEditor: wt config with no args now
// launches the config editor TUI. We stub sessioneditorRun to avoid the
// TTY requirement in tests.
func TestConfigCmd_NoSubcommand_LaunchesEditor(t *testing.T) {
	call := stubConfigEditor(t, sessioneditor.Result{}, nil)

	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !call.called {
		t.Fatal("expected sessioneditorRun to be called")
	}
	if call.opts.StartTab != sessioneditor.TabAgents {
		t.Errorf("wt config opened on tab %d, want the Agents tab", call.opts.StartTab)
	}
}

// TestWtConfigShowsSessionValidationOnOpen pins that `wt config` opens on a
// config.toml that parses but breaks a session rule (an agent named twice):
// the editor gets the loaded config and the ValidateSession error, so the
// banner shows it on open. Without this a broken config.toml would be a dead
// end.
func TestWtConfigShowsSessionValidationOnOpen(t *testing.T) {
	modelHome(t, "providers = []\nmodels = []\n")
	call := stubConfigEditor(t, sessioneditor.Result{}, nil)
	p := filepath.Join(config.Dir(), "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "default_tag = \"code\"\n\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"ollama\"]\n\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"ollama\"]\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runWT(t, "config"); err != nil {
		t.Fatalf("wt config error = %v, want the editor opened", err)
	}
	if !call.called || call.cfg == nil {
		t.Fatalf("editor called %v with cfg %v; want the loaded config", call.called, call.cfg)
	}
	if call.cfgErr == nil || !strings.Contains(call.cfgErr.Error(), `duplicate agent name "claude"`) {
		t.Fatalf("cfgErr = %v, want the duplicate agent name", call.cfgErr)
	}
}

// TestWtConfigReadsNoRegistry pins that `wt config` is a session screen: a
// registry.toml that does not parse neither keeps it from opening nor reaches
// the editor as an error, and the file is left as it was.
func TestWtConfigReadsNoRegistry(t *testing.T) {
	const garbage = "not toml [["
	registry := modelHome(t, garbage)
	call := stubConfigEditor(t, sessioneditor.Result{}, nil)
	if _, err := runWT(t, "config"); err != nil {
		t.Fatalf("wt config error = %v, want the editor opened", err)
	}
	if !call.called || call.cfgErr != nil {
		t.Fatalf("editor called %v with cfgErr %v; want it opened with none", call.called, call.cfgErr)
	}
	if got := mustRead(t, registry); got != garbage {
		t.Fatalf("registry.toml changed: %q", got)
	}
}

// TestWtConfigRunsWtModelSetupWhenAsked pins the Mode tab's hand-off: when
// the editor asks for `wt-model setup`, wt config runs that child with the
// path the tab found once the editor closed, and a failed child is wt
// config's own error ("wt-model setup: ..."). Why: the editor never runs an
// admin program itself.
func TestWtConfigRunsWtModelSetupWhenAsked(t *testing.T) {
	modelHome(t, "providers = []\nmodels = []\n")
	stubConfigEditor(t, sessioneditor.Result{RunModelSetup: true, ModelSetup: "/x/wt-model"}, nil)
	paths := stubModelSetupChild(t, nil, nil)
	if _, err := runWT(t, "config"); err != nil {
		t.Fatalf("wt config error = %v", err)
	}
	if len(*paths) != 1 || (*paths)[0] != "/x/wt-model" {
		t.Fatalf("child ran with %q, want once with /x/wt-model", *paths)
	}
	stubModelSetupChild(t, nil, errors.New("exit status 2"))
	_, err := runWT(t, "config")
	if err == nil || err.Error() != "wt-model setup: exit status 2" {
		t.Fatalf("wt config error = %v, want \"wt-model setup: exit status 2\"", err)
	}
}

// TestConfigCmd_UnknownSubcommand: wt config foo → exits non-zero.
func TestConfigCmd_UnknownSubcommand(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"foo"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for unknown subcommand")
	}
}

// TestConfigThemeCmd_NoAction_ShowsActive: wt config theme → exits 0 and
// shows the active theme. (We chose RunE-over-help behavior so users can
// quickly check their current theme.)
func TestConfigThemeCmd_NoAction_ShowsActive(t *testing.T) {
	a, _ := newTestApp(t)
	var out bytes.Buffer
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "default") {
		t.Errorf("output %q should contain the active theme name \"default\"", out.String())
	}
	if !strings.Contains(out.String(), "available:") {
		t.Errorf("output %q should contain \"available:\"", out.String())
	}
}

// TestConfigThemeShow_NoArg_ShowsActive: `wt config theme show` with no
// name → shows the active theme's tokens.
func TestConfigThemeShow_NoArg_ShowsActive(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	var out bytes.Buffer
	cmd.SetArgs([]string{"theme", "show"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "default") {
		t.Errorf("output %q should contain the active theme name \"default\"", out.String())
	}
	// All 9 tokens should appear
	for _, token := range []string{"border", "error", "header", "dim",
		"accent", "selected", "unselected", "warning", "success"} {
		if !strings.Contains(out.String(), token) {
			t.Errorf("output should contain token %q", token)
		}
	}
}

// TestConfigThemeShow_UnknownName_Errors: error message contains available
// theme names.
func TestConfigThemeShow_UnknownName_Errors(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "show", "nonexistent"})
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	out := stderr.String() + err.Error()
	for _, name := range []string{"default", "solarized", "mono", "tokyo-night"} {
		if !strings.Contains(out, name) {
			t.Errorf("error %q should list available theme %q", out, name)
		}
	}
}

// TestConfigThemeSet_UnknownName_DoesNotWrite: wt config theme set foo →
// error AND themes.toml is unchanged. Atomic-write guarantee.
func TestConfigThemeSet_UnknownName_DoesNotWrite(t *testing.T) {
	a, tmp := newTestApp(t)
	original := "theme = \"solarized\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "set", "nonexistent"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	data, _ := os.ReadFile(filepath.Join(tmp, "themes.toml"))
	if string(data) != original {
		t.Errorf("themes.toml changed: got %q, want %q", string(data), original)
	}
}

// TestConfigThemeSet_ValidName_WritesFile: file contains the new theme.
func TestConfigThemeSet_ValidName_WritesFile(t *testing.T) {
	a, tmp := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "set", "tokyo-night"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "themes.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "theme = \"tokyo-night\"\n"
	if string(data) != want {
		t.Errorf("themes.toml = %q, want %q", string(data), want)
	}
}

// TestConfigThemeSet_EmptyName_Errors: empty string isn't a valid theme.
func TestConfigThemeSet_EmptyName_Errors(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "set", ""})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for empty theme name")
	}
}

// TestConfigThemeList_PrintsAllThemes: output contains all 4 names.
func TestConfigThemeList_PrintsAllThemes(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	var out bytes.Buffer
	cmd.SetArgs([]string{"theme", "list"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, name := range []string{"default", "solarized", "mono", "tokyo-night"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("output should contain %q", name)
		}
	}
}

// TestConfigTheme_ShowsActive_WithNonDefault: writes solarized to
// themes.toml, then runs `wt config theme` and confirms solarized shows
// up as active.
func TestConfigTheme_ShowsActive_WithNonDefault(t *testing.T) {
	_, tmp := newTestApp(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"),
		[]byte("theme = \"solarized\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Reload the app so it picks up the new themes.toml
	a2, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	var out bytes.Buffer
	cmd := configCmd(a2)
	cmd.SetArgs([]string{"theme"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "solarized") {
		t.Errorf("output %q should contain active theme \"solarized\"", out.String())
	}
}

// TestConfigPath_PrintsDir: `wt config path` prints the directory.
func TestConfigPath_PrintsDir(t *testing.T) {
	a, tmp := newTestApp(t)
	cmd := configCmd(a)
	var out bytes.Buffer
	cmd.SetArgs([]string{"path"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), tmp) {
		t.Errorf("output %q should contain config dir %q", out.String(), tmp)
	}
}

// TestConfigEditorIsGivenTheModeDeps pins that runConfigEditor hands the
// editor the Mode tab's seams: without them the tab would open on the zero
// ModeDeps and could neither observe nor apply anything.
func TestConfigEditorIsGivenTheModeDeps(t *testing.T) {
	call := stubConfigEditor(t, sessioneditor.Result{}, nil)
	a, _ := newTestApp(t)
	if err := runConfigEditor(a, sessioneditor.TabAgents); err != nil {
		t.Fatal(err)
	}
	if call.opts.Mode.Observe == nil {
		t.Fatal("Options.Mode.Observe is nil: the Mode tab has no dependencies")
	}
}

// TestConfigLongTextNamesAgentsAndMode pins that `wt config --help` names
// its two tabs (Agents and Mode), points local at `wt-model setup` and says
// the models are managed with `wt-model model`, so nobody looks for a Model
// server or Models tab that is gone.
func TestConfigLongTextNamesAgentsAndMode(t *testing.T) {
	a, _ := newTestApp(t)
	long := configCmd(a).Long
	for _, want := range []string{"two tabs", "Agents", "Mode", "wt-model setup", "wt-model model"} {
		if !strings.Contains(long, want) {
			t.Errorf("Long text lacks %q:\n%s", want, long)
		}
	}
	if strings.Contains(long, "Model server") {
		t.Errorf("Long text still names the Model server tab:\n%s", long)
	}
}

// TestWtConfigOpensWhenConfigTomlIsSyntaxBroken pins that a config.toml that
// does not parse does not keep `wt config` from opening: the editor is the
// place to see and fix that, so it must be reached with a non-nil load error,
// and nothing may be written on the way.
func TestWtConfigOpensWhenConfigTomlIsSyntaxBroken(t *testing.T) {
	registry := modelHome(t, "providers = []\nmodels = []\n")
	call := stubConfigEditor(t, sessioneditor.Result{}, nil)
	broken := filepath.Join(config.Dir(), "config.toml")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	const garbage = "this is = = not toml [[\n"
	if err := os.WriteFile(broken, []byte(garbage), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runWT(t, "config"); err != nil {
		t.Fatalf("wt config error = %v, want the editor opened", err)
	}
	if !call.called {
		t.Fatal("sessioneditorRun was not reached")
	}
	if call.cfgErr == nil {
		t.Fatal("the editor got a nil load error for a syntax-broken config.toml")
	}
	if b, _ := os.ReadFile(broken); string(b) != garbage {
		t.Fatalf("config.toml was rewritten: %q", b)
	}
	if b, _ := os.ReadFile(registry); string(b) != "providers = []\nmodels = []\n" {
		t.Fatalf("registry.toml was rewritten: %q", b)
	}
}
