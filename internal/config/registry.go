package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RegistryPath returns the modelman-owned registry.toml location. It honors
// XDG_CONFIG_HOME the same way Dir() does; modelman writes the registry to
// ~/.config/local-ai/registry.toml by default. wt reads this file read-only.
func RegistryPath() string {
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
