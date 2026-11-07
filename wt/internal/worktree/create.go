package worktree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// nameKind is what a typed -W name means.
type nameKind int

const (
	nameReuseWorktree  nameKind = iota // a worktree already holds the branch (or the path)
	nameExistingBranch                 // a local branch with no worktree
	nameTrackRemote                    // a remote-tracking ref to track
	nameCreate                         // nothing matches: a new branch
)

// nameClass is what classifyName decided about a typed name.
type nameClass struct {
	kind  nameKind
	path  string // the worktree to reuse or to add (not for nameTrackRemote)
	short string // nameTrackRemote: the local branch to create and track
}

// classifyName decides what name means in dir, given the registered worktree
// entries. It is the one decision order behind EnsureForName and WouldCreate,
// so a question about creating is asked about exactly the case that creates.
// path is the worktree to reuse (nameReuseWorktree) or to add (the others,
// except nameTrackRemote, whose path addTrackingWorktree derives from short).
// A name that cannot be used (empty, default-branch form, path collision, too
// long) is an error.
func classifyName(dir, name string, entries []Entry) (nameClass, error) {
	if name == "" {
		return nameClass{}, fmt.Errorf("worktree name cannot be empty")
	}

	// Already checked out in a worktree? Use it.
	for _, e := range entries {
		if e.Branch == name && e.Path != "" {
			return nameClass{kind: nameReuseWorktree, path: e.Path}, nil
		}
	}

	// The default branch must never be a linked worktree, whether typed as
	// the bare name or as a remote-tracking form (origin/<default>,
	// <remote>/HEAD, ...).
	if err := refuseDefaultBranch(dir, name); err != nil {
		return nameClass{}, err
	}

	// A remote-tracking name (origin/feature) resolves like the picker does:
	// reuse a worktree already on the local branch, else create one tracking
	// the remote. remoteRefParts resolves the ref file, which a
	// case-insensitive volume matches in any case; only an exact ref is
	// tracked, by the same rule as the local branch below — tracking a
	// case-only variant would check out the other branch under the wrong
	// name (and for a default-branch form like origin/Main put the default
	// branch in a linked worktree).
	if _, short, ok := remoteRefParts(dir, name); ok {
		exact, err := refExactly(dir, "refs/remotes/"+name)
		if err != nil {
			return nameClass{}, err
		}
		if !exact {
			return nameClass{}, caseVariantError(name)
		}
		for _, e := range entries {
			if e.Branch == short && e.Path != "" {
				return nameClass{kind: nameReuseWorktree, path: e.Path}, nil
			}
		}
		return nameClass{kind: nameTrackRemote, short: short}, nil
	}

	path, err := worktreePath(dir, name)
	if err != nil {
		return nameClass{}, err
	}
	// Idempotent: if the path is already a registered worktree for the same
	// branch, reuse it. If it's registered for a DIFFERENT branch — a name
	// that flattens to another's (feature/x and feature-x share
	// .worktrees/feature-x), or a last-component worktree left by an older
	// wt — error rather than silently returning the wrong worktree. The
	// collision error is pathCollision's, the one "already in use" message
	// for -W and the picker.
	if err := pathCollision(entries, path, name); err != nil {
		return nameClass{}, err
	}
	if _, registered := branchAtPath(entries, path); registered {
		return nameClass{kind: nameReuseWorktree, path: path}, nil
	}

	if refExists(dir, "refs/heads/"+name) {
		// refExists resolves the ref file, which a case-insensitive volume
		// matches in any case. Only an exact branch is reused; a case-only
		// variant would check out the other branch under the wrong name.
		exact, err := refExactly(dir, "refs/heads/"+name)
		if err != nil {
			return nameClass{}, err
		}
		if !exact {
			return nameClass{}, caseVariantError(name)
		}
		return nameClass{kind: nameExistingBranch, path: path}, nil
	}
	// Last, so a name git would refuse is an error before anyone is asked to
	// create it (the question and git's own failure must agree).
	if err := checkBranchName(dir, name); err != nil {
		return nameClass{}, err
	}
	if err := refNamespaceFree(dir, name); err != nil {
		return nameClass{}, err
	}
	return nameClass{kind: nameCreate, path: path}, nil
}

