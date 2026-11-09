package removal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// The stages of a release that can fail after a write (ReleaseFailed.Stage).
const (
	StageReset           = "reset"
	StageRemoveUntracked = "remove_untracked"
	StageVerifyClean     = "verify_clean"
	StageSwitch          = "switch"
)

// rootHooksOff is the -c setting every root write carries: a hook is
// arbitrary code, and the reset fires reference-transaction and
// post-index-change, the switch post-checkout too.
const rootHooksOff = "core.hooksPath=/dev/null"

// rootStatusArgs read every uncommitted change of the root: untracked files
// one by one and submodule changes whatever their ignore config says.
var rootStatusArgs = []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none"}

// RootRelease is what `wt restore-root` hands ReleaseRoot: the primary
// checkout and the default branch to switch it to, and the proofs that its
// uncommitted work exists elsewhere.
//
// With NothingUncommitted the root had nothing to copy (no record, Untracked
// empty): the proof is an unchanged fingerprint and an empty status. Else
// Verified says the rescue engine verified a copy of the root, RecordRef is
// the record it wrote (refs/wt/rescued/<id>) and Untracked the untracked
// paths the copy carried, relative to Root (rescue.Plan.UntrackedPaths).
// Unchanged is rescue.Plan.SourceUnchanged: "" when the root still equals the
// state the copy was verified against.
type RootRelease struct {
	Root, Default      string
	NothingUncommitted bool
	Verified           bool
	RecordRef          string
	Untracked          []string
	Unchanged          func() (string, error)
}

// ReleaseResult says what a release did: the reset ran, how many carried
// untracked files were removed, and whether the root is on the default.
type ReleaseResult struct {
	Reset    bool
	Removed  int
	Switched bool
}

// ReleaseRefused is a release that touched nothing: a proof was missing or an
// input did not hold.
type ReleaseRefused struct{ Reason string }

func (e *ReleaseRefused) Error() string { return "the root was not released: " + e.Reason }

// ReleaseFailed is a release that stopped at Stage (StageReset,
// StageRemoveUntracked, StageVerifyClean or StageSwitch) after the gates
// passed; Result says what had already happened. At StageReset nothing was
// done unless git failed part-way through the reset; from
// StageRemoveUntracked on, the root was reset and its work is in the copy and
// the record.
type ReleaseFailed struct {
	Stage  string
	Result ReleaseResult
	Err    error
}

func (e *ReleaseFailed) Error() string {
	return fmt.Sprintf("releasing the root failed at %s: %v", e.Stage, e.Err)
}

func (e *ReleaseFailed) Unwrap() error { return e.Err }

