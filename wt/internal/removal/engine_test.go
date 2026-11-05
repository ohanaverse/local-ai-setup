package removal

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// engine is an Engine over f's repository built from snapshot s (what the
// user's list came from) and inputs in, with the clock at later().
func (f *fixture) engine(s cleanup.Snapshot, in cleanup.GatherInputs, o Options) Engine {
	if o.Now == nil {
		o.Now = later
	}
	return Engine{G: cleanup.Git{Dir: f.Dir}, Prior: s, In: in, Classify: cleanup.Options{Now: later()}, Opts: o}
}

// reportWith is report with the caller's GatherInputs (another CWD, protected
// paths).
func (f *fixture) reportWith(in cleanup.GatherInputs) (cleanup.Snapshot, cleanup.Report) {
	f.t.Helper()
	s, err := cleanup.Gather(cleanup.Git{Dir: f.Dir}, in)
	if err != nil {
		f.t.Fatalf("Gather: %v", err)
	}
	return s, cleanup.Classify(s, cleanup.Options{Now: later()})
}

// exists reports whether p is on disk (Lstat), failing the test on any error
// other than not-exist.
func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return false
}

// registered reports whether git still lists a worktree at path.
func (f *fixture) registered(path string) bool {
	f.t.Helper()
	facts, err := cleanup.ListWorktrees(cleanup.Git{Dir: f.Dir}, f.Dir)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, w := range facts {
		if cleanup.ResolvePath(w.Path) == cleanup.ResolvePath(path) {
			return true
		}
	}
	return false
}

// one runs e on the single item it and returns its outcome.
func one(t *testing.T, e Engine, it Item) Outcome {
	t.Helper()
	out := e.Run([]Item{it}, nil)
	if len(out) != 1 {
		t.Fatalf("%d outcomes for one item", len(out))
	}
	return out[0]
}

// stubRunGit swaps the removal runner for the rest of the test.
func stubRunGit(t *testing.T, fn func(g cleanup.Git, args ...string) (cleanup.Out, error)) {
	t.Helper()
	old := runGit
	runGit = fn
	t.Cleanup(func() { runGit = old })
}

// recordRemoves records the argument vector of every removal git call, then
// runs it for real.
func recordRemoves(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	stubRunGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		calls = append(calls, append([]string(nil), args...))
		return g.Run(args...)
	})
	return &calls
}

// TestEngineRemovesProvenWorktree removes a squash-merged, clean worktree the
// user ticked as proven: the basic path must delete the directory and its
// registration, and the branch stage then deletes its proven branch.
func TestEngineRemovesProvenWorktree(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "topic.txt", "work", "topic work")
	f.Push("topic")
	f.SquashMerge("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Status != cleanup.StatusProven {
		t.Fatalf("precondition: status %s, want proven", row.Status)
	}
	o := one(t, f.engine(s, f.inputs(), Options{}), itemOf(row))
	if o.Worktree != WtRemoved {
		t.Fatalf("outcome %+v, want removed", o)
	}
	if exists(t, p) || f.registered(p) {
		t.Errorf("worktree %s still on disk (%v) or registered (%v)", p, exists(t, p), f.registered(p))
	}
	if o.Branch != BrDeleted {
		t.Errorf("branch outcome %s (%s), want deleted", o.Branch, o.Reason)
	}
}

// TestEngineSkipsItemThatChanged dirties a worktree after the list was made:
// the user ticked a proven row, and acting on it now would lose the new file.
// A status worse than the one the user saw is skipped_changed, untouched.
func TestEngineSkipsItemThatChanged(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if row.Status != cleanup.StatusProven {
		t.Fatalf("precondition: status %s, want proven", row.Status)
	}
	f.Dirty(p, "new.txt")
	o := one(t, f.engine(s, f.inputs(), Options{Force: true}), itemOf(row))
	if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "status changed") {
		t.Fatalf("outcome %+v, want skipped_changed naming the status change", o)
	}
	if !exists(t, filepath.Join(p, "new.txt")) {
		t.Error("the new file is gone")
	}
}

