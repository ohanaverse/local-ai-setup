package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// WorktreeFact is one entry of `git worktree list --porcelain -z`.
type WorktreeFact struct {
	Path     string
	Branch   string // full ref name (refs/heads/x); "" when detached
	Head     string
	Detached bool
	Bare     bool
	Primary  bool // the first entry: the main working tree (or the bare repo)
	Locked   bool
	Prunable bool
	Current  bool // cwd is this worktree or inside it
}

// Blocker codes: stable strings, kept in rows and JSON.
const (
	BlockPrimary           = "primary"
	BlockCurrent           = "current"
	BlockLocked            = "locked"
	BlockNested            = "nested"
	BlockOperation         = "operation_in_progress"
	BlockProtectedArtifact = "protected_artifact"
)

// BlockHeld is the held code under its old blocker name. Held is a label,
// never a blocker: the classifier raises it as ReasonHeld (the same code) and
// lists it in Row.Reasons. This alias only keeps the held test seam
// (heldseam.go) and rescue's preflight switch compiling until a later task
// retires them.
const BlockHeld = ReasonHeld

// Reason is one code and what it names (a path, an operation, a session's
// claim description) — a blocker or a row reason alike.
type Reason struct {
	Code   string
	Detail string
}

// Gates are a worktree's facts that do not depend on its branch. Blockers
// are the codes above and every one refuses a removal outright (IsHardStop);
// Dirty, ignored files and a unique detached HEAD are soft: they move a row
// out of PROVEN. (Held used to block here too; it is a label now, raised
// from the session claims — see ReasonHeld.)
type Gates struct {
	Blockers       []Reason
	Dirty          bool  // tracked changes or non-ignored untracked files
	IgnoredCount   int   // ignored entries (a wholly ignored directory counts once)
	IgnoredBytes   int64 // their size on disk, symlinks not followed
	DetachedUnique bool  // detached HEAD the base does not reach
}

// GateInputs are what CheckGates cannot learn from git.
type GateInputs struct {
	Base           string   // fully-qualified ref or 40-hex id; needed for a detached HEAD
	ProtectedPaths []string // registered local_path model artifacts
}

// ListWorktrees parses `worktree list --porcelain -z` run in g.Dir. The
// first entry is Primary. Every entry that contains cwd is Current: removing
// any of them would delete the caller's directory.
func ListWorktrees(g Git, cwd string) ([]WorktreeFact, error) {
	facts, err := listWorktrees(g)
	if err != nil {
		return nil, err
	}
	here := resolve(cwd)
	for i := range facts {
		facts[i].Current = within(here, resolve(facts[i].Path))
	}
	return facts, nil
}

func listWorktrees(g Git) ([]WorktreeFact, error) {
	args := []string{"worktree", "list", "--porcelain", "-z"}
	o, err := g.Run(args...)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, gitFailure(o, args...)
	}
	var facts []WorktreeFact
	var cur *WorktreeFact
	for _, field := range strings.Split(o.Stdout, "\x00") {
		if field == "" { // a record ends with an empty field
			if cur != nil {
				facts = append(facts, *cur)
				cur = nil
			}
			continue
		}
		key, val, _ := strings.Cut(field, " ")
		if key == "worktree" {
			if cur != nil {
				facts = append(facts, *cur)
			}
			cur = &WorktreeFact{Path: val, Primary: len(facts) == 0}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("cleanup: worktree list: %q before any worktree line", field)
		}
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = val
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	if cur != nil {
		facts = append(facts, *cur)
	}
	if len(facts) == 0 {
		return nil, errors.New("cleanup: worktree list: no worktrees")
	}
	return facts, nil
}

