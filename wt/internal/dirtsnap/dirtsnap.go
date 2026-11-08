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
// exit is an error. It must set GIT_OPTIONAL_LOCKS=0: without it the
// `git status` inside Build may rewrite the repository's index (its stat and
// untracked caches), a change to a source that must only be read. rescue's
// sgit runs through cleanup.Git, which sets it.
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

// prefixCheck remembers, per proper prefix of a status path, whether it
// blocks the path: the answer git's has_symlink_leading_path gives. os.Lstat
// declines to follow only the last component, so without this a tracked
// directory replaced by a symlink would be read through the link.
type prefixCheck map[string]bool

// blocked reports whether a proper prefix of path (slash-separated, relative
// to dir) is a symlink, not a directory, or missing; the path is then absent
// from the working tree as git sees it.
func (c prefixCheck) blocked(dir, path string) (bool, error) {
	for i := 0; i < len(path); i++ {
		if path[i] != '/' {
			continue
		}
		prefix := path[:i]
		b, seen := c[prefix]
		if !seen {
			fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(prefix)))
			switch {
			case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
				b = true
			case err != nil:
				return false, fmt.Errorf("dirtsnap: %s: %w", prefix, err)
			default:
				b = !fi.IsDir()
			}
			c[prefix] = b
		}
		if b {
			return true, nil
		}
	}
	return false, nil
}

// lsChunkBytes bounds the path bytes on one ls-tree command line.
const lsChunkBytes = 64 * 1024

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
//
// A changed file's executable bit is read from disk (core.fileMode=false is
// not honoured) and a regular file is hashed as a file even where
// core.symlinks=false would keep a 120000 entry, so in those configurations
// Build can differ from add -A. Trees are encoded with 20-byte ids, so a
// SHA-256 repository fails with an error (rescue then reports
// record_not_written). A path below a symlink, a file or a missing directory
// is absent, as git's has_symlink_leading_path says.
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
	leading := prefixCheck{}
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
		lead, err := leading.blocked(dir, path)
		if err != nil {
			return "", err
		}
		if lead {
			removals = append(removals, path)
			continue
		}
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(path)))
		// ENOTDIR: a parent that was a directory is now a file, so the
		// tracked child below it is gone (leading.blocked has already said
		// so; this stays for a parent replaced between the two looks).
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
	// The file-count limit comes before any further git call, so a huge
	// dirty tree is refused cheaply.
	if lim.MaxFiles > 0 && len(changes) > lim.MaxFiles {
		return "", fmt.Errorf("%w: %d changed files over %d", ErrTooLarge, len(changes), lim.MaxFiles)
	}
	// The byte total uses the Lstat size; it is not recounted if a file grows
	// before it is read.
	var total int64
	for _, c := range changes {
		total += c.fi.Size()
	}
	if lim.MaxBytes > 0 && total > lim.MaxBytes {
		return "", fmt.Errorf("%w: %d bytes over %d", ErrTooLarge, total, lim.MaxBytes)
	}
	// A gitlink in HEAD at a changed path (even a deleted one) cannot be
	// rebuilt from the files on disk. Paths go in bounded chunks so the
	// command line never grows with the number of changed paths.
	for len(paths) > 0 {
		n, size := 0, 0
		for n < len(paths) && (n == 0 || size+len(paths[n])+1 <= lsChunkBytes) {
			size += len(paths[n]) + 1
			n++
		}
		lsArgs := append([]string{"--literal-pathspecs", "ls-tree", "-z", "HEAD", "--"}, paths[:n]...)
		paths = paths[n:]
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
	adds := map[string]*entry{}
	for _, c := range changes {
		full := filepath.Join(dir, filepath.FromSlash(c.path))
		body := []byte{}
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
		// An empty body is passed as empty (non-nil) stdin. A Runner that
		// treats empty input as no stdin (cleanup.Git.RunInput does, at
		// git.go:81) makes git read /dev/null: still the empty blob.
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
