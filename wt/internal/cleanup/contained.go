package cleanup

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Containment answers one question: is this change-set already in base's
// content, regardless of commit hashes? The patch (or blob set) must
// reverse-apply cleanly onto base's tree — if undoing the change against
// base's content works, base holds the change. This is what makes a squash
// merge read as merged after main edits the same file, a case hash-equality
// proofs miss; and it fails when main later reverted the change (the text is
// gone), exactly why cherry/patch-id stay weak signals.
//
// Every failure is fail-closed: a git error, an over-cap change-set or an
// unreadable file reads TriUnknown — never TriYes. All git work runs against
// throwaway indexes (a fresh GIT_INDEX_FILE per call, a byte-copy of the real
// index for the worktree diff): internal/cleanup never rewrites the real one,
// even though `git diff` would refresh its stat cache under
// GIT_OPTIONAL_LOCKS=0. Diff flags pin the shape: -U0 keeps every hunk's own
// preimage the only thing that must match (an adjacent later edit in main
// then still proves — the same text is in main, which is the claim), --binary
// carries binary patches, --no-renames judges a rename as delete+add,
// --no-color/--no-ext-diff/--no-textconv keep the patch reversible.

// reverseApplies reports whether patch reverse-applies cleanly onto the tree
// baseTree through a throwaway index: read-tree loads base's tree, then
// `git apply -R --check --cached` validates without writing anything. Exit 0
// is TriYes; git's plain "patch does not apply" refusal is TriNo; any other
// failure (a corrupt patch, a spawn error) is TriUnknown. An empty patch is
// TriYes: nothing to apply means nothing missing from base.
func reverseApplies(g Git, baseTree, patch string) Tri {
	if patch == "" {
		return TriYes
	}
	tmp, err := os.MkdirTemp("", "wt-contained-")
	if err != nil {
		return TriUnknown
	}
	defer os.RemoveAll(tmp)
	idx := filepath.Join(tmp, "index")
	gi := g
	gi.Env = append(slices.Clone(g.Env), "GIT_INDEX_FILE="+idx)
	if o, err := gi.Run("read-tree", baseTree); err != nil || o.Code != 0 {
		return TriUnknown
	}
	o, err := gi.RunInput(patch, "apply", "-R", "--check", "--cached", "--unidiff-zero", "--binary")
	if err != nil {
		return TriUnknown
	}
	switch {
	case o.Code == 0:
		return TriYes
	case o.Code == 1 && strings.Contains(o.Stderr, "patch does not apply"):
		return TriNo
	default:
		return TriUnknown
	}
}

// tipContainedInBase reports whether tip's change-set against the merge-base
// of baseTip and tip is contained in baseTip's tree. baseTip and tip follow
// commitOf's contract (qualified refs or full commit ids); anything unresolvable
// — a missing ref, no merge-base, an unreadable tree — is TriUnknown. The
// patch is bounded by dirtLimits.MaxBytes before it is judged.
func tipContainedInBase(g Git, baseTip, tip string) Tri {
	b, ok := commitOf(g, baseTip)
	if !ok {
		return TriUnknown
	}
	t, ok := commitOf(g, tip)
	if !ok {
		return TriUnknown
	}
	mb, ok := mergeBase(g, b, t)
	if !ok {
		return TriUnknown
	}
	o, err := g.Run("diff-tree", "--binary", "--no-renames", "--no-color", "--no-ext-diff",
		"--no-textconv", "-U0", "-p", mb, t)
	if err != nil || o.Code != 0 {
		return TriUnknown
	}
	if int64(len(o.Stdout)) > dirtLimits.MaxBytes {
		return TriUnknown
	}
	bt, ok := treeOf(g, b)
	if !ok {
		return TriUnknown
	}
	return reverseApplies(g, bt, o.Stdout)
}

