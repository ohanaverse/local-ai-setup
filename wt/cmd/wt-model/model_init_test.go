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
// row added is named, and the routes are synced once. This is the command the
// "model registry not found" error sends people to, so it has to work with
// no registry and no config at all.
func TestModelInitCreatesTheRegistryAndSaysWhatItAdded(t *testing.T) {
	h := testHost()
	registry := modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{OnPath: onPath("ollama")})
	synced := stubRouteSync(h, "")

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	want := "registry: " + registry + " (created)\nadded provider: ollama\nadded provider: claude\nadded provider: codex\nadded provider: copilot\nadded provider: agy\n"
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
	for _, id := range []string{"ollama", "claude", "codex", "copilot", "agy"} {
		if cfg.ProviderByID(id) == nil {
			t.Errorf("providers after init = %+v, want %s", cfg.Providers, id)
		}
	}

	// A second run finds nothing to do, writes nothing and syncs nothing.
	before, _ := os.ReadFile(registry)
	out.Reset()
	if err := h.runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("second runModelInit: %v", err)
	}
	if want := "registry: " + registry + "\nnothing to add\n"; out.String() != want {
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
// stay out of it, and the retired providers_unseeded key stays gone (a
// script that still reads it gets null, not a list that no longer means
// anything).
func TestModelInitJSON(t *testing.T) {
	h := testHost()
	registry := modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{OnPath: onPath("mtplx")})
	h.syncRoutesAfterWrite = func(out, _ io.Writer) string {
		io.WriteString(out, "some/model: routed\n")
		return "LiteLLM routes not synced: proxy down"
	}

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want := `{"registry":"` + registry + `","created":true,"changed":true,"providers_added":["mtplx","claude","codex","copilot","agy"],"warnings":["LiteLLM routes not synced: proxy down"]}` + "\n"
	if out.String() != want || errOut.Len() != 0 {
		t.Errorf("stdout = %s stderr = %q\nwant stdout %s and no stderr", out.String(), errOut.String(), want)
	}
	if strings.Contains(out.String(), "providers_unseeded") {
		t.Errorf("the retired providers_unseeded key is back: %s", out.String())
	}
	out.Reset()
	if err := h.runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	want = `{"registry":"` + registry + `","created":false,"changed":false,"providers_added":[],"warnings":[]}` + "\n"
	if out.String() != want {
		t.Errorf("no-op stdout = %s\nwant %s", out.String(), want)
	}
}

// TestModelInitMakesAFreshConfigUsable pins that on a machine with a
// config.toml and no registry, one run of the command leaves wt able to load
// and validate its config. Nothing is installed and the file names only pi
// and claude, with no provider lists: init reads none of it, so the four
// native rows are added (pi has no native provider, so no pi row), and no
// ollama or openrouter row appears because nothing asks for one.
func TestModelInitMakesAFreshConfigUsable(t *testing.T) {
	h := testHost()
	registry := modelHome(t, "")
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	const agents = `default_tag = "code"

[[agents]]
name = "claude"

[[agents]]
name = "pi"
`
	if err := os.WriteFile(config.Path(), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real seed environment, on a machine with nothing installed.
	env := config.DefaultSeedEnv()
	env.OnPath = nil
	stubSeedEnv(h, env)
	stubRouteSync(h, "")

	var out, errOut bytes.Buffer
	if err := h.runModelInit(&out, &errOut, false); err != nil {
		t.Fatalf("runModelInit: %v", err)
	}
	want := "registry: " + registry + " (created)\nadded provider: claude\nadded provider: codex\nadded provider: copilot\nadded provider: agy\n"
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
	if cfg.ProviderByID("pi") != nil {
		t.Error("init added a pi row, but pi has no native provider")
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
	modelHome(t, "[[models]]\nid = \"openrouter/m\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"org/m\"\ntags = []\n")
	stubSeedEnv(h, config.SeedEnv{})
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

// TestModelInitIgnoresConfigToml pins that init no longer reads config.toml
// at all: a config.toml that does not parse produces no warning (stderr is
// empty, --json has "warnings":[]) and the same output as a machine with no
// config.toml, namely the four native rows. Before, an unreadable file
// meant no agent got a row and the user had to be told why; the set is now
// fixed, so there is nothing to tell. The next wt command still fails on the
// broken file, which is the right place to say so.
func TestModelInitIgnoresConfigToml(t *testing.T) {
	wantText := func(registry string) string {
		return "registry: " + registry + " (created)\nadded provider: claude\nadded provider: codex\nadded provider: copilot\nadded provider: agy\n"
	}
	for _, c := range []struct {
		name   string
		config string // "" writes no config.toml
	}{
		{"an unparseable config.toml", "this is not toml [[["},
		{"no config.toml", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := testHost()
			registry := modelHome(t, "")
			if c.config != "" {
				if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(config.Path(), []byte(c.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			env := config.DefaultSeedEnv()
			env.OnPath = nil
			stubSeedEnv(h, env)
			stubRouteSync(h, "")

			var out, errOut bytes.Buffer
			if err := h.runModelInit(&out, &errOut, false); err != nil {
				t.Fatalf("runModelInit: %v", err)
			}
			if out.String() != wantText(registry) || errOut.Len() != 0 {
				t.Errorf("stdout = %q, stderr = %q; want stdout %q and no stderr", out.String(), errOut.String(), wantText(registry))
			}
			if _, err := os.Stat(registry); err != nil {
				t.Errorf("the registry was not created: %v", err)
			}
		})
	}

	// --json: no warning either, with the config.toml broken.
	h := testHost()
	modelHome(t, "")
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
	if err := h.runModelInit(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"warnings":[]`) || errOut.Len() != 0 {
		t.Errorf("--json: stdout = %s stderr = %q, want no warnings", out.String(), errOut.String())
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
	if err != nil || !strings.Contains(out, `"providers_added":["ollama","claude","codex","copilot","agy"]`) {
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
