package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessioneditor"
)

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

// TestModelCommandGroup pins the `wt model` group as cobra serves it: bare
// `wt model` points at `wt-model model` (TestBareWtModelPointsAtWtModel), an
// unknown word under it is an error rather than a worktree name, `wt model
// init` is the pointer stub (TestMovedCommandsPointAtWtModel; the command
// itself is TestModelCommandGroupOnWtModel in cmd/wt-model), and the removed
// `wt models` keeps its own message.
func TestModelCommandGroup(t *testing.T) {
	modelHome(t, "")
	stubStdinTTY(t, false)
	_, err := runWT(t, "model")
	if err == nil || !strings.Contains(err.Error(), "run 'wt-model model'") {
		t.Errorf("bare `wt model` with no terminal: err = %v, want the pointer to wt-model model", err)
	}
	if _, err = runWT(t, "model", "bogus"); err == nil || !strings.Contains(err.Error(), `unknown command "bogus" for "wt model"`) {
		t.Errorf("wt model bogus: err = %v, want an unknown-command error", err)
	}
	if _, err = runWT(t, "model", "init", "extra"); err == nil || !strings.Contains(err.Error(), "run 'wt-model model init'") {
		t.Errorf("wt model init extra: err = %v, want the pointer to wt-model model init", err)
	}
	if _, err = runWT(t, "models"); err == nil || !strings.Contains(err.Error(), "wt models is removed") {
		t.Errorf("wt models: err = %v, want the removed-subcommand message", err)
	}
}

// TestRegistryWriteGuardIsArmedHere pins that this test binary, whose tests
// reach config.UpdateRegistry through `wt cloud-sync`, cannot write the
// developer's real registry: TestMain must have called
// config.IsolateConfigHomeForTest. Without it one test that unsets
// XDG_CONFIG_HOME would seed provider rows into the registry on the machine
// running the suite.
func TestRegistryWriteGuardIsArmedHere(t *testing.T) {
	if !config.RegistryWriteGuardArmed() {
		t.Fatal("registry write guard is not armed: TestMain must call config.IsolateConfigHomeForTest")
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

// TestBareWtModelPointsAtWtModel pins bare `wt model` after the Models screen
// moved to wt-model: on a terminal or not it opens no editor, prints the one
// pointer line naming `wt-model model`, exits 1 and leaves config.toml and
// registry.toml as they were.
func TestBareWtModelPointsAtWtModel(t *testing.T) {
	for _, tty := range []bool{true, false} {
		registry := modelHome(t, writeRegistry)
		stubStdinTTY(t, tty)
		cfgPath := filepath.Join(config.Dir(), "config.toml")
		_, statErr := os.Stat(cfgPath)
		cfgExisted := statErr == nil
		cfgBefore := ""
		if cfgExisted {
			cfgBefore = mustRead(t, cfgPath)
		}
		regBefore := mustRead(t, registry)
		call := stubConfigEditor(t, sessioneditor.Result{}, nil)
		_, err := runWT(t, "model")
		var buf bytes.Buffer
		if code := exitcode.Report("wt", err, &buf); code != 1 || buf.String() != "wt: model moved to wt-model — run 'wt-model model'; nothing was changed\n" {
			t.Errorf("tty=%v: code %d, reported %q", tty, code, buf.String())
		}
		if call.called {
			t.Errorf("tty=%v: the editor was opened", tty)
		}
		if got := mustRead(t, registry); got != regBefore {
			t.Errorf("tty=%v: registry.toml changed:\n%s", tty, got)
		}
		_, statErr = os.Stat(cfgPath)
		if cfgExisted {
			if got := mustRead(t, cfgPath); got != cfgBefore {
				t.Errorf("tty=%v: config.toml changed:\n%s", tty, got)
			}
		} else if statErr == nil {
			t.Errorf("tty=%v: config.toml was created", tty)
		}
	}
}
