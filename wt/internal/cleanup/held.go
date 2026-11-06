package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// HeldRefs is every full refname worktree w holds the way git's own branch
// protection counts it (`git branch -D` refuses such a branch "used by
// worktree"): its HEAD branch, the branch an in-progress rebase will return
// to (rebase-merge/head-name, or rebase-apply/head-name when that rebase-apply
// is a rebase and not `git am`), the other branches that rebase will move
// (the rebase-merge/update-refs file), and the branch a bisect started from
// (BISECT_START). A rebase or bisect leaves HEAD detached, so the worktree
// list alone does not show these. Callers compare case-folded.
//
// The state files are read from w's own git dir: for the primary, the common
// dir; for a linked worktree, its administrative directory
// (<common>/worktrees/<id>, found by its gitdir registration), never by
// running git in w.Path, which for a worktree that lost its .git file and
// sits inside another checkout would answer for that outer repository. An
// administrative directory that cannot be found, an operation marker whose
// holder file cannot be read or parsed, and any failed read are errors:
// callers fail closed. It reads only.
func HeldRefs(g Git, w WorktreeFact) ([]string, error) {
	var refs []string
	if w.Branch != "" {
		refs = append(refs, w.Branch)
	}
	paths, err := heldStatePaths(g, w)
	if err != nil {
		return nil, err
	}
	rebaseMerge, rebaseApply, bisectStart := paths[0], paths[1], paths[2]

	if ok, err := present(rebaseMerge); err != nil {
		return nil, err
	} else if ok {
		ref, err := headName(filepath.Join(rebaseMerge, "head-name"))
		if err != nil {
			return nil, err
		}
		if ref != "" {
			refs = append(refs, ref)
		}
		more, err := rebaseRefs(filepath.Join(filepath.Dir(rebaseMerge), rebaseMovesFile))
		if err != nil {
			return nil, err
		}
		refs = append(refs, more...)
	}

	if ok, err := present(rebaseApply); err != nil {
		return nil, err
	} else if ok {
		// rebase-apply is also `git am`'s, marked by an "applying" file;
		// anything else there is a rebase, as git itself decides.
		applying, err := present(filepath.Join(rebaseApply, "applying"))
		if err != nil {
			return nil, err
		}
		if !applying {
			ref, err := headName(filepath.Join(rebaseApply, "head-name"))
			if err != nil {
				return nil, err
			}
			if ref != "" {
				refs = append(refs, ref)
			}
		}
	}

	if ok, err := present(bisectStart); err != nil {
		return nil, err
	} else if ok {
		ref, err := bisectFrom(bisectStart)
		if err != nil {
			return nil, err
		}
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// rebaseMovesFile is the sequencer's record of the other branches a rebase
// will move, relative to the git dir. It is read only; its name is the one
// spelling TestCleanupCmdContainsNoDeletion allows to contain a banned token.
const rebaseMovesFile = "rebase-merge/update-refs"

// heldStateNames are the state paths HeldRefs reads, in its order.
var heldStateNames = []string{"rebase-merge", "rebase-apply", "BISECT_START"}

// heldStatePaths resolves heldStateNames in w's git dir: the common dir for
// the primary, the administrative directory for a linked worktree.
func heldStatePaths(g Git, w WorktreeFact) ([]string, error) {
	common, err := commonDir(g)
	if err != nil {
		return nil, err
	}
	dir := common
	if !w.Primary {
		if dir, err = adminDir(common, w.Path); err != nil {
			return nil, err
		}
	}
	var out []string
	for _, n := range heldStateNames {
		out = append(out, filepath.Join(dir, n))
	}
	return out, nil
}

// commonDir is the repository's absolute git common dir, read where g runs.
func commonDir(g Git) (string, error) {
	args := []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
	o, err := g.Run(args...)
	if err != nil {
		return "", err
	}
	if o.Code != 0 {
		return "", gitFailure(o, args...)
	}
	common := strings.TrimSpace(o.Stdout)
	if common == "" || !filepath.IsAbs(common) {
		return "", fmt.Errorf("cleanup: rev-parse --git-common-dir gave %q", flat(common))
	}
	return common, nil
}

// adminDir finds the administrative directory (<common>/worktrees/<id>) of
// the linked worktree registered at path: the one whose gitdir file names
// <path>/.git (absolute, or relative to the administrative directory). None,
// or more than one, is an error. The worktrees directory is listed, never
// globbed: the repository's path may hold glob metacharacters (`x[1]`).
// An entry with no gitdir file registers nothing and is skipped.
func adminDir(common, path string) (string, error) {
	root := filepath.Join(common, "worktrees")
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("cleanup: %s: %w", flat(root), err)
	}
	want, wantClean := resolve(path), filepath.Clean(path)
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := filepath.Join(root, e.Name(), "gitdir")
		b, err := os.ReadFile(m)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cleanup: %s: %w", flat(m), err)
		}
		gd := strings.TrimSpace(string(b))
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(filepath.Dir(m), gd)
		}
		wt := filepath.Dir(gd)
		if resolve(wt) == want || filepath.Clean(wt) == wantClean {
			found = append(found, filepath.Dir(m))
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("cleanup: no administrative directory found for the worktree %s", flat(path))
	}
	return "", fmt.Errorf("cleanup: %d administrative directories claim the worktree %s", len(found), flat(path))
}

// present reports whether p exists; any error but not-exist is an error.
func present(p string) (bool, error) {
	_, err := os.Lstat(p)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("cleanup: %s: %w", flat(p), err)
}

// headName reads a rebase's head-name: a full refname, or "" for a rebase of
// a detached HEAD. A missing, unreadable or unrecognised file is an error.
func headName(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("cleanup: a rebase is in progress but %s cannot be read: %w", flat(p), err)
	}
	s := strings.TrimSpace(string(b))
	switch {
	case s == "detached HEAD":
		return "", nil
	case strings.HasPrefix(s, "refs/") && !strings.ContainsAny(s, " \t\n"):
		return s, nil
	}
	return "", fmt.Errorf("cleanup: a rebase is in progress but %s holds %q, not a ref", flat(p), flat(s))
}

// rebaseRefs reads the rebaseMovesFile: records of three lines
// (the ref, its old and its new commit). No file means none; anything else
// that is not such records is an error.
func rebaseRefs(p string) ([]string, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cleanup: %s cannot be read: %w", flat(p), err)
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil, nil
	}
	lines := strings.Split(s, "\n")
	if len(lines)%3 != 0 {
		return nil, fmt.Errorf("cleanup: %s: %d lines, not records of three", flat(p), len(lines))
	}
	var refs []string
	for i := 0; i < len(lines); i += 3 {
		if !strings.HasPrefix(lines[i], "refs/") {
			return nil, fmt.Errorf("cleanup: %s: %q is not a ref", flat(p), flat(lines[i]))
		}
		refs = append(refs, lines[i])
	}
	return refs, nil
}

// bisectFrom reads BISECT_START: the short name of the branch the bisect
// started from (a full refname is returned), or a commit id when it started
// detached (""). Empty or unreadable is an error.
func bisectFrom(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("cleanup: a bisect is in progress but %s cannot be read: %w", flat(p), err)
	}
	s := strings.TrimSpace(string(b))
	switch {
	case s == "" || strings.ContainsAny(s, " \t\n"):
		return "", fmt.Errorf("cleanup: a bisect is in progress but %s holds %q", flat(p), flat(s))
	case isHexID(s):
		return "", nil
	}
	return "refs/heads/" + s, nil
}

// isHexID reports a full sha1 or sha256 object id.
func isHexID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
