package removal

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// The stages of a release that can fail after a write (ReleaseFailed.Stage).
const (
	StageReset           = "reset"
	StageRemoveUntracked = "remove_untracked"
	StageVerifyClean     = "verify_clean"
	StageSwitch          = "switch"
)

// Settings the two root writes (the reset and the switch) carry as -c
// options. Hooks off: a hook is arbitrary code, and the reset fires
// reference-transaction and post-index-change, the switch post-checkout too.
// Submodule recursion off: a user's submodule.recurse=true would make the
// reset and the switch move submodule checkouts as well.
const (
	rootHooksOff      = "core.hooksPath=/dev/null"
	rootNoSubRecurse  = "submodule.recurse=false"
	rootStatusIgnored = "--ignored=matching"
)

// rootStatusArgs read every uncommitted change of the root: untracked files
// one by one and submodule changes whatever their ignore config says.
var rootStatusArgs = []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none"}

// rootRepoEnv are the variables that override which repository, index or
// object store `git -C <root>` uses. ReleaseRoot refuses while any is set
// (even empty): stripping them is not possible through cleanup.Git, which
// only appends to the environment. GIT_CEILING_DIRECTORIES is not listed: it
// only stops discovery above a directory, and the root holds its own .git.
var rootRepoEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE"}

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
// passed; Result says what had certainly happened. Uncertain says the state
// is not fully known: the reset or switch call itself failed (a non-zero
// exit, a timeout or a kill), and git may have written part of the index or
// working tree before it stopped, so Reset or Switched being false does not
// mean nothing changed. It is set for every failure at StageReset and
// StageSwitch and never at StageRemoveUntracked or StageVerifyClean, whose
// Result is exact. From StageRemoveUntracked on, the root was reset and its
// work is in the copy and the record.
type ReleaseFailed struct {
	Stage     string
	Result    ReleaseResult
	Uncertain bool
	Err       error
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
//  1. Root is absolute; no variable of rootRepoEnv is set; Root is the top of
//     the repository's primary checkout, on a branch (not detached); Default
//     names an existing local branch (not an option, a valid ref name) that
//     no other worktree has checked out;
//  2. Unchanged is set and reports no difference (the fingerprint);
//  3. the status now: empty with NothingUncommitted (and no carried list);
//     otherwise Verified, RecordRef under refs/wt/rescued/, and the carried
//     list exactly the status's untracked paths (each relative, clean, no
//     `..`, no `.git` component, no repeats), so nothing tracked, ignored,
//     outside the root or under a `.git` directory is ever on it;
//  4. no index entry is assume-unchanged or skip-worktree
//     (cleanup.HiddenIndexFlags): status cannot see an edit to one, and with
//     nothing uncommitted an empty status is the only content proof;
//  5. otherwise, cleanup.RecordCoversDirt: the root's index and working tree
//     equal the record (the check `wt remove` uses for a dirty rescued
//     original, so the two agree by construction), and the record's tree is
//     read for the removal's content check;
//  6. last and nearest the reset, no ignored file is where the reset would
//     delete or overwrite it (ignoredAtRisk): ignored files are the one thing
//     neither the copy nor the record holds.
//
// So the last gates are the record check, then the ignored-files check.
// A root with any skip-worktree entry (a sparse checkout) is refused by gate
// 4, failing closed as cleanup's dirt check over-reports one.
//
// Every git call runs with core.fsmonitor=false, set after g's -c settings
// so a caller cannot turn it back on: a stale or wrong fsmonitor hook would
// hide an edit from the status and index-flag gates, and the reset trusts
// it too.
//
// Then, each a stage of *ReleaseFailed: `git reset --hard HEAD`; removal of
// exactly the carried untracked paths whose content still equals the record
// (never `clean`; see removeCarried); a read-back that the status is empty;
// `git switch --no-overwrite-ignore <Default>`, so an ignored file at a path
// the default tracks fails the switch instead of being overwritten (the root
// is then reset and clean on its branch, and the work is in the copy and the
// record). The reset and the switch run with hooks and submodule recursion
// off; the reads do not need it. Directories the removal leaves empty are
// left.
//
// Declared window: a change made to the root between the last gates (the
// record check, then the ignored-files check) and the reset is lost, as in
// `wt remove` of a dirty rescued original (a carried untracked file edited in
// that window is kept, by the content check).
func ReleaseRoot(g cleanup.Git, r RootRelease) (ReleaseResult, error) {
	var res ReleaseResult
	refuse := func(format string, a ...any) (ReleaseResult, error) {
		return ReleaseResult{}, &ReleaseRefused{Reason: fmt.Sprintf(format, a...)}
	}
	if r.Root == "" || !filepath.IsAbs(r.Root) {
		return refuse("the root path %q is not absolute", r.Root)
	}
	for _, v := range rootRepoEnv {
		if _, set := os.LookupEnv(v); set {
			return refuse("%s is set in the environment, so git would not act on the root alone; unset it", v)
		}
	}
	root := filepath.Clean(r.Root)
	timeout := g.Timeout
	if timeout == 0 {
		timeout = DefaultRemoveTimeout
	}
	rg := cleanup.Git{Dir: root, Timeout: timeout, Config: append(slices.Clone(g.Config), "core.fsmonitor=false")}

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
	}
	// 4. Edits status cannot see.
	if hidden, err := cleanup.HiddenIndexFlags(rg); err != nil {
		return refuse("could not read the root's index flags: %v", err)
	} else if hidden {
		return refuse("the root has an assume-unchanged or skip-worktree index entry, which status cannot see; clear it first (git update-index --no-assume-unchanged / --no-skip-worktree)")
	}
	// 5. The record.
	fold := pathFold(rg)
	var rec *recordTree
	if !r.NothingUncommitted {
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
		if rec, err = readRecordTree(rg, r.RecordRef); err != nil {
			return refuse("could not read the record %s: %v", r.RecordRef, err)
		}
	}
	// 6. Ignored files the reset would destroy, last: they are the one thing
	// the record does not hold.
	if why, err := ignoredAtRisk(rg, root, fold); err != nil {
		return refuse("could not check the root's ignored files: %v", err)
	} else if why != "" {
		return refuse("%s; move it first", why)
	}

	fail := func(stage string, err error) (ReleaseResult, error) {
		return res, &ReleaseFailed{Stage: stage, Result: res, Err: err}
	}
	// A failed reset or switch call may have written part of the index or
	// working tree: Uncertain.
	failWrite := func(stage string, err error) (ReleaseResult, error) {
		return res, &ReleaseFailed{Stage: stage, Result: res, Uncertain: true, Err: err}
	}
	if err := gitWrite(rg, "-c", rootHooksOff, "-c", rootNoSubRecurse, "reset", "--hard", "HEAD"); err != nil {
		return failWrite(StageReset, err)
	}
	res.Reset = true

	n, err := removeCarried(rg, root, r.Untracked, rec, fold)
	res.Removed = n
	if err != nil {
		return fail(StageRemoveUntracked, err)
	}

	if st, err := readRootStatus(rg); err != nil {
		return fail(StageVerifyClean, fmt.Errorf("could not read the root's status: %w", err))
	} else if st != "" {
		return fail(StageVerifyClean, fmt.Errorf("the root still has changes after the reset: %s", statusPaths(st)))
	}

	if err := gitWrite(rg, "-c", rootHooksOff, "-c", rootNoSubRecurse, "switch", "--no-overwrite-ignore", r.Default); err != nil {
		return failWrite(StageSwitch, err)
	}
	if ref, err := gitText(rg, "symbolic-ref", "-q", "HEAD"); err != nil {
		return failWrite(StageSwitch, fmt.Errorf("could not read the root's branch after the switch: %w", err))
	} else if ref != "refs/heads/"+r.Default {
		return failWrite(StageSwitch, fmt.Errorf("the root is on %q after the switch, not %s", ref, r.Default))
	}
	res.Switched = true
	return res, nil
}

