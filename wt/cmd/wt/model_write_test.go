package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
)

// writeRegistry is a registry with an ollama and an openrouter provider and
// one model of each.
const writeRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "OPENROUTER_API_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
tags = []

[[models]]
id = "openrouter/a--b"
family = "f"
provider_id = "openrouter"
model_name = "a/b"
tags = []
`

// modelHome is a throwaway home whose registry holds content ("" for none).
// It returns the registry's path.
func modelHome(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registry, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stubOllamaCaps answers the capability lookup and counts its calls.
func stubOllamaCaps(t *testing.T, info map[string]any, err error) *int {
	t.Helper()
	calls := 0
	old := ollamaCaps
	ollamaCaps = func(*config.Config, string) (map[string]any, error) {
		calls++
		return info, err
	}
	t.Cleanup(func() { ollamaCaps = old })
	return &calls
}

// stubConfirmRemove answers `wt model rm`'s question and records it.
func stubConfirmRemove(t *testing.T, answer bool) *string {
	t.Helper()
	asked := new(string)
	old := confirmRemove
	confirmRemove = func(q string) (bool, error) { *asked = q; return answer, nil }
	t.Cleanup(func() { confirmRemove = old })
	return asked
}

func sp(s string) *string { return &s }

// runWT runs the wt command line in-process and returns stdout+stderr.
func runWT(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// TestModelAddOnAFreshMachine verifies the first `wt model add` on a machine
// with no registry: one write creates the file with the model and the default
// row of its provider, both are named in the output, and the routes are
// synced exactly once. This is the first thing a new user does after `wt
// model init`, or instead of it.
func TestModelAddOnAFreshMachine(t *testing.T) {
	registry := modelHome(t, "")
	stubSeedEnv(t, config.SeedEnv{})
	syncs := stubRouteSync(t, "")
	var out, errOut bytes.Buffer
	req := modeladmin.AddRequest{ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit", Fields: modeladmin.Fields{Family: sp("qwen3.8"), Tags: sp("code")}}
	if err := runModelAdd(&out, &errOut, nil, req); err != nil {
		t.Fatal(err)
	}
	if want := "added model: omlx/Qwen3.8-27B-4bit\nadded provider: omlx\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.Len() != 0 || *syncs != 1 {
		t.Errorf("stderr = %q, syncs = %d; want no warning and one sync", errOut.String(), *syncs)
	}
	got := mustRead(t, registry)
	if !strings.Contains(got, "id = \"omlx/Qwen3.8-27B-4bit\"\nfamily = \"qwen3.8\"\nprovider_id = \"omlx\"") || !strings.Contains(got, "[[providers]]\nid = \"omlx\"") {
		t.Errorf("registry =\n%s", got)
	}
}

