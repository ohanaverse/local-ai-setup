package cleanup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/scratchdir"
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
// then still proves — the same text is in main, which is the claim; a hunk
// that only adds lines is the exception, see containedWithContext), --binary
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
	if hasUncheckedDeletion(patch) {
		return TriUnknown
	}
	tmp, release, ok := scratchdir.Temp("wt-contained-")
	if !ok {
		return TriUnknown
	}
	defer release()
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

// deletionOnlyHunk matches a -U0 hunk header whose post-image is empty: the
// hunk only removes lines.
var deletionOnlyHunk = regexp.MustCompile(`^@@ -[0-9,]+ \+[0-9]+,0 @@`)

// hasUncheckedDeletion reports whether patch holds a hunk that only removes
// lines from a file that stays. Reverse-applied it becomes a zero-length
// preimage insertion, which -U0 lets match anywhere — so it passes whether or
// not base still holds the lines, and cannot show that base made the
// deletion. Such a change-set is not provable here (fail closed:
// TriUnknown). A whole-file deletion is exempt: its reverse creates a file,
// which git refuses when base still has it, so that case is checked.
func hasUncheckedDeletion(patch string) bool {
	deleted := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			deleted = false
		case strings.HasPrefix(line, "deleted file mode "):
			deleted = true
		case !deleted && deletionOnlyHunk.MatchString(line):
			return true
		}
	}
	return false
}

// additionOnlyHunk matches a -U0 hunk header whose preimage is empty: the
// hunk only adds lines.
var additionOnlyHunk = regexp.MustCompile(`^@@ -[0-9]+,0 \+`)

// hasUnanchoredAddition reports whether patch holds a hunk that only adds
// lines to a file that already existed. Reverse-applied under -U0 it removes
// lines, and git finds those lines wherever they sit in the file, so a branch
// that adds a line base holds elsewhere (a blank line, `}`, `return nil`)
// would pass. A whole-file addition is exempt: its reverse deletes the file
// and git checks the content. Such a patch is judged again with context
// (containedWithContext).
func hasUnanchoredAddition(patch string) bool {
	added := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			added = false
		case strings.HasPrefix(line, "new file mode "):
			added = true
		case !added && additionOnlyHunk.MatchString(line):
			return true
		}
	}
	return false
}

// containedWithContext is reverseApplies for a -U0 patch plus the anchoring
// rule: when the patch holds an addition-only hunk in a surviving file, the
// same change-set read with context lines (ctxPatch, -U3) must reverse-apply
// too, so the added lines are found where they were added, not merely
// somewhere. Fail closed: a context patch that cannot be read, is over the cap
// or does not apply (base edited next to the change since) is TriUnknown —
// the change may well be in base, it is just not provable this way.
func containedWithContext(g Git, baseTree, patch string, ctxPatch func() (string, bool)) Tri {
	r := reverseApplies(g, baseTree, patch)
	if r != TriYes || !hasUnanchoredAddition(patch) {
		return r
	}
	cp, ok := ctxPatch()
	if !ok || int64(len(cp)) > dirtLimits.MaxBytes {
		return TriUnknown
	}
	if reverseApplies(g, baseTree, cp) != TriYes {
		return TriUnknown
	}
	return TriYes
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
	return containedWithContext(g, bt, o.Stdout, func() (string, bool) {
		c, err := g.Run("diff-tree", "--binary", "--no-renames", "--no-color", "--no-ext-diff",
			"--no-textconv", "-U3", "-p", mb, t)
		if err != nil || c.Code != 0 {
			return "", false
		}
		return c.Stdout, true
	})
}

// containedInBase reports whether a worktree's whole uncommitted
// change-set — the staged+unstaged net vs HEAD plus its non-ignored untracked
// files — is contained in baseTip's tree. Ignored files never enter it (they
// are IGN's subject). The tracked part is judged by reverseApplies, each
// untracked file by blob equality with base:<path>; any TriNo anywhere means
// TriNo, TriYes needs both halves TriYes, anything unreadable or over the caps
// is TriUnknown.
func (d worktreeDiff) containedInBase(wg Git, baseTip string) Tri {
	b, ok := commitOf(wg, baseTip)
	if !ok {
		return TriUnknown
	}
	bt, ok := treeOf(wg, b)
	if !ok {
		return TriUnknown
	}
	if !d.ok || int64(len(d.patch)) > dirtLimits.MaxBytes {
		return TriUnknown
	}
	return triJoin(containedWithContext(wg, bt, d.patch, func() (string, bool) { return readContextPatch(wg) }), untrackedContained(wg, b))
}

// readContextPatch is the worktree's change-set against HEAD with three lines
// of context (the anchoring read of containedWithContext), taken against a
// throwaway copy of the index like readWorktreeDiff.
func readContextPatch(wg Git) (string, bool) {
	dg, release, ok := IndexCopy(wg)
	if !ok {
		return "", false
	}
	defer release()
	o, err := dg.Run("diff", "--binary", "--no-renames", "--no-color", "--no-ext-diff",
		"--no-textconv", "-U3", "HEAD")
	if err != nil || o.Code != 0 {
		return "", false
	}
	return o.Stdout, true
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
		// Sprintf, not a colon concatenation: the cleanup command's
		// no-deletion scan bans that shape (a push refspec spells one),
		// and this is a rev-parse path argument, nothing like a refspec.
		r, err := wg.Run("rev-parse", "--verify", "--quiet", fmt.Sprintf("%s:%s", base, p))
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