// caseVariantError is the one refusal for a name that resolves an existing ref
// only through the case-insensitive ref-file lookup: using it would check out
// the other branch under the wrong name.
func caseVariantError(name string) error {
	return fmt.Errorf("branch %q differs only in case from an existing branch; use that branch's exact name", name)
}

// refExactly reports whether the fully-qualified ref exists with exactly that
// spelling, comparing the names git lists rather than resolving a ref file (a
// case-insensitive volume matches the file in any case).
func refExactly(dir, ref string) (bool, error) {
	out, err := runGit(dir, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return false, fmt.Errorf("git for-each-ref: %w", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if l == ref {
			return true, nil
		}
	}
	return false, nil
}

// refNamespaceFree errors when git could not create refs/heads/<name> because
// a local branch already sits on its path: a leaf branch above it (feature
// blocks feature/x) or a branch nested under it (feature/x blocks feature).
// Git nests refs as directories and refuses both at the ref lock — after the
// create question has already been asked — so it is refused here instead.
// With core.ignorecase (git init writes it true on a case-insensitive
// volume) the comparison folds case, so feature blocks Feature/x exactly as
// it blocks feature/x (git's mkdir of refs/heads/Feature collides with the
// refs/heads/feature file); unset or false — a case-sensitive volume —
// compares exactly, and Feature/x is its own name there.
func refNamespaceFree(dir, name string) error {
	out, err := runGit(dir, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		return fmt.Errorf("git for-each-ref: %w", err)
	}
	prefix := "refs/heads/" + name
	fold := gitIgnoreCase(dir)
	for _, l := range strings.Split(string(out), "\n") {
		ref := strings.TrimSpace(l)
		if ref == "" {
			continue
		}
		r, p := ref, prefix
		if fold {
			r, p = strings.ToLower(r), strings.ToLower(p)
		}
		if strings.HasPrefix(r, p+"/") || strings.HasPrefix(p, r+"/") {
			return fmt.Errorf("cannot create branch %q: %q is in the way (git nests branches as directories, so one name cannot sit under the other)",
				name, strings.TrimPrefix(ref, "refs/heads/"))
		}
	}
	return nil
}

// gitIgnoreCase reports whether git compares ref names case-insensitively in
// dir (core.ignorecase, normalized by --bool; git init writes it true when
// the filesystem is case-insensitive).
func gitIgnoreCase(dir string) bool {
	out, err := runGit(dir, "config", "--bool", "--get", "core.ignorecase")
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// checkBranchName asks git whether name is a usable branch name, returning
// git's own message when it is not.
func checkBranchName(dir, name string) error {
	if _, err := runGit(dir, "check-ref-format", "--branch", name); err != nil {
		msg := err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.TrimSpace(string(ee.Stderr)) != "" {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return fmt.Errorf("invalid branch name %q: %s", name, msg)
	}
	return nil
}

// EnsureForName returns the worktree path for branch name, creating it if needed.
// dir is the repo root. If the branch is already checked out somewhere, that
// path is returned (idempotent).
func EnsureForName(dir, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("worktree name cannot be empty")
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return "", err
	}
	cls, err := classifyName(dir, name, entries)
	if err != nil {
		return "", err
	}
	switch cls.kind {
	case nameTrackRemote:
		return addTrackingWorktree(dir, name, cls.short)
	case nameExistingBranch:
		if _, err := runGit(dir, "worktree", "add", cls.path, name); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
	case nameCreate:
		if _, err := runGit(dir, "worktree", "add", "-b", name, cls.path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
	}
	return cls.path, nil
}

// WouldCreate reports whether EnsureForName(dir, name) would create a new
// branch (git worktree add -b for a branch that does not exist), and how that
// branch would start — the phrase the create question joins after "create
// it": "from <current branch>", "from detached HEAD <short sha>", or "as an
// empty orphan branch with no shared history (this repository has no commits
// yet)" on an unborn HEAD, where git infers --orphan rather than a base. It
// creates nothing. A name EnsureForName would reject comes back as that same
// error, so callers can refuse before asking anything.
func WouldCreate(dir, name string) (creates bool, start string, err error) {
	if name == "" {
		return false, "", fmt.Errorf("worktree name cannot be empty")
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return false, "", err
	}
	cls, err := classifyName(dir, name, entries)
	if err != nil || cls.kind != nameCreate {
		return false, "", err
	}
	// An unborn HEAD (a fresh `git init`) is not a base: git prints "No
	// possible source branch, inferring --orphan" and the branch shares no
	// history, so say that rather than name the unborn branch it would not
	// use.
	if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return true, "as an empty orphan branch with no shared history (this repository has no commits yet)", nil
	}
	if b := CurrentBranchAt(dir); b != "" {
		return true, "from " + b, nil
	}
	out, err := runGit(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return false, "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return true, "from detached HEAD " + strings.TrimSpace(string(out)), nil
}

// maxDirNameBytes is the filesystem's per-entry name limit (NAME_MAX on macOS
// and Linux). Git's .git/worktrees/<basename> entry shares it.
const maxDirNameBytes = 255

// worktreeDirName derives a worktree directory name from a full branch name.
// Every path-hostile character — path separators on any platform, colon, and
// ASCII control characters — becomes "-", so feature/x and bug/x land in
// distinct directories (.worktrees/feature-x, .worktrees/bug-x) instead of
// racing for the last path component. Everything else is kept (invalid
// UTF-8 bytes re-encode as U+FFFD). A derived "." or ".." is an error:
// filepath.Join cleans ".." up to the repo root and "." to the .worktrees
// container — the wrong path either way. A derived name over maxDirNameBytes
// is an error too: git nests a ref's components as directories, so a long
// branch can be valid while its flattened name, one directory entry, is not.
func worktreeDirName(name string) (string, error) {
	dirName := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		}
		if r < 0x20 || r == 0x7f {
			return '-'
		}
		return r
	}, name)
	if dirName == "." || dirName == ".." {
		return "", fmt.Errorf("worktree name must not be %q", dirName)
	}
	if len(dirName) > maxDirNameBytes {
		return "", fmt.Errorf("branch name %q is too long for a worktree directory (%d bytes after flattening, limit %d): shorten the branch name",
			name, len(dirName), maxDirNameBytes)
	}
	return dirName, nil
}

