package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// ErrRegistryBusy is returned by UpdateRegistry when registry.toml changed
// under it on every attempt: another program (modelman, an editor) kept
// writing the file. Nothing was written; running the command again is safe.
var ErrRegistryBusy = errors.New("registry.toml kept changing while wt was writing it")

// registryWriteAttempts is how many times UpdateRegistry runs apply before
// giving up with ErrRegistryBusy.
const registryWriteAttempts = 3

// registryWriteGuard, when set, is asked before a registry write goes ahead,
// with the registry path as named and (once known) the file it resolves to.
// Production never sets it. IsolateConfigHomeForTest sets it to refuse the
// developer's real registry, so a test that reaches UpdateRegistry without
// redirecting the registry fails instead of rewriting it. It is a seam, not
// an `import "testing"`: the shipped binary carries one nil check.
var registryWriteGuard func(named, target string) error

// registryBeforeRename runs after a write is encoded and before the file is
// re-checked and replaced. Tests swap it to play the part of another program
// writing the file in that window.
var registryBeforeRename = func() {}

// UpdateRegistry is the only way wt writes registry.toml. It reads the file,
// hands it to apply as a RegistryDoc, and writes the result back:
//
//  1. It takes a flock on <registry path>.lock — the path as named, not a
//     symlink's target — so two wt writers never interleave.
//  2. It resolves the path. A symlink that leads nowhere is ErrRegistryLink,
//     never "missing"; a symlink that resolves is written through, so the
//     link survives (#248).
//  3. It reads and decodes the file. A missing file is an empty document. An
//     unknown top-level key refuses the write and names the keys (#247).
//  4. It runs apply.
//  5. It validates the rows apply touched, and only those (ErrRegistryInvalid).
//  6. It skips the write when the file exists and apply changed nothing, so
//     a no-op leaves the file byte-identical, comments and all. Otherwise it
//     re-reads the file: if another program changed it since
//     step 3 it starts over from step 2, up to three runs of apply, then
//     returns ErrRegistryBusy. If not, it replaces the file atomically, with
//     the mode it had (0600 for a new file).
//
// apply must be pure. It may run more than once, so it must not print, run a
// command, call a server or change anything outside the document it is given.
// A result it hands back through a captured variable must be assigned afresh
// on every run, never appended to.
//
// changed reports whether the file was written. A registry that did not
// exist is created by any apply that succeeds, even one that adds nothing.
// The first write that does change something lays the whole file out in
// tomli-w's form, which drops comments — as every modelman save always has.
//
// UpdateRegistry never touches LiteLLM's config.yaml and does not ask whether
// the registry is redirected: a write to a redirected registry succeeds, and
// it is the route sync a caller runs afterwards that litellm refuses
// (ErrRegistryRedirected) unless WT_LITELLM_CONFIG names the config.yaml.
func UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error) {
	path := RegistryPath()
	if registryWriteGuard != nil {
		// Before the lock: taking it creates the lock file and its directory.
		if err := registryWriteGuard(path, ""); err != nil {
			return false, err
		}
	}
	err = withFileLock(path+".lock", func() error {
		for range registryWriteAttempts {
			var retry bool
			var err error
			changed, retry, err = updateRegistryOnce(path, apply)
			if err != nil || !retry {
				return err
			}
		}
		return fmt.Errorf("%w (%s)", ErrRegistryBusy, path)
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// readRegistryFile resolves and reads the registry: the file to write, its
// bytes, and whether it exists. A missing file reads as no bytes.
func readRegistryFile(path string) (target string, data []byte, exists bool, err error) {
	target, exists, err = resolveRegistryFile(path)
	if err != nil || !exists {
		return target, nil, false, err
	}
	data, err = os.ReadFile(target)
	if os.IsNotExist(err) {
		return target, nil, false, nil
	}
	return target, data, err == nil, err
}

// updateRegistryOnce is one attempt: steps 2 to 6 of UpdateRegistry. retry is
// true when the file changed between the read and the write.
func updateRegistryOnce(path string, apply func(*RegistryDoc) error) (changed, retry bool, err error) {
	target, before, exists, err := readRegistryFile(path)
	if err != nil {
		return false, false, err
	}
	if registryWriteGuard != nil {
		if err := registryWriteGuard(path, target); err != nil {
			return false, false, err
		}
	}
	root, err := tomlw.Decode(before)
	if err != nil {
		return false, false, fmt.Errorf("parse %s: %w", path, err)
	}
	doc, err := newRegistryDoc(root)
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", path, err)
	}
	// What the document encodes to before apply runs. Comparing against this,
	// not against the file's bytes, is what lets a hand-formatted registry
	// (comments, its own layout) survive a write that changes nothing.
	unchanged, err := tomlw.Encode(root)
	if err != nil {
		return false, false, fmt.Errorf("encode %s: %w", path, err)
	}
	if err := apply(doc); err != nil {
		return false, false, err
	}
	if err := doc.validateTouched(); err != nil {
		return false, false, err
	}
	after, err := tomlw.Encode(root)
	if err != nil {
		return false, false, fmt.Errorf("encode %s: %w", path, err)
	}
	if exists && bytes.Equal(unchanged, after) {
		return false, false, nil
	}

	registryBeforeRename()
	nowTarget, now, nowExists, err := readRegistryFile(path)
	if err != nil {
		return false, false, err
	}
	if nowTarget != target || nowExists != exists || !bytes.Equal(now, before) {
		return false, true, nil
	}
	mode := os.FileMode(0o600)
	if exists {
		info, err := os.Stat(target)
		if err != nil {
			return false, false, err
		}
		mode = info.Mode().Perm()
	}
	if err := WriteFileAtomic(target, after, mode); err != nil {
		return false, false, err
	}
	return true, false, nil
}
