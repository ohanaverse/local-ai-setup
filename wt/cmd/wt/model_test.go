package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/configeditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"gopkg.in/yaml.v3"
)

// stubSeedEnv makes `wt model init` seed for the machine env describes.
func stubSeedEnv(t *testing.T, env config.SeedEnv) {
	t.Helper()
	old := seedEnv
	seedEnv = func() config.SeedEnv { return env }
	t.Cleanup(func() { seedEnv = old })
}

// stubRouteSync replaces the route sync that follows a registry write with
// one that records its calls and returns warning.
func stubRouteSync(t *testing.T, warning string) *int {
	t.Helper()
	calls := 0
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		calls++
		return warning
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })
	return &calls
}

// realRouteSync lets a test run the real route sync after a registry write.
// It stays off the machine: no provider is probed (an empty inventory) and
// the proxy restart is `true`. The caller decides which config.yaml, if any,
// the sync can reach.
func realRouteSync(t *testing.T) {
	t.Helper()
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = realSyncRoutesAfterWrite
	t.Cleanup(func() { syncRoutesAfterWrite = old })
	stubProbeInventory(t, localmodels.Snapshot{})
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
}

func onPath(commands ...string) func(string) bool {
	return func(c string) bool {
		for _, have := range commands {
			if have == c {
				return true
			}
		}
		return false
	}
}

// TestModelInitCreatesTheRegistryAndSaysWhatItAdded pins the first run on a
// new machine, as the user sees it: the registry is created, each provider
// row added is named, a provider an agent lists that wt has no default row
// for is named too (on every run, since it stays a config error until the
// user adds it), and the routes are synced once. This is the command the
// "model registry not found" error sends people to, so it has to work with
// no registry and no config at all.
func TestModelInitCreatesTheRegistryAndSaysWhatItAdded(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	stubSeedEnv(t, config.SeedEnv{
		Agents: []string{"claude"}, AgentProviders: []string{"claude", "corp-gateway"}, OnPath: onPath("ollama"),
	})
	synced := stubRouteSync(t, "")
	const unseeded = "no default row for provider: corp-gateway (an agent lists it; add it to registry.toml by hand)\n"

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	want := "registry: " + registry + " (created)\nadded provider: ollama\nadded provider: claude\n" + unseeded
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want stdout %q and no stderr", out.String(), errOut.String(), want)
	}
	if *synced != 1 {
		t.Errorf("route sync ran %d time(s), want once", *synced)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load after init: %v", err)
	}
	if cfg.ProviderByID("ollama") == nil || cfg.ProviderByID("claude") == nil {
		t.Errorf("providers after init = %+v, want ollama and claude", cfg.Providers)
	}

	// A second run finds nothing to do, writes nothing and syncs nothing.
	before, _ := os.ReadFile(registry)
	out.Reset()
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("second runModelInit: %v", err)
	}
	if want := "registry: " + registry + "\nnothing to add\n" + unseeded; out.String() != want {
		t.Errorf("second run stdout = %q, want %q", out.String(), want)
	}
	if after, _ := os.ReadFile(registry); !bytes.Equal(before, after) {
		t.Error("a second init changed the registry")
	}
	if *synced != 1 {
		t.Errorf("route sync ran again on a run that changed nothing (%d calls)", *synced)
	}
}

// TestModelInitJSON pins the machine-readable document: one object, every
// key always present, arrays never null — so a script can read
// `.providers_added | length` without guarding. The route sync's own lines
// stay out of it.
func TestModelInitJSON(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	stubSeedEnv(t, config.SeedEnv{AgentProviders: []string{"corp-gateway"}, OnPath: onPath("mtplx")})
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(out, _ io.Writer) string {
		io.WriteString(out, "some/model: routed\n")
		return "LiteLLM routes not synced: proxy down"
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want := `{"registry":"` + registry + `","created":true,"changed":true,"providers_added":["mtplx"],"providers_unseeded":["corp-gateway"],"warnings":["LiteLLM routes not synced: proxy down"]}` + "\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %s stderr = %q\nwant stdout %s and no stderr", out.String(), errOut.String(), want)
	}
	out.Reset()
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"providers_unseeded":["corp-gateway"],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("no-op stdout = %s\nwant %s", out.String(), want)
	}

	// With nothing unseeded the key is an empty array, like the others.
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("mtplx")})
	out.Reset()
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"providers_unseeded":[],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("nothing unseeded: stdout = %s\nwant %s", out.String(), want)
	}
}