// checkReleaseInputs returns why root or def cannot be released ("" when
// both hold): root must be the top of the primary checkout (not a linked
// worktree or a subdirectory) and on a branch (not a detached HEAD, whose
// commits would be left on no branch), and def an existing local branch that
// no other worktree has checked out.
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
	// A detached root's commits may be on no branch: switching away would
	// orphan them.
	if o, err := runGit(rg, "symbolic-ref", "-q", "HEAD"); err != nil {
		return "", err
	} else if ref := strings.TrimSpace(o.Stdout); o.Code != 0 || !strings.HasPrefix(ref, "refs/heads/") || ref == "refs/heads/" {
		if o.Code > 1 {
			return "", fmt.Errorf("git symbolic-ref: exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
		}
		return fmt.Sprintf("%s is not on a branch (detached HEAD); its commits would be left on no branch", root), nil
	}
	o, err := runGit(rg, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	if o.Code != 0 {
		return "", fmt.Errorf("git worktree list: exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}
	cur := ""
	for _, field := range strings.Split(o.Stdout, "\x00") {
		if p, ok := strings.CutPrefix(field, "worktree "); ok {
			cur = p
		}
		if field == "branch refs/heads/"+def && cleanup.ResolvePath(cur) != cleanup.ResolvePath(root) {
			return fmt.Sprintf("the default branch %s is checked out in another worktree (%s)", def, cleanup.OneLine(cur)), nil
		}
	}
	return "", nil
}

// sameUntracked returns why carried is not exactly the untracked paths of the
// status st ("" when it is): each carried path must be relative, clean,
// inside the root and naming no `.git` directory, named once, and listed by
// git as untracked (not tracked, not ignored), and every untracked path must
// be carried.
func sameUntracked(st string, carried []string) string {
	now := map[string]bool{}
	for _, p := range entriesIn(st, "? ") {
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

// ignoredAtRisk returns which ignored entry of the root `git reset --hard
// HEAD` would delete or overwrite ("" none). The reset writes every path HEAD
// or the index tracks, so an ignored entry is at risk when it is such a path
// (an ignored file at an index-deleted path), lies under one (an ignored file
// in a directory that replaced a tracked file: git removes the directory to
// write the file), or sits where a tracked path needs a directory: a
// non-directory replacing a tracked directory, or a directory git lists as
// ignored as a whole ("foo/", which it does only when the index has nothing
// under it) while HEAD tracks paths inside it. Ignored entries elsewhere,
// including ignored files inside tracked directories, are never touched.
//
// Two passes, both needed. By name: the paths HEAD and the index track
// against git's ignored listing, compared the way git compares them (folded
// when core.ignorecase is on, pathFold, which the caller read once).
// On disk (ignoredOnDisk): every path
// the status lists as changed is looked up in the filesystem, which resolves
// case and Unicode normalization the way the volume does, and compared by
// file identity with the ignored entries; this catches what folding cannot
// (an NFD name on disk for an NFC name in git, on a normalization-insensitive
// volume such as APFS).
func ignoredAtRisk(rg cleanup.Git, root string, fold func(string) string) (string, error) {
	head, err := runGitZ(rg, "ls-tree", "-r", "-z", "--name-only", "--full-tree", "HEAD")
	if err != nil {
		return "", err
	}
	index, err := runGitZ(rg, "ls-files", "-z")
	if err != nil {
		return "", err
	}
	tracked, trackedDirs := pathSets(append(head, index...), fold)
	st, err := runGitZ(rg, append(slices.Clone(rootStatusArgs), rootStatusIgnored)...)
	if err != nil {
		return "", err
	}
	listing := strings.Join(st, "\x00")
	ignored := entriesIn(listing, "! ")
	for _, ig := range ignored {
		name := strings.TrimSuffix(ig, "/")
		key := fold(name)
		if tracked[key] {
			return fmt.Sprintf("the reset would overwrite the ignored %s, a path HEAD or the index tracks", cleanup.OneLine(name)), nil
		}
		for a := path.Dir(name); a != "."; a = path.Dir(a) {
			if tracked[fold(a)] {
				return fmt.Sprintf("the reset would delete the ignored %s: it is inside %s, which HEAD or the index tracks as a file", cleanup.OneLine(name), cleanup.OneLine(a)), nil
			}
		}
		if trackedDirs[key] {
			if strings.HasSuffix(ig, "/") {
				return fmt.Sprintf("the reset would write paths HEAD tracks inside the ignored directory %s, whose contents it does not know", cleanup.OneLine(name)), nil
			}
			return fmt.Sprintf("the reset would replace the ignored %s with a tracked directory", cleanup.OneLine(name)), nil
		}
	}
	return ignoredOnDisk(root, ignored, changedPaths(listing))
}

// RootIgnoredAtRisk is ReleaseRoot's ignored-files gate (gate 6) on its own,
// for a caller that must refuse before anything is created: which ignored
// entry of the root `git reset --hard HEAD` would delete or overwrite ("" none),
// read through the same git ReleaseRoot builds (Dir = root, never g.Dir; g's
// timeout and -c settings, then core.fsmonitor=false) and the same pathFold.
// It only reads. An error is a failed read; the caller fails closed.
func RootIgnoredAtRisk(g cleanup.Git, root string) (string, error) {
	timeout := g.Timeout
	if timeout == 0 {
		timeout = DefaultRemoveTimeout
	}
	root = filepath.Clean(root)
	rg := cleanup.Git{Dir: root, Timeout: timeout, Config: append(slices.Clone(g.Config), "core.fsmonitor=false")}
	return ignoredAtRisk(rg, root, pathFold(rg))
}

// pathFold returns how the root's git compares path names: unchanged, or
// case-folded when core.ignorecase is true. A config that cannot be read, or
// reads as anything but a clean "unset" or a boolean, folds (fail closed:
// folding only finds more conflicts). The fold is Unicode simple case folding
// (upper then lower, so EqualFold-equal names share a key). It does not
// normalize NFD/NFC: ignoredAtRisk's on-disk pass covers that by asking the
// filesystem, and removeCarried treats a normalization-only difference as an
// error, never a skip.
func pathFold(rg cleanup.Git) func(string) string {
	ignoreCase := true
	if o, err := runGit(rg, "config", "--get", "--type=bool", "core.ignorecase"); err == nil {
		switch v := strings.TrimSpace(o.Stdout); {
		case o.Code == 0 && v == "false":
			ignoreCase = false
		case o.Code == 1 && v == "":
			ignoreCase = false // unset: git's default is false
		}
	}
	if !ignoreCase {
		return func(s string) string { return s }
	}
	return func(s string) string { return strings.ToLower(strings.ToUpper(s)) }
}

// pathSets returns the set of paths and the set of their ancestor
// directories (slash-separated, relative), keyed by fold.
func pathSets(paths []string, fold func(string) string) (files, dirs map[string]bool) {
	files, dirs = map[string]bool{}, map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		files[fold(p)] = true
		for d := path.Dir(p); d != "." && !dirs[fold(d)]; d = path.Dir(d) {
			dirs[fold(d)] = true
		}
	}
	return files, dirs
}

// changedPaths returns the paths of a `status --porcelain=v2 -z` listing's
// tracked entries (changed, renamed or copied with their original path, and
// unmerged): every path the reset may write.
func changedPaths(st string) []string {
	var out []string
	fields := strings.Split(st, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		var n int
		switch {
		case strings.HasPrefix(f, "1 "):
			n = 9
		case strings.HasPrefix(f, "2 "):
			n = 10
		case strings.HasPrefix(f, "u "):
			n = 11
		default:
			continue
		}
		if parts := strings.SplitN(f, " ", n); len(parts) == n {
			out = append(out, parts[n-1])
		}
		if n == 10 && i+1 < len(fields) {
			i++
			out = append(out, fields[i])
		}
	}
	return out
}

// fileID is a file's identity on its volume.
type fileID struct{ dev, ino uint64 }

func idOf(fi fs.FileInfo) (fileID, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, fmt.Errorf("no file identity for %s", fi.Name())
	}
	return fileID{uint64(st.Dev), uint64(st.Ino)}, nil
}

// ignoredOnDisk is ignoredAtRisk's on-disk pass. It records the identity of
// each ignored entry and of every directory holding one, then walks each
// changed tracked path from the root down through the filesystem: an
// ignored entry met on the way (the path, or a directory above it) would be
// overwritten or have paths written into it; a changed path that is a
// directory on disk holding an ignored entry would be deleted to write the
// tracked file.
func ignoredOnDisk(root string, ignored, changed []string) (string, error) {
	self := map[fileID]string{}   // an ignored entry itself
	within := map[fileID]string{} // a directory holding an ignored entry (or one that is ignored)
	lstat := func(rel string) (fs.FileInfo, fileID, bool, error) {
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil, fileID{}, false, nil
		}
		if err != nil {
			return nil, fileID{}, false, err
		}
		id, err := idOf(fi)
		return fi, id, err == nil, err
	}
	for _, ig := range ignored {
		name := strings.TrimSuffix(ig, "/")
		fi, id, ok, err := lstat(name)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		if _, seen := self[id]; !seen {
			self[id] = name
		}
		if fi.IsDir() {
			if _, seen := within[id]; !seen {
				within[id] = name
			}
		}
		for a := path.Dir(name); a != "."; a = path.Dir(a) {
			_, aid, ok, err := lstat(a)
			if err != nil {
				return "", err
			}
			if ok {
				if _, seen := within[aid]; !seen {
					within[aid] = name
				}
			}
		}
	}
	if len(self) == 0 {
		return "", nil
	}
	for _, p := range changed {
		parts := strings.Split(p, "/")
		for i := range parts {
			q := strings.Join(parts[:i+1], "/")
			fi, id, ok, err := lstat(q)
			if err != nil {
				return "", err
			}
			if !ok {
				break
			}
			if name, hit := self[id]; hit {
				return fmt.Sprintf("the reset would overwrite or write into the ignored %s (on disk it is %s, which HEAD or the index tracks)", cleanup.OneLine(name), cleanup.OneLine(q)), nil
			}
			if i == len(parts)-1 && fi.IsDir() {
				if name, hit := within[id]; hit {
					return fmt.Sprintf("the reset would delete the ignored %s: it is inside %s, a directory on disk where HEAD or the index tracks a file", cleanup.OneLine(name), cleanup.OneLine(q)), nil
				}
			}
			if !fi.IsDir() {
				break
			}
		}
	}
	return "", nil
}

// unsafeRel says why p cannot name a file inside the root (and outside any
// .git directory of it, repository metadata rescue never carries), or "".
// The rules are rescue's safeRel's.
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
	case slices.ContainsFunc(strings.Split(filepath.ToSlash(p), "/"), func(c string) bool {
		return strings.EqualFold(c, ".git")
	}):
		return "names a .git directory, which is repository metadata, never a carried file"
	}
	return ""
}