// TestEngineSkipsWhenTipMoved commits to a unique_work worktree after the
// list: its status is no worse, but the commit the user saw is not the one
// that would go. A moved tip is skipped_changed.
func TestEngineSkipsWhenTipMoved(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "a.txt", "a", "first")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	f.Commit(p, "b.txt", "b", "second")
	_, r2 := f.report()
	if now := f.row(r2, cleanup.KindWorktree, "topic").Status; now.WorseThan(row.Status) {
		t.Fatalf("precondition: status went %s -> %s; this case needs it no worse", row.Status, now)
	}
	o := one(t, f.engine(s, f.inputs(), Options{Force: true, DeleteUnmerged: true}), itemOf(row))
	if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "moved") {
		t.Fatalf("outcome %+v, want skipped_changed naming the moved tip", o)
	}
	if !exists(t, p) {
		t.Error("worktree removed")
	}
}

// blockerCase builds a fixture whose target worktree carries one hard
// blocker; it returns the inputs the list and engine use, the target's path
// and row name.
type blockerCase struct {
	code  string
	setup func(f *fixture) (in cleanup.GatherInputs, path, name string)
}

func blockerCases() []blockerCase {
	return []blockerCase{
		{cleanup.BlockPrimary, func(f *fixture) (cleanup.GatherInputs, string, string) {
			return f.inputs(), f.Dir, "main"
		}},
		{cleanup.BlockCurrent, func(f *fixture) (cleanup.GatherInputs, string, string) {
			p := f.Worktree("topic")
			in := f.inputs()
			in.CWD = p
			return in, p, "topic"
		}},
		{cleanup.BlockLocked, func(f *fixture) (cleanup.GatherInputs, string, string) {
			p := f.Worktree("topic")
			f.Lock(p)
			return f.inputs(), p, "topic"
		}},
		{cleanup.BlockNested, func(f *fixture) (cleanup.GatherInputs, string, string) {
			p := f.Worktree("topic")
			f.git("worktree", "add", "-b", "inner", filepath.Join(p, ".worktrees", "inner"))
			return f.inputs(), p, "topic"
		}},
		{cleanup.BlockOperation, func(f *fixture) (cleanup.GatherInputs, string, string) {
			p := f.Worktree("topic")
			mh := gitIn(f.t, p, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
			if err := os.WriteFile(mh, []byte(gitIn(f.t, p, "rev-parse", "HEAD")+"\n"), 0o644); err != nil {
				f.t.Fatal(err)
			}
			return f.inputs(), p, "topic"
		}},
		{cleanup.BlockProtectedArtifact, func(f *fixture) (cleanup.GatherInputs, string, string) {
			p := f.Worktree("topic")
			in := f.inputs()
			in.ProtectedPaths = []string{filepath.Join(p, "model.safetensors")}
			return in, p, "topic"
		}},
		{cleanup.BlockDefaultBranch, func(f *fixture) (cleanup.GatherInputs, string, string) {
			f.git("switch", "-c", "other")
			p := filepath.Join(f.Dir, ".worktrees", "main")
			f.git("worktree", "add", p, "main")
			return f.inputs(), p, "main"
		}},
	}
}

// runBlockerCases removes each blocked target with opts and requires a
// refusal naming the blocker, the worktree untouched.
func runBlockerCases(t *testing.T, opts Options) {
	for _, c := range blockerCases() {
		t.Run(c.code, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			in, p, name := c.setup(f)
			s, r := f.reportWith(in)
			row := f.row(r, cleanup.KindWorktree, name)
			// The most permissive thing a user could have ticked: proven.
			it := itemOf(row)
			it.Saw = cleanup.StatusProven
			o := one(t, f.engine(s, in, opts), it)
			if o.Worktree != WtRefused || !strings.Contains(o.Reason, c.code) {
				t.Fatalf("outcome %+v, want refused naming %s", o, c.code)
			}
			if o.Branch != BrNotAttempted {
				t.Errorf("branch outcome %s, want not_attempted", o.Branch)
			}
			if !exists(t, p) || !f.registered(p) {
				t.Errorf("worktree %s touched: on disk %v, registered %v", p, exists(t, p), f.registered(p))
			}
		})
	}
}

// TestEngineRefusesEveryHardBlocker runs every hard blocker through the
// engine: primary, current dir, locked, nested, operation in progress,
// protected artifact and a default-branch worktree must each be refused with
// its code, whatever status the user saw.
func TestEngineRefusesEveryHardBlocker(t *testing.T) { runBlockerCases(t, Options{}) }

// TestForceNeverOverridesHardBlockers repeats the blocker table with Force
// and DeleteUnmerged both set: those flags approve a loss (dirty files, an
// unmerged branch), never a hard blocker (plan call 4).
func TestForceNeverOverridesHardBlockers(t *testing.T) {
	runBlockerCases(t, Options{Force: true, DeleteUnmerged: true})
}