// TestModelInitMakesAFreshConfigUsable is the reason `wt model init` seeds
// for the providers agents list: on a machine with a config.toml and no
// registry, one run of the command must leave wt able to load and validate
// its config. Here ollama is not installed and no model uses it, and one
// agent lists openrouter — the two rows a user used to have to write by hand
// before any wt command would start.
func TestModelInitMakesAFreshConfigUsable(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	const agents = `default_tag = "code"

[[agents]]
name = "claude"
supported_providers = ["claude", "openrouter"]

[[agents]]
name = "pi"
supported_providers = ["ollama"]
`
	if err := os.WriteFile(config.Path(), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real reading of config.toml, on a machine with nothing installed.
	env := config.DefaultSeedEnv()
	env.OnPath = nil
	stubSeedEnv(t, env)
	stubRouteSync(t, "")

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	want := "registry: " + registry + " (created)\n" +
		"added provider: ollama\nadded provider: openrouter\n" +
		"added provider: claude\nadded provider: pi\nadded provider: agy\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q; want stdout %q and no stderr", out.String(), errOut.String(), want)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load after init: %v", err)
	}
	if err := cfg.ValidateAll(); err != nil {
		t.Errorf("the config should validate after init: %v", err)
	}
}

// TestModelInitSeedsAnOpenRouterRowThatFailsLoudlyWithoutItsKey pins why the
// seeded openrouter row names OPENROUTER_API_KEY. With no secret_ref the
// route wt builds for an openrouter model carries `api_key: ""` and every
// request fails at OpenRouter with nothing in wt's output to say why. With
// the variable named, an unset variable refuses the route ("resolved
// empty"), and a set one is the key that is sent. The registry itself never
// holds the key.
func TestModelInitSeedsAnOpenRouterRowThatFailsLoudlyWithoutItsKey(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	stubSeedEnv(t, config.SeedEnv{AgentProviders: []string{"openrouter"}})
	stubRouteSync(t, "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test-not-a-real-key")
	if err := runModelInit(io.Discard, io.Discard, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	data, err := os.ReadFile(config.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `secret_ref = "OPENROUTER_API_KEY"`) || strings.Contains(string(data), "sk-or-test") {
		t.Fatalf("the row should name the variable and never hold the key:\n%s", data)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load after init: %v", err)
	}
	provider := cfg.ProviderByID("openrouter")
	if provider == nil {
		t.Fatal("no openrouter provider after init")
	}
	model := config.Model{ID: "openrouter/m", ProviderID: "openrouter", ModelName: "org/m"}

	entry, err := litellm.BuildEntry(model, *provider)
	if err != nil {
		t.Fatalf("BuildEntry with the key in the environment: %v", err)
	}
	if text, _ := yaml.Marshal(entry); !strings.Contains(string(text), "api_key: sk-or-test-not-a-real-key") {
		t.Errorf("the route should carry the key from the environment:\n%s", text)
	}

	t.Setenv("OPENROUTER_API_KEY", "")
	if _, err := litellm.BuildEntry(model, *provider); err == nil || !strings.Contains(err.Error(), "resolved empty") {
		t.Errorf("BuildEntry with the variable unset: err = %v, want the `resolved empty` refusal", err)
	}
}

// TestModelInitWarnsWhenConfigTomlCannotBeRead pins what the user is told
// when config.toml is there and does not parse. The command still seeds what
// it can and exits 0, but it must say that no agent got a row and why:
// without the warning the output is the same "nothing to add" a healthy
// machine prints, and the next wt command fails on the config.
func TestModelInitWarnsWhenConfigTomlCannotBeRead(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("this is not toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	env := config.DefaultSeedEnv()
	env.OnPath = nil
	stubSeedEnv(t, env)
	stubRouteSync(t, "")

	var out, errOut bytes.Buffer
	if err := runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	// A first run that adds nothing still creates the registry, and says so.
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if want := "registry: " + registry + " (created)\nnothing to add\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if _, err := os.Stat(registry); err != nil {
		t.Errorf("the registry was not created: %v", err)
	}
	const wantWarn = "warning: config.toml could not be read, so no provider row was added for its agents (fix it and run `wt model init` again): parse config: "
	if !strings.HasPrefix(errOut.String(), wantWarn) || strings.Count(errOut.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", errOut.String(), wantWarn)
	}

	out.Reset()
	errOut.Reset()
	if err := runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"warnings":["config.toml could not be read`) || errOut.Len() != 0 {
		t.Errorf("--json: stdout = %s stderr = %q, want the warning in the document only", out.String(), errOut.String())
	}
}

// TestModelInitUnderARedirectedRegistry is the scratch-registry safety test
// for the one command that writes the registry in this step. With the
// registry redirected, the registry write always succeeds; what happens to
// LiteLLM's config.yaml — which follows neither registry variable — depends
// on whether WT_LITELLM_CONFIG names one. Unnamed, the default config.yaml
// must not be touched and the user is told why; named, that file is synced.
func TestModelInitUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string) {
		home := t.TempDir()
		registry = filepath.Join(home, "scratch", "registry.toml")
		defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(defaultYAML), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(defaultYAML, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("MODELMAN_REGISTRY", "")
		t.Setenv("WT_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
		realRouteSync(t)
		return registry, defaultYAML
	}

	t.Run("config.yaml not named: registry written, routes refused, exit 0", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		var out, errOut bytes.Buffer
		if err := runModelInit(&out, &errOut, false); err != nil {
			t.Fatalf("the registry write succeeded, so the command must too: %v", err)
		}
		if data, err := os.ReadFile(registry); err != nil || !strings.Contains(string(data), `id = "ollama"`) {
			t.Errorf("the redirected registry was not written (err %v):\n%s", err, data)
		}
		wantWarn := "warning: LiteLLM routes not touched: the registry is " + registry +
			" but config.yaml is the default " + defaultYAML +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
		if errOut.String() != wantWarn {
			t.Errorf("stderr = %q\nwant %q", errOut.String(), wantWarn)
		}
		if data, _ := os.ReadFile(defaultYAML); string(data) != "model_list: []\n" {
			t.Errorf("the default config.yaml was touched:\n%s", data)
		}
	})

	t.Run("config.yaml named: registry written, that file synced, no warning", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		named := filepath.Join(t.TempDir(), "scratch-config.yaml")
		if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WT_LITELLM_CONFIG", named)
		var out, errOut bytes.Buffer
		if err := runModelInit(&out, &errOut, false); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want no warning when config.yaml is named", errOut.String())
		}
		if _, err := os.Stat(registry); err != nil {
			t.Errorf("the redirected registry was not written: %v", err)
		}
		if data, _ := os.ReadFile(defaultYAML); string(data) != "model_list: []\n" {
			t.Errorf("the default config.yaml was touched:\n%s", data)
		}
	})

	t.Run("no LiteLLM at all: registry written, nothing said", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		if err := os.Remove(defaultYAML); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := runModelInit(&out, &errOut, false); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 {
			t.Errorf("stderr = %q, want silence on a machine with no LiteLLM config", errOut.String())
		}
		if _, err := os.Stat(registry); err != nil {
			t.Errorf("the redirected registry was not written: %v", err)
		}
		if _, err := os.Stat(defaultYAML); !os.IsNotExist(err) {
			t.Error("init must not create a config.yaml")
		}
	})
}