// ReleaseRoot resets the primary checkout at r.Root and switches it to
// r.Default. It is the one place in wt that resets a checkout, and it runs
// only behind three proofs that the root's uncommitted work exists elsewhere.
//
// It ignores g.Dir: every git call runs in r.Root (g gives only the timeout
// and -c settings), so a caller in another worktree can never reset its own.
// The gates, in order, all reads; any miss is a *ReleaseRefused with nothing
// touched:
//
//  1. Root is absolute, the top of the repository's primary checkout, and
//     Default names an existing local branch (not an option, a valid ref
//     name);
//  2. Unchanged is set and reports no difference (the fingerprint);
//  3. the status now: empty with NothingUncommitted (and no carried list);
//     otherwise Verified, RecordRef under refs/wt/rescued/, and the carried
//     list exactly the status's untracked paths (each relative, clean, no
//     `..`, no repeats), so nothing tracked, ignored or outside the root is
//     ever on it;
//  4. otherwise, last and nearest the reset, cleanup.RecordCoversDirt: the
//     root's index and working tree equal the record (the check `wt remove`
//     uses for a dirty rescued original, so the two agree by construction).
//
// Then, each a stage of *ReleaseFailed: `git reset --hard HEAD`; removal of
// exactly the carried untracked paths (never `clean`; see removeCarried); a
// read-back that the status is empty; `git switch --no-overwrite-ignore
// <Default>`, so an ignored file at a path the default tracks fails the
// switch instead of being overwritten (the root is then reset and clean on
// its branch, and the work is in the copy and the record). Every write runs
// with hooks off. Directories the removal leaves empty are left.
//
// Declared window: a change made to the root between the last gate and the
// reset is lost, as in `wt remove` of a dirty rescued original.
func ReleaseRoot(g cleanup.Git, r RootRelease) (ReleaseResult, error) {
	var res ReleaseResult
	refuse := func(format string, a ...any) (ReleaseResult, error) {
		return ReleaseResult{}, &ReleaseRefused{Reason: fmt.Sprintf(format, a...)}
	}
	if r.Root == "" || !filepath.IsAbs(r.Root) {
		return refuse("the root path %q is not absolute", r.Root)
	}
	root := filepath.Clean(r.Root)
	timeout := g.Timeout
	if timeout == 0 {
		timeout = DefaultRemoveTimeout
	}
	rg := cleanup.Git{Dir: root, Timeout: timeout, Config: slices.Clone(g.Config)}

	// 1. Inputs.
	if why, err := checkReleaseInputs(rg, root, r.Default); err != nil {
		return refuse("could not check the root: %v", err)
	} else if why != "" {
		return refuse("%s", why)
	}
	// 2. The fingerprint.
	if r.Unchanged == nil {
		return refuse("there is no fingerprint to compare the root with")
	}
	if d, err := r.Unchanged(); err != nil {
		return refuse("could not re-read the root: %v", err)
	} else if d != "" {
		return refuse("the root changed since its copy was verified (%s)", cleanup.OneLine(d))
	}
	// 3. The status, and the proofs it must agree with.
	st, err := readRootStatus(rg)
	if err != nil {
		return refuse("could not read the root's status: %v", err)
	}
	if r.NothingUncommitted {
		if len(r.Untracked) > 0 {
			return refuse("a carried untracked list was given for a root with nothing uncommitted")
		}
		if st != "" {
			return refuse("the root has uncommitted changes, so it cannot be released without a verified copy and a record")
		}
	} else {
		if !r.Verified {
			return refuse("no verified copy of the root's work")
		}
		if !strings.HasPrefix(r.RecordRef, cleanup.RecordRefPrefix) || r.RecordRef == cleanup.RecordRefPrefix {
			return refuse("no rescue record (%q is not under %s)", r.RecordRef, cleanup.RecordRefPrefix)
		}
		if why := sameUntracked(st, r.Untracked); why != "" {
			return refuse("%s", why)
		}
		// 4. The record, last.
		head, err := gitText(rg, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return refuse("could not read the root's HEAD: %v", err)
		}
		if !cleanup.IsFullCommitID(head) {
			return refuse("the root's HEAD %q is not a commit", head)
		}
		covered, err := cleanup.RecordCoversDirt(rg, root, head, r.RecordRef)
		if err != nil {
			return refuse("could not check the record %s: %v", r.RecordRef, err)
		}
		if !covered {
			return refuse("the record %s does not hold the root's current index and working tree", r.RecordRef)
		}
	}

	fail := func(stage string, err error) (ReleaseResult, error) {
		return res, &ReleaseFailed{Stage: stage, Result: res, Err: err}
	}
	if err := gitWrite(rg, "-c", rootHooksOff, "reset", "--hard", "HEAD"); err != nil {
		return fail(StageReset, err)
	}
	res.Reset = true

	n, err := removeCarried(rg, root, r.Untracked)
	res.Removed = n
	if err != nil {
		return fail(StageRemoveUntracked, err)
	}

	if st, err := readRootStatus(rg); err != nil {
		return fail(StageVerifyClean, fmt.Errorf("could not read the root's status: %w", err))
	} else if st != "" {
		return fail(StageVerifyClean, fmt.Errorf("the root still has changes after the reset: %s", statusPaths(st)))
	}

	if err := gitWrite(rg, "-c", rootHooksOff, "switch", "--no-overwrite-ignore", r.Default); err != nil {
		return fail(StageSwitch, err)
	}
	if ref, err := gitText(rg, "symbolic-ref", "-q", "HEAD"); err != nil {
		return fail(StageSwitch, fmt.Errorf("could not read the root's branch after the switch: %w", err))
	} else if ref != "refs/heads/"+r.Default {
		return fail(StageSwitch, fmt.Errorf("the root is on %q after the switch, not %s", ref, r.Default))
	}
	res.Switched = true
	return res, nil
}

