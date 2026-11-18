// Tests for the wt config cobra wiring. Verifies subcommand dispatch,
// error messages, and the atomic-write guarantee (unknown theme names
// never leave a broken file). Uses the themes test seam to redirect
// themes.toml to a temp dir.

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
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
	opts   configeditor.Options
}

// stubConfigEditor replaces the editor (which needs a terminal) with one that
// records how it was opened and returns res and err.
func stubConfigEditor(t *testing.T, res configeditor.Result, err error) *editorCall {
	t.Helper()
	call := &editorCall{}
	old := configeditorRun
	configeditorRun = func(_ themes.Theme, cfg *config.Config, cfgErr error, o configeditor.Options) (configeditor.Result, error) {
		*call = editorCall{called: true, cfg: cfg, cfgErr: cfgErr, opts: o}
		return res, err
	}
	t.Cleanup(func() { configeditorRun = old })
	return call
}

// TestConfigCmd_NoSubcommand_LaunchesEditor: wt config with no args now
// launches the config editor TUI. We stub configeditorRun to avoid the
// TTY requirement in tests.
func TestConfigCmd_NoSubcommand_LaunchesEditor(t *testing.T) {
	call := stubConfigEditor(t, configeditor.Result{}, nil)

	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !call.called {
		t.Fatal("expected configeditorRun to be called")
	}
	if call.opts.StartTab != configeditor.TabAgents {
		t.Errorf("wt config opened on tab %d, want the Agents tab", call.opts.StartTab)
	}
}

// TestConfigCmd_InvalidConfig_LaunchesEditor verifies that `wt config`
// launches the repair TUI even when the loaded config has a validation
// error. Without this, a broken config.toml would be a dead end.
func TestConfigCmd_InvalidConfig_LaunchesEditor(t *testing.T) {
	call := stubConfigEditor(t, configeditor.Result{}, nil)

	a, _ := newTestApp(t)
	setHostCfgErr(a, fmt.Errorf("validation failed"))
	cmd := configCmd(a)
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if call.cfg == nil {
		t.Fatal("expected configeditorRun to receive the config")
	}
	if call.cfgErr == nil {
		t.Fatal("expected configeditorRun to receive the validation error")
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

// TestConfigEditorIsGivenTheModelServerDeps pins that `wt config` hands the
// editor its Model server dependencies: without them the tab would open on
// the zero ServerDeps and could neither observe nor apply anything.
func TestConfigEditorIsGivenTheModelServerDeps(t *testing.T) {
	call := stubConfigEditor(t, configeditor.Result{}, nil)
	a, _ := newTestApp(t)
	if err := runConfigEditor(&bytes.Buffer{}, &bytes.Buffer{}, a, configeditor.TabAgents); err != nil {
		t.Fatal(err)
	}
	if call.opts.Server.Observe == nil {
		t.Fatal("Options.Server.Observe is nil: the Model server tab has no dependencies")
	}
}

// TestConfigLongTextNamesThreeTabs pins that `wt config --help` names all
// three tabs and says what the Model server one does; the text used to say
// "two tabs" and would otherwise hide the new screen.
func TestConfigLongTextNamesThreeTabs(t *testing.T) {
	a, _ := newTestApp(t)
	long := configCmd(a).Long
	for _, want := range []string{"three tabs", "Agents", "Models", "Model server"} {
		if !strings.Contains(long, want) {
			t.Errorf("Long text lacks %q:\n%s", want, long)
		}
	}
	if strings.Contains(long, "two tabs") {
		t.Errorf("Long text still says two tabs:\n%s", long)
	}
}

// TestWtConfigOpensWhenConfigTomlIsSyntaxBroken pins that a config.toml that
// does not parse does not keep `wt config` from opening: the editor is the
// place to see and fix that, so it must be reached with a non-nil load error,
// and nothing may be written on the way.
func TestWtConfigOpensWhenConfigTomlIsSyntaxBroken(t *testing.T) {
	registry := modelHome(t, "providers = []\nmodels = []\n")
	call := stubConfigEditor(t, configeditor.Result{}, nil)
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
		t.Fatal("configeditorRun was not reached")
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

// TestDesiredRouteIDsAreWhatASyncWouldWrite pins the Model server tab's
// routes seam end to end in the command package: with the registry, the
// provider probe and config.yaml all faked, it returns the ids a sync would
// leave, a cloud model added and a stale wt row dropped. It touches no real
// provider or config.
func TestDesiredRouteIDsAreWhatASyncWouldWrite(t *testing.T) {
	modelHome(t, writeRegistry)
	t.Setenv("OPENROUTER_API_KEY", "sk-test")
	yamlPath := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("WT_LITELLM_CONFIG", yamlPath)
	if err := os.WriteFile(yamlPath, []byte(`model_list:
  - model_name: gone/stale
    litellm_params: {model: openai/stale}
    model_info: {wt_managed: true}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	t.Cleanup(func() { probeInventory = old })

	got, err := desiredRouteIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"openrouter/a--b"}) {
		t.Fatalf("desiredRouteIDs = %v, want [openrouter/a--b]", got)
	}
}