// TestModelInitReportsABrokenRegistryLink pins the refusal for a registry
// path that is a dangling symlink: an error with the link, its target and a
// hint, nothing created at the target, and no route sync.
func TestModelInitReportsABrokenRegistryLink(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	link := filepath.Join(home, ".config", "local-ai", "registry.toml")
	target := filepath.Join(home, "unmounted", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
	synced := stubRouteSync(t, "")

	err := runModelInit(io.Discard, io.Discard, false)
	if !errors.Is(err, config.ErrRegistryLink) {
		t.Fatalf("err = %v, want config.ErrRegistryLink", err)
	}
	want := "registry link is broken: " + link + " is a symlink to " + target +
		", which does not exist (fix the link or move it aside)"
	if err.Error() != want {
		t.Errorf("err = %q\nwant  %q", err, want)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Error("init created the link's target")
	}
	if *synced != 0 {
		t.Error("the route sync ran after a refused write")
	}
}

// TestModelCommandGroup pins the `wt model` group as cobra serves it:
// `wt model init` runs with no registry (the case it exists for — the root
// command would refuse on the same load error), bare `wt model` opens the
// editor on the Models tab (TestBareModelOpensTheModelsTab) and without a
// terminal says which commands work without one, an unknown word under it is
// an error rather than a worktree name, and the removed `wt models` keeps its
// own message.
func TestModelCommandGroup(t *testing.T) {
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("ollama")})
	stubRouteSync(t, "")
	run := func(args ...string) (string, error) {
		var buf bytes.Buffer
		root := rootCmd()
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(args)
		err := root.Execute()
		return buf.String(), err
	}

	out, err := run("model", "init", "--json")
	if err != nil || !strings.Contains(out, `"providers_added":["ollama"]`) {
		t.Errorf("wt model init --json = %q, %v", out, err)
	}
	oldTTY := stdinTTY
	stdinTTY = func() bool { return false }
	t.Cleanup(func() { stdinTTY = oldTTY })
	if _, err = run("model"); err == nil || !strings.Contains(err.Error(), "wt model needs a terminal to open the Models tab") || !strings.Contains(err.Error(), "`wt model list`") {
		t.Errorf("bare `wt model` with no terminal: err = %v, want it to name the commands that need none", err)
	}
	if _, err = run("model", "bogus"); err == nil || !strings.Contains(err.Error(), `unknown command "bogus" for "wt model"`) {
		t.Errorf("wt model bogus: err = %v, want an unknown-command error", err)
	}
	if _, err = run("model", "init", "extra"); err == nil {
		t.Error("wt model init takes no arguments")
	}
	if _, err = run("models"); err == nil || !strings.Contains(err.Error(), "wt models is removed") {
		t.Errorf("wt models: err = %v, want the removed-subcommand message", err)
	}
}

