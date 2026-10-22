package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegistryPathPrecedence pins which file is the registry: WT_REGISTRY,
// then MODELMAN_REGISTRY, then $XDG_CONFIG_HOME/local-ai, then ~/.config.
// llmbench (tests/test_registry.py) pins the same order. If wt alone
// resolved a different file, a scratch run would have llmbench benchmarking
// one registry while wt wrote another.
func TestRegistryPathPrecedence(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	cases := []struct {
		name, wt, alias, xdg, want string
	}{
		{"nothing set", "", "", "", filepath.Join(home, ".config", "local-ai", "registry.toml")},
		{"XDG_CONFIG_HOME", "", "", "/custom/xdg", "/custom/xdg/local-ai/registry.toml"},
		{"MODELMAN_REGISTRY beats XDG", "", "/old/registry.toml", "/custom/xdg", "/old/registry.toml"},
		{"WT_REGISTRY beats MODELMAN_REGISTRY", "/new/registry.toml", "/old/registry.toml", "/custom/xdg", "/new/registry.toml"},
		{"WT_REGISTRY alone", "/new/registry.toml", "", "", "/new/registry.toml"},
		{"WT_REGISTRY expands a tilde", "~/scratch/registry.toml", "/old/registry.toml", "", filepath.Join(home, "scratch", "registry.toml")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("WT_REGISTRY", c.wt)
			t.Setenv("MODELMAN_REGISTRY", c.alias)
			t.Setenv("XDG_CONFIG_HOME", c.xdg)
			if got := RegistryPath(); got != c.want {
				t.Errorf("RegistryPath() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRegistryRedirectedSeesWTRegistry pins that the new name counts as a
// redirect. The LiteLLM route writers ask RegistryRedirected before touching
// config.yaml; if WT_REGISTRY were invisible to it, a scratch registry named
// the new way would be reconciled onto the developer's real proxy config.
func TestRegistryRedirectedSeesWTRegistry(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	t.Setenv("WT_REGISTRY", filepath.Join(home, "scratch", "registry.toml"))
	if !RegistryRedirected() {
		t.Error("WT_REGISTRY naming another file should count as a redirected registry")
	}
	t.Setenv("WT_REGISTRY", "~/.config/local-ai/registry.toml")
	if RegistryRedirected() {
		t.Error("WT_REGISTRY spelling the default path is not a redirect")
	}
}

// TestIsolateConfigHomeForTestClearsBothRegistryNames pins the test helper
// every isolating TestMain relies on: after it runs, neither registry
// variable is set and the registry resolves under the throwaway home. A
// developer with WT_REGISTRY exported would otherwise have every wt test run
// read their real registry.
func TestIsolateConfigHomeForTestClearsBothRegistryNames(t *testing.T) {
	// t.Setenv restores all three after the test; the helper's own
	// os.Setenv/os.Unsetenv calls are then undone with them.
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/else")
	t.Setenv("WT_REGISTRY", "/dev/real/registry.toml")
	t.Setenv("MODELMAN_REGISTRY", "/dev/real/registry.toml")

	home, cleanup := IsolateConfigHomeForTest()
	defer cleanup()

	for _, name := range []string{"WT_REGISTRY", "MODELMAN_REGISTRY"} {
		if v, set := os.LookupEnv(name); set {
			t.Errorf("%s is still set (%q)", name, v)
		}
	}
	if got, want := RegistryPath(), filepath.Join(home, "local-ai", "registry.toml"); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
	if !strings.Contains(home, "wt-test-config-") {
		t.Errorf("home = %q, want the throwaway directory", home)
	}
}
