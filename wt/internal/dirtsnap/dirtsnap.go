// Package dirtsnap names the tree that is HEAD plus a worktree's working
// changes (staged, unstaged, untracked and not ignored) without a temporary
// index: it asks git for the changed paths, hashes what is on disk, and
// rebuilds only the trees along those paths. It creates no temp file and
// deletes nothing.
package dirtsnap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Runner runs git in the repository with stdin (nil for none); a non-zero
// exit is an error.
type Runner func(stdin []byte, args ...string) ([]byte, error)

// Limits bounds a build. Zero means unlimited.
type Limits struct {
	MaxFiles int
	MaxBytes int64
}

var (
	// ErrUnsupported marks a working tree this package will not describe.
	ErrUnsupported = errors.New("dirtsnap: unsupported working tree")
	// ErrTooLarge marks a working tree over the Limits.
	ErrTooLarge = errors.New("dirtsnap: working tree over the limits")
)

type builder struct {
	run   Runner
	store bool
}

// Build returns the 40-hex tree id of HEAD plus the working changes in dir.
// write adds -w to every hash-object (objects are stored); false stores
// nothing. HEAD must exist.
func Build(dir string, run Runner, write bool, lim Limits) (string, error) {
	b := &builder{run: run, store: write}
	out, err := run(nil, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return "", fmt.Errorf("dirtsnap: HEAD tree: %w", err)
	}
	headTree := strings.TrimSpace(string(out))

	out, err = run(nil, "status", "--porcelain=v1", "-z", "--untracked-files=all",
		"--no-renames", "--ignore-submodules=none")
	if err != nil {
		return "", fmt.Errorf("dirtsnap: status: %w", err)
	}
	var removals []string
	adds := map[string]*entry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		if len(rec) < 4 {
			return "", fmt.Errorf("dirtsnap: malformed status record %q", rec)
		}
		path := string(rec[3:])
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			removals = append(removals, path)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("dirtsnap: %s: %w", path, err)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%w: %s is not a regular file", ErrUnsupported, path)
		}
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return "", fmt.Errorf("dirtsnap: %s: %w", path, err)
		}
		args := []string{"hash-object", "--no-filters", "-t", "blob"}
		if write {
			args = append(args, "-w")
		}
		id, err := run(body, append(args, "--stdin")...)
		if err != nil {
			return "", fmt.Errorf("dirtsnap: hash-object %s: %w", path, err)
		}
		mode := "100644"
		if fi.Mode().Perm()&0o100 != 0 {
			mode = "100755"
		}
		adds[path] = &entry{mode: mode, id: strings.TrimSpace(string(id))}
	}
	if len(removals) == 0 && len(adds) == 0 {
		return headTree, nil
	}

	root := &entry{mode: treeMode, id: headTree}
	if err := b.open(root); err != nil {
		return "", err
	}
	if err := b.mergeTree(root.child, removals, adds); err != nil {
		return "", err
	}
	id, _, err := b.write(root.child)
	return id, err
}