// TestEngineNeverRemovesTheMainCheckout passes the primary checkout with
// every flag set: removing it would take the repository's .git with it.
func TestEngineNeverRemovesTheMainCheckout(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "main")
	it := itemOf(row)
	it.Saw = cleanup.StatusProven
	o := one(t, f.engine(s, f.inputs(), Options{Force: true, DeleteUnmerged: true}), it)
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, cleanup.BlockPrimary) {
		t.Fatalf("outcome %+v, want refused naming primary", o)
	}
	if !exists(t, filepath.Join(f.Dir, ".git")) || !exists(t, filepath.Join(f.Dir, "README")) {
		t.Error("the main checkout lost files")
	}
}

// TestEngineRefusesDirtyWithoutForce ticks a dirty worktree (unique_work, the
// loss shown) without --force: uncommitted changes are lost for good, so the
// flag is required.
func TestEngineRefusesDirtyWithoutForce(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Dirty(p, "wip.txt")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "--force required") {
		t.Fatalf("outcome %+v, want refused asking for --force", o)
	}
	if !exists(t, filepath.Join(p, "wip.txt")) {
		t.Error("the uncommitted file is gone")
	}
}

// TestEngineForceRemovesDirtyAfterApproval removes the same dirty worktree
// with Force (the user approved the loss): git needs --force for it, and the
// engine passes it.
func TestEngineForceRemovesDirtyAfterApproval(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Dirty(p, "wip.txt")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	calls := recordRemoves(t)
	o := one(t, f.engine(s, f.inputs(), Options{Force: true}), itemOf(row))
	if o.Worktree != WtRemoved {
		t.Fatalf("outcome %+v, want removed", o)
	}
	if exists(t, p) || f.registered(p) {
		t.Error("dirty worktree still there")
	}
	if len(*calls) != 1 || !containsArg((*calls)[0], "--force") {
		t.Errorf("git calls %q, want one removal with --force", *calls)
	}
}

func containsArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// TestEngineForcesOnlyApprovedDirtyRemoval checks git's --force goes only to
// a dirty worktree the user approved: with Force set on a clean or
// ignored-files-only worktree it must not be passed, or a file written
// between the rescan and the removal would be deleted unseen.
func TestEngineForcesOnlyApprovedDirtyRemoval(t *testing.T) {
	for _, c := range []struct {
		name  string
		prep  func(f *fixture, p string)
		force bool
	}{
		{"clean", func(*fixture, string) {}, false},
		{"ignored only", func(f *fixture, p string) { f.Ignored(p, ".env") }, false},
		{"dirty", func(f *fixture, p string) { f.Dirty(p, "wip.txt") }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			noGh(t)
			f := newFixture(t)
			p := f.Worktree("topic")
			c.prep(f, p)
			s, r := f.report()
			row := f.row(r, cleanup.KindWorktree, "topic")
			calls := recordRemoves(t)
			o := one(t, f.engine(s, f.inputs(), Options{Force: true}), itemOf(row))
			if o.Worktree != WtRemoved {
				t.Fatalf("outcome %+v, want removed", o)
			}
			if len(*calls) != 1 {
				t.Fatalf("git calls %q, want one", *calls)
			}
			want := []string{"worktree", "remove"}
			if c.force {
				want = append(want, "--force")
			}
			want = append(want, "--", row.Path)
			if !reflect.DeepEqual((*calls)[0], want) {
				t.Errorf("argv %q, want %q", (*calls)[0], want)
			}
		})
	}
}

// TestEngineRefusesDetachedUniqueWithoutForce ticks a detached worktree
// holding a commit no branch has: removing it orphans that commit, so it
// needs Force like a dirty tree; with Force it goes.
func TestEngineRefusesDetachedUniqueWithoutForce(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := filepath.Join(f.Dir, ".worktrees", "loose")
	f.git("worktree", "add", "--detach", p)
	f.Commit(p, "x.txt", "x", "only here")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "loose")
	if !row.Losses.DetachedUnique {
		t.Fatalf("precondition: losses %+v, want DetachedUnique", row.Losses)
	}
	o := one(t, f.engine(s, f.inputs(), Options{DeleteUnmerged: true}), itemOf(row))
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "only in this worktree") {
		t.Fatalf("outcome %+v, want refused naming the unique commits", o)
	}
	if !exists(t, p) {
		t.Fatal("worktree removed")
	}
	o = one(t, f.engine(s, f.inputs(), Options{Force: true}), itemOf(row))
	if o.Worktree != WtRemoved || exists(t, p) {
		t.Fatalf("with Force: outcome %+v, on disk %v; want removed", o, exists(t, p))
	}
}