// entriesIn returns the paths of a `status --porcelain=v2 -z` listing's
// entries of one kind: "? " untracked, "! " ignored.
func entriesIn(st, kind string) []string {
	var out []string
	fields := strings.Split(st, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case strings.HasPrefix(f, kind):
			out = append(out, f[len(kind):])
		case strings.HasPrefix(f, "2 "):
			i++ // a rename or copy is followed by its original path
		}
	}
	return out
}

// statusPaths is a short, one-line list of the entries in a status listing,
// with "..." only when entries were left unshown (a rename or copy consumes
// two fields but is one entry).
func statusPaths(st string) string {
	var out []string
	fields := strings.Split(strings.TrimRight(st, "\x00"), "\x00")
	i := 0
	for ; i < len(fields) && len(out) < 5; i++ {
		out = append(out, cleanup.OneLine(fields[i]))
		if strings.HasPrefix(fields[i], "2 ") {
			i++ // a rename or copy is followed by its original path
		}
	}
	if i < len(fields) {
		out = append(out, "...")
	}
	return strings.Join(out, "; ")
}

// recordTree is the record's working-state tree: per path, its mode and
// object id, and the repository's object format (sha1 or sha256).
type recordTree struct {
	format  string
	entries map[string]recordEntry
}

type recordEntry struct{ mode, oid string }