// checkReleaseInputs returns why root or def cannot be released ("" when
// both hold): root must be the top of the primary checkout (not a linked
// worktree or a subdirectory) and def an existing local branch.
func checkReleaseInputs(rg cleanup.Git, root, def string) (string, error) {
	if def == "" || strings.HasPrefix(def, "-") {
		return fmt.Sprintf("%q is not a branch name", def), nil
	}
	if o, err := runGit(rg, "check-ref-format", "refs/heads/"+def); err != nil {
		return "", err
	} else if o.Code != 0 {
		return fmt.Sprintf("%q is not a branch name", def), nil
	}
	if o, err := runGit(rg, "rev-parse", "--verify", "--quiet", "refs/heads/"+def+"^{commit}"); err != nil {
		return "", err
	} else if o.Code != 0 {
		return fmt.Sprintf("the default branch %s does not exist locally", def), nil
	}
	top, err := gitText(rg, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if cleanup.ResolvePath(top) != cleanup.ResolvePath(root) {
		return fmt.Sprintf("%s is not the top of a checkout (%s is)", root, top), nil
	}
	gitDir, err := gitText(rg, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return "", err
	}
	common, err := gitText(rg, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if cleanup.ResolvePath(gitDir) != cleanup.ResolvePath(common) {
		return fmt.Sprintf("%s is a linked worktree, not the primary checkout", root), nil
	}
	return "", nil
}

// sameUntracked returns why carried is not exactly the untracked paths of the
// status st ("" when it is): each carried path must be relative, clean and
// inside the root, named once, and listed by git as untracked (not tracked,
// not ignored), and every untracked path must be carried.
func sameUntracked(st string, carried []string) string {
	now := map[string]bool{}
	for _, p := range untrackedIn(st) {
		now[p] = true
	}
	seen := map[string]bool{}
	for _, p := range carried {
		if why := unsafeRel(p); why != "" {
			return fmt.Sprintf("the carried path %q %s", cleanup.OneLine(p), why)
		}
		if seen[p] {
			return fmt.Sprintf("the carried path %q is listed twice", cleanup.OneLine(p))
		}
		seen[p] = true
		if !now[p] {
			return fmt.Sprintf("the carried path %q is not an untracked file of the root now", cleanup.OneLine(p))
		}
	}
	for p := range now {
		if !seen[p] {
			return fmt.Sprintf("the untracked path %q was not carried", cleanup.OneLine(p))
		}
	}
	return ""
}

// unsafeRel says why p cannot name a file inside the root, or "".
func unsafeRel(p string) string {
	switch {
	case p == "" || p == ".":
		return "is empty"
	case filepath.IsAbs(p) || strings.HasPrefix(p, "/"):
		return "is absolute"
	case filepath.Clean(p) != filepath.FromSlash(p) || strings.Contains(p, "\x00"):
		return "is not a clean relative path"
	case slices.Contains(strings.Split(filepath.ToSlash(p), "/"), ".."):
		return "leaves the root"
	}
	return ""
}

// untrackedIn returns the untracked paths ("? <path>" entries) of a
// `status --porcelain=v2 -z` listing.
func untrackedIn(st string) []string {
	var out []string
	fields := strings.Split(st, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case strings.HasPrefix(f, "? "):
			out = append(out, f[2:])
		case strings.HasPrefix(f, "2 "):
			i++ // a rename or copy is followed by its original path
		}
	}
	return out
}

// statusPaths is a short, one-line list of the entries in a status listing.
func statusPaths(st string) string {
	var out []string
	fields := strings.Split(strings.TrimRight(st, "\x00"), "\x00")
	for i := 0; i < len(fields) && len(out) < 5; i++ {
		out = append(out, cleanup.OneLine(fields[i]))
		if strings.HasPrefix(fields[i], "2 ") {
			i++
		}
	}
	if len(fields) > len(out) {
		out = append(out, "...")
	}
	return strings.Join(out, "; ")
}

// removeCarried removes exactly the carried untracked paths from the root,
// after the reset, and returns how many it removed. Per path: it must be a
// clean relative path inside the root; a path the index tracks now was
// restored from HEAD by the reset (`git rm --cached foo` with foo on disk)
// and is skipped; a parent that is gone means the path is gone (skipped); a
// parent that is a symlink or not a directory is an error; the path itself
// must be a regular file or a symlink (the link is removed, never its
// target), and one already gone is skipped. A directory or anything else is
// an error. It stops at the first error.
func removeCarried(rg cleanup.Git, root string, carried []string) (int, error) {
	removed := 0
	for _, p := range carried {
		if why := unsafeRel(p); why != "" {
			return removed, fmt.Errorf("the carried path %q %s", cleanup.OneLine(p), why)
		}
		o, err := runGit(rg, "--literal-pathspecs", "ls-files", "--error-unmatch", "--", p)
		if err != nil {
			return removed, fmt.Errorf("checking whether %q is tracked: %w", cleanup.OneLine(p), err)
		}
		switch o.Code {
		case 0:
			continue // restored from HEAD by the reset: tracked, never removed
		case 1:
		default:
			return removed, fmt.Errorf("checking whether %q is tracked: git ls-files: exit %d: %s", cleanup.OneLine(p), o.Code, cleanup.OneLine(o.Stderr))
		}
		parts := strings.Split(filepath.ToSlash(p), "/")
		dir, gone := root, false
		for _, part := range parts[:len(parts)-1] {
			dir = filepath.Join(dir, part)
			fi, err := os.Lstat(dir)
			if errors.Is(err, fs.ErrNotExist) {
				gone = true
				break
			}
			if err != nil {
				return removed, err
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				return removed, fmt.Errorf("the carried path %q is under a symlink (%s); nothing below it is removed", cleanup.OneLine(p), cleanup.OneLine(dir))
			}
			if !fi.IsDir() {
				return removed, fmt.Errorf("the carried path %q is under %s, which is not a directory", cleanup.OneLine(p), cleanup.OneLine(dir))
			}
		}
		if gone {
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(p))
		fi, err := os.Lstat(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if !fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0 {
			return removed, fmt.Errorf("the carried path %q is now a %s, not a file; it is left", cleanup.OneLine(p), kindOf(fi.Mode()))
		}
		if err := os.Remove(full); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// kindOf names a file mode's type for a message.
func kindOf(m fs.FileMode) string {
	if m.IsDir() {
		return "directory"
	}
	return "special file (" + m.Type().String() + ")"
}

// readRootStatus is the root's full status listing ("" when clean).
func readRootStatus(rg cleanup.Git) (string, error) {
	o, err := runGit(rg, rootStatusArgs...)
	if err != nil {
		return "", err
	}
	if o.Code != 0 {
		return "", fmt.Errorf("git status: exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}
	return o.Stdout, nil
}

// gitText runs a read in the root and returns its trimmed output.
func gitText(rg cleanup.Git, args ...string) (string, error) {
	o, err := runGit(rg, args...)
	if err != nil {
		return "", err
	}
	if o.Code != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, cleanup.OneLine(o.Stderr))
	}
	return strings.TrimSpace(o.Stdout), nil
}

// gitWrite runs a write in the root; a timeout, a kill or a non-zero exit is
// an error naming git's text.
func gitWrite(rg cleanup.Git, args ...string) error {
	o, err := runGit(rg, args...)
	if err != nil {
		return fmt.Errorf("git did not finish (timeout %s): %w", rg.Timeout, err)
	}
	if o.Code != 0 {
		return fmt.Errorf("git exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}
	return nil
}
