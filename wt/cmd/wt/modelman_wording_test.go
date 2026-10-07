package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// TestRefusalsKeepTheWordingModelmanMatches pins the exact text of the three
// refusals modelman decides on by matching wt's stderr, for as long as
// modelman exists (delete this file with it; see the modelman retirement
// design, Step 6):
//
//   - `wt stop <id>` for a registry model that is not running, and for an id
//     with no registry row. modelman/src/modelman/local_control.py looks for
//     the substrings "is not running" and "unknown model" (_WT_NOT_RUNNING) to
//     tell "nothing to stop" from a failed stop. Reworded, every `modelman
//     stop` of an omlx model that is already unloaded fails with wt's message
//     instead of clearing the model's running flag.
//   - the registry-redirected refusal of a route write. modelman's
//     wt_bridge.py tests whether wt's message starts with "LiteLLM routes not
//     touched" (_REGISTRY_REDIRECTED). Reworded, modelman reports a scratch
//     registry as a failed sync and tells the user to re-run it.
//
// There is no status contract for these (#267 is left unfixed), so the text is
// the contract. modelman strips a leading `wt: ` or `Error: ` from each stderr
// line before matching (wt_bridge.py, _msg), so the `wt:` prefix main puts in
// front of a returned error (main.go) is part of the contract too; this test
// sees the error before that prefix is added.
func TestRefusalsKeepTheWordingModelmanMatches(t *testing.T) {
	t.Run("wt stop, registered and not running", func(t *testing.T) {
		stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
		err := runStop(io.Discard, modelCmdConfig(), "ollama/b:1", true)
		if want := `model "ollama/b:1" is not running`; err == nil || err.Error() != want {
			t.Errorf("err = %v, want exactly %q", err, want)
		}
	})
	t.Run("wt stop, no registry row", func(t *testing.T) {
		stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
		err := runStop(io.Discard, modelCmdConfig(), "ollama/nope:9", true)
		if want := `unknown model "ollama/nope:9"`; err == nil || err.Error() != want {
			t.Errorf("err = %v, want exactly %q", err, want)
		}
	})
	t.Run("route write under a redirected registry", func(t *testing.T) {
		// The combination the refusal exists for: the registry is redirected
		// and nothing names config.yaml. HOME is a temp directory, so the
		// default config.yaml it resolves to is not the developer's; the
		// file has to exist, because a missing one is refused earlier, as
		// "LiteLLM config not found".
		home := t.TempDir()
		registry := filepath.Join(home, "scratch", "registry.toml")
		yaml := filepath.Join(home, ".config", "litellm", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(yaml), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(yaml, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("MODELMAN_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		t.Setenv("WT_LITELLM_RESTART_CMD", "true")
		stubProbeInventory(t, localmodels.Snapshot{})
		err := runLitellmSync(io.Discard, io.Discard, modelCmdConfig(), true, false)
		want := "LiteLLM routes not touched: the registry is " + registry +
			" but config.yaml is the default " + yaml +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v\nwant exactly %q", err, want)
		}
	})
}