// readRecordTree lists the record commit's tree (every path, recursively).
func readRecordTree(rg cleanup.Git, ref string) (*recordTree, error) {
	format, err := gitText(rg, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	if format != "sha1" && format != "sha256" {
		return nil, fmt.Errorf("unknown object format %q", format)
	}
	lines, err := runGitZ(rg, "ls-tree", "-r", "-z", "--full-tree", ref+"^{commit}")
	if err != nil {
		return nil, err
	}
	t := &recordTree{format: format, entries: map[string]recordEntry{}}
	for _, l := range lines {
		if l == "" {
			continue
		}
		meta, p, ok := strings.Cut(l, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree line %q", cleanup.OneLine(l))
		}
		t.entries[p] = recordEntry{mode: f[0], oid: f[2]}
	}
	return t, nil
}

// matches says why the file at full (relative path p, Lstat info fi) is not
// what the record holds at p ("" when it is): same kind (a regular file or a
// symlink) and the same blob, hashed as git does.
func (t *recordTree) matches(p, full string, fi fs.FileInfo) (string, error) {
	e, ok := t.entries[p]
	if !ok {
		return "the record does not hold it", nil
	}
	isLink := fi.Mode()&fs.ModeSymlink != 0
	if isLink != (e.mode == "120000") {
		return "its kind differs from the record's", nil
	}
	var h hash.Hash
	if t.format == "sha256" {
		h = sha256.New()
	} else {
		h = sha1.New()
	}
	if isLink {
		tgt, err := os.Readlink(full)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "blob %d\x00%s", len(tgt), tgt)
	} else {
		fh, err := os.Open(full)
		if err != nil {
			return "", err
		}
		defer fh.Close()
		st, err := fh.Stat()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "blob %d\x00", st.Size())
		if n, err := io.Copy(h, fh); err != nil {
			return "", err
		} else if n != st.Size() {
			return "it changed while being read", nil
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != e.oid {
		return "its content differs from the record's (changed after the copy)", nil
	}
	return "", nil
}

// removeCarried removes exactly the carried untracked paths from the root,
// after the reset, and returns how many it removed. The index is read once
// (one ls-files) and the record's tree was read before the reset. Per path:
// it must be a clean relative path inside the root that names no `.git`
// directory; a path the index tracks
// now was restored from HEAD by the reset (`git rm --cached foo` with foo on
// disk) and is skipped; a parent that is gone, or that the index tracks as a
// file or symlink (one the root had replaced with a directory, which the
// reset restored), means the path is gone (skipped); any other parent that
// is a symlink or not a directory is an error. Index lookups are folded as
// git compares names (pathFold); a parent whose name differs from the
// tracked one only by Unicode normalization is not matched and is an error;
// the path itself must be a regular file or a symlink whose content equals the record's (the link is
// removed, never its target), and one already gone is skipped. A directory,
// anything else, or a file that differs from the record is an error and is
// left. It stops at the first error.
func removeCarried(rg cleanup.Git, root string, carried []string, rec *recordTree, fold func(string) string) (int, error) {
	if len(carried) == 0 {
		return 0, nil
	}
	if rec == nil {
		return 0, errors.New("no record to check the carried files against")
	}
	index, err := runGitZ(rg, "ls-files", "-z")
	if err != nil {
		return 0, fmt.Errorf("reading the index after the reset: %w", err)
	}
	tracked, trackedDirs := pathSets(index, fold)
	removed := 0
	for _, p := range carried {
		if why := unsafeRel(p); why != "" {
			return removed, fmt.Errorf("the carried path %q %s", cleanup.OneLine(p), why)
		}
		if tracked[fold(p)] || trackedDirs[fold(p)] {
			continue // restored from HEAD by the reset: tracked, never removed
		}
		parts := strings.Split(p, "/")
		rel, gone := "", false
		for _, part := range parts[:len(parts)-1] {
			rel = path.Join(rel, part)
			dir := filepath.Join(root, filepath.FromSlash(rel))
			fi, err := os.Lstat(dir)
			if errors.Is(err, fs.ErrNotExist) {
				gone = true
				break
			}
			if err != nil {
				return removed, err
			}
			if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
				if tracked[fold(rel)] {
					// The reset put the tracked file or symlink back in place
					// of the directory the carried path was in.
					gone = true
					break
				}
				if fi.Mode()&fs.ModeSymlink != 0 {
					return removed, fmt.Errorf("the carried path %q is under a symlink (%s); nothing below it is removed", cleanup.OneLine(p), cleanup.OneLine(dir))
				}
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
		if why, err := rec.matches(p, full, fi); err != nil {
			return removed, fmt.Errorf("checking %q against the record: %w", cleanup.OneLine(p), err)
		} else if why != "" {
			return removed, fmt.Errorf("the carried path %q is left: %s", cleanup.OneLine(p), why)
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

// runGitZ runs a read in the root and splits its NUL-separated output.
func runGitZ(rg cleanup.Git, args ...string) ([]string, error) {
	o, err := runGit(rg, args...)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, cleanup.OneLine(o.Stderr))
	}
	out := strings.Split(o.Stdout, "\x00")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out, nil
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

// gitWrite runs a write in the root; a timeout, a kill, a git that could not
// run or a non-zero exit is an error naming git's text.
func gitWrite(rg cleanup.Git, args ...string) error {
	o, err := runGit(rg, args...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("git did not finish (timeout %s): %w", rg.Timeout, err)
		}
		return fmt.Errorf("git did not finish or could not run: %w", err)
	}
	if o.Code != 0 {
		return fmt.Errorf("git exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}
	return nil
}
