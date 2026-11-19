package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
)

// TestWtModelModelOpensTheModelsTab verifies `wt-model model`, on a terminal,
// opens the Models screen on its Models tab and hands it this package's own
// seams for the probe, the seeding environment and the ollama lookup — so the
// tab reaches the machine the same way the commands do.
func TestWtModelModelOpensTheModelsTab(t *testing.T) {
	modelHome(t, writeRegistry)
	stubStdinTTY(t, true)
	call := stubModelEditor(t, modeleditor.Result{}, nil)
	syncs := stubRouteSync(t, "")
	// Each seam is given an answer no real function would give, and the
	// functions the tab was handed are then asked: non-nil alone would pass
	// localmodels.Inventory handed over directly, and the tab would probe
	// the developer's providers under this package's tests.
	stubProbeInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{{ModelID: "marker/probe"}}})
	stubSeedEnv(t, config.SeedEnv{OnPath: onPath("marker-seed")})
	stubOllamaCaps(t, map[string]any{"marker_caps": true}, nil)
	if _, _, err := execWTModel(t, "model"); err != nil {
		t.Fatal(err)
	}
	if !call.called || call.opts.StartTab != modeleditor.TabModels {
		t.Fatalf("called = %v, start tab = %d; want the screen opened on the Models tab", call.called, call.opts.StartTab)
	}
	d := call.opts.Models
	if d.Probe == nil || d.SeedEnv == nil || d.Capabilities == nil {
		t.Fatalf("the tab was not given cmd/wt-model's seams: %+v", d)
	}
	if snap := d.Probe(&config.Config{}); len(snap.Entries) != 1 || snap.Entries[0].ModelID != "marker/probe" {
		t.Errorf("the tab's probe is not cmd/wt-model's probeInventory seam: %+v", snap)
	}
	if env := d.SeedEnv(); env.OnPath == nil || !env.OnPath("marker-seed") {
		t.Error("the tab's seeding environment is not cmd/wt-model's seedEnv seam")
	}
	if info, err := d.Capabilities(nil, "x"); err != nil || info["marker_caps"] != true {
		t.Errorf("the tab's ollama lookup is not cmd/wt-model's ollamaCaps seam: %v, %v", info, err)
	}
	if *syncs != 0 {
		t.Errorf("syncs = %d with nothing changed, want 0", *syncs)
	}
}

// TestWtModelModelSyncsRoutesOnceOnExit verifies the Models screen's one
// route sync: it runs once after the screen closes, only when the tab wrote
// the registry, a sync warning is printed, and it still runs when the screen
// ended with an error — the registry write already happened. Per-change syncs
// would restart the proxy once for every model added in a sitting.
func TestWtModelModelSyncsRoutesOnceOnExit(t *testing.T) {
	modelHome(t, writeRegistry)
	stubStdinTTY(t, true)

	stubModelEditor(t, modeleditor.Result{}, nil)
	syncs := stubRouteSync(t, "")
	if _, _, err := execWTModel(t, "model"); err != nil || *syncs != 0 {
		t.Errorf("nothing changed: err = %v, syncs = %d; want no sync", err, *syncs)
	}
	stubModelEditor(t, modeleditor.Result{RegistryChanged: true}, nil)
	syncs = stubRouteSync(t, "LiteLLM routes not synced: proxy config is read-only")
	_, errOut, err := execWTModel(t, "model")
	if err != nil || *syncs != 1 || !strings.Contains(errOut, "warning: LiteLLM routes not synced: proxy config is read-only") {
		t.Errorf("registry changed: err = %v, syncs = %d, stderr = %q; want one sync and its warning", err, *syncs, errOut)
	}

	stubModelEditor(t, modeleditor.Result{RegistryChanged: true}, errors.New("terminal went away"))
	syncs = stubRouteSync(t, "")
	if _, _, err := execWTModel(t, "model"); err == nil || *syncs != 1 {
		t.Errorf("screen failed after a write: err = %v, syncs = %d; want the error and still one sync", err, *syncs)
	}
}

// TestWtModelModelExitSyncUnderARedirectedRegistry is the scratch-registry
// safety test for `wt-model model`: the Models tab writes the registry, and
// the sync that follows on exit obeys the same rule as `wt model add`.
// LiteLLM's config.yaml follows neither registry variable, so with the
// registry redirected and config.yaml not named, the default file is not
// touched and the user is told why; named, that file is the one synced. The
// exit status is 0 either way: the registry change was made.
func TestWtModelModelExitSyncUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string) {
		registry, defaultYAML = redirectedRegistry(t, writeRegistry)
		stubStdinTTY(t, true)
		// The screen itself needs a terminal; what it reports is a registry
		// its Models tab wrote.
		stubModelEditor(t, modeleditor.Result{RegistryChanged: true}, nil)
		return registry, defaultYAML
	}
	t.Run("config.yaml not named", func(t *testing.T) {
		registry, defaultYAML := setup(t)
		out, errOut, err := execWTModel(t, "model")
		if err != nil {
			t.Fatalf("the registry was written, so the command must exit 0: %v", err)
		}
		want := "warning: LiteLLM routes not touched: the registry is " + registry +
			" but config.yaml is the default " + defaultYAML +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
		if got := out + errOut; got != want {
			t.Errorf("output = %q\nwant %q", got, want)
		}
		if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
			t.Errorf("the default config.yaml was touched:\n%s", got)
		}
	})
	t.Run("config.yaml named", func(t *testing.T) {
		_, defaultYAML := setup(t)
		scratchYAML := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(scratchYAML, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WT_LITELLM_CONFIG", scratchYAML)
		out, errOut, err := execWTModel(t, "model")
		if err != nil || strings.Contains(out+errOut, "warning") {
			t.Fatalf("wt-model model = %q, %q, %v; want a clean sync", out, errOut, err)
		}
		if got := mustRead(t, scratchYAML); !strings.Contains(got, "openrouter/a--b") {
			t.Errorf("the named config.yaml was not synced:\n%s", got)
		}
		if got := mustRead(t, defaultYAML); got != "model_list: []\n" {
			t.Errorf("the default config.yaml was touched:\n%s", got)
		}
	})
}

// TestWtModelModelWithoutATerminalSaysSo pins `wt-model model` with no
// terminal on stdin: the screen is not opened and the error says a terminal
// is needed (and which commands work without one).
func TestWtModelModelWithoutATerminalSaysSo(t *testing.T) {
	modelHome(t, writeRegistry)
	stubStdinTTY(t, false)
	call := stubModelEditor(t, modeleditor.Result{}, nil)
	_, _, err := execWTModel(t, "model")
	if err == nil || !strings.HasPrefix(err.Error(), "wt-model model needs a terminal") {
		t.Errorf("err = %v, want it to say wt-model model needs a terminal", err)
	}
	if call.called {
		t.Error("the screen was opened without a terminal")
	}
}

// TestBareWtModelStillPrintsHelp pins bare `wt-model`: cobra's help, naming
// every command (model included) under the new root Short, and no screen.
func TestBareWtModelStillPrintsHelp(t *testing.T) {
	stubStdinTTY(t, true)
	call := stubModelEditor(t, modeleditor.Result{}, nil)
	out, _, err := execWTModel(t)
	if err != nil {
		t.Fatalf("bare wt-model: %v", err)
	}
	for _, want := range []string{"serve", "litellm", "model", "Serve wt's models and administer the registry, routes and this machine's setup"} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not name %q:\n%s", want, out)
		}
	}
	if call.called {
		t.Error("bare wt-model opened the Models screen")
	}
}
