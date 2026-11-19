package main

import (
	"bytes"
	"errors"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeladmin"
)

// TestModelAddOnAFreshMachine verifies the first `wt-model model add` on a machine
// with no registry: one write creates the file with the model and the default
// row of its provider, both are named in the output, and the routes are
// synced exactly once. This is the first thing a new user does after `wt-model
// model init`, or instead of it.
func TestModelAddOnAFreshMachine(t *testing.T) {
	h := testHost()
	registry := modelHome(t, "")
	stubSeedEnv(h, config.SeedEnv{})
	syncs := stubRouteSync(h, "")
	var out, errOut bytes.Buffer
	req := modeladmin.AddRequest{ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit", Fields: modeladmin.Fields{Family: sp("qwen3.8"), Tags: sp("code")}}
	if err := h.runModelAdd(&out, &errOut, nil, req); err != nil {
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
		h := testHost()
		registry := modelHome(t, writeRegistry)
		stubRouteSync(h, "")
		calls := stubOllamaCaps(h, map[string]any{"supports_function_calling": true, "supports_vision": true}, nil)
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3")}}
		if err := h.runModelAdd(&out, &errOut, nil, req); err != nil {
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
		h := testHost()
		registry := modelHome(t, writeRegistry)
		stubRouteSync(h, "")
		stubOllamaCaps(h, nil, errors.New("`ollama show qwen3:8b` (at http://localhost:11434) failed: exit status 1"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3")}}
		if err := h.runModelAdd(&out, &errOut, nil, req); err != nil {
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
		h := testHost()
		registry := modelHome(t, writeRegistry)
		calls := stubOllamaCaps(h, nil, errors.New("must not be called"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "ollama", ModelName: "qwen3:8b", Fields: modeladmin.Fields{Family: sp("qwen3"), InputPrice: sp("cheap")}}
		err := h.runModelAdd(&out, &errOut, nil, req)
		if err == nil || !strings.Contains(err.Error(), `input-price must be a number, got "cheap"`) || *calls != 0 {
			t.Errorf("err = %v, lookups = %d; want the price refused before `ollama show` runs (it can take its whole timeout)", err, *calls)
		}
		if got := mustRead(t, registry); got != writeRegistry {
			t.Error("a refused add changed the registry")
		}
	})
	t.Run("not an ollama model", func(t *testing.T) {
		h := testHost()
		modelHome(t, writeRegistry)
		stubRouteSync(h, "")
		calls := stubOllamaCaps(h, nil, errors.New("must not be called"))
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "c/d", Fields: modeladmin.Fields{Family: sp("f")}}
		if err := h.runModelAdd(&out, &errOut, nil, req); err != nil {
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
	h := testHost()
	registry := modelHome(t, writeRegistry)
	stubSeedEnv(h, config.SeedEnv{})
	syncs := stubRouteSync(h, "")
	stubProbeInventory(h, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "ollama", ModelID: "ollama/gemma4:9b", ModelName: "gemma4:9b", Artifact: "gemma4:9b", Registered: true, ArtifactKnown: true}},
	})

	out, err := runWTModelOn(t, h, "model", "add", "openrouter", "qwen/qwen3.8-27b", "--family", "qwen3.8", "--tags", "code,design",
		"--input-price", "0.5", "--output-price", "2", "--id", "openrouter/qwen")
	if err != nil || out != "added model: openrouter/qwen\n" {
		t.Fatalf("add = %q, %v", out, err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "id = \"openrouter/qwen\"\nfamily = \"qwen3.8\"\nprovider_id = \"openrouter\"\nmodel_name = \"qwen/qwen3.8-27b\"\nsource = \"manual\"\ntags = [\n    \"code\",\n    \"design\",\n]\n\n[models.cost]\ninput_price_per_million = 0.5\noutput_price_per_million = 2.0\n") {
		t.Errorf("the added row is not as asked:\n%s", got)
	}

	out, err = runWTModelOn(t, h, "model", "edit", "openrouter/qwen", "--tags", "", "--output-price", "2.5")
	if err != nil || out != "updated model: openrouter/qwen\n" {
		t.Fatalf("edit = %q, %v", out, err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "model_name = \"qwen/qwen3.8-27b\"\nsource = \"manual\"\ntags = []\n\n[models.cost]\ninput_price_per_million = 0.5\noutput_price_per_million = 2.5\n") {
		t.Errorf("the edit did not change exactly the two fields:\n%s", got)
	}
	if out, err = runWTModelOn(t, h, "model", "edit", "openrouter/qwen", "--output-price", "2.5"); err != nil || out != "no change: openrouter/qwen\n" {
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
		{[]string{"model", "edit", "openrouter/nope", "--family", "f"}, "model not found: \"openrouter/nope\" in the registry (`wt-model model list` shows every id)"},
		{[]string{"model", "edit", "openrouter/qwen", "--location", "mars"}, `location must be local or cloud, got "mars"`},
		{[]string{"model", "rm", "openrouter/qwen", "openrouter/nope", "--yes"}, `model not found: "openrouter/nope"`},
		{[]string{"model", "rm"}, "requires at least 1 arg(s)"},
	}
	for _, f := range failures {
		if _, err := runWTModelOn(t, h, f.args...); err == nil || !strings.Contains(err.Error(), f.want) {
			t.Errorf("wt %s: err = %v, want it to contain %q", strings.Join(f.args, " "), err, f.want)
		}
	}
	if got := mustRead(t, registry); got != before {
		t.Error("a failed command changed the registry")
	}
	if *syncs != 2 {
		t.Errorf("syncs = %d, want none for a failed command", *syncs)
	}

	asked := stubConfirmRemove(h, false)
	if _, err := runWTModelOn(t, h, "model", "rm", "ollama/gemma4:9b"); err == nil || !strings.Contains(err.Error(), "approval missing: declined; nothing was removed") || exitcode.Of(err) != 3 {
		t.Errorf("declined rm: err = %v (exit %d), want a cancellation, exit 3", err, exitcode.Of(err))
	}
	if want := "Remove 1 model(s) from the registry (ollama/gemma4:9b)? Weights are not deleted."; *asked != want {
		t.Errorf("asked %q, want %q", *asked, want)
	}
	if got := mustRead(t, registry); got != before {
		t.Error("a declined removal changed the registry")
	}

	out, err = runWTModelOn(t, h, "model", "rm", "ollama/gemma4:9b", "openrouter/qwen", "ollama/gemma4:9b", "--yes")
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

// TestModelRmWithoutATerminalNeedsYes verifies `wt-model model rm` in a script
// (no terminal to ask on) refuses, names --yes and removes nothing, instead
// of removing unasked or hanging. The terminal is taken away through the
// openTTY seam, so the refusal is tested on a developer's machine too, where
// the test run has one.
func TestModelRmWithoutATerminalNeedsYes(t *testing.T) {
	h := testHost()
	registry := modelHome(t, writeRegistry)
	syncs := stubRouteSync(h, "")
	h.openTTY = func() (*os.File, error) { return nil, errors.New("no tty") }
	h.confirmRemove = h.realConfirmRemove // the real prompt; testHost fails the seam by default

	var out, errOut bytes.Buffer
	err := h.runModelRm(&out, &errOut, nil, []string{"openrouter/a--b"}, false)
	want := "approval missing: pass --yes (there is no terminal to ask); nothing was removed"
	if err == nil || err.Error() != want || exitcode.Of(err) != 3 {
		t.Fatalf("err = %v (exit %d), want %q and exit 3", err, exitcode.Of(err), want)
	}
	var line bytes.Buffer
	exitcode.Report("wt-model", err, &line)
	if line.String() != "wt-model: "+want+"\n" {
		t.Errorf("reported %q, want one line", line.String())
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
	h := testHost()
	registry := modelHome(t, writeRegistry)
	stubRouteSync(h, "")
	var out, errOut bytes.Buffer
	if err := h.runModelRm(&out, &errOut, nil, []string{"ollama/gemma4:9b"}, true); err != nil {
		t.Fatal(err)
	}
	if out.String() != "removed model: ollama/gemma4:9b\n" {
		t.Errorf("stdout = %q, want the removal and no weights line", out.String())
	}
	if strings.Contains(mustRead(t, registry), "gemma4") {
		t.Error("the row is still in the registry")
	}
}

// TestModelWritesUnderARedirectedRegistry is the scratch-registry safety
// test for add, edit and rm. With the registry redirected the write always
// succeeds. LiteLLM's config.yaml follows neither registry variable, so
// unnamed, the default file must not be touched and the user is told why;
// named, that file is the one synced. The exit status is 0 either way.
func TestModelWritesUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (h *host, registry, defaultYAML string) {
		h = testHost()
		registry, defaultYAML = redirectedRegistry(t, h, writeRegistry)
		stubSeedEnv(h, config.SeedEnv{})
		return h, registry, defaultYAML
	}
	verbs := []struct {
		name string
		run  func(h *host, out, errOut *bytes.Buffer) error
		in   string // what the registry holds afterwards
		out  string // and what it no longer does
		// routes are the model_names the synced config.yaml must hold, and
		// noRoute one it must not: the registry as the verb left it.
		routes  []string
		noRoute string
	}{
		{"add", func(h *host, out, errOut *bytes.Buffer) error {
			return h.runModelAdd(out, errOut, nil, modeladmin.AddRequest{ProviderID: "openrouter", ModelName: "c/d", Fields: modeladmin.Fields{Family: sp("f")}})
		}, `id = "openrouter/c--d"`, "", []string{"openrouter/a--b", "openrouter/c--d"}, ""},
		{"edit", func(h *host, out, errOut *bytes.Buffer) error {
			return h.runModelEdit(out, errOut, "openrouter/a--b", modeladmin.Fields{Family: sp("renamed")})
		}, `family = "renamed"`, "", []string{"openrouter/a--b"}, "openrouter/c--d"},
		{"rm", func(h *host, out, errOut *bytes.Buffer) error {
			return h.runModelRm(out, errOut, nil, []string{"openrouter/a--b"}, true)
		}, `id = "ollama/gemma4:9b"`, `id = "openrouter/a--b"`, nil, "openrouter/a--b"},
	}
	for _, v := range verbs {
		t.Run(v.name+": config.yaml not named", func(t *testing.T) {
			h, registry, defaultYAML := setup(t)
			var out, errOut bytes.Buffer
			if err := v.run(h, &out, &errOut); err != nil {
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
			h, registry, defaultYAML := setup(t)
			named := filepath.Join(t.TempDir(), "scratch-config.yaml")
			if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WT_LITELLM_CONFIG", named)
			var out, errOut bytes.Buffer
			if err := v.run(h, &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want no warning when config.yaml is named", errOut.String())
			}
			if got := mustRead(t, registry); !strings.Contains(got, v.in) {
				t.Errorf("the redirected registry was not written:\n%s", got)
			}
			// The file wt wrote, holding the routes of the registry as the
			// verb left it: "it changed" alone would pass a sync that wrote
			// no route, or one that kept a removed model's.
			synced := mustRead(t, named)
			if !strings.Contains(synced, "litellm_settings:") {
				t.Errorf("the named config.yaml was not synced:\n%s", synced)
			}
			for _, r := range v.routes {
				if !strings.Contains(synced, "{model_name: "+r+",") {
					t.Errorf("the named config.yaml has no route for %s:\n%s", r, synced)
				}
			}
			if strings.Contains(synced, "{model_name: "+v.noRoute+",") {
				t.Errorf("the named config.yaml still routes %s:\n%s", v.noRoute, synced)
			}
			if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
				t.Errorf("the default config.yaml was touched:\n%s", got)
			}
		})
	}
}

// TestModelRmRefusesAnUnknownIdBeforeItAsks verifies an id that is not in
// the registry is refused before the confirmation question: a user who has
// just answered "yes, remove these" must not then be told one of them was
// never there. Nothing is removed, and no sync runs.
func TestModelRmRefusesAnUnknownIdBeforeItAsks(t *testing.T) {
	h := testHost()
	registry := modelHome(t, writeRegistry)
	syncs := stubRouteSync(h, "")
	asked := stubConfirmRemove(h, true)
	_, err := runWTModelOn(t, h, "model", "rm", "openrouter/a--b", "openrouter/nope")
	if want := "model not found: \"openrouter/nope\" in the registry (`wt-model model list` shows every id)"; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if *asked != "" {
		t.Errorf("the question was asked before the refusal: %q", *asked)
	}
	if got := mustRead(t, registry); got != writeRegistry || *syncs != 0 {
		t.Errorf("a refused removal must change nothing: syncs = %d", *syncs)
	}
}

// TestModelRmCommandRunsWhenTheConfigDidNotLoad runs `wt-model model rm` through
// its command with a config load error on the host, as loadHost leaves it (an
// empty Config standing in for the one that could not be read). The command
// must go to the registry itself and not judge the ids by that empty Config:
// removing a row is one way to repair a registry wt cannot load, and without
// the command's own nil-the-config step every id would be "not found".
func TestModelRmCommandRunsWhenTheConfigDidNotLoad(t *testing.T) {
	h := testHost()
	registry := modelHome(t, writeRegistry)
	stubRouteSync(h, "")
	h.probeInventory = func(*config.Config) localmodels.Snapshot {
		t.Error("the providers were probed over a config that did not load")
		return localmodels.Snapshot{}
	}
	h.hostSet, h.hostCfg, h.hostLoadErr = true, &config.Config{DefaultTag: "code"}, errors.New("config.toml: not valid TOML")
	c := modelRmCmd(h)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs([]string{"ollama/gemma4:9b", "--yes"})
	if err := c.Execute(); err != nil {
		t.Fatalf("rm on a config that did not load: %v", err)
	}
	if out.String() != "removed model: ollama/gemma4:9b\n" {
		t.Errorf("output = %q, want the removal and no weights line", out.String())
	}
	if strings.Contains(mustRead(t, registry), "gemma4") {
		t.Error("the row is still in the registry")
	}
}

// dupWriteRegistry is writeRegistry with a second row under the id
// "ollama/gemma4:9b", held by openrouter: wt's validation refuses it
// ("duplicate model id"), and `wt-model model edit` and `wt-model model rm` are the
// commands still let in to repair it.
const dupWriteRegistry = writeRegistry + `
[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "openrouter"
model_name = "google/gemma-4-9b"
tags = []
`

// dupIDRefusal is what every command says about that id.
func dupIDRefusal(registry string) string {
	return `model "ollama/gemma4:9b" is in the registry twice (providers ollama, openrouter); wt cannot tell which one you mean — fix the entry in ` + registry
}

// TestModelEditRefusesAnIDTheRegistryHoldsTwice runs `wt-model model edit` on an
// id two registry rows carry: it fails (exit 1) naming both providers and
// the file to repair, leaves the registry byte for byte as it was, and runs
// no route sync. Before this the edit silently changed the first row — the
// user could not choose the row, and was told "updated model" about an id
// that names two.
func TestModelEditRefusesAnIDTheRegistryHoldsTwice(t *testing.T) {
	h := testHost()
	registry := modelHome(t, dupWriteRegistry)
	syncs := stubRouteSync(h, "")
	out, err := runWTModelOn(t, h, "model", "edit", "ollama/gemma4:9b", "--tags", "code")
	if want := dupIDRefusal(registry); err == nil || err.Error() != want {
		t.Fatalf("err:\n got %v\nwant %s", err, want)
	}
	if !errors.Is(err, config.ErrModelAmbiguous) {
		t.Errorf("err = %v does not match config.ErrModelAmbiguous", err)
	}
	if strings.Contains(out, "updated model") || strings.Contains(out, "Usage:") {
		t.Errorf("output = %q, want neither a success line nor the usage text", out)
	}
	if got := mustRead(t, registry); got != dupWriteRegistry {
		t.Errorf("a refused edit changed the registry:\n%s", got)
	}
	if *syncs != 0 {
		t.Errorf("a refused edit ran %d route sync(s)", *syncs)
	}
	// The row beside the duplicate is still editable: the repair commands
	// must keep working on the rows that are not the problem.
	if _, err := runWTModelOn(t, h, "model", "edit", "openrouter/a--b", "--tags", "code"); err != nil {
		t.Errorf("edit of a row with its own id: %v", err)
	}
	if *syncs != 1 {
		t.Errorf("the edit that went through ran %d route sync(s), want 1", *syncs)
	}
}

// TestModelRmRefusesAnIDTheRegistryHoldsTwice runs `wt-model model rm` with an id
// two registry rows carry, alone and beside a good id: it is refused before
// the confirmation question — as an id that is not there is — with nothing
// removed, the registry byte for byte as it was, and no route sync. Before
// this the first row was removed, whichever provider's it was, and the
// command reported the id as gone while the other row still held it.
func TestModelRmRefusesAnIDTheRegistryHoldsTwice(t *testing.T) {
	h := testHost()
	registry := modelHome(t, dupWriteRegistry)
	syncs := stubRouteSync(h, "")
	asked := stubConfirmRemove(h, true)
	for _, args := range [][]string{
		{"model", "rm", "ollama/gemma4:9b"},
		{"model", "rm", "ollama/gemma4:9b", "--yes"},
		{"model", "rm", "openrouter/a--b", "ollama/gemma4:9b", "--yes"},
	} {
		out, err := runWTModelOn(t, h, args...)
		if want := dupIDRefusal(registry); err == nil || err.Error() != want {
			t.Fatalf("%v: err:\n got %v\nwant %s", args, err, want)
		}
		if strings.Contains(out, "removed model") {
			t.Errorf("%v: output = %q, want no removal line", args, out)
		}
		if *asked != "" {
			t.Errorf("%v: the question was asked before the refusal: %q", args, *asked)
		}
		if got := mustRead(t, registry); got != dupWriteRegistry {
			t.Fatalf("%v: a refused removal changed the registry:\n%s", args, got)
		}
	}
	if *syncs != 0 {
		t.Errorf("the refused removals ran %d route sync(s)", *syncs)
	}
}

// TestModelRmRefusesADuplicatedIDWhenTheConfigDidNotLoad verifies the same
// refusal when wt could not load its config, where the command has no model
// list to check the ids against and the registry write is what finds out:
// the question is answered, nothing is removed, and the message is the same
// one. The removal path that runs on a config that does not load is the one
// a user repairing a registry is most likely to be on.
func TestModelRmRefusesADuplicatedIDWhenTheConfigDidNotLoad(t *testing.T) {
	h := testHost()
	registry := modelHome(t, dupWriteRegistry)
	syncs := stubRouteSync(h, "")
	var out, errOut bytes.Buffer
	err := h.runModelRm(&out, &errOut, nil, []string{"openrouter/a--b", "ollama/gemma4:9b"}, true)
	if want := dupIDRefusal(registry); err == nil || err.Error() != want {
		t.Fatalf("err:\n got %v\nwant %s", err, want)
	}
	if got := mustRead(t, registry); got != dupWriteRegistry || *syncs != 0 || out.Len() != 0 {
		t.Errorf("a refused removal must change nothing: syncs = %d, stdout = %q", *syncs, out.String())
	}
}

// TestModelAddOfADuplicatedIDStillSaysItExists verifies `wt-model model add` of an
// id the registry already holds twice answers "already exists", as for an id
// it holds once: the add is not looking for a row, so "wt cannot tell which
// one you mean" would be the wrong thing to say.
func TestModelAddOfADuplicatedIDStillSaysItExists(t *testing.T) {
	h := testHost()
	registry := modelHome(t, dupWriteRegistry)
	syncs := stubRouteSync(h, "")
	_, err := runWTModelOn(t, h, "model", "add", "openrouter", "x/y", "--family", "f", "--id", "ollama/gemma4:9b")
	if !errors.Is(err, config.ErrModelExists) || errors.Is(err, config.ErrModelAmbiguous) {
		t.Fatalf("err = %v, want config.ErrModelExists", err)
	}
	if got := mustRead(t, registry); got != dupWriteRegistry || *syncs != 0 {
		t.Errorf("a refused add must change nothing: syncs = %d", *syncs)
	}
}

// TestModelAddAPairing verifies `wt-model model add mlx_lm_server <target> --draft
// <draft>` registers the pairing and prints the llmbench command that starts
// it, since wt has no engine for one, and that --draft on any other provider
// is refused. Without the command the user has a registry row and no way to
// find out how to run it.
func TestModelAddAPairing(t *testing.T) {
	h := testHost()
	registry := modelHome(t, writeRegistry)
	stubSeedEnv(h, config.SeedEnv{})
	stubRouteSync(h, "")
	out, err := runWTModelOn(t, h, "model", "add", "mlx_lm_server", "mlx-community/Qwen3.8-27B-4bit", "--draft", "mlx-community/Qwen3.8-4B-4bit", "--family", "qwen3.8")
	want := "added model: mlx_lm_server/Qwen3.8-27B-4bit+draft-Qwen3.8-4B-4bit\n" +
		"start it with: llmbench provider isolate --solo mlx_lm_server mlx-community/Qwen3.8-27B-4bit --draft mlx-community/Qwen3.8-4B-4bit\n" +
		"added provider: mlx_lm_server\n"
	if err != nil || out != want {
		t.Fatalf("add = %q, %v\nwant %q", out, err, want)
	}
	got := mustRead(t, registry)
	if !strings.Contains(got, "[models.fetch]\nrepo = \"mlx-community/Qwen3.8-27B-4bit\"\n\n[models.draft]\nrepo = \"mlx-community/Qwen3.8-4B-4bit\"\n") {
		t.Errorf("the pairing's two sides are not in the registry:\n%s", got)
	}
	// The command names the sides as the row holds them: trimmed.
	var out2, errOut2 bytes.Buffer
	if err := h.runModelAdd(&out2, &errOut2, nil, modeladmin.AddRequest{ProviderID: "mlx_lm_server", ModelName: " org/T ", Draft: " ~/d/D ", Fields: modeladmin.Fields{Family: sp("f")}}); err != nil {
		t.Fatal(err)
	}
	if want := "added model: mlx_lm_server/T+draft-D\nstart it with: llmbench provider isolate --solo mlx_lm_server org/T --draft ~/d/D\n"; out2.String() != want {
		t.Errorf("add with padded arguments = %q\nwant %q", out2.String(), want)
	}
	// A ./ or ../ side is the directory it names from here, written out in
	// full. The registry is read by llmbench from llmbench/ and by wt from
	// anywhere, so a relative local_path would be a different directory for
	// each reader, and the printed command would fail from llmbench/.
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(filepath.Join(work, "quant", "Big-4bit"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(work)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	absT, absD := filepath.Join(cwd, "T"), filepath.Join(filepath.Dir(cwd), "D")
	out, err = runWTModelOn(t, h, "model", "add", "mlx_lm_server", "./T", "--draft", "../D/", "--family", "f", "--id", "mlx_lm_server/relative")
	if want := "added model: mlx_lm_server/relative\nstart it with: llmbench provider isolate --solo mlx_lm_server " + absT + " --draft " + absD + "\n"; err != nil || out != want {
		t.Errorf("add with ./ and ../ sides = %q, %v\nwant %q", out, err, want)
	}
	if got := mustRead(t, registry); !filepath.IsAbs(absT) || !strings.Contains(got, "[models.fetch]\nlocal_path = \""+absT+"\"\n\n[models.draft]\nlocal_path = \""+absD+"\"\n") {
		t.Errorf("the ./ and ../ sides are not in the registry as absolute paths (%s, %s):\n%s", absT, absD, got)
	}
	// The same two directories spelled the same way again are the same
	// pairing, since both spellings are compared as the paths they name.
	if _, err := runWTModelOn(t, h, "model", "add", "mlx_lm_server", "./T/", "--draft", "../work/../D", "--family", "f", "--id", "mlx_lm_server/again"); err == nil || !strings.Contains(err.Error(), "already registers this pairing") {
		t.Errorf("the same ./ and ../ sides again: err = %v, want it refused as the same pairing", err)
	}
	// A side with no such prefix is a repo id even when a directory of that
	// name is here; the add says what it took it for, on stderr, and how to
	// register the directory instead. A repo id that is no directory here
	// (every add above) gets no note.
	out, err = runWTModelOn(t, h, "model", "add", "mlx_lm_server", "quant/Big-4bit", "--draft", "org/D", "--family", "f")
	if want := "added model: mlx_lm_server/Big-4bit+draft-D\n" +
		"start it with: llmbench provider isolate --solo mlx_lm_server quant/Big-4bit --draft org/D\n" +
		"note: quant/Big-4bit is also a directory here; it was registered as a Hugging Face repo — to register the directory, run `wt-model model rm mlx_lm_server/Big-4bit+draft-D` and add the pairing again with ./quant/Big-4bit\n"; err != nil || out != want {
		t.Errorf("add of a bare relative directory = %q, %v\nwant %q", out, err, want)
	}
	if got := mustRead(t, registry); !strings.Contains(got, "[models.fetch]\nrepo = \"quant/Big-4bit\"\n") {
		t.Errorf("a side with no path prefix was not registered as a repo:\n%s", got)
	}
	got = mustRead(t, registry)
	before := got
	for _, args := range [][]string{
		{"model", "add", "mlx_lm_server", "org/target", "--family", "f"},
		{"model", "add", "ollama", "qwen3:8b", "--family", "f", "--draft", "org/d"},
	} {
		if _, err := runWTModelOn(t, h, args...); err == nil || !strings.Contains(err.Error(), "--draft") {
			t.Errorf("wt %s: err = %v, want a refusal about --draft", strings.Join(args, " "), err)
		}
	}
	if mustRead(t, registry) != before {
		t.Error("a refused add changed the registry")
	}
}

// TestModelEditAndRmOfAPairing verifies a pairing `wt-model model add` wrote is
// edited and removed like any model: `edit` changes the metadata it is given
// and leaves [models.fetch] and [models.draft] byte for byte (llmbench starts
// the server from those two tables), and `rm` names both sides as untouched,
// since wt deletes no weights and a local directory the user quantized for
// hours is one of them.
func TestModelEditAndRmOfAPairing(t *testing.T) {
	h := testHost()
	registry := modelHome(t, writeRegistry)
	stubSeedEnv(h, config.SeedEnv{})
	stubRouteSync(h, "")
	const id = "mlx_lm_server/T-4bit+draft-D-4bit"
	if _, err := runWTModelOn(t, h, "model", "add", "mlx_lm_server", "/quant/T-4bit", "--draft", "~/models/D-4bit", "--family", "qwen3.8"); err != nil {
		t.Fatal(err)
	}
	const sides = "[models.fetch]\nlocal_path = \"/quant/T-4bit\"\n\n[models.draft]\nlocal_path = \"~/models/D-4bit\"\n"
	added := mustRead(t, registry)
	if !strings.Contains(added, sides) {
		t.Fatalf("the pairing's two sides are not in the registry:\n%s", added)
	}
	out, err := runWTModelOn(t, h, "model", "edit", id, "--tags", "code", "--family", "qwen")
	if err != nil || out != "updated model: "+id+"\n" {
		t.Fatalf("edit = %q, %v", out, err)
	}
	want := strings.Replace(added, "family = \"qwen3.8\"\nprovider_id = \"mlx_lm_server\"\nmodel_name = \"T-4bit+draft-D-4bit\"\ntags = []\n",
		"family = \"qwen\"\nprovider_id = \"mlx_lm_server\"\nmodel_name = \"T-4bit+draft-D-4bit\"\ntags = [\n    \"code\",\n]\n", 1)
	if got := mustRead(t, registry); got != want || !strings.Contains(got, sides) {
		t.Errorf("registry after the edit =\n%s\nwant only family and tags changed:\n%s", got, want)
	}
	out, err = runWTModelOn(t, h, "model", "rm", id, "--yes")
	if want := "removed model: " + id + "\n  target /quant/T-4bit and draft ~/models/D-4bit are untouched\n"; err != nil || out != want {
		t.Errorf("rm = %q, %v\nwant %q", out, err, want)
	}
	if strings.Contains(mustRead(t, registry), id) {
		t.Error("the pairing is still in the registry")
	}
}

// TestModelEditSourceFlag runs `wt-model model edit --source manual` on an
// openrouter row and checks the registry holds the mark, and that `wt-model
// model add` has no --source flag (add sets manual itself). The flag is how a user
// protects a model added before cloud-sync learned the mark; offering it on
// add too would let a sync-owned row be created already curated by hand.
func TestModelEditSourceFlag(t *testing.T) {
	h := testHost()
	registry := modelHome(t, dupWriteRegistry)
	stubRouteSync(h, "")
	if _, err := runWTModelOn(t, h, "model", "edit", "openrouter/a--b", "--source", "manual"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, registry); !strings.Contains(got, `source = "manual"`) {
		t.Errorf("registry has no source = \"manual\":\n%s", got)
	}
	if _, err := runWTModelOn(t, h, "model", "add", "openrouter", "x/y", "--family", "f", "--source", "manual"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("model add --source: err = %v, want an unknown-flag error", err)
	}
}

// runEditCmd runs `wt-model model edit` through cobra, the way a user types it, so
// the flag parsing is part of what is tested.
func runEditCmd(t *testing.T, h *host, args ...string) (stdout string, err error) {
	t.Helper()
	cmd := modelEditCmd(h)
	var out bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err = cmd.Execute()
	return out.String(), err
}

// TestModelEditServeArgFlags pins the command line of #432 end to end:
// `--serve-arg` is repeated, one argv element each, in either spelling (`--serve-arg
// --profile --serve-arg turbo`, where the flag-looking value is taken as the
// value, and `--serve-arg=--profile`), and reads back as the list; an edit
// that sets them says they apply from the next start; `--clear-serve-args`
// removes them; the two flags together are refused; and a refused value (a
// flag wt sets itself) is named and writes nothing.
func TestModelEditServeArgFlags(t *testing.T) {
	h := testHost()
	registry, _ := redirectedRegistry(t, h, writeRegistry)
	stubSeedEnv(h, config.SeedEnv{})
	var out, errOut bytes.Buffer
	if err := h.runModelAdd(&out, &errOut, nil, modeladmin.AddRequest{ProviderID: "mtplx", ModelName: "Org/T", Fields: modeladmin.Fields{Family: sp("f")}}); err != nil {
		t.Fatal(err)
	}
	read := func() []string {
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Models[config.IndexModelByID(cfg.Models, "mtplx/Org/T")].ServeArgs.Args
	}

	for name, args := range map[string][]string{
		"separate values": {"mtplx/Org/T", "--serve-arg", "--profile", "--serve-arg", "turbo"},
		"equals form":     {"mtplx/Org/T", "--serve-arg=--profile", "--serve-arg=turbo"},
	} {
		if _, err := runEditCmd(t, h, "mtplx/Org/T", "--clear-serve-args"); err != nil {
			t.Fatal(err)
		}
		stdout, err := runEditCmd(t, h, args...)
		if err != nil || !slices.Equal(read(), []string{"--profile", "turbo"}) {
			t.Errorf("%s: err = %v, serve_args = %v, want [--profile turbo]", name, err, read())
		}
		if !strings.Contains(stdout, "updated model: mtplx/Org/T\n") || !strings.Contains(stdout, "note: serve_args apply the next time mtplx/Org/T starts") {
			t.Errorf("%s: output %q lacks the update line and the next-start note", name, stdout)
		}
	}

	if stdout, err := runEditCmd(t, h, "mtplx/Org/T", "--clear-serve-args"); err != nil || read() != nil || !strings.Contains(stdout, "note: serve_args apply") {
		t.Errorf("--clear-serve-args: err = %v, serve_args = %v, output %q", err, read(), stdout)
	}
	if _, err := runEditCmd(t, h, "mtplx/Org/T", "--serve-arg", "--x", "--clear-serve-args"); err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Errorf("both flags: err = %v, want them refused as contradictory", err)
	}
	before := mustRead(t, registry)
	if _, err := runEditCmd(t, h, "mtplx/Org/T", "--serve-arg=--port", "--serve-arg=9"); err == nil || !strings.Contains(err.Error(), "serve_args names --port, which wt sets itself") || mustRead(t, registry) != before {
		t.Errorf("an owned flag: err = %v, registry changed: %v", err, mustRead(t, registry) != before)
	}
	if _, err := runEditCmd(t, h, "mtplx/Org/T"); err == nil || !strings.Contains(err.Error(), "--serve-arg") {
		t.Errorf("no change given: err = %v, want the message to name --serve-arg", err)
	}
}

// agentsToml is a config.toml shaped like a stock one: claude and pi without
// sushi, plus opencode, which no sushi add may touch.
const agentsToml = `[[agents]]
  name = "claude"
  supported_providers = ["claude", "ollama"]
  default_provider = "claude"

[[agents]]
  name = "pi"
  supported_providers = ["ollama", "sushi"]
  default_provider = "ollama"

[[agents]]
  name = "opencode"
  supported_providers = ["ollama"]
  default_provider = "ollama"
`

func writeAgentsToml(t *testing.T, registry string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(filepath.Dir(registry)), "agent-wt", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(agentsToml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSushiAddListsSushiForClaudeAndPi verifies registering a sushi model adds
// "sushi" to the supported_providers of the agents that can run it and only
// those that lack it, says so, and that any other add leaves config.toml alone.
func TestSushiAddListsSushiForClaudeAndPi(t *testing.T) {
	sushi := modeladmin.AddRequest{ProviderID: "sushi", ModelName: "beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw", Fields: modeladmin.Fields{Family: sp("qwen3.8")}}
	t.Run("sushi add", func(t *testing.T) {
		h := testHost()
		registry := modelHome(t, "")
		stubSeedEnv(h, config.SeedEnv{})
		stubRouteSync(h, "")
		cfgPath := writeAgentsToml(t, registry)
		var out, errOut bytes.Buffer
		if err := h.runModelAdd(&out, &errOut, nil, sushi); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "added sushi to supported_providers of: claude\n") || errOut.Len() != 0 {
			t.Errorf("stdout = %q, stderr = %q; want only claude named (pi already lists sushi)", out.String(), errOut.String())
		}
		got := mustRead(t, cfgPath)
		if !strings.Contains(got, `supported_providers = ["claude", "ollama", "sushi"]`) {
			t.Errorf("claude not updated:\n%s", got)
		}
		if strings.Count(got, `"sushi"`) != 2 || strings.Contains(got, `["ollama", "sushi", "sushi"]`) {
			t.Errorf("sushi must be listed once per agent, and not for opencode:\n%s", got)
		}
	})
	t.Run("second sushi add changes nothing", func(t *testing.T) {
		h := testHost()
		registry := modelHome(t, "")
		stubSeedEnv(h, config.SeedEnv{})
		stubRouteSync(h, "")
		cfgPath := writeAgentsToml(t, registry)
		var out, errOut bytes.Buffer
		if err := h.runModelAdd(&out, &errOut, nil, sushi); err != nil {
			t.Fatal(err)
		}
		before := mustRead(t, cfgPath)
		out.Reset()
		other := sushi
		other.ModelName = "beamster/Other-Pack"
		if err := h.runModelAdd(&out, &errOut, nil, other); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "supported_providers") || mustRead(t, cfgPath) != before {
			t.Errorf("a second add rewrote config.toml or announced a change; stdout = %q", out.String())
		}
	})
	t.Run("another provider", func(t *testing.T) {
		h := testHost()
		registry := modelHome(t, "")
		stubSeedEnv(h, config.SeedEnv{})
		stubRouteSync(h, "")
		cfgPath := writeAgentsToml(t, registry)
		var out, errOut bytes.Buffer
		req := modeladmin.AddRequest{ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit", Fields: modeladmin.Fields{Family: sp("qwen3.8")}}
		if err := h.runModelAdd(&out, &errOut, nil, req); err != nil {
			t.Fatal(err)
		}
		if mustRead(t, cfgPath) != agentsToml {
			t.Error("a non-sushi add changed config.toml")
		}
	})
	t.Run("no config.toml", func(t *testing.T) {
		h := testHost()
		modelHome(t, "")
		stubSeedEnv(h, config.SeedEnv{})
		stubRouteSync(h, "")
		var out, errOut bytes.Buffer
		if err := h.runModelAdd(&out, &errOut, nil, sushi); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "supported_providers") || errOut.Len() != 0 {
			t.Errorf("stdout = %q, stderr = %q; want no change and no warning without a config.toml", out.String(), errOut.String())
		}
	})
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
	h := testHost()
	realRouteSync(t, h)
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	t.Setenv("WT_LITELLM_CONFIG", yaml)
	restarts := filepath.Join(t.TempDir(), "restarts")
	t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)

	var out, errOut bytes.Buffer
	if w := h.syncRoutesAfterWrite(&out, &errOut); w != "" {
		t.Fatalf("first sync: %s", w)
	}
	first := mustRead(t, yaml)
	if !strings.Contains(first, "openrouter/a--b") || mustRead(t, restarts) != "restart\n" {
		t.Fatalf("the first sync should write the cloud route and restart once; restarts = %q, config.yaml:\n%s", mustRead(t, restarts), first)
	}
	if w := h.syncRoutesAfterWrite(&out, &errOut); w != "" {
		t.Fatalf("second sync: %s", w)
	}
	if mustRead(t, yaml) != first {
		t.Error("a sync with nothing to change rewrote config.yaml")
	}
	if got := mustRead(t, restarts); got != "restart\n" {
		t.Errorf("restarts = %q after a sync that changed nothing, want still one", got)
	}
}
