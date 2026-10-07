package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrRegistryMissing is returned by loadRegistry when the modelman-owned
// registry.toml does not exist yet. cmd/wt tolerates it for command agents
// (which have no model layer) while still failing closed for real agents.
var ErrRegistryMissing = errors.New("model registry not found")

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
// tilde expansion), falling back to ~/.config. It honors NEITHER
// MODELMAN_REGISTRY NOR modelman's MODELMAN_STATE override — a deliberate
// asymmetry: wt is a read-only consumer and never needs to redirect the state
// file the way tests (or wt itself) redirect the registry. The subset wt
// reads (price_refresh_last_run and the legacy [litellm] table) is pinned by
// docs/contracts/modelman.sample.toml. wt reads this file read-only.
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

// ExpandHome expands a leading "~" or "~/" in path against the user's home
// directory; other paths are returned unchanged.
func ExpandHome(path string) (string, error) { return expandHome(path) }

// ErrRegistryLink is returned when the registry path is a symbolic link that
// cannot be followed to a file: its target does not exist, or the chain of
// links loops. It is deliberately not ErrRegistryMissing, which the same path
// looks like to os.ReadFile. A link is the user's pointer at where their
// registry lives — a dotfiles checkout, a file on a volume that is not mounted
// right now — so wt must neither run as if there were no registry (the
// unconfigured-agent passthrough) nor create a new file in the link's place,
// which would shadow the real registry when its target comes back (#248).
var ErrRegistryLink = errors.New("registry link is broken")

// resolveRegistryFile applies the symlink rule the registry's reader and
// writer share (#248) to path, the registry path as named:
//
//   - nothing there: (path, false, nil) — the registry is missing.
//   - a regular file: (path, true, nil).
//   - a symlink that leads to a file: (the file it leads to, true, nil), so a
//     writer renames onto the real file and the link survives.
//   - a symlink that leads nowhere: ErrRegistryLink, naming the link and what
//     it points at.
func resolveRegistryFile(path string) (target string, exists bool, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, true, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, true, nil
	}
	dest, _ := os.Readlink(path)
	if os.IsNotExist(err) {
		return "", false, fmt.Errorf("%w: %s is a symlink to %s, which does not exist", ErrRegistryLink, path, dest)
	}
	return "", false, fmt.Errorf("%w: %s is a symlink to %s, which cannot be followed: %v", ErrRegistryLink, path, dest, err)
}

// loadRegistry decodes modelman-owned registry.toml into providers and
// models. Registry fields wt doesn't consume (fetch) are ignored by the
// decoder; model_info is decoded into Model.ModelInfo and merged into the
// LiteLLM rows wt writes; model_dir and auth fields are parsed into the
// provider data, and auth.type drives Model.Native — the single source of
// truth for native-ness, consumed by driver dispatch and route resolution.
//
// Fail-closed: a missing or malformed registry is an error — wt has no
// editor for this file; seed it once with `modelman migrate`.
// A symlinked registry is read through; a link that leads nowhere is
// ErrRegistryLink, never "missing" (resolveRegistryFile).
func loadRegistry() ([]Provider, []Model, error) {
	path := RegistryPath()
	missing := fmt.Errorf("%w at %s — seed it with `modelman migrate`", ErrRegistryMissing, path)
	target, exists, err := resolveRegistryFile(path)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, missing
	}
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		// Removed between the check and the read.
		return nil, nil, missing
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

// RegistryRedirected reports whether the environment sends RegistryPath
// somewhere other than the default ~/.config/local-ai/registry.toml —
// MODELMAN_REGISTRY or XDG_CONFIG_HOME naming another place. LiteLLM's
// config.yaml follows neither variable (litellm.DefaultPath), so a run that
// redirects the registry alone would pair a scratch registry with the real
// proxy config; the route writers ask this before they write. The paths are
// compared, not the variables: spelling the default out is not a redirect.
// With no home directory there is no default to compare against, and the
// answer is false.
func RegistryRedirected() bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return filepath.Clean(RegistryPath()) != filepath.Join(home, ".config", "local-ai", "registry.toml")
}