// TestRegistryWriteGuardIsArmedHere pins that this test binary, whose tests
// reach config.UpdateRegistry through `wt model init`, cannot write the
// developer's real registry: TestMain must have called
// config.IsolateConfigHomeForTest. Without it one test that unsets
// XDG_CONFIG_HOME would seed provider rows into the registry on the machine
// running the suite.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}

// TestBareModelOpensTheModelsTab verifies bare `wt model`, on a terminal,
// opens the config editor on its Models tab and hands it this package's own
// seams for the probe, the seeding environment and the ollama lookup — so the
// tab reaches the machine the same way the commands do, and a test of either
// can stand in for both.
func TestBareModelOpensTheModelsTab(t *testing.T) {
	modelHome(t, writeRegistry)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	call := stubConfigEditor(t, configeditor.Result{}, nil)
	syncs := stubRouteSync(t, "")
	// Each seam is given an answer no real function would give, and the
	// functions the tab was handed are then asked: non-nil alone would pass
	// localmodels.Inventory handed over directly, and the tab would probe
	// the developer's providers under this package's tests.
	stubProbeInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{{ModelID: "marker/probe"}}})
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("marker-seed")})
	stubOllamaCaps(t, map[string]any{"marker_caps": true}, nil)
	if _, err := runWT(t, "model"); err != nil {
		t.Fatal(err)
	}
	if !call.called || call.opts.StartTab != configeditor.TabModels {
		t.Fatalf("called = %v, start tab = %d; want the editor opened on the Models tab", call.called, call.opts.StartTab)
	}
	d := call.opts.Models
	if d.Probe == nil || d.SeedEnv == nil || d.Capabilities == nil {
		t.Fatalf("the tab was not given cmd/wt's seams: %+v", d)
	}
	if snap := d.Probe(&config.Config{}); len(snap.Entries) != 1 || snap.Entries[0].ModelID != "marker/probe" {
		t.Errorf("the tab's probe is not cmd/wt's probeInventory seam: %+v", snap)
	}
	if env := d.SeedEnv(); env.OnPath == nil || !env.OnPath("marker-seed") {
		t.Error("the tab's seeding environment is not cmd/wt's seedEnv seam")
	}
	if info, err := d.Capabilities(nil, "x"); err != nil || info["marker_caps"] != true {
		t.Errorf("the tab's ollama lookup is not cmd/wt's ollamaCaps seam: %v, %v", info, err)
	}
	if *syncs != 0 {
		t.Errorf("syncs = %d with nothing changed, want 0", *syncs)
	}
}

// TestEditorSyncsRoutesOnceOnExit verifies the Models tab's one route sync:
// it runs once after the editor closes, only when the tab wrote the registry,
// a sync warning is printed, and it still runs when the editor ended with an
// error — the registry write already happened. Per-change syncs would restart
// the proxy once for every model added in a sitting.
func TestEditorSyncsRoutesOnceOnExit(t *testing.T) {
	modelHome(t, writeRegistry)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })

	for _, args := range [][]string{{"model"}, {"config"}} {
		stubConfigEditor(t, configeditor.Result{}, nil)
		syncs := stubRouteSync(t, "")
		if _, err := runWT(t, args...); err != nil || *syncs != 0 {
			t.Errorf("wt %s, nothing changed: err = %v, syncs = %d; want no sync", args[0], err, *syncs)
		}
		stubConfigEditor(t, configeditor.Result{RegistryChanged: true}, nil)
		syncs = stubRouteSync(t, "LiteLLM routes not synced: proxy config is read-only")
		out, err := runWT(t, args...)
		if err != nil || *syncs != 1 || !strings.Contains(out, "warning: LiteLLM routes not synced: proxy config is read-only") {
			t.Errorf("wt %s, registry changed: err = %v, syncs = %d, out = %q; want one sync and its warning", args[0], err, *syncs, out)
		}
	}

	stubConfigEditor(t, configeditor.Result{RegistryChanged: true}, errors.New("terminal went away"))
	syncs := stubRouteSync(t, "")
	if _, err := runWT(t, "model"); err == nil || *syncs != 1 {
		t.Errorf("editor failed after a write: err = %v, syncs = %d; want the error and still one sync", err, *syncs)
	}
}