// TestEngineRefusesUnknownGatesEvenWithForce makes the worktree's gates
// unreadable (an ignored directory it cannot walk): whether it holds
// uncommitted work is unknown, and Force approves a known loss only.
func TestEngineRefusesUnknownGatesEvenWithForce(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	f.exclude("cache/")
	sub := filepath.Join(p, "cache", "sub")
	f.write(sub, "f", "x")
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	if !row.Losses.GatesUnknown {
		t.Fatalf("precondition: losses %+v, want GatesUnknown", row.Losses)
	}
	o := one(t, f.engine(s, f.inputs(), Options{Force: true, DeleteUnmerged: true}), itemOf(row))
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "could not read") {
		t.Fatalf("outcome %+v, want refused: state unreadable", o)
	}
	if !exists(t, p) {
		t.Error("worktree removed")
	}
}

// TestEngineRefusesCaseOnlyBranchSpellingChange hands the engine an item
// whose ref differs from the fresh one only in case (Task 2 carry): Rescan's
// identity check folds case, but refs/heads/Topic and refs/heads/topic can be
// two branches, and the engine must act only on the one the user saw.
func TestEngineRefusesCaseOnlyBranchSpellingChange(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	it := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
	it.Ref = "refs/heads/Topic"
	o := one(t, f.engine(s, f.inputs(), Options{Force: true, DeleteUnmerged: true}), it)
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "spelling") {
		t.Fatalf("outcome %+v, want refused: branch spelling changed", o)
	}
	if !exists(t, p) {
		t.Error("worktree removed")
	}
}

// TestEngineRefusesWorktreeOnAnotherBranch switches the worktree to another
// branch after the list: the row the user ticked no longer describes it, so
// it is refused with Rescan's identity error, never re-read as the new branch.
func TestEngineRefusesWorktreeOnAnotherBranch(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	it := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
	f.Branch("other")
	f.Switch(p, "other")
	o := one(t, f.engine(s, f.inputs(), Options{Force: true}), it)
	if o.Worktree != WtRefused || !strings.Contains(o.Reason, "now on refs/heads/other") {
		t.Fatalf("outcome %+v, want refused naming the switch", o)
	}
	if !exists(t, p) {
		t.Error("worktree removed")
	}
}

// TestEngineSkipsGoneWorktree removes the worktree behind the engine's back:
// a target that no longer exists is skipped_changed, not an error.
func TestEngineSkipsGoneWorktree(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	it := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
	f.git("worktree", "remove", p)
	o := one(t, f.engine(s, f.inputs(), Options{}), it)
	if o.Worktree != WtSkippedChanged || !strings.Contains(o.Reason, "no longer exists") {
		t.Fatalf("outcome %+v, want skipped_changed: no longer exists", o)
	}
}

// TestEngineRefusesInvalidTargets hands the engine items no row could
// produce: a relative or empty path, the filesystem root, an ancestor of the
// repository, a path inside its git dir, a remote or unknown kind. Each is
// refused before git is asked anything.
func TestEngineRefusesInvalidTargets(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	good := itemOf(f.row(r, cleanup.KindWorktree, "topic"))
	cases := map[string]func(it *Item){
		"relative":    func(it *Item) { it.Path = ".worktrees/topic" },
		"empty":       func(it *Item) { it.Path = "" },
		"root":        func(it *Item) { it.Path = "/" },
		"repo parent": func(it *Item) { it.Path = filepath.Dir(f.Dir) },
		"git dir":     func(it *Item) { it.Path = filepath.Join(f.Dir, ".git", "worktrees", "topic") },
		"remote kind": func(it *Item) { it.Kind = cleanup.KindRemote },
		"no kind":     func(it *Item) { it.Kind = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			it := good
			mut(&it)
			o := one(t, f.engine(s, f.inputs(), Options{Force: true, DeleteUnmerged: true}), it)
			if o.Worktree != WtRefused || !strings.Contains(o.Reason, "invalid target") {
				t.Fatalf("outcome %+v, want refused: invalid target", o)
			}
		})
	}
	if !exists(t, p) || !exists(t, filepath.Join(f.Dir, ".git")) {
		t.Error("something was removed")
	}
}

