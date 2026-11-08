package cleanup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a real repository: a bare "origin", a clone with main pushed and
// origin/HEAD set. Each test builds its own.
type fixture struct {
	t      *testing.T
	Origin string
	Dir    string
	n      int
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, Origin: filepath.Join(root, "origin.git"), Dir: filepath.Join(root, "repo")}
	if b, err := exec.Command("git", "init", "--bare", "-b", "main", f.Origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "clone", f.Origin, f.Dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
	f.git("config", "user.email", "t@example.com")
	f.git("config", "user.name", "T")
	f.git("config", "commit.gpgsign", "false")
	f.git("checkout", "-B", "main")
	f.Commit("README", "init", "init")
	f.git("push", "-u", "origin", "main")
	f.git("remote", "set-head", "origin", "main")
	return f
}

func (f *fixture) git(args ...string) string { f.t.Helper(); return gitIn(f.t, f.Dir, args...) }

// Commit writes file, commits it on the current branch and returns the sha.
func (f *fixture) Commit(file, content, msg string) string {
	f.t.Helper()
	p := filepath.Join(f.Dir, file)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", file)
	f.git("commit", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

// Branch creates name at HEAD without switching.
func (f *fixture) Branch(name string) { f.t.Helper(); f.git("branch", name) }

// Checkout switches the primary checkout to name.
func (f *fixture) Checkout(name string) { f.t.Helper(); f.git("checkout", name) }

// Push pushes ref to origin, setting upstream.
func (f *fixture) Push(ref string) { f.t.Helper(); f.git("push", "-u", "origin", ref) }

// Squash squash-merges branch into main as one new commit and pushes main.
func (f *fixture) Squash(branch string) {
	f.t.Helper()
	f.git("checkout", "main")
	f.git("merge", "--squash", branch)
	f.git("commit", "-m", "squash "+branch)
	f.git("push", "origin", "main")
}

// Revert reverts sha on the current branch.
func (f *fixture) Revert(sha string) { f.t.Helper(); f.git("revert", "--no-edit", sha) }

// Worktree adds a linked worktree on a new branch and returns its path.
func (f *fixture) Worktree(branch string) string {
	f.t.Helper()
	f.n++
	p := filepath.Join(filepath.Dir(f.Dir), "wt-"+strings.ReplaceAll(branch, "/", "-"))
	f.git("worktree", "add", "-b", branch, p)
	return p
}

// Dirty leaves an uncommitted untracked file at path (relative to the repo).
func (f *fixture) Dirty(path string) { f.t.Helper(); f.DirtyIn(f.Dir, path) }

// DirtyIn leaves an untracked file at path relative to dir, a checkout of
// this repo (the primary or a linked worktree).
func (f *fixture) DirtyIn(dir, path string) {
	f.t.Helper()
	p := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("dirty"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// Ignored creates a git-ignored file at path, ignored through info/exclude.
func (f *fixture) Ignored(path string) { f.t.Helper(); f.IgnoredIn(f.Dir, path) }

// IgnoredIn creates a git-ignored file at path relative to dir. The pattern
// goes to info/exclude, which every worktree of the repo shares.
func (f *fixture) IgnoredIn(dir, path string) {
	f.t.Helper()
	ex := gitIn(f.t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err := os.MkdirAll(filepath.Dir(ex), 0o755); err != nil {
		f.t.Fatal(err)
	}
	fh, err := os.OpenFile(ex, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(path + "\n"); err != nil {
		f.t.Fatal(err)
	}
	f.DirtyIn(dir, path)
}

// NestedWorktree adds a linked worktree on a new branch inside parent (a
// worktree directory) and returns its path.
func (f *fixture) NestedWorktree(parent, branch string) string {
	f.t.Helper()
	p := filepath.Join(parent, "nested-"+strings.ReplaceAll(branch, "/", "-"))
	f.git("worktree", "add", "-b", branch, p)
	return p
}

// StoppedRebase leaves the worktree at dir (on its own branch) in a rebase
// stopped on a conflict: dir and main both change file "conflict".
func (f *fixture) StoppedRebase(dir string) {
	f.t.Helper()
	f.conflictPair(dir)
	f.mustFail(dir, "", "rebase", "main")
}

// conflictPair commits file "conflict" as "ours" in dir and as "theirs" on
// main (in the primary checkout), and returns main's commit.
func (f *fixture) conflictPair(dir string) string {
	f.t.Helper()
	commitIn(f.t, dir, "conflict", "ours", "ours")
	return f.Commit("conflict", "theirs", "theirs")
}

// mustFail runs git in dir with stdin input and fails the test if git
// succeeds: the caller wants an operation stopped on a conflict.
func (f *fixture) mustFail(dir, input string, args ...string) {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	if b, err := cmd.CombinedOutput(); err == nil {
		f.t.Fatalf("git %s: expected a conflict, got none\n%s", strings.Join(args, " "), b)
	}
}

// StoppedMerge leaves dir in a merge of main stopped on a conflict.
func (f *fixture) StoppedMerge(dir string) {
	f.t.Helper()
	f.conflictPair(dir)
	f.mustFail(dir, "", "merge", "--no-edit", "main")
}

// StoppedCherryPick leaves dir in a cherry-pick of one main commit stopped on
// a conflict.
func (f *fixture) StoppedCherryPick(dir string) {
	f.t.Helper()
	f.mustFail(dir, "", "cherry-pick", f.conflictPair(dir))
}

// ResumedCherryPick runs a three-commit cherry-pick in dir that stops on the
// second, then resolves and commits it: CHERRY_PICK_HEAD is gone, but the
// sequencer still holds the third commit.
func (f *fixture) ResumedCherryPick(dir string) {
	f.t.Helper()
	a := f.Commit("pick-a", "a", "pick a")
	b := f.conflictPair(dir)
	c := f.Commit("pick-c", "c", "pick c")
	f.mustFail(dir, "", "cherry-pick", a, b, c)
	if err := os.WriteFile(filepath.Join(dir, "conflict"), []byte("resolved"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	gitIn(f.t, dir, "add", "conflict")
	gitIn(f.t, dir, "commit", "--no-edit")
}

// StoppedRevert leaves dir in a revert stopped on a conflict: the reverted
// commit created a file that a later commit changed.
func (f *fixture) StoppedRevert(dir string) {
	f.t.Helper()
	x := commitIn(f.t, dir, "rev", "1", "rev 1")
	commitIn(f.t, dir, "rev", "2", "rev 2")
	f.mustFail(dir, "", "revert", "--no-edit", x)
}

// StartedBisect starts a bisect in dir.
func (f *fixture) StartedBisect(dir string) {
	f.t.Helper()
	gitIn(f.t, dir, "bisect", "start")
}

// StoppedAm leaves dir in `git am` stopped on a patch that does not apply.
func (f *fixture) StoppedAm(dir string) {
	f.t.Helper()
	sha := f.conflictPair(dir)
	patch := f.git("format-patch", "-1", "--stdout", sha)
	f.mustFail(dir, patch+"\n", "am")
}

// DetachOrphan detaches HEAD in dir and commits there, leaving HEAD on a
// commit no branch or remote ref reaches. It returns that commit.
func (f *fixture) DetachOrphan(dir string) string {
	f.t.Helper()
	gitIn(f.t, dir, "checkout", "--detach")
	return commitIn(f.t, dir, "orphan", "orphan", "orphan")
}

// commitIn writes file in dir, commits it on dir's current HEAD and returns
// the sha.
func commitIn(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", file)
	gitIn(t, dir, "commit", "-m", msg)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// Merge merges branch into the current branch with a merge commit (--no-ff).
func (f *fixture) Merge(branch string) { f.t.Helper(); f.git("merge", "--no-ff", "--no-edit", branch) }

// FastForward fast-forwards the current branch to branch.
func (f *fixture) FastForward(branch string) { f.t.Helper(); f.git("merge", "--ff-only", branch) }

// CherryPick re-applies each sha, in order, on the current branch: a 1:1
// rebase-merge as a forge does it.
func (f *fixture) CherryPick(shas ...string) {
	f.t.Helper()
	for _, s := range shas {
		f.git("cherry-pick", s)
	}
}

// MergeResolving merges branch into the current branch, expecting a conflict
// on file, and resolves it with content that neither side has.
func (f *fixture) MergeResolving(branch, file, content string) string {
	f.t.Helper()
	cmd := exec.Command("git", "-C", f.Dir, "merge", "--no-ff", "--no-edit", branch)
	if b, err := cmd.CombinedOutput(); err == nil {
		f.t.Fatalf("merge %s: expected a conflict, got none\n%s", branch, b)
	}
	if err := os.WriteFile(filepath.Join(f.Dir, file), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", file)
	f.git("commit", "--no-edit")
	return f.git("rev-parse", "HEAD")
}

// recordOpts overrides what addRecord writes; an empty field takes the value a
// real rescue of the source would write, so a test sets only the field it
// wants to make wrong.
type recordOpts struct {
	Version, SourcePath, SourceRef, SourceHead, DestBranch, DestPath string
	Omit                                                             []string // trailer keys left out
	Extra                                                            []string // trailer lines appended as given
	Message                                                          *string  // replaces the whole message
	Object                                                           string   // when set, the ref points at this object instead
	// Snapshot makes I the source's index tree and W its working-tree tree
	// (index plus `add -A`), as a real rescue of a dirty source writes them,
	// computed here with git alone (a temporary index copy, add -A, write-tree)
	// so the oracle shares no code with the code under test. False keeps the
	// HEAD tree in both commits.
	Snapshot bool
}

// snapshotTrees returns the source's index tree and the tree add -A would
// make of its working tree, both through a throwaway copy of the index so the
// source's own index is untouched.
func snapshotTrees(t *testing.T, srcPath string) (itree, wtree string) {
	t.Helper()
	idx := gitIn(t, srcPath, "rev-parse", "--path-format=absolute", "--git-path", "index")
	b, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "index")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", srcPath}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+tmp)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	itree = run("write-tree")
	run("add", "-A")
	return itree, run("write-tree")
}

// addRecord builds by hand the chain a rescue writes (I: a tree with parent
// the source HEAD, W: a tree with parent I, six trailers on both) and points
// refs/wt/rescued/<16 hex of W> at W. It returns the ref name.
func (f *fixture) addRecord(t *testing.T, srcPath string, o recordOpts) string {
	t.Helper()
	head := gitIn(t, srcPath, "rev-parse", "HEAD")
	def := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	ref := "detached"
	if b := gitIn(t, srcPath, "branch", "--show-current"); b != "" {
		ref = "refs/heads/" + b
	}
	vals := [][2]string{
		{"Wt-Version", def(o.Version, "1")},
		{"Wt-Source-Path", def(o.SourcePath, ResolvePath(srcPath))},
		{"Wt-Source-Ref", def(o.SourceRef, ref)},
		{"Wt-Source-Head", def(o.SourceHead, head)},
		{"Wt-Dest-Branch", def(o.DestBranch, "rescue/x")},
		{"Wt-Dest-Path", def(o.DestPath, "/tmp/dest")},
	}
	msg := "wt rescue record\n\n"
	for _, kv := range vals {
		skip := false
		for _, k := range o.Omit {
			skip = skip || k == kv[0]
		}
		if !skip {
			msg += kv[0] + ": " + kv[1] + "\n"
		}
	}
	for _, e := range o.Extra {
		msg += e + "\n"
	}
	if o.Message != nil {
		msg = *o.Message
	}
	itree := gitIn(t, srcPath, "rev-parse", "HEAD^{tree}")
	wtree := itree
	if o.Snapshot {
		itree, wtree = snapshotTrees(t, srcPath)
	}
	commit := func(tree, parent string) string {
		cmd := exec.Command("git", "-C", srcPath, "commit-tree", tree, "-p", parent, "-F", "-")
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@invalid")
		cmd.Stdin = strings.NewReader(msg)
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("commit-tree: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	w := commit(wtree, commit(itree, head))
	target := w
	if o.Object != "" {
		target = o.Object
	}
	name := fmt.Sprintf("refs/wt/rescued/%s", w[:16])
	gitIn(t, srcPath, "update-ref", name, target)
	return name
}
