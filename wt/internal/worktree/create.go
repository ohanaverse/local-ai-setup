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
// so a creation is announced for exactly the case that creates (and a reuse
// announces nothing).
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

	// A remote-tracking name (origin/feature) resolves like the picker does:
	// reuse a worktree already on the local branch, else create one tracking
	// the remote. remoteRefParts resolves the ref file, which a
	// case-insensitive volume matches in any case; only an exact ref is
	// tracked, by the same rule as the local branch below — tracking a
	// case-only variant would check out the other branch under the wrong
	// name.
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
	// Last, so a name git would refuse is an error before the creation is
	// announced (the announcement and git's own failure must agree).
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
// creation has already been announced — so it is refused here instead.
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

// Plan is one classification of a typed -W name, resolved once: the creation
// line reads it (Creates, Branch, Tracks, Path) and the creation consumes the
// same object (Ensure), so what a user is told and the branch that is made
// come from one set of git probes instead of two.
type Plan struct {
	dir, name string
	cls       nameClass
	start     string // the phrase Start returns; empty unless cls creates
}

// Resolve classifies name against the repo at dir once and returns the plan.
// It creates nothing. A name Ensure would reject comes back as that same
// error, so callers can refuse before asking anything.
func Resolve(dir, name string) (*Plan, error) {
	if name == "" {
		return nil, fmt.Errorf("worktree name cannot be empty")
	}
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return nil, err
	}
	cls, err := classifyName(dir, name, entries)
	if err != nil {
		return nil, err
	}
	p := &Plan{dir: dir, name: name, cls: cls}
	if cls.kind == nameCreate {
		if p.start, err = startPhrase(dir); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Creates reports whether Ensure will create a new branch (git worktree add
// -b for a branch that does not exist), as opposed to reusing a worktree or
// checking out an existing or remote-tracking branch.
func (p *Plan) Creates() bool { return p.cls.kind == nameCreate }

// Branch is the branch the planned worktree checks out: the typed name, or the
// local branch a remote-tracking name creates.
func (p *Plan) Branch() string {
	if p.cls.kind == nameTrackRemote {
		return p.cls.short
	}
	return p.name
}

// Tracks is the remote-tracking ref the new local branch tracks, "" when the
// plan tracks nothing.
func (p *Plan) Tracks() string {
	if p.cls.kind == nameTrackRemote {
		return p.name
	}
	return ""
}

// Path is where the planned worktree is (reused) or goes (created); "" when a
// tracking plan's path cannot be derived (Ensure reports why).
func (p *Plan) Path() string {
	if p.cls.kind == nameTrackRemote {
		path, _ := newWorktreePath(p.dir, p.cls.short)
		return path
	}
	return p.cls.path
}

// Start is how the branch Ensure would create starts: "from <current
// branch>", "from detached HEAD <short sha>", or "as an empty orphan branch
// with no shared history (this repository has no commits yet)" on an unborn
// HEAD, where git infers --orphan rather than a base. Empty when the plan
// does not create. Ensure's change check reports it when the repository
// moved between the classification and the creation.
func (p *Plan) Start() string { return p.start }

// Ensure returns the worktree path for the planned name, creating it if
// needed. A reused worktree is returned as is (idempotent).
//
// The classification is not repeated, but a creation re-checks the two things
// that could have moved since the plan was read: the base (a changed Start is
// an error naming both phrases, never a branch made from a base other than
// the one the plan read) and the name itself (git refuses `worktree add -b`
// for a branch another wt made meanwhile, so a planned creation is never
// silently turned into a reuse of someone else's branch).
func (p *Plan) Ensure() (string, error) {
	switch p.cls.kind {
	case nameTrackRemote:
		path, _, err := addTrackingWorktree(p.dir, p.name, p.cls.short)
		return path, err
	case nameExistingBranch:
		if _, err := runGit(p.dir, "worktree", "add", p.cls.path, p.name); err != nil {
			return "", gitError("git worktree add", err)
		}
	case nameCreate:
		now, err := startPhrase(p.dir)
		if err != nil {
			return "", err
		}
		if now != p.start {
			return "", fmt.Errorf("the repository changed while asking: %q was to be created %s, it would now start %s; run the command again", p.name, p.start, now)
		}
		if _, err := runGit(p.dir, "worktree", "add", "-b", p.name, p.cls.path); err != nil {
			return "", gitError("git worktree add -b", err)
		}
	}
	return p.cls.path, nil
}

// startPhrase says how a branch created now would start (see Plan.Start).
func startPhrase(dir string) (string, error) {
	// An unborn HEAD (a fresh `git init`) is not a base: git prints "No
	// possible source branch, inferring --orphan" and the branch shares no
	// history, so say that rather than name the unborn branch it would not
	// use.
	if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return "as an empty orphan branch with no shared history (this repository has no commits yet)", nil
	}
	if b := CurrentBranchAt(dir); b != "" {
		return "from " + b, nil
	}
	out, err := runGit(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return "from detached HEAD " + strings.TrimSpace(string(out)), nil
}

// EnsureForName returns the worktree path for branch name, creating it if needed.
// dir is the repo root. If the branch is already checked out somewhere, that
// path is returned (idempotent). Callers that announce a creation use Resolve
// and Plan.Ensure so the announcement and the creation share one
// classification.
func EnsureForName(dir, name string) (string, error) {
	p, err := Resolve(dir, name)
	if err != nil {
		return "", err
	}
	return p.Ensure()
}

// WouldCreate reports whether EnsureForName(dir, name) would create a new
// branch, and how it would start (see Plan.Start). It creates nothing.
// Resolve is the form that also carries the classification into the creation.
func WouldCreate(dir, name string) (creates bool, start string, err error) {
	p, err := Resolve(dir, name)
	if err != nil {
		return false, "", err
	}
	return p.Creates(), p.Start(), nil
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

// Created is what an Ensure made (or reused): the branch the worktree checks
// out, the remote-tracking ref a new local branch tracks ("" when it tracks
// nothing), and whether the branch itself is new (git worktree add -b). A
// reused worktree reports NewBranch false and Tracks "".
type Created struct {
	Branch    string
	Tracks    string
	NewBranch bool
}

// EnsureForBranch creates (or reuses) a worktree for a picked branch and
// reports what it made (Created). Handles local, remote-tracking, and new
// branches. A branch whose derived path already holds it is reused as is
// (idempotent); a branch checked out at a different path is left to git, whose
// own "already used by worktree" error — surfaced verbatim — stays the message
// the user sees.
func EnsureForBranch(dir, branch string) (string, Created, error) {
	switch {
	case refExists(dir, "refs/heads/"+branch):
		// Local branch (possibly with slashes) — the directory derives from
		// the full name.
		path, err := newWorktreePath(dir, branch)
		if err != nil {
			return "", Created{}, err
		}
		if made, ok := reusedWorktree(dir, path, branch); ok {
			return path, made, nil
		}
		if _, err := runGit(dir, "worktree", "add", path, branch); err != nil {
			return "", Created{}, gitError("git worktree add", err)
		}
		return path, Created{Branch: branch}, nil

	case refExists(dir, "refs/remotes/"+branch):
		// Remote-tracking branch — create a local branch tracking it.
		short := branch[strings.IndexByte(branch, '/')+1:]
		return addTrackingWorktree(dir, branch, short)

	default:
		// Brand-new branch.
		path, err := newWorktreePath(dir, branch)
		if err != nil {
			return "", Created{}, err
		}
		if _, err := runGit(dir, "worktree", "add", "-b", branch, path); err != nil {
			return "", Created{}, gitError("git worktree add -b", err)
		}
		return path, Created{Branch: branch, NewBranch: true}, nil
	}
}

// addTrackingWorktree creates (or reuses) a worktree on a new local branch
// short that tracks the remote-tracking ref remoteRef, and reports what it
// made (Created).
func addTrackingWorktree(dir, remoteRef, short string) (string, Created, error) {
	path, err := newWorktreePath(dir, short)
	if err != nil {
		return "", Created{}, err
	}
	if made, ok := reusedWorktree(dir, path, short); ok {
		return path, made, nil
	}
	if refExists(dir, "refs/heads/"+short) {
		return "", Created{}, fmt.Errorf("local branch %q already exists — cannot create from remote %q", short, remoteRef)
	}
	if _, err := runGit(dir, "worktree", "add", "-b", short, path, remoteRef); err != nil {
		return "", Created{}, gitError(fmt.Sprintf("git worktree add -b %s %s", short, remoteRef), err)
	}
	return path, Created{Branch: short, Tracks: remoteRef, NewBranch: true}, nil
}

// reusedWorktree returns the Created for a worktree already registered at
// path holding exactly branch — a second Ensure for the same name reuses it,
// idempotent. A worktree at path on another branch is a pathCollision the
// caller checks first; a branch checked out at a different path is not a
// reuse here and reaches git, whose own error stays.
func reusedWorktree(dir, path, branch string) (Created, bool) {
	entries, err := listWorktrees(dir, dir)
	if err != nil {
		return Created{}, false
	}
	if b, registered := branchAtPath(entries, path); registered && b == branch {
		return Created{Branch: branch}, true
	}
	return Created{}, false
}

// gitError surfaces a failed git command's own message: what the user sees is
// what git printed (its captured stderr), never a friendlier replacement for
// it. With no stderr the plain error is used.
func gitError(op string, err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
			return fmt.Errorf("%s: %s", op, msg)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
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