// TestEngineRunsItemsInGivenOrder hands two worktrees in reverse name order:
// the engine acts on them one at a time in the order given (the user's
// order), and its outcomes and progress calls follow it.
func TestEngineRunsItemsInGivenOrder(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	a := f.Worktree("topic")
	b := f.Worktree("topic2")
	s, r := f.report()
	items := []Item{itemOf(f.row(r, cleanup.KindWorktree, "topic2")), itemOf(f.row(r, cleanup.KindWorktree, "topic"))}
	calls := recordRemoves(t)
	var seen []string
	out := f.engine(s, f.inputs(), Options{}).Run(items, func(o Outcome) { seen = append(seen, o.Item.Name) })
	if len(out) != 2 || out[0].Item.Name != "topic2" || out[1].Item.Name != "topic" {
		t.Fatalf("outcomes %+v, want topic2 then topic", out)
	}
	if !reflect.DeepEqual(seen, []string{"topic2", "topic"}) {
		t.Errorf("progress order %v", seen)
	}
	if len(*calls) != 2 || (*calls)[0][len((*calls)[0])-1] != items[0].Path || (*calls)[1][len((*calls)[1])-1] != items[1].Path {
		t.Errorf("git calls %q, want %s then %s", *calls, b, a)
	}
}

// TestEngineComparesPathsAfterEvalSymlinks names the worktree by another
// spelling of the same directory (macOS /var vs /private/var, /tmp vs
// /private/tmp): the rescan must still find it, and git is handed the path
// the repository reports.
func TestEngineComparesPathsAfterEvalSymlinks(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	row := f.row(r, cleanup.KindWorktree, "topic")
	resolved, err := filepath.EvalSymlinks(row.Path)
	if err != nil {
		t.Fatal(err)
	}
	alt := ""
	switch {
	case strings.HasPrefix(row.Path, "/private/"):
		alt = strings.TrimPrefix(row.Path, "/private")
	case resolved != row.Path:
		alt = resolved
	}
	if alt == "" {
		t.Skipf("no second spelling of %s on this machine", row.Path)
	}
	if r2, err := filepath.EvalSymlinks(alt); err != nil || r2 != resolved {
		t.Skipf("%s does not resolve to %s here", alt, resolved)
	}
	it := itemOf(row)
	it.Path = alt
	calls := recordRemoves(t)
	o := one(t, f.engine(s, f.inputs(), Options{}), it)
	if o.Worktree != WtRemoved || exists(t, p) {
		t.Fatalf("outcome %+v, want removed", o)
	}
	if len(*calls) != 1 || (*calls)[0][len((*calls)[0])-1] != row.Path {
		t.Errorf("git calls %q, want the repository's spelling %s", *calls, row.Path)
	}
}

// TestPlanMutatesNothing plans a mix of items (removable, dirty without
// force, locked, gone-later branch item) and checks the repository is
// byte-for-byte unchanged, then runs the same items: every decision Plan made
// is the one Run makes (a dry run is only useful if it predicts the run).
func TestPlanMutatesNothing(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	clean := f.Worktree("topic")
	dirty := f.Worktree("topic2")
	f.Dirty(dirty, "wip.txt")
	locked := f.Worktree("topic3")
	f.Lock(locked)
	f.Branch("loose")
	s, r := f.report()
	items := []Item{
		itemOf(f.row(r, cleanup.KindWorktree, "topic")),
		itemOf(f.row(r, cleanup.KindWorktree, "topic2")),
		itemOf(f.row(r, cleanup.KindWorktree, "topic3")),
		itemOf(f.row(r, cleanup.KindBranch, "loose")),
	}
	items[2].Saw = cleanup.StatusProven // a row ticked proven, locked since: refused by its blocker
	state := func() string {
		return f.git("worktree", "list", "--porcelain") + "\n" + f.git("for-each-ref") + "\n" + f.git("status", "--porcelain", "--ignored")
	}
	before := state()
	e := f.engine(s, f.inputs(), Options{})
	plan := e.Plan(items)
	dry := f.engine(s, f.inputs(), Options{DryRun: true}).Run(items, nil)
	if after := state(); after != before {
		t.Fatalf("Plan changed the repository:\nbefore %s\nafter %s", before, after)
	}
	for _, p := range []string{clean, dirty, locked} {
		if !exists(t, p) {
			t.Fatalf("Plan removed %s", p)
		}
	}
	if !reflect.DeepEqual(plan, dry) {
		t.Errorf("Plan %+v\nDryRun %+v", plan, dry)
	}
	if plan[0].Worktree != WtRemoved || !strings.Contains(plan[0].Reason, "dry run") {
		t.Errorf("plan[0] %+v, want removed (dry run)", plan[0])
	}
	run := e.Run(items, nil)
	for i := range items {
		if plan[i].Worktree != run[i].Worktree || plan[i].Branch != run[i].Branch {
			t.Errorf("item %s: plan %s/%s, run %s/%s", items[i].Name, plan[i].Worktree, plan[i].Branch, run[i].Worktree, run[i].Branch)
		}
		// A plan's reason differs from the run's only where the run acted.
		if run[i].Worktree != WtRemoved && run[i].Branch != BrDeleted && plan[i].Reason != run[i].Reason {
			t.Errorf("item %s: plan reason %q, run reason %q", items[i].Name, plan[i].Reason, run[i].Reason)
		}
	}
	if exists(t, clean) {
		t.Error("Run did not remove the clean worktree")
	}
}

