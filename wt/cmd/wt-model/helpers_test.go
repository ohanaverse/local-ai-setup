package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modeleditor"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// The helpers below are copied from cmd/wt-session's tests (helpers_test.go,
// commands_config_test.go, model_test.go, model_write_test.go and
// testmain_test.go): the Models screen and its route sync moved here, and
// their tests with them.

// testHost is a host whose seams reach nothing on the developer's machine:
// the probe reads on-disk state only, no ollama is run, nothing is seeded
// from PATH, stdin is no terminal, the Models screen cannot open and a
// registry write's route sync reports that it was not stubbed, and stdout is
// no terminal (width 0, so a table does not depend on where `go test` was
// started). A test replaces the fields it exercises; each test owns its
// host, so nothing is shared between parallel tests.
func testHost() *host {
	h := newHost()
	h.probeInventory = localmodels.OnDiskSnapshotForTest
	h.ollamaCaps = func(*config.Config, string) (map[string]any, error) {
		return nil, errors.New("ollamaCaps not stubbed in this test")
	}
	h.seedEnv = func() config.SeedEnv { return config.SeedEnv{} }
	h.syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		return "syncRoutesAfterWrite not stubbed in this test"
	}
	h.stdinTTY = func() bool { return false }
	h.stdoutWidth = func() int { return 0 }
	h.modeleditorRun = func(themes.Theme, *config.Config, error, modeleditor.Options) (modeleditor.Result, error) {
		return modeleditor.Result{}, errors.New("the Models screen is not stubbed in this test")
	}
	return h
}

// execWTModel runs `wt-model <args>` over the real root tree (as main builds
// it) on host h, with the two streams separate, and returns them with the
// command's error.
func execWTModel(t *testing.T, h *host, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	asWTModel(t)
	cmd := rootCmdWith(fakeDeps, h)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

// editorCall records one run of the Models screen.
type editorCall struct {
	called bool
	cfg    *config.Config
	cfgErr error
	opts   modeleditor.Options
}

// stubModelEditor replaces h's Models screen (which needs a terminal) with
// one that records how it was opened and returns res and err.
func stubModelEditor(h *host, res modeleditor.Result, err error) *editorCall {
	call := &editorCall{}
	h.modeleditorRun = func(_ themes.Theme, cfg *config.Config, cfgErr error, o modeleditor.Options) (modeleditor.Result, error) {
		*call = editorCall{called: true, cfg: cfg, cfgErr: cfgErr, opts: o}
		return res, err
	}
	return call
}

// stubRouteSync replaces h's route sync with one that records its calls and
// returns warning.
func stubRouteSync(h *host, warning string) *int {
	calls := 0
	h.syncRoutesAfterWrite = func(io.Writer, io.Writer) string {
		calls++
		return warning
	}
	return &calls
}

// realRouteSync lets a test run the real route sync after a registry write.
// It stays off the machine: no provider is probed (an empty inventory) and
// the proxy restart is `true`. The caller decides which config.yaml, if any,
// the sync can reach.
func realRouteSync(t *testing.T, h *host) {
	t.Helper()
	h.syncRoutesAfterWrite = h.realSyncRoutesAfterWrite
	stubProbeInventory(h, localmodels.Snapshot{})
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
}

// stubProbeInventory makes h see snap as the inventory.
func stubProbeInventory(h *host, snap localmodels.Snapshot) {
	h.probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
}

// stubSeedEnv makes an add through h seed for the machine env describes.
func stubSeedEnv(h *host, env config.SeedEnv) {
	h.seedEnv = func() config.SeedEnv { return env }
}

// stubOllamaCaps answers h's capability lookup and counts its calls.
func stubOllamaCaps(h *host, info map[string]any, err error) *int {
	calls := 0
	h.ollamaCaps = func(*config.Config, string) (map[string]any, error) {
		calls++
		return info, err
	}
	return &calls
}

func onPath(commands ...string) func(string) bool {
	return func(c string) bool { return slices.Contains(commands, c) }
}

// withCleanConfigEnv points HOME and XDG_CONFIG_HOME at home for the test.
func withCleanConfigEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

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

// redirectedRegistry is a throwaway home whose registry lives under scratch/,
// away from the default registry path, with the default config.yaml beside
// it holding an empty model_list. WT_LITELLM_CONFIG is unset, the route sync
// is the real one and ollama answers its probe with nothing pulled, so the
// sync has no provider to warn about. It returns the registry's path and the
// default config.yaml's.
func redirectedRegistry(t *testing.T, h *host, content string) (registry, defaultYAML string) {
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
	t.Setenv("WT_REGISTRY", registry)
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	t.Setenv("OPENROUTER_API_KEY", "sk-test-not-a-key")
	realRouteSync(t, h)
	stubProbeInventory(h, localmodels.Snapshot{Providers: map[string]localmodels.Status{"ollama": localmodels.StatusOK}})
	return registry, defaultYAML
}