// TestModelAddAsksOllamaWhatTheModelCanDo verifies an ollama add records the
// capabilities `ollama show` reports, that a failed lookup still adds the
// model and warns about exactly what is missing, and that a non-ollama add
// never asks. A route without supports_function_calling has an agent's tools
// dropped by LiteLLM.
func TestModelAddAsksOllamaWhatTheModelCanDo(t *testing.T) {
	t.Run("capabilities recorded", func(t *testing.T) {
		registry := modelHome(t, writeRegistry)
		stubRouteSync(t, "")
		calls := stubOllamaCaps(t, map[string]any{"supports_function_calling": true, "supports_vision": true}, nil)
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3")}}
		if err := runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatal(err)
		}
		if *calls != 1 || errOut.Len() != 0 {
			t.Errorf("lookups = %d, stderr = %q; want one and no warning", *calls, errOut.String())
		}
		if got := mustRead(t, registry); !strings.Contains(got, "[models.model_info]\nsupports_function_calling = true\nsupports_vision = true\n") {
			t.Errorf("the capabilities are not in the registry:\n%s", got)
		}
	})
	t.Run("lookup failed", func(t *testing.T) {
		registry := modelHome(t, writeRegistry)
		stubRouteSync(t, "")
		stubOllamaCaps(t, nil, errors.New("`ollama show qwen3:8b` (at http://localhost:11434) failed: exit status 1"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3")}}
		if err := runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatalf("a failed lookup must not fail the add: %v", err)
		}
		want := "warning: added without model_info.supports_function_calling / supports_vision: `ollama show qwen3:8b` (at http://localhost:11434) failed: exit status 1\n"
		if errOut.String() != want {
			t.Errorf("stderr = %q\nwant %q", errOut.String(), want)
		}
		if got := mustRead(t, registry); !strings.Contains(got, `id = "ollama/qwen3:8b"`) || strings.Count(got, "model_info") != 0 {
			t.Errorf("want the model added with no model_info:\n%s", got)
		}
	})
	t.Run("an add that is going to be refused does not ask first", func(t *testing.T) {
		registry := modelHome(t, writeRegistry)
		calls := stubOllamaCaps(t, nil, errors.New("must not be called"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3"), InputPrice: sp("cheap")}}
		err := runModelAdd(&out, &errOut, nil, req)
		if err == nil || !strings.Contains(err.Error(), `input-price must be a number, got "cheap"`) || *calls != 0 {
			t.Errorf("err = %v, lookups = %d; want the price refused before `ollama show` runs (it can take its whole timeout)", err, *calls)
		}
		if got := mustRead(t, registry); got != writeRegistry {
			t.Error("a refused add changed the registry")
		}
	})
	t.Run("not an ollama model", func(t *testing.T) {
		modelHome(t, writeRegistry)
		stubRouteSync(t, "")
		calls := stubOllamaCaps(t, nil, errors.New("must not be called"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "c/d", Fields: modeladmin.Fields{Family: sp("f")}}
		if err := runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatal(err)
		}
		if *calls != 0 || errOut.Len() != 0 {
			t.Errorf("lookups = %d, stderr = %q; want none", *calls, errOut.String())
		}
	})
}

// TestModelCommandsThroughTheCommandLine drives add, edit and rm as a user
// types them: the flags reach the registry, a flag left out changes nothing,
// each failure is a non-zero exit that leaves the file alone, and each
// success is followed by exactly one sync.
func TestModelCommandsThroughTheCommandLine(t *testing.T) {
	registry := modelHome(t, writeRegistry)
	stubSeedEnv(t, config.SeedEnv{})
	syncs := stubRouteSync(t, "")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true}},
	})

	out, err := runWT(t, "model", "add", "openrouter", "qwen/qwen3.8-27b", "--family", "qwen3.8", "--tags", "code,design",
		"--input-price", "0.5", "--output-price", "2", "--id", "openrouter/qwen")
	if err != nil || out != "added model: openrouter/qwen\n" {
		t.Fatalf("add = %q, %v", out, err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "id = \"openrouter/qwen\"\nfamily = \"qwen3.8\"\nprovider_id = \"openrouter\"\nmodel_name = \"qwen/qwen3.8-27b\"\ntags = [\n    \"code\",\n    \"design\",\n]\n\n[models.cost]\ninput_price_per_million = 0.5\noutput_price_per_million = 2.0\n") {
		t.Errorf("the added row is not as asked:\n%s", got)
	}

	out, err = runWT(t, "model", "edit", "openrouter/qwen", "--tags", "", "--output-price", "2.5")
	if err != nil || out != "updated model: openrouter/qwen\n" {
		t.Fatalf("edit = %q, %v", out, err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "model_name = \"qwen/qwen3.8-27b\"\ntags = []\n\n[models.cost]\ninput_price_per_million = 0.5\noutput_price_per_million = 2.5\n") {
		t.Errorf("the edit did not change exactly the two fields:\n%s", got)
	}
	if out, err = runWT(t, "model", "edit", "openrouter/qwen", "--output-price", "2.5"); err != nil || out != "no change: openrouter/qwen\n" {
		t.Errorf("a repeated edit = %q, %v; want `no change`", out, err)
	}
	if *syncs != 2 {
		t.Errorf("syncs = %d after an add, an edit and a no-op; want 2", *syncs)
	}

	before := mustRead(t, registry)
	failures := []struct {
		args []string
		want string
	}{
		{[]string{"model", "add", "openrouter", "x/y"}, `required flag(s) "family" not set`},
		{[]string{"model", "add", "openrouter"}, "accepts 2 arg(s), received 1"},
		{[]string{"model", "add", "openrouter", "qwen/qwen3.8-27b", "--family", "f", "--id", "openrouter/qwen"}, `model already exists: "openrouter/qwen"`},
		{[]string{"model", "add", "corp", "m", "--family", "f"}, `provider_id "corp" names no provider row`},
		{[]string{"model", "add", "openrouter", "x/y", "--family", "f", "--input-price", "cheap"}, `input-price must be a number, got "cheap"`},
		{[]string{"model", "add", "openrouter", "x/y", "--family", "f", "--id", "has space"}, `an id is <provider>/<name> with no spaces, got "has space"`},
		{[]string{"model", "edit", "openrouter/qwen"}, "nothing to change"},
		{[]string{"model", "edit", "openrouter/nope", "--family", "f"}, "model not found: \"openrouter/nope\" in the registry (`wt model list` shows every id)"},
		{[]string{"model", "edit", "openrouter/qwen", "--location", "mars"}, `location must be local or cloud, got "mars"`},
		{[]string{"model", "rm", "openrouter/qwen", "openrouter/nope", "--yes"}, `model not found: "openrouter/nope"`},
		{[]string{"model", "rm"}, "requires at least 1 arg(s)"},
	}
	for _, f := range failures {
		if _, err := runWT(t, f.args...); err == nil || !strings.Contains(err.Error(), f.want) {
			t.Errorf("wt %s: err = %v, want it to contain %q", strings.Join(f.args, " "), err, f.want)
		}
	}
	if got := mustRead(t, registry); got != before {
		t.Error("a failed command changed the registry")
	}
	if *syncs != 2 {
		t.Errorf("syncs = %d, want none for a failed command", *syncs)
	}

	asked := stubConfirmRemove(t, false)
	if _, err := runWT(t, "model", "rm", "ollama/gemma4:9b"); err == nil || !strings.Contains(err.Error(), "cancelled — nothing was removed") {
		t.Errorf("declined rm: err = %v, want a cancellation", err)
	}
	if want := "Remove 1 model(s) from the registry (ollama/gemma4:9b)? Weights are not deleted."; *asked != want {
		t.Errorf("asked %q, want %q", *asked, want)
	}
	if got := mustRead(t, registry); got != before {
		t.Error("a declined removal changed the registry")
	}

	out, err = runWT(t, "model", "rm", "ollama/gemma4:9b", "openrouter/qwen", "ollama/gemma4:9b", "--yes")
	want := "removed model: ollama/gemma4:9b\n  still pulled in ollama (`ollama rm gemma4:9b` deletes it)\nremoved model: openrouter/qwen\n"
	if err != nil || out != want {
		t.Fatalf("rm = %q, %v\nwant %q", out, err, want)
	}
	if got := mustRead(t, registry); strings.Contains(got, "gemma4") || strings.Contains(got, "openrouter/qwen") || !strings.Contains(got, `id = "openrouter/a--b"`) {
		t.Errorf("after rm:\n%s", got)
	}
	if *syncs != 3 {
		t.Errorf("syncs = %d, want one more for the removal", *syncs)
	}
}

// TestModelRmWithoutATerminalNeedsYes verifies `wt model rm` in a script
// (no terminal to ask on) refuses, names --yes and removes nothing, instead
// of removing unasked or hanging. The terminal is taken away through the
// openTTY seam, so the refusal is tested on a developer's machine too, where
// the test run has one.
func TestModelRmWithoutATerminalNeedsYes(t *testing.T) {
	registry := modelHome(t, writeRegistry)
	syncs := stubRouteSync(t, "")
	oldTTY, oldConfirm := openTTY, confirmRemove
	openTTY = func() (*os.File, error) { return nil, errors.New("no tty") }
	confirmRemove = promptStop // the real prompt; TestMain fails the seam by default
	t.Cleanup(func() { openTTY, confirmRemove = oldTTY, oldConfirm })

	var out, errOut bytes.Buffer
	err := runModelRm(&out, &errOut, nil, []string{"openrouter/a--b"}, false)
	want := "Remove 1 model(s) from the registry (openrouter/a--b)? Weights are not deleted. — rerun with --yes to confirm"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if got := mustRead(t, registry); got != writeRegistry || *syncs != 0 || out.Len() != 0 {
		t.Errorf("a refused removal must change nothing: syncs = %d, stdout = %q", *syncs, out.String())
	}
}

// TestModelRmStillWorksWhenTheConfigDoesNotLoad verifies a removal goes
// through when wt could not load its config, with no weights line: removing
// a bad row is one of the ways to repair a registry, and it must not need a
// registry that already loads.
func TestModelRmStillWorksWhenTheConfigDoesNotLoad(t *testing.T) {
	registry := modelHome(t, writeRegistry)
	stubRouteSync(t, "")
	var out, errOut bytes.Buffer
	if err := runModelRm(&out, &errOut, nil, []string{"ollama/gemma4:9b"}, true); err != nil {
		t.Fatal(err)
	}
	if out.String() != "removed model: ollama/gemma4:9b\n" {
		t.Errorf("stdout = %q, want the removal and no weights line", out.String())
	}
	if strings.Contains(mustRead(t, registry), "gemma4") {
		t.Error("the row is still in the registry")
	}
}

// redirectedRegistry is a throwaway home whose registry lives under scratch/,
// away from the default registry path, with the default config.yaml beside
// it holding an empty model_list. WT_LITELLM_CONFIG is unset, the route sync
// is the real one and ollama answers its probe with nothing pulled, so the
// sync has no provider to warn about. It returns the registry's path and the
// default config.yaml's.
func redirectedRegistry(t *testing.T, content string) (registry, defaultYAML string) {
	t.Helper()
	home := t.TempDir()
	registry = filepath.Join(home, "scratch", "registry.toml")
	defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
	for path, body := range map[string]string{registry: content, defaultYAML: "model_list: []\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	t.Setenv("WT_REGISTRY", registry)
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	t.Setenv("OPENROUTER_API_KEY", "sk-test-not-a-key")
	realRouteSync(t)
	stubProbeInventory(t, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	return registry, defaultYAML
}

// TestModelWritesUnderARedirectedRegistry is the scratch-registry safety
// test for add, edit and rm. With the registry redirected the write always
// succeeds. LiteLLM's config.yaml follows neither registry variable, so
// unnamed, the default file must not be touched and the user is told why;
// named, that file is the one synced. The exit status is 0 either way.
func TestModelWritesUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string) {
		registry, defaultYAML = redirectedRegistry(t, writeRegistry)
		stubSeedEnv(t, config.SeedEnv{})
		return registry, defaultYAML
	}
	verbs := []struct {
		name string
		run  func(out, errOut *bytes.Buffer) error
		in   string // what the registry holds afterwards
		out  string // and what it no longer does
	}{
		{"add", func(out, errOut *bytes.Buffer) error {
			return runModelAdd(out, errOut, nil, modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "c/d", Fields: modeladmin.Fields{Family: sp("f")}})
		}, `id = "openrouter/c--d"`, ""},
		{"edit", func(out, errOut *bytes.Buffer) error {
			return runModelEdit(out, errOut, "openrouter/a--b", modeladmin.Fields{Family: sp("renamed")})
		}, `family = "renamed"`, ""},
		{"rm", func(out, errOut *bytes.Buffer) error {
			return runModelRm(out, errOut, nil, []string{"openrouter/a--b"}, true)
		}, `id = "ollama/gemma4:9b"`, `id = "openrouter/a--b"`},
	}
	for _, v := range verbs {
		t.Run(v.name+": config.yaml not named", func(t *testing.T) {
			registry, defaultYAML := setup(t)
			var out, errOut bytes.Buffer
			if err := v.run(&out, &errOut); err != nil {
				t.Fatalf("the registry write succeeded, so the command must too: %v", err)
			}
			got := mustRead(t, registry)
			if !strings.Contains(got, v.in) || (v.out != "" && strings.Contains(got, v.out)) {
				t.Errorf("the redirected registry was not written:\n%s", got)
			}
			wantWarn := "warning: LiteLLM routes not touched: the registry is " + registry +
				" but config.yaml is the default " + defaultYAML +
				" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
			if errOut.String() != wantWarn {
				t.Errorf("stderr = %q\nwant %q", errOut.String(), wantWarn)
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
		t.Run(v.name+": config.yaml named", func(t *testing.T) {
			registry, defaultYAML := setup(t)
			named := filepath.Join(t.TempDir(), "scratch-config.yaml")
			if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WT_LITELLM_CONFIG", named)
			var out, errOut bytes.Buffer
			if err := v.run(&out, &errOut); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want no warning when config.yaml is named", errOut.String())
			}
			if got := mustRead(t, registry); !strings.Contains(got, v.in) {
				t.Errorf("the redirected registry was not written:\n%s", got)
			}
			if got := mustRead(t, named); got == "model_list: []\n" {
				t.Error("the named config.yaml was not synced")
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
	}
}