// resolve makes p absolute and resolves symlinks (macOS /var is
// /private/var). A path that does not exist resolves through its nearest
// existing ancestor, so a missing worktree or artifact still compares.
func resolve(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	rest := ""
	for dir := abs; ; {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// ResolvePath is p as every comparison here sees it: absolute, symlinks
// resolved (through its nearest existing ancestor when p is gone). Removal
// compares paths with it so /var and /private/var are one directory.
func ResolvePath(p string) string { return resolve(p) }

// Within reports whether child is parent or below it, separator-safe and
// case-folded (an over-match only blocks more). Both should be resolved.
func Within(child, parent string) bool { return within(child, parent) }

// within reports whether child is parent or below it. Case is folded: on a
// case-insensitive volume (the macOS default) Foo and foo are one directory,
// and an over-match only blocks more.
func within(child, parent string) bool {
	c, p := strings.ToLower(child), strings.ToLower(parent)
	if c == p {
		return true
	}
	if !strings.HasSuffix(p, string(filepath.Separator)) {
		p += string(filepath.Separator)
	}
	return strings.HasPrefix(c, p)
}

// operationMarkers are the git-dir entries of an operation stopped midway.
// sequencer outlives CHERRY_PICK_HEAD/REVERT_HEAD: a multi-commit pick
// resumed by a plain commit still has commits queued there.
var operationMarkers = []string{"rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG", "sequencer"}

// CheckGates evaluates w's hard blockers and soft facts. g runs in the
// repository (any of its worktrees); the status checks run in w.Path. Any
// gate it cannot evaluate is an error, never a clean answer. With an error
// the Gates hold only the hard blockers found before it (the soft facts are
// zero, never read as clean): a blocker found is a blocker whatever failed
// after it. It reads only.
func CheckGates(g Git, w WorktreeFact, in GateInputs) (Gates, error) {
	var out Gates
	block := func(code, detail string) { out.Blockers = append(out.Blockers, Reason{code, detail}) }
	partial := func() Gates { return Gates{Blockers: out.Blockers} }

	if w.Primary {
		block(BlockPrimary, w.Path)
	}
	if w.Current {
		block(BlockCurrent, w.Path)
	}
	if w.Locked {
		block(BlockLocked, w.Path)
	}
	self := resolve(w.Path)
	// Every check that needs no git runs before the first git call, so its
	// blocker is in the partial Gates whatever fails later.
	// Both directions: an artifact inside the worktree, or the worktree
	// inside (or equal to) an artifact directory; removing it would delete
	// part of the artifact either way. within is separator-safe (/a/b is not
	// under /a/bc) and both sides are resolved through symlinks.
	for _, p := range in.ProtectedPaths {
		if rp := resolve(p); within(rp, self) || within(self, rp) {
			block(BlockProtectedArtifact, flat(p))
		}
	}
	all, err := listWorktrees(g)
	if err != nil {
		return partial(), err
	}
	for _, o := range all {
		if op := resolve(o.Path); op != self && within(op, self) {
			block(BlockNested, o.Path)
		}
	}
	if w.Bare {
		return out, nil // no working tree to inspect
	}
	if w.Prunable {
		// Its .git link is broken: git -C on a directory that is still
		// there walks up into an enclosing repository and would answer
		// for that one.
		return partial(), fmt.Errorf("cleanup: worktree %s is prunable: its git link is broken, so its state cannot be read", w.Path)
	}

	wg := Git{Dir: w.Path, Timeout: g.Timeout}
	ops, err := operationsInProgress(wg, self)
	if err != nil {
		return partial(), err
	}
	for _, op := range ops {
		block(BlockOperation, op)
	}

	if out.Dirty, err = dirty(wg); err != nil {
		return partial(), err
	}
	if out.IgnoredCount, out.IgnoredBytes, err = ignored(wg); err != nil {
		return partial(), err
	}
	if w.Detached {
		if out.DetachedUnique, err = detachedUnique(g, w.Head, in.Base); err != nil {
			return partial(), err
		}
	}
	return out, nil
}

// operationsInProgress names each operation marker present in the
// worktree's own git dir. It first checks that git's top level for wg.Dir is
// self (the resolved worktree path): otherwise git found an enclosing
// repository, and every later answer would be about that one.
func operationsInProgress(wg Git, self string) ([]string, error) {
	args := []string{"rev-parse", "--path-format=absolute", "--show-toplevel"}
	for _, m := range operationMarkers {
		args = append(args, "--git-path", m)
	}
	o, err := wg.Run(args...)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, gitFailure(o, args...)
	}
	lines := strings.Split(strings.TrimRight(o.Stdout, "\n"), "\n")
	if len(lines) != 1+len(operationMarkers) {
		return nil, fmt.Errorf("cleanup: rev-parse --show-toplevel --git-path: %d lines for %d markers", len(lines), len(operationMarkers))
	}
	if top := resolve(lines[0]); top != self {
		return nil, fmt.Errorf("cleanup: %s is not a worktree top level (git found %s)", wg.Dir, top)
	}
	paths := lines[1:]
	var ops []string
	for i, p := range paths {
		_, err := os.Lstat(p)
		switch {
		case err == nil:
			ops = append(ops, operationMarkers[i])
		case errors.Is(err, fs.ErrNotExist):
		default:
			return nil, fmt.Errorf("cleanup: %s: %w", p, err)
		}
	}
	return ops, nil
}

// statusArgs are pinned so user config (status.showUntrackedFiles=no,
// diff.ignoreSubmodules, submodule.<n>.ignore) cannot hide changes, and
// fsmonitor is off so a stale daemon answer cannot either.
var statusArgs = []string{"-c", "core.fsmonitor=false", "status", "--porcelain", "-z", "--untracked-files=normal", "--ignore-submodules=none"}

// dirty reports tracked changes, non-ignored untracked files, or any index
// entry flagged skip-worktree or assume-unchanged: status does not see edits
// to those, so their presence counts as dirty (a sparse checkout, which sets
// skip-worktree on purpose, over-reports; that fails closed).
func dirty(wg Git) (bool, error) {
	o, err := wg.Run(statusArgs...)
	if err != nil {
		return false, err
	}
	if o.Code != 0 {
		return false, gitFailure(o, statusArgs...)
	}
	if o.Stdout != "" {
		return true, nil
	}
	return HiddenIndexFlags(wg)
}

// HiddenIndexFlags reports whether `ls-files -v` tags any entry of the
// checkout wg runs in `S` (skip-worktree) or with a lowercase letter
// (assume-unchanged): `git status` cannot see an edit to such an entry. It
// only reads (removal.ReleaseRoot refuses on it).
func HiddenIndexFlags(wg Git) (bool, error) {
	args := []string{"ls-files", "-v", "-z"}
	o, err := wg.Run(args...)
	if err != nil {
		return false, err
	}
	if o.Code != 0 {
		return false, gitFailure(o, args...)
	}
	for _, e := range strings.Split(o.Stdout, "\x00") {
		if e == "" {
			continue
		}
		if tag := e[0]; tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return true, nil
		}
	}
	return false, nil
}

