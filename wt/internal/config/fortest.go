package config

import "os"

// IsolateConfigHomeForTest points the test process's config environment at a
// throwaway directory, so Dir(), RegistryPath() and everything derived from
// them never resolve into the developer's real ~/.config/agent-wt. The test
// binaries that need it (cmd/wt and internal/tui) call it from their TestMain:
// their tests launch stub agents, record usage, take profile locks and load
// the config, and without this every run rewrote the real rotation.state with
// a test model and appended wt-stub launches to the real usage.jsonl.
// WT_REGISTRY and MODELMAN_REGISTRY are cleared for the same reason: either
// would send Load to the developer's registry whatever XDG says. A test that sets its own
// XDG_CONFIG_HOME (t.Setenv) still wins. The returned home names the
// throwaway directory (its name is what
// TestConfigHomeIsNotTheDevelopersOwn pins on); the returned cleanup removes
// it and must run after m.Run(), before os.Exit. Production code never calls
// it — like internal/localmodels' OnDiskSnapshotForTest, it exists so
// cross-package tests share one definition instead of a copy each.
func IsolateConfigHomeForTest() (home string, cleanup func()) {
	dir, err := os.MkdirTemp("", "wt-test-config-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	for _, name := range registryEnvNames {
		os.Unsetenv(name)
	}
	return dir, func() { os.RemoveAll(dir) }
}