// TestEditorExitSyncUnderARedirectedRegistry is the scratch-registry safety
// test for the two commands that open the editor, `wt model` and `wt config`:
// the Models tab writes the registry, and the sync that follows on exit obeys
// the same rule as `wt model add`. LiteLLM's config.yaml follows neither
// registry variable, so with the registry redirected and config.yaml not
// named, the default file is not touched and the user is told why; named,
// that file is the one synced. The exit status is 0 either way: the registry
// change was made.
func TestEditorExitSyncUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string) {
		registry, defaultYAML = redirectedRegistry(t, writeRegistry)
		oldTTY := stdinTTY
		stdinTTY = func() bool { return true }
		t.Cleanup(func() { stdinTTY = oldTTY })
		// The editor itself needs a terminal; what it reports is a registry
		// its Models tab wrote.
		stubConfigEditor(t, configeditor.Result{RegistryChanged: true}, nil)
		return registry, defaultYAML
	}
	for _, command := range []string{"model", "config"} {
		t.Run("wt "+command+": config.yaml not named", func(t *testing.T) {
			registry, defaultYAML := setup(t)
			out, err := runWT(t, command)
			if err != nil {
				t.Fatalf("the registry was written, so the command must exit 0: %v", err)
			}
			want := "warning: LiteLLM routes not touched: the registry is " + registry +
				" but config.yaml is the default " + defaultYAML +
				" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
			if out != want {
				t.Errorf("output = %q\nwant %q", out, want)
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
		t.Run("wt "+command+": config.yaml named", func(t *testing.T) {
			_, defaultYAML := setup(t)
			scratchYAML := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(scratchYAML, []byte("model_list: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WT_LITELLM_CONFIG", scratchYAML)
			out, err := runWT(t, command)
			if err != nil || strings.Contains(out, "warning") {
				t.Fatalf("wt %s = %q, %v; want a clean sync", command, out, err)
			}
			if got := mustRead(t, scratchYAML); !strings.Contains(got, "openrouter/a--b") {
				t.Errorf("the named config.yaml was not synced:\n%s", got)
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
	}
}

// TestRouteSyncThatChangesNothingDoesNotRestartTheProxy is the requirement
// the exit sync rests on: a sync that leaves config.yaml byte-identical does
// not restart the LiteLLM proxy. The first sync here writes the registry's
// cloud route and restarts once; the second finds nothing to change, leaves
// the file's bytes alone and runs no restart command. Without this, opening
// the Models tab and renaming a family would bounce the proxy under every
// running agent.
func TestRouteSyncThatChangesNothingDoesNotRestartTheProxy(t *testing.T) {
	modelHome(t, writeRegistry)
	t.Setenv("OPENROUTER_API_KEY", "sk-test-not-a-key")
	yaml := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(yaml, []byte("model_list: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realRouteSync(t)
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	t.Setenv("WT_LITELLM_CONFIG", yaml)
	restarts := filepath.Join(t.TempDir(), "restarts")
	t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)

	var out, errOut bytes.Buffer
	if w := syncRoutesAfterWrite(&out, &errOut); w != "" {
		t.Fatalf("first sync: %s", w)
	}
	first := mustRead(t, yaml)
	if !strings.Contains(first, "openrouter/a--b") || mustRead(t, restarts) != "restart\n" {
		t.Fatalf("the first sync should write the cloud route and restart once; restarts = %q, config.yaml:\n%s", mustRead(t, restarts), first)
	}
	if w := syncRoutesAfterWrite(&out, &errOut); w != "" {
		t.Fatalf("second sync: %s", w)
	}
	if mustRead(t, yaml) != first {
		t.Error("a sync with nothing to change rewrote config.yaml")
	}
	if got := mustRead(t, restarts); got != "restart\n" {
		t.Errorf("restarts = %q after a sync that changed nothing, want still one", got)
	}
}
