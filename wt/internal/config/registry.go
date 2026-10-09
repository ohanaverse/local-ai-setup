package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrRegistryMissing is returned by loadRegistry when the shared
// registry.toml does not exist yet. cmd/wt tolerates it for command agents
// (which have no model layer) while still failing closed for real agents.
var ErrRegistryMissing = errors.New("model registry not found")

// registryEnvNames are the variables that name registry.toml outright, in
// precedence order. WT_REGISTRY is the name wt and llmbench share;
// MODELMAN_REGISTRY is the older name, kept as an alias.
var registryEnvNames = []string{"WT_REGISTRY", "MODELMAN_REGISTRY"}

// RegistryPath returns the registry.toml location: WT_REGISTRY, then
// MODELMAN_REGISTRY, then $XDG_CONFIG_HOME/local-ai/registry.toml, then
// ~/.config/local-ai/registry.toml. llmbench's registry_path uses the same
// precedence, so the two tools agree on which file is the registry; each has
// a test of it.
func RegistryPath() string {
	// A named registry is the only branch with a side effect: it writes to
	// stderr on expandHome failure. Acceptable because the path-resolution
	// failure must be visible to the user, and there is no logger to inject
	// at this layer. See expandHome's docstring for the literal-fallback contract.
	for _, name := range registryEnvNames {
		override := os.Getenv(name)
		if override == "" {
			continue
		}
		expanded, err := expandHome(override)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wt: cannot expand %s (%v); using literal path\n", name, err)
			return override
		}
		return expanded
	}
	return filepath.Join(baseConfigHome(), "local-ai", "registry.toml")
}

// ModelmanPath returns the modelman-owned modelman.toml location. It uses the
// same XDG base-directory resolution as RegistryPath(): XDG_CONFIG_HOME (with
// tilde expansion), falling back to ~/.config. It honors none of
// WT_REGISTRY, MODELMAN_REGISTRY or modelman's MODELMAN_STATE override — a
// deliberate asymmetry: wt is a read-only consumer and never needs to redirect
// the state file the way tests (or wt itself) redirect the registry. The
// one table wt reads (the legacy [litellm] table) is pinned by
// docs/contracts/modelman.sample.toml. wt reads this file read-only.
func ModelmanPath() string {
	return filepath.Join(baseConfigHome(), "local-ai", "modelman.toml")
}

// expandHome expands a leading "~" or "~/" in path to the user's home
// directory, matching Python's Path.expanduser() as llmbench's registry_path
// does, so WT_REGISTRY and MODELMAN_REGISTRY behave the same in both tools.
// Paths that don't start with "~" are returned unchanged.
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

// ErrRegistryFile marks an error about the registry file itself: wt cannot
// examine its path (lstat fails for a reason other than absence), cannot
// read it, or it is not TOML. The reader (loadRegistry) and the writer
// (UpdateRegistry) mark the same failures. The repair is a hand edit
// of that file — `wt config` edits config.toml and cannot help — and
// RegistryFixHint says so. The marked error's text is unchanged; it names
// the file.
var ErrRegistryFile = errors.New("registry.toml cannot be read")

type registryFileErr struct{ err error }

func (e registryFileErr) Error() string        { return e.err.Error() }
func (e registryFileErr) Unwrap() error        { return e.err }
func (e registryFileErr) Is(target error) bool { return target == ErrRegistryFile }

// registryFileError marks err as ErrRegistryFile, keeping its text and
// whatever it wraps.
func registryFileError(err error) error { return registryFileErr{err: err} }

// brokenLinkAbove returns ErrRegistryLink when path does not exist because a
// directory above it is a symlink that cannot be followed, and nil when the
// path is simply absent. It asks the nearest ancestor that is there: every
// ancestor below that one is missing for the same reason path is, and one
// that is a real directory, or a link that resolves, means nothing is broken.
func brokenLinkAbove(path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
			_, statErr := os.Stat(dir)
			if statErr == nil {
				return nil
			}
			dest, _ := os.Readlink(dir)
			if os.IsNotExist(statErr) {
				return fmt.Errorf("%w: %s, a directory above %s, is a symlink to %s, which does not exist", ErrRegistryLink, dir, path, dest)
			}
			return fmt.Errorf("%w: %s, a directory above %s, is a symlink to %s, which cannot be followed: %v", ErrRegistryLink, dir, path, dest, statErr)
		}
		if !os.IsNotExist(err) || dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// resolveRegistryFile applies the symlink rule the registry's reader and
// writer share (#248) to path, the registry path as named:
//
//   - nothing there: (path, false, nil) — the registry is missing.
//   - nothing there because a directory above it is a symlink that leads
//     nowhere (the whole registry directory linked into a checkout or a volume
//     that is not there now): ErrRegistryLink, as for the file itself. Lstat
//     reports that exactly like a missing registry, so it is looked for.
//   - a regular file: (path, true, nil).
//   - a symlink that leads to a file: (the file it leads to, true, nil), so a
//     writer renames onto the real file and the link survives.
//   - a symlink that leads nowhere: ErrRegistryLink, naming the link and what
//     it points at.
//   - a path that cannot be examined at all (a regular file where its
//     directory should be, no permission to search the directory):
//     ErrRegistryFile, with the system's error text.
func resolveRegistryFile(path string) (target string, exists bool, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if linkErr := brokenLinkAbove(path); linkErr != nil {
			return "", false, linkErr
		}
		return path, false, nil
	}
	if err != nil {
		// Something is in the way of the path (a file where the registry's
		// directory should be, a directory wt may not search). Not a
		// config.toml problem, so it is marked; the error names the path.
		return "", false, registryFileError(err)
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

// loadRegistry decodes the shared registry.toml into providers and
// models. A model's fetch and draft tables are decoded into Model.Fetch and
// Model.Draft (a repo or a local_path each; a malformed one reads as absent
// and never fails the load); model_info is decoded into
// Model.ModelInfo and merged into the LiteLLM rows wt writes; model_dir and auth fields are parsed into the
// provider data, and auth.type drives Model.Native — the single source of
// truth for native-ness, consumed by driver dispatch and route resolution.
//
// Fail-closed: a missing or malformed registry is an error — wt has no
// editor for this file; `wt model init` creates it.
// A symlinked registry is read through; a link that leads nowhere is
// ErrRegistryLink, never "missing" (resolveRegistryFile).
func loadRegistry() ([]Provider, []Model, error) {
	path := RegistryPath()
	missing := fmt.Errorf("%w at %s — seed it with `wt model init`", ErrRegistryMissing, path)
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
		return nil, nil, registryFileError(err)
	}
	var reg struct {
		Providers []Provider `toml:"providers"`
		Models    []Model    `toml:"models"`
	}
	if _, err := toml.Decode(string(data), &reg); err != nil {
		return nil, nil, registryFileError(fmt.Errorf("parse %s: %w", path, err))
	}
	return reg.Providers, reg.Models, nil
}

// RegistryRedirected reports whether the environment sends RegistryPath
// somewhere other than the default ~/.config/local-ai/registry.toml —
// WT_REGISTRY, MODELMAN_REGISTRY or XDG_CONFIG_HOME naming another place.
// LiteLLM's config.yaml follows none of them (litellm.DefaultPath), so a run
// that redirects the registry alone would pair a scratch registry with the
// real proxy config; the route writers ask this before they write. The paths
// are compared, not the variables: spelling the default out is not a
// redirect. With no home directory there is no default to compare against,
// and the answer is false.
func RegistryRedirected() bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return filepath.Clean(RegistryPath()) != filepath.Join(home, ".config", "local-ai", "registry.toml")
}
