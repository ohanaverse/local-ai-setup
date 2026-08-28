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
	if override := os.Getenv("MODELMAN_REGISTRY"); override != "" {
		return expandHome(override)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = home
		if base != "" {
			base = filepath.Join(base, ".config")
		}
	}
	return filepath.Join(base, "local-ai", "registry.toml")
}

// expandHome expands a leading "~" or "~/" in path to the user's home
// directory, matching Python's Path.expanduser() semantics used by
// modelman's _default_registry_path so MODELMAN_REGISTRY behaves the same
// in both tools. Paths that don't start with "~" are returned unchanged.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// loadRegistry decodes modelman-owned registry.toml into providers and
// models. Registry fields wt doesn't consume (cost, model_info, fetch, and
// model_dir) are ignored by the decoder; auth fields are parsed into the
// provider data but are not consumed by launch behavior.
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
