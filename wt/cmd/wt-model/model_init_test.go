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
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"gopkg.in/yaml.v3"
)

// TestModelInitCreatesTheRegistryAndSaysWhatItAdded pins the first run on a
// new machine, as the user sees it: the registry is created, each provider
// row added is named, a provider an agent lists that wt has no default row
// for is named too (on every run, since it stays a config error until the
// user adds it), and the routes are synced once. This is the command the
// "model registry not found" error sends people to, so it has to work with
// no registry and no config at all.
func TestModelInitCreatesTheRegistryAndSaysWhatItAdded(t *testing.T) {
	h := testHost()
	registry := modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{
		Agents: []string{"claude"}, AgentProviders: []string{"claude", "corp-gateway"}, OnPath: onPath("ollama"),
	})
	synced := stubRouteSync(h, "")
	const unseeded = "no default row for provider: corp-gateway (an agent lists it; add it to registry.toml by hand)\n"

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, false); err != nil {
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
	if err := h.runModelInit(&out, &errOut, false); err != nil {
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
	h := testHost()
	registry := modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{AgentProviders: []string{"corp-gateway"}, OnPath: onPath("mtplx")})
	h.syncRoutesAfterWrite = func(out, _ io.Writer) string {
		io.WriteString(out, "some/model: routed\n")
		return "LiteLLM routes not synced: proxy down"
	}

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want := `{"registry":"` + registry + `","created":true,"changed":true,"providers_added":["mtplx"],"providers_unseeded":["corp-gateway"],"warnings":["LiteLLM routes not synced: proxy down"]}` + "\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %s stderr = %q\nwant stdout %s and no stderr", out.String(), errOut.String(), want)
	}
	out.Reset()
	if err := h.runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"providers_unseeded":["corp-gateway"],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("no-op stdout = %s\nwant %s", out.String(), want)
	}

	// With nothing unseeded the key is an empty array, like the others.
	stubSeedEnv(h, config.SeedEnv{OnPath: onPath("mtplx")})
	out.Reset()
	if err := h.runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"providers_unseeded":[],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("nothing unseeded: stdout = %s\nwant %s", out.String(), want)
	}
}

// TestModelInitMakesAFreshConfigUsable is the reason `wt-model model init` seeds
// for the providers agents list: on a machine with a config.toml and no
// registry, one run of the command must leave wt able to load and validate
// its config. Here ollama is not installed and no model uses it, and one
// agent lists openrouter — the two rows a user used to have to write by hand
// before any wt command would start.
func TestModelInitMakesAFreshConfigUsable(t *testing.T) {
	h := testHost()
	registry := modelHome(t, "")
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
	stubSeedEnv(h, env)
	stubRouteSync(h, "")

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, false); err != nil {
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
	h := testHost()
	modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{AgentProviders: []string{"openrouter"}})
	stubRouteSync(h, "")
	t.Setenv("OPENROUTER_API_KEY", "sk-or-test-not-a-real-key")
	if err := h.runModelInit(io.Discard, io.Discard, false); err != nil {
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
	h := testHost()
	registry := modelHome(t, "")
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("this is not toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	env := config.DefaultSeedEnv()
	env.OnPath = nil
	stubSeedEnv(h, env)
	stubRouteSync(h, "")

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	// A first run that adds nothing still creates the registry, and says so.
	if want := "registry: " + registry + " (created)\nnothing to add\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if _, err := os.Stat(registry); err != nil {
		t.Errorf("the registry was not created: %v", err)
	}
	const wantWarn = "warning: config.toml could not be read, so no provider row was added for its agents (fix it and run `wt-model model init` again): parse config: "
	if !strings.HasPrefix(errOut.String(), wantWarn) || strings.Count(errOut.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", errOut.String(), wantWarn)
	}

	out.Reset()
	errOut.Reset()
	if err := h.runModelInit(&out, &errOut, true); err != nil {
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
	setup := func(t *testing.T) (h *host, registry, defaultYAML string) {
		h = testHost()
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
		t.Setenv("WT_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		stubSeedEnv(h, config.SeedEnv{OnPath: onPath("ollama")})
		realRouteSync(t, h)
		return h, registry, defaultYAML
	}

	t.Run("config.yaml not named: registry written, routes refused, exit 0", func(t *testing.T) {
		h, registry, defaultYAML := setup(t)
		var out, errOut bytes.Buffer
		if err := h.runModelInit(&out, &errOut, false); err != nil {
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
		h, registry, defaultYAML := setup(t)
		named := filepath.Join(t.TempDir(), "scratch-config.yaml")
		if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WT_LITELLM_CONFIG", named)
		var out, errOut bytes.Buffer
		if err := h.runModelInit(&out, &errOut, false); err != nil {
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
		h, registry, defaultYAML := setup(t)
		if err := os.Remove(defaultYAML); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := h.runModelInit(&out, &errOut, false); err != nil {
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
	h := testHost()
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
	stubSeedEnv(h, config.SeedEnv{OnPath: onPath("ollama")})
	synced := stubRouteSync(h, "")

	err := h.runModelInit(io.Discard, io.Discard, false)
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

// TestModelCommandGroupOnWtModel pins the registry half of the `wt-model
// model` group as cobra serves it: `wt-model model init` runs with no
// registry (the case it exists for), an unknown word under the group is an
// error rather than a silent help page, and init takes no arguments. The
// commands moved here from the model group under wt, whose names now only print a pointer.
func TestModelCommandGroupOnWtModel(t *testing.T) {
	h := testHost()
	modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{OnPath: onPath("ollama")})
	stubRouteSync(h, "")

	out, err := runWTModelOn(t, h, "model", "init", "--json")
	if err != nil || !strings.Contains(out, `"providers_added":["ollama"]`) {
		t.Errorf("wt-model model init --json = %q, %v", out, err)
	}
	if _, err = runWTModelOn(t, h, "model", "bogus"); err == nil || !strings.Contains(err.Error(), `unknown command "bogus" for "wt-model model"`) {
		t.Errorf("wt-model model bogus: err = %v, want an unknown-command error", err)
	}
	if _, err = runWTModelOn(t, h, "model", "init", "extra"); err == nil {
		t.Error("wt-model model init takes no arguments")
	}
}

// TestRegistryWriteGuardIsArmedHere pins that this test binary, whose tests
// reach config.UpdateRegistry through `wt-model model init|add|edit|rm`, cannot write the
// developer's real registry: TestMain must have called
// config.IsolateConfigHomeForTest. Without it one test that unsets
// XDG_CONFIG_HOME would seed provider rows into the registry on the machine
// running the suite.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
	}
}