// ignored counts the ignored entries (a wholly ignored directory once) and
// sums their size, never following a symlink.
func ignored(wg Git) (int, int64, error) {
	args := []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"}
	o, err := wg.Run(args...)
	if err != nil {
		return 0, 0, err
	}
	if o.Code != 0 {
		return 0, 0, gitFailure(o, args...)
	}
	var n int
	var size int64
	for _, rel := range strings.Split(o.Stdout, "\x00") {
		if rel == "" {
			continue
		}
		n++
		p := filepath.Join(wg.Dir, rel)
		fi, err := os.Lstat(p)
		if err != nil {
			return 0, 0, fmt.Errorf("cleanup: ignored %s: %w", p, err)
		}
		if !fi.IsDir() {
			size += fi.Size()
			continue
		}
		err = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info() // lstat: a symlink is its own size
			if err != nil {
				return err
			}
			size += info.Size()
			return nil
		})
		if err != nil {
			return 0, 0, fmt.Errorf("cleanup: ignored %s: %w", p, err)
		}
	}
	return n, size, nil
}

// detachedUnique reports whether head is not an ancestor of base: a detached
// HEAD the base does not reach holds work only this worktree has.
func detachedUnique(g Git, head, base string) (bool, error) {
	if !fullCommitID.MatchString(head) {
		return false, fmt.Errorf("cleanup: detached HEAD %q is not a full commit id", head)
	}
	if !qualified(base) {
		return false, fmt.Errorf("cleanup: base %q is not a fully-qualified ref or commit id", base)
	}
	args := []string{"merge-base", "--is-ancestor", head, base}
	o, err := g.Run(args...)
	if err != nil {
		return false, err
	}
	switch o.Code {
	case 0:
		return false, nil
	case 1:
		return true, nil
	}
	return false, gitFailure(o, args...)
}