// TestRunProgressCalledPerItem checks progress is called once per item, with
// that item's outcome, and that a nil progress is accepted: the TUI draws
// each line from it, and a missed call leaves an item looking unfinished.
func TestRunProgressCalledPerItem(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	f.Worktree("topic")
	f.Branch("loose")
	s, r := f.report()
	items := []Item{
		itemOf(f.row(r, cleanup.KindWorktree, "topic")),
		itemOf(f.row(r, cleanup.KindBranch, "loose")),
		{Kind: cleanup.KindWorktree, Path: "relative"},
	}
	var got []Outcome
	out := f.engine(s, f.inputs(), Options{DryRun: true}).Run(items, func(o Outcome) { got = append(got, o) })
	if !reflect.DeepEqual(got, out) || len(out) != len(items) {
		t.Fatalf("progress %+v, outcomes %+v", got, out)
	}
	if out2 := f.engine(s, f.inputs(), Options{DryRun: true}).Run(items, nil); len(out2) != len(items) {
		t.Fatalf("nil progress: %d outcomes", len(out2))
	}
	if out[1].Worktree != WtNotRequested || out[1].Branch != BrDeleted {
		t.Errorf("branch item %+v, want not_requested / deleted (dry run)", out[1])
	}
}

type stubProcs struct {
	procs []Proc
	err   error
	dirs  []string
}

func (s *stubProcs) InDir(dir string) ([]Proc, error) {
	s.dirs = append(s.dirs, dir)
	return s.procs, s.err
}

// TestEngineWarnsAboutLiveProcesses finds four processes in the worktree: the
// user is warned (three names and the count), and the removal still happens;
// a process cwd is a warning only (spec).
func TestEngineWarnsAboutLiveProcesses(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	pf := &stubProcs{procs: []Proc{{1, "vim"}, {2, "node"}, {3, "zsh"}, {4, "python"}}}
	o := one(t, f.engine(s, f.inputs(), Options{Procs: pf}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if o.Worktree != WtRemoved || exists(t, p) {
		t.Fatalf("outcome %+v, want removed", o)
	}
	if len(o.Warnings) != 1 {
		t.Fatalf("warnings %q, want one", o.Warnings)
	}
	w := o.Warnings[0]
	for _, s := range []string{"4", "vim", "node", "zsh"} {
		if !strings.Contains(w, s) {
			t.Errorf("warning %q does not name %s", w, s)
		}
	}
	if strings.Contains(w, "python") {
		t.Errorf("warning %q names more than three commands", w)
	}
	if len(pf.dirs) != 1 || cleanup.ResolvePath(pf.dirs[0]) != cleanup.ResolvePath(p) {
		t.Errorf("finder asked about %v, want %s", pf.dirs, p)
	}
}

// TestEngineIgnoresProcessFinderError makes the process finder fail: that is
// a warning saying the check could not run, never a refusal (the check is
// advisory and lsof can be missing).
func TestEngineIgnoresProcessFinderError(t *testing.T) {
	noGh(t)
	f := newFixture(t)
	p := f.Worktree("topic")
	s, r := f.report()
	pf := &stubProcs{err: errors.New("lsof: not found")}
	o := one(t, f.engine(s, f.inputs(), Options{Procs: pf}), itemOf(f.row(r, cleanup.KindWorktree, "topic")))
	if o.Worktree != WtRemoved || exists(t, p) {
		t.Fatalf("outcome %+v, want removed", o)
	}
	if len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "could not check for running processes") {
		t.Errorf("warnings %q", o.Warnings)
	}
}
