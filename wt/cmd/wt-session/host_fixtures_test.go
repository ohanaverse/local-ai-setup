package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Helpers that wt's registry-write tests defined (model_write_test.go, which
// moved to cmd/wt-model with `wt-model model init|add|edit|rm`) and that the
// tests still here use: a throwaway home with a registry, a file read, a run
// of the real command tree. (The redirected-registry helper moved to
// cmd/wt-model with cloud-sync.)

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

// runWT runs the wt command line in-process and returns stdout+stderr.
func runWT(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runWTWith(t, testHooks(), args...)
}

// runWTWith is runWT over an app carrying h (testHooks() is the starting value for a test that overrides other hooks).
func runWTWith(t *testing.T, h hooks, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	root := rootCmdWith(h)
	root.SetOut(&buf)
	root.SetErr(&buf)
	if args == nil {
		// Cobra reads a nil slice as "use os.Args[1:]", the test binary's own
		// command line.
		args = []string{}
	}
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}
