package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// RegistryPath returns the modelman-owned registry.toml location. It honors
// MODELMAN_REGISTRY as an explicit override, then XDG_CONFIG_HOME, falling
// back to ~/.config — the same precedence modelman's _default_registry_path
// uses, so the two tools agree on the registry location. wt reads this file
// read-only.
func RegistryPath() string {
	// MODELMAN_REGISTRY is the only branch with a side effect: it writes to
	// stderr on expandHome failure. Acceptable because the path-resolution
	// failure must be visible to the user, and there is no logger to inject
	// at this layer. See expandHome's docstring for the literal-fallback contract.
	if override := os.Getenv("MODELMAN_REGISTRY"); override != "" {
		if expanded, err := expandHome(override); err == nil {
			return expanded
		} else {
			fmt.Fprintf(os.Stderr, "wt: cannot expand MODELMAN_REGISTRY (%v); using literal path\n", err)
			return override
		}
	}
	return filepath.Join(baseConfigHome(), "local-ai", "registry.toml")
}

// ModelmanPath returns the modelman-owned modelman.toml location. It uses the
// same XDG base-directory resolution as RegistryPath(): XDG_CONFIG_HOME (with
// tilde expansion), falling back to ~/.config. It does NOT honor
// MODELMAN_REGISTRY. wt reads this file read-only.
func ModelmanPath() string {
	return filepath.Join(baseConfigHome(), "local-ai", "modelman.toml")
}

// expandHome expands a leading "~" or "~/" in path to the user's home
// directory, matching Python's Path.expanduser() semantics used by
// modelman's _default_registry_path so MODELMAN_REGISTRY behaves the same
// in both tools. Paths that don't start with "~" are returned unchanged.
//
// "~username/..." forms are NOT expanded: Go has no portable equivalent
// of Python's pwd.getpwnam. Returning the literal keeps the failure mode
// loud (wt will report "registry not found" rather than silently reading
// the current user's home).
//
// Returns the error from os.UserHomeDir() so callers can surface a
// clearer message than "file not found" when HOME is unset.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path, err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// loadRegistry decodes modelman-owned registry.toml into providers and
// models. Registry fields wt doesn't consume (cost, model_info, fetch, and
// model_dir) are ignored by the decoder; auth fields are parsed into the
// provider data, and auth.type drives Model.Native — the single source of
// truth for native-ness, consumed by driver dispatch and resume-skip.
//
// Fail-closed: a missing or malformed registry is an error — wt has no
// editor for this file; seed it once with `modelman migrate`.
func loadRegistry() ([]Provider, []Model, error) {
	path := RegistryPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, fmt.Errorf(
			"model registry not found at %s — seed it with `modelman migrate`", path)
	}
	if err != nil {
		return nil, nil, err
	}
	var reg struct {
		Providers []Provider `toml:"providers"`
		Models    []Model    `toml:"models"`
	}
	if _, err := toml.Decode(string(data), &reg); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return reg.Providers, reg.Models, nil
}