// worktreeContainedInBase reports whether a worktree's whole uncommitted
// change-set — the staged+unstaged net vs HEAD plus its non-ignored untracked
// files — is contained in baseTip's tree. Ignored files never enter it (they
// are IGN's subject). The tracked part is judged by reverseApplies, each
// untracked file by blob equality with base:<path>; any TriNo anywhere means
// TriNo, TriYes needs both halves TriYes, anything unreadable or over the caps
// is TriUnknown.
func worktreeContainedInBase(wg Git, baseTip string) Tri {
	b, ok := commitOf(wg, baseTip)
	if !ok {
		return TriUnknown
	}
	bt, ok := treeOf(wg, b)
	if !ok {
		return TriUnknown
	}
	patch, ok := worktreePatch(wg)
	if !ok {
		return TriUnknown
	}
	if int64(len(patch)) > dirtLimits.MaxBytes {
		return TriUnknown
	}
	return triJoin(reverseApplies(wg, bt, patch), untrackedContained(wg, b))
}

// triJoin combines two containment answers: any TriNo wins (one part not in
// base means the change-set is not contained), TriYes only when both halves
// are TriYes, otherwise TriUnknown.
func triJoin(a, b Tri) Tri {
	switch {
	case a == TriNo || b == TriNo:
		return TriNo
	case a == TriYes && b == TriYes:
		return TriYes
	}
	return TriUnknown
}

// worktreePatch returns the worktree's staged+unstaged net diff against HEAD
// as a reversible patch. ok is false on any failure, including a missing or
// unreadable real index. The diff runs against a byte-copy of the real index
// under GIT_INDEX_FILE: `git diff` rewrites the index's stat cache even under
// GIT_OPTIONAL_LOCKS=0, and this package must leave the real index untouched.
func worktreePatch(wg Git) (string, bool) {
	o, err := wg.Run("rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil || o.Code != 0 {
		return "", false
	}
	real, err := os.ReadFile(filepath.Join(strings.TrimSpace(o.Stdout), "index"))
	if err != nil {
		return "", false
	}
	tmp, err := os.MkdirTemp("", "wt-contained-")
	if err != nil {
		return "", false
	}
	defer os.RemoveAll(tmp)
	idx := filepath.Join(tmp, "index")
	if err := os.WriteFile(idx, real, 0o600); err != nil {
		return "", false
	}
	dg := wg
	dg.Env = append(slices.Clone(wg.Env), "GIT_INDEX_FILE="+idx)
	d, err := dg.Run("diff", "--binary", "--no-renames", "--no-color", "--no-ext-diff",
		"--no-textconv", "-U0", "HEAD")
	if err != nil || d.Code != 0 {
		return "", false
	}
	return d.Stdout, true
}

// untrackedContained judges the worktree's non-ignored untracked files
// against base's tree: each path's blob (hash-object, never stored) must equal
// base:<path>. A path base does not hold is TriNo (absence: not contained);
// over the file or byte caps, a symlink or special file, or any read failure
// is TriUnknown; every blob equal is TriYes. ls-files --exclude-standard never
// lists ignored files, so they never enter the count or the comparison.
func untrackedContained(wg Git, base string) Tri {
	o, err := wg.Run("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || o.Code != 0 {
		return TriUnknown
	}
	var paths []string
	for _, p := range strings.Split(o.Stdout, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return TriYes
	}
	if len(paths) > dirtLimits.MaxFiles {
		return TriUnknown
	}
	var total int64
	for _, p := range paths {
		fi, err := os.Lstat(filepath.Join(wg.Dir, p))
		if err != nil {
			return TriUnknown
		}
		if !fi.Mode().IsRegular() {
			return TriUnknown // a symlink or special file cannot be blob-compared here
		}
		total += fi.Size()
		if total > dirtLimits.MaxBytes {
			return TriUnknown
		}
		h, err := wg.Run("hash-object", "--", p)
		if err != nil || h.Code != 0 {
			return TriUnknown
		}
		got := strings.TrimSpace(h.Stdout)
		r, err := wg.Run("rev-parse", "--verify", "--quiet", base+":"+p)
		if err != nil {
			return TriUnknown
		}
		switch {
		case r.Code == 0:
			if strings.TrimSpace(r.Stdout) != got {
				return TriNo
			}
		case r.Code == 1:
			return TriNo // base does not hold the path
		default:
			return TriUnknown
		}
	}
	return TriYes
}