// worktreePath is where branch name's linked worktree lives: the derived
// directory name under <dir>/.worktrees. Every creation path goes through it.
func worktreePath(dir, name string) (string, error) {
	dirName, err := worktreeDirName(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".worktrees", dirName), nil
}

// pathCollision errors when path is a registered worktree of a DIFFERENT
// branch than name — the one "already in use" message for -W and the picker.
// Flattening makes it easy to hit (feature/x and feature-x share
// .worktrees/feature-x), and git's own "already exists" never names the
// branch holding the path. A detached worktree or the same branch is not a
// collision here; the caller decides what reusing it means.
func pathCollision(entries []Entry, path, name string) error {
	if branch, ok := branchAtPath(entries, path); ok && branch != "" && branch != name {
		return fmt.Errorf("worktree path %s is already in use by branch %q", path, branch)
	}
	return nil
}

// newWorktreePath is worktreePath for the picker paths, which create a
// worktree without the -W reuse loop: it also lists the registered worktrees
// and refuses a path another branch holds.
func newWorktreePath(dir, name string) (string, error) {
	path, err := worktreePath(dir, name)
	if err != nil {
		return "", err
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return "", err
	}
	if err := pathCollision(entries, path, name); err != nil {
		return "", err
	}
	return path, nil
}

// refExists reports whether the fully-qualified ref (e.g. refs/heads/main)
// exists in dir.
func refExists(dir, ref string) bool {
	_, err := runGit(dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// branchAtPath returns the branch checked out at path among the already-listed
// worktree entries, if path is a registered worktree. The bool is true when
// path is registered (even when detached, in which case branch is ""). Paths
// are compared through symlinks so the lookup matches git's resolved porcelain
// output (macOS /var → /private/var); the old byte-compare failed on symlinked
// repos. Reusing the entries from listWorktrees avoids a second
// `git worktree list` subprocess on the EnsureForName path.
func branchAtPath(entries []Entry, path string) (string, bool) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	// The same-file test catches what a string compare cannot: on a
	// case-insensitive volume .worktrees/Feature-x and .worktrees/feature-x are
	// one directory, but EvalSymlinks keeps the case it was given. The
	// filesystem, not a case-folding guess, decides, so a case-sensitive volume
	// keeps its two directories distinct. It needs the path to exist.
	info, _ := os.Stat(path)
	for _, e := range entries {
		ep, err := filepath.EvalSymlinks(e.Path)
		if err != nil {
			ep = e.Path
		}
		same := ep == path
		if !same && info != nil {
			if ei, err := os.Stat(ep); err == nil {
				same = os.SameFile(info, ei)
			}
		}
		if same {
			if e.Branch == "(detached)" {
				return "", true
			}
			return e.Branch, true
		}
	}
	return "", false
}

// EnsureForBranch creates (or reuses) a worktree for a picked branch.
// Handles local, remote-tracking, and new branches.
func EnsureForBranch(dir, branch string) (string, error) {
	if err := refuseDefaultBranch(dir, branch); err != nil {
		return "", err
	}
	switch {
	case refExists(dir, "refs/heads/"+branch):
		// Local branch (possibly with slashes) — the directory derives from
		// the full name.
		path, err := newWorktreePath(dir, branch)
		if err != nil {
			return "", err
		}
		if _, err := runGit(dir, "worktree", "add", path, branch); err != nil {
			return "", fmt.Errorf("git worktree add: %w", err)
		}
		return path, nil

	case refExists(dir, "refs/remotes/"+branch):
		// Remote-tracking branch — create a local branch tracking it.
		short := branch[strings.IndexByte(branch, '/')+1:]
		return addTrackingWorktree(dir, branch, short)

	default:
		// Brand-new branch.
		path, err := newWorktreePath(dir, branch)
		if err != nil {
			return "", err
		}
		if _, err := runGit(dir, "worktree", "add", "-b", branch, path); err != nil {
			return "", fmt.Errorf("git worktree add -b: %w", err)
		}
		return path, nil
	}
}

// addTrackingWorktree creates a worktree on a new local branch short that
// tracks the remote-tracking ref remoteRef.
func addTrackingWorktree(dir, remoteRef, short string) (string, error) {
	path, err := newWorktreePath(dir, short)
	if err != nil {
		return "", err
	}
	if refExists(dir, "refs/heads/"+short) {
		return "", fmt.Errorf("local branch %q already exists — cannot create from remote %q", short, remoteRef)
	}
	if _, err := runGit(dir, "worktree", "add", "-b", short, path, remoteRef); err != nil {
		return "", fmt.Errorf("git worktree add -b %s %s: %w", short, remoteRef, err)
	}
	return path, nil
}

// refuseDefaultBranch is the one default-branch guard for both creation
// paths. It errors when name selects the repo default branch, naming it as the
// user typed it.
func refuseDefaultBranch(dir, name string) error {
	if db := DefaultBranch(dir); db != "" && isDefaultBranchSelection(name, db, dir) {
		return fmt.Errorf("refusing to create a worktree for the default branch %q; check it out in the primary checkout instead", name)
	}
	return nil
}

// remoteRefParts splits name into (remote, branch) when it is a real
// remote-tracking ref: refs/remotes/<name> exists, no local branch of that
// exact name exists, and the prefix is a configured remote (longest wins, so
// remotes with slashes work). A local branch such as feature/main is never
// treated as remote.
func remoteRefParts(dir, name string) (remote, short string, ok bool) {
	if refExists(dir, "refs/heads/"+name) || !refExists(dir, "refs/remotes/"+name) {
		return "", "", false
	}
	out, err := runGit(dir, "remote")
	if err != nil {
		return "", "", false
	}
	for _, r := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, r+"/") && len(r) > len(remote) {
			remote = r
		}
	}
	if remote == "" {
		return "", "", false
	}
	return remote, name[len(remote)+1:], true
}

// isDefaultBranchSelection reports whether picking branch would create (or
// reuse) a worktree on the repo default branch db: the bare local default, or
// a real remote-tracking ref (see remoteRefParts) whose branch is db or HEAD
// (<remote>/HEAD is a symbolic alias for the default).
func isDefaultBranchSelection(branch, db, dir string) bool {
	if branch == db {
		return true
	}
	_, short, ok := remoteRefParts(dir, branch)
	return ok && (short == db || short == "HEAD")
}
