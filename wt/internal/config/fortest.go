package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// IsolateConfigHomeForTest points the test process's config environment at a
// throwaway directory, so Dir(), RegistryPath() and everything derived from
// them never resolve into the developer's real ~/.config/agent-wt. The test
// binaries that need it (cmd/wt, internal/tui and internal/config) call it
// from their TestMain: their tests launch stub agents, record usage, take
// profile locks and load the config, and without this every run rewrote the
// real rotation.state with a test model and appended wt-stub launches to the
// real usage.jsonl. WT_REGISTRY and MODELMAN_REGISTRY are cleared for the same
// reason: either would send Load to the developer's registry whatever XDG
// says. A test that sets its own XDG_CONFIG_HOME (t.Setenv) still wins.
//
// It also arms registryWriteGuard for the rest of the process: UpdateRegistry
// then refuses the registry this environment named before the redirect, and
// the default one under the home directory, so a test that undoes the
// redirect (an empty XDG_CONFIG_HOME with the real HOME) fails instead of
// rewriting the developer's registry.
//
// The returned home names the throwaway directory (its name is what
// TestConfigHomeIsNotTheDevelopersOwn pins on); the returned cleanup removes
// it and must run after m.Run(), before os.Exit. Production code never calls
// it — like internal/localmodels' OnDiskSnapshotForTest, it exists so
// cross-package tests share one definition instead of a copy each.
func IsolateConfigHomeForTest() (home string, cleanup func()) {
	// Before the environment changes: this is the developer's registry.
	guardedRegistryPaths = append(guardedRegistryPaths, developerRegistryPaths()...)
	registryWriteGuard = refuseRegistryPaths(guardedRegistryPaths)
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

// errRegistryGuarded is what registryWriteGuard returns in a test binary.
var errRegistryGuarded = errors.New("a test tried to write the developer's registry")

// guardedRegistryPaths is every path IsolateConfigHomeForTest has protected
// in this process. It only grows: a second call (a test of the helper itself)
// must not drop the paths the first one found.
var guardedRegistryPaths []string

// developerRegistryPaths lists the registry files a test must never write:
// the one the current environment names, the default one under the home
// directory, and what each of those resolves to through symlinks — the file's
// own link or a linked directory above it.
func developerRegistryPaths() []string {
	paths := []string{RegistryPath()}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, ".config", "local-ai", "registry.toml"))
	}
	for _, p := range slices.Clone(paths) {
		paths = append(paths, resolveExisting(p))
	}
	for i := range paths {
		paths[i] = filepath.Clean(paths[i])
	}
	return paths
}

// resolveExisting follows the symlinks in p as far as p exists: the longest
// leading part that is on disk is resolved and the rest is joined back on. A
// registry that has not been created yet, under a directory reached through a
// link, still resolves to where it would be written.
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for dir := p; ; {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// refuseRegistryPaths builds a registryWriteGuard that refuses a write whose
// named path or resolved target is one of protected, as spelled or with its
// symlinks followed (a linked directory, or /tmp for /private/tmp).
func refuseRegistryPaths(protected []string) func(named, target string) error {
	return func(named, target string) error {
		for _, p := range []string{named, target} {
			if p == "" {
				continue
			}
			if slices.Contains(protected, filepath.Clean(p)) || slices.Contains(protected, resolveExisting(p)) {
				return fmt.Errorf("%w: %s", errRegistryGuarded, p)
			}
		}
		return nil
	}
}

// RegistryWriteGuardArmed reports whether this process refuses writes to the
// developer's registry. Test binaries assert it; production never calls it.
func RegistryWriteGuardArmed() bool { return registryWriteGuard != nil }
