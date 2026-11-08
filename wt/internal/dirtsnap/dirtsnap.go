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
	"syscall"
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
// nothing. HEAD must exist, and dir must be the repository root.
//
// Build trusts git status for the changed paths, and status hides
// assume-unchanged and skip-worktree files, so Build can differ from git's
// own add -A there. Callers must refuse those states first (the rescue and
// the classifier do). A nested repository, submodule or special file gives
// ErrUnsupported; changed files or bytes over lim give ErrTooLarge.
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
	type change struct {
		path string
		fi   os.FileInfo
	}
	var removals []string
	var changes []change
	var paths []string
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		if len(rec) < 4 {
			return "", fmt.Errorf("dirtsnap: malformed status record %q", rec)
		}
		path := string(rec[3:])
		if strings.HasSuffix(path, "/") {
			// status names an untracked nested repository as "dir/".
			return "", fmt.Errorf("%w: %s is a nested repository", ErrUnsupported, path)
		}
		paths = append(paths, path)
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path)))
		// ENOTDIR: a parent that was a directory is now a file, so the
		// tracked child below it is gone.
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			removals = append(removals, path)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("dirtsnap: %s: %w", path, err)
		}
		if fi.IsDir() {
			// A tracked file now a directory (its files are listed on their
			// own); a submodule is caught by the gitlink check below.
			removals = append(removals, path)
			continue
		}
		if !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("%w: %s is a special file", ErrUnsupported, path)
		}
		changes = append(changes, change{path, fi})
	}
	if len(paths) > 0 {
		// A gitlink in HEAD at a changed path (even a deleted one) cannot be
		// rebuilt from the files on disk.
		lsArgs := append([]string{"--literal-pathspecs", "ls-tree", "-z", "HEAD", "--"}, paths...)
		lt, err := run(nil, lsArgs...)
		if err != nil {
			return "", fmt.Errorf("dirtsnap: ls-tree HEAD: %w", err)
		}
		for _, rec := range bytes.Split(lt, []byte{0}) {
			if bytes.HasPrefix(rec, []byte("160000 ")) {
				_, name, _ := bytes.Cut(rec, []byte{'\t'})
				return "", fmt.Errorf("%w: %s is a submodule", ErrUnsupported, name)
			}
		}
	}
	var total int64
	for _, c := range changes {
		total += c.fi.Size()
	}
	if (lim.MaxFiles > 0 && len(changes) > lim.MaxFiles) || (lim.MaxBytes > 0 && total > lim.MaxBytes) {
		return "", fmt.Errorf("%w: %d changed files, %d bytes", ErrTooLarge, len(changes), total)
	}
	adds := map[string]*entry{}
	for _, c := range changes {
		full := filepath.Join(dir, filepath.FromSlash(c.path))
		var body []byte
		mode := "100644"
		if c.fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return "", fmt.Errorf("dirtsnap: %s: %w", c.path, err)
			}
			body, mode = []byte(target), "120000"
		} else {
			body, err = os.ReadFile(full)
			if err != nil {
				return "", fmt.Errorf("dirtsnap: %s: %w", c.path, err)
			}
			if c.fi.Mode().Perm()&0o100 != 0 {
				mode = "100755"
			}
		}
		args := []string{"hash-object", "--no-filters", "-t", "blob"}
		if write {
			args = append(args, "-w")
		}
		// An empty body reaches the runner as empty stdin, which git reads
		// as the empty blob either way.
		id, err := run(body, append(args, "--stdin")...)
		if err != nil {
			return "", fmt.Errorf("dirtsnap: hash-object %s: %w", c.path, err)
		}
		adds[c.path] = &entry{mode: mode, id: strings.TrimSpace(string(id))}
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
