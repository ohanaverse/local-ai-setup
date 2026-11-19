package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessioneditor"
)

// TestModelCommandGroup pins the `wt model` group as cobra serves it: bare
// `wt model` points at `wt-model model` (TestBareWtModelPointsAtWtModel), an
// unknown word under it is an error rather than a worktree name, `model
// init` under it is the pointer stub (TestMovedCommandsPointAtWtModel; the command
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
		t.Errorf("model init extra under wt: err = %v, want the pointer to wt-model model init", err)
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
