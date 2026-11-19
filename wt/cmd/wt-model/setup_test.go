package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
)

// TestWtModelSetupOpensTheSetupTab verifies `wt-model setup`, on a terminal,
// opens the Models screen on its Setup tab and hands that tab the machine's
// setup functions.
func TestWtModelSetupOpensTheSetupTab(t *testing.T) {
	modelHome(t, writeRegistry)
	h := testHost()
	h.stdinTTY = func() bool { return true }
	call := stubModelEditor(h, modeleditor.Result{}, nil)
	syncs := stubRouteSync(h, "")
	if _, _, err := execWTModel(t, h, "setup"); err != nil {
		t.Fatal(err)
	}
	if !call.called || call.opts.StartTab != modeleditor.TabSetup {
		t.Fatalf("called = %v, start tab = %d; want the screen opened on the Setup tab", call.called, call.opts.StartTab)
	}
	if call.opts.Setup.Observe == nil {
		t.Error("the Setup tab was not given its Observe function")
	}
	if *syncs != 0 {
		t.Errorf("syncs = %d with nothing changed, want 0", *syncs)
	}
}

// TestWtModelSetupGivesTheRoutesCheckTheProbeSeam pins the Setup tab's
// routes check end to end in wt-model (the twin of cmd/wt-session's
// TestDesiredRouteIDsAreWhatASyncWouldWrite): with the registry, the
// host's probeInventory seam and config.yaml all faked, setupEnv's
// DesiredRoutes asks that seam and returns the ids a sync would leave, a
// cloud model added and a stale wt row dropped. It touches no real provider
// or config.
func TestWtModelSetupGivesTheRoutesCheckTheProbeSeam(t *testing.T) {
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
	h := testHost()
	probes := 0
	h.probeInventory = func(*config.Config) localmodels.Snapshot {
		probes++
		return localmodels.Snapshot{}
	}

	got, err := h.setupEnv().DesiredRoutes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"openrouter/a--b"}) {
		t.Fatalf("DesiredRoutes = %v, want [openrouter/a--b]", got)
	}
	if probes == 0 {
		t.Error("the routes check did not ask the host's probeInventory seam")
	}
}

// TestWtModelSetupWithoutATerminalSaysSo pins `wt-model setup` with no
// terminal on stdin: the screen is not opened and the error says why.
func TestWtModelSetupWithoutATerminalSaysSo(t *testing.T) {
	modelHome(t, writeRegistry)
	h := testHost()
	h.stdinTTY = func() bool { return false }
	call := stubModelEditor(h, modeleditor.Result{}, nil)
	_, _, err := execWTModel(t, h, "setup")
	if err == nil || err.Error() != "wt-model setup needs a terminal to open the Setup tab" {
		t.Errorf("err = %v, want %q", err, "wt-model setup needs a terminal to open the Setup tab")
	}
	if call.called {
		t.Error("the screen was opened without a terminal")
	}
}
