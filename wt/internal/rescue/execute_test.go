package rescue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// rescueOnce runs Preflight and Execute for the worktree at src and returns
// the plan and outcome. The caller stands in the primary checkout.
func rescueOnce(t *testing.T, f *fixture, src string) (*Plan, Outcome) {
	t.Helper()
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(src), "")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	return plan, eng.Execute(plan)
}

// TestExecuteCarriesEachKindOfWork checks, for each shape of local-only work,
// that Execute reports moved, the new worktree sits on rescued/<branch> at the
// source's exact HEAD with no upstream, its `git status` equals the source's
// byte for byte, and the source is unchanged (same fingerprint apart from the
// new branch). Why: this is the feature; every shape is a way a diff or a copy
// can silently differ (modes, deletions, binaries, renames, links, line ends).
func TestExecuteCarriesEachKindOfWork(t *testing.T) {
	bin := string([]byte{0, 1, 2, 255, 254, 0, 10, 13, 0})
	cases := []struct {
		name  string
		setup func(f *fixture, p string)
	}{
		{"commits only", func(f *fixture, p string) {}},
		{"staged only", func(f *fixture, p string) {
			f.write(p, "work.txt", "staged edit")
			gitIn(f.t, p, "add", "work.txt")
		}},
		{"unstaged only", func(f *fixture, p string) { f.write(p, "work.txt", "unstaged edit") }},
		{"staged new file", func(f *fixture, p string) {
			f.write(p, "dir/new.txt", "new")
			gitIn(f.t, p, "add", "dir/new.txt")
		}},
		{"unstaged deletion", func(f *fixture, p string) { os.Remove(filepath.Join(p, "work.txt")) }},
		{"staged deletion", func(f *fixture, p string) { gitIn(f.t, p, "rm", "-q", "work.txt") }},
		{"mode change", func(f *fixture, p string) { os.Chmod(filepath.Join(p, "work.txt"), 0o755) }},
		{"binary edit", func(f *fixture, p string) { f.write(p, "work.txt", bin) }},
		{"staged binary new", func(f *fixture, p string) {
			f.write(p, "b.bin", bin)
			gitIn(f.t, p, "add", "b.bin")
		}},
		{"staged rename", func(f *fixture, p string) { gitIn(f.t, p, "mv", "work.txt", "moved.txt") }},
		{"staged and unstaged different files", func(f *fixture, p string) {
			f.write(p, "other.txt", "o")
			gitIn(f.t, p, "add", "other.txt")
			f.write(p, "work.txt", "edit")
		}},
		{"untracked file", func(f *fixture, p string) { f.write(p, "deep/er/note.txt", "note") }},
		{"untracked executable script", func(f *fixture, p string) {
			f.write(p, "run.sh", "#!/bin/sh\necho hi\n")
			os.Chmod(filepath.Join(p, "run.sh"), 0o755)
		}},
		{"untracked symlink", func(f *fixture, p string) {
			if err := os.Symlink("work.txt", filepath.Join(p, "link")); err != nil {
				f.t.Fatal(err)
			}
			if err := os.Symlink("/nowhere/at/all", filepath.Join(p, "dangling")); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"CRLF edit", func(f *fixture, p string) { f.write(p, "work.txt", "a\r\nb\r\n") }},
		{"empty untracked file", func(f *fixture, p string) { f.write(p, "empty", "") }},
		{"everything at once", func(f *fixture, p string) {
			f.write(p, "s.txt", "s")
			gitIn(f.t, p, "add", "s.txt")
			f.write(p, "work.txt", "u")
			f.write(p, "n/new", "n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Commit(p, "work.txt", "work", "work")
			c.setup(f, p)
			head := gitIn(t, p, "rev-parse", "HEAD")
			before := snapshot(t, p)

			plan, out := rescueOnce(t, f, p)

			if out.Status != StatusMoved {
				t.Fatalf("status %s (%s): %s", out.Status, out.Code, out.Reason)
			}
			dest := out.Plan.DestPath
			if got := gitIn(t, dest, "rev-parse", "HEAD"); got != head {
				t.Errorf("destination HEAD %s, want %s", got, head)
			}
			if got := gitIn(t, dest, "symbolic-ref", "HEAD"); got != "refs/heads/rescued/topic" {
				t.Errorf("destination branch %s", got)
			}
			// Read with optional locks off, as the engine does: a plain git status
			// on the source would itself refresh its index.
			got, err := sgit{dir: dest}.out(nil, cleanup.StatusArgs()...)
			if err != nil || string(got) != before.status {
				t.Errorf("status differs (err %v):\n%q\n%q", err, got, before.status)
			}
			if d := before.diff(snapshot(t, p)); d != "" {
				t.Errorf("the source changed (%s)", d)
			}
			if plan.Counts.Commits != 1 {
				t.Errorf("commits counted %d, want 1", plan.Counts.Commits)
			}
			if len(out.Completed) == 0 || !out.Created {
				t.Errorf("outcome = %+v", out)
			}
		})
	}
}

// TestExecuteCarriesADetachedWorktreesCommits checks a detached HEAD with two
// commits the base lacks lands on a new branch at that HEAD. Why: a detached
// worktree has no branch, so these commits survive only if a ref names them;
// this is the rescue's main reason to exist.
func TestExecuteCarriesADetachedWorktreesCommits(t *testing.T) {
	f := newFixture(t)
	p := f.Detached("scratch")
	f.Commit(p, "a.txt", "a", "a")
	head := f.Commit(p, "b.txt", "b", "b")
	f.write(p, "c.txt", "untracked")

	plan, out := rescueOnce(t, f, p)

	if out.Status != StatusMoved {
		t.Fatalf("status %s (%s): %s", out.Status, out.Code, out.Reason)
	}
	if plan.Branch != "rescued/scratch" || gitIn(t, f.Dir, "rev-parse", "refs/heads/rescued/scratch") != head {
		t.Errorf("branch %s does not point at %s", plan.Branch, head)
	}
	if _, err := os.Stat(filepath.Join(out.Plan.DestPath, "c.txt")); err != nil {
		t.Errorf("untracked file not carried: %v", err)
	}
	if plan.Counts.Commits != 2 {
		t.Errorf("commits counted %d, want 2 (a detached HEAD's commits count too)", plan.Counts.Commits)
	}
}

// TestExecuteLeavesIgnoredFilesBehindAndSaysSo checks an ignored file is not
// copied and is counted as left behind. Why: ignored files are caches and
// secrets; copying them is a separate decision, and the user must be told
// they stay.
func TestExecuteLeavesIgnoredFilesBehindAndSaysSo(t *testing.T) {
	f := newFixture(t)
	f.exclude("*.secret")
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "key.secret", "hunter2")

	plan, out := rescueOnce(t, f, p)

	if out.Status != StatusMoved {
		t.Fatalf("status %s: %s", out.Status, out.Reason)
	}
	if plan.Counts.IgnoredLeft != 1 {
		t.Errorf("IgnoredLeft = %d, want 1", plan.Counts.IgnoredLeft)
	}
	if _, err := os.Stat(filepath.Join(out.Plan.DestPath, "key.secret")); err == nil {
		t.Error("an ignored file was copied")
	}
}

// TestExecuteSurvivesAPostCheckoutHook checks a post-checkout hook (run by
// git worktree add in the new worktree) that writes to stderr does not
// stop or change the rescue. Why: wt repositories commonly carry hooks, and
// the verification would catch one that rewrote files.
func TestExecuteSurvivesAPostCheckoutHook(t *testing.T) {
	f := newFixture(t)
	hooks := f.git("rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\necho hook ran >&2\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")

	_, out := rescueOnce(t, f, p)

	if out.Status != StatusMoved {
		t.Fatalf("status %s: %s", out.Status, out.Reason)
	}
}

// TestExecuteRefusesWhenTheSourceChangedAfterThePlan checks a source edited
// between Preflight and Execute is refused and nothing is created. Why: the
// user confirmed one state; carrying another would copy work nobody saw.
func TestExecuteRefusesWhenTheSourceChangedAfterThePlan(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	f.write(p, "work.txt", "edited again")
	world := f.world()

	out := eng.Execute(plan)

	if out.Status != StatusRefused || out.Code != CodeSourceChanged || out.Created {
		t.Fatalf("outcome = %+v", out)
	}
	if f.world() != world {
		t.Error("something was created")
	}
}

// TestExecuteFailsWhenTheSourceChangesWhileCopying checks an edit to the
// source during the copy (a live session) makes the outcome failed with the
// source-changed-while-copying code, keeps the destination, and does not touch
// the source further. Why: a stale copy must never be reported as a rescue.
func TestExecuteFailsWhenTheSourceChangesWhileCopying(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { afterCarry = func(*Plan) {} })
	afterCarry = func(pl *Plan) { f.write(p, "work.txt", "a session edited this") }

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeSourceChangedMid || !out.Created {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.Contains(out.Reason, out.Plan.DestPath) {
		t.Errorf("the reason does not name the kept destination: %s", out.Reason)
	}
	if b, _ := os.ReadFile(filepath.Join(p, "work.txt")); string(b) != "a session edited this" {
		t.Errorf("the source file was touched: %q", b)
	}
}

// TestExecuteFailsWhenTheCopyDiffers checks a destination that drifts from the
// source after the carry (here: a file edited in it) is a failed outcome with
// the verify code, never moved. Why: verification is what lets the command say
// the work is safe; it must be able to say no.
func TestExecuteFailsWhenTheCopyDiffers(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	f.write(p, "u.txt", "untracked")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, p)
	t.Cleanup(func() { afterCarry = func(*Plan) {} })
	afterCarry = func(pl *Plan) { f.write(pl.DestPath, "u.txt", "drift") }

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeVerifyFailed || !out.Created {
		t.Fatalf("outcome = %+v", out)
	}
	if d := before.diff(snapshot(t, p)); d != "" {
		t.Errorf("the source changed (%s)", d)
	}
}

// TestExecuteKeepsTheDestinationOnAFailureAndARerunNamesTheTaken checks a
// failure after creation keeps the new worktree and branch and says where, and
// that running the same rescue again is refused naming --to (no silent -2
// suffix). Why: rolling back would need a forced worktree removal on a
// directory holding carried work; keeping is safe and visible.
func TestExecuteKeepsTheDestinationOnAFailureAndARerunNamesTheTaken(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "u.txt", "untracked")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	plan.untracked = append(plan.untracked, "ghost.txt") // vanished after the check
	before := snapshot(t, p)

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeCarryFailed || !out.Created {
		t.Fatalf("outcome = %+v", out)
	}
	if _, err := os.Stat(out.Plan.DestPath); err != nil {
		t.Errorf("the destination was removed: %v", err)
	}
	if gitIn(t, f.Dir, "rev-parse", "--verify", "refs/heads/rescued/topic") == "" {
		t.Error("the branch is gone")
	}
	if d := before.diff(snapshot(t, p)); d != "" {
		t.Errorf("the source changed (%s)", d)
	}
	eng2, find2 := f.engine(f.Dir)
	_, err = eng2.Preflight(find2(p), "")
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeNameUnavailable || !strings.Contains(r.Message, "--to") {
		t.Fatalf("rerun = %v, want name_unavailable naming --to", err)
	}
}

// TestCopyUntrackedNeverOverwritesOrFollowsLinks checks the copy refuses an
// existing destination file (O_EXCL) and a parent that is a symlink out of the
// destination. Why: the copy runs on the user's machine into a directory git
// populated; it must never write through a link or clobber a file.
func TestCopyUntrackedNeverOverwritesOrFollowsLinks(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := copyUntracked(src, dst, "a.txt"); err == nil {
		t.Error("an existing file was overwritten")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "old" {
		t.Errorf("destination file now %q", b)
	}

	if err := os.MkdirAll(filepath.Join(src, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "d", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "d")); err != nil {
		t.Fatal(err)
	}
	if _, err := copyUntracked(src, dst, "d/f"); err == nil {
		t.Error("a symlinked parent was followed")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("a file was written outside the destination: %v", ents)
	}
}

// TestSafeRel checks the path judge: relative paths with ordinary components
// pass; absolute, empty, dot, dot-dot and .git components do not.
func TestSafeRel(t *testing.T) {
	for rel, want := range map[string]bool{
		"a": true, "a/b.txt": true, ".github/x": true, "a b/c": true,
		"": false, "/a": false, "../a": false, "a/../b": false, "./a": false, "a//b": false, ".git/config": false, "x/.GIT/y": false, "a/": false,
	} {
		if got := safeRel(rel); got != want {
			t.Errorf("safeRel(%q) = %v, want %v", rel, got, want)
		}
	}
}

// TestExecuteIgnoresTheUsersDiffPrefixSettings checks a rescue of staged and
// unstaged edits under a subdirectory moves when the repository sets
// diff.noprefix, and when it sets diff.srcPrefix and diff.dstPrefix. Why:
// git apply strips one path component, so a diff written without git's a/ b/
// prefixes fails or patches the wrong path.
func TestExecuteIgnoresTheUsersDiffPrefixSettings(t *testing.T) {
	for _, cfg := range [][]string{
		{"diff.noprefix=true"},
		{"diff.srcPrefix=", "diff.dstPrefix=x/"},
	} {
		t.Run(strings.Join(cfg, ","), func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Commit(p, "sub/f", "one", "sub")
			f.Commit(p, "a.txt", "one", "a")
			for _, kv := range cfg {
				k, v, _ := strings.Cut(kv, "=")
				f.git("config", k, v)
			}
			f.write(p, "sub/f", "staged")
			gitIn(t, p, "add", "sub/f")
			f.write(p, "a.txt", "unstaged")

			_, out := rescueOnce(t, f, p)
			if out.Status != StatusMoved {
				t.Fatalf("status %s (%s): %s", out.Status, out.Code, out.Reason)
			}
		})
	}
}

// churn does what parallel sessions and background tools do around a source
// without touching what the rescue promises to keep: a new branch, a moved
// remote-tracking ref, a commit in another worktree, and a `git status` in
// the source that refreshes its index's stat cache (raw index bytes change,
// content does not).
func churn(t *testing.T, f *fixture, source string) {
	t.Helper()
	other := f.Worktree("bystander")
	f.Commit(other, "other.txt", "other", "other work")
	f.git("branch", "unrelated")
	f.git("update-ref", "refs/remotes/origin/elsewhere", gitIn(t, other, "rev-parse", "HEAD"))
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(source, "work.txt"), future, future); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "status", "--porcelain")
}

// TestExecuteIgnoresChangesOutsideTheSource checks that refs, other
// worktrees' commits and an index stat-cache refresh between the check and
// the run, or while the copy runs, neither refuse the rescue nor fail it
// half built. Why: the fingerprint guards only what the rescue promises not
// to change (the source's HEAD, its branch, its index content, its files);
// hashing every ref and the raw index bytes made any parallel session or
// background fetch abort the rescue.
func TestExecuteIgnoresChangesOutsideTheSource(t *testing.T) {
	for _, when := range []string{"between check and run", "while copying"} {
		t.Run(when, func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Commit(p, "work.txt", "work", "work")
			f.write(p, "work.txt", "edit")
			eng, find := f.engine(f.Dir)
			plan, err := eng.Preflight(find(p), "")
			if err != nil {
				t.Fatal(err)
			}
			if when == "between check and run" {
				churn(t, f, p)
			} else {
				t.Cleanup(func() { afterCarry = func(*Plan) {} })
				afterCarry = func(*Plan) { churn(t, f, p) }
			}

			out := eng.Execute(plan)

			if out.Status != StatusMoved {
				t.Fatalf("status %s (%s): %s", out.Status, out.Code, out.Reason)
			}
		})
	}
}

// TestExecuteReportsABranchLeftByAFailedCreate checks that when
// worktree.CreateAt fails after git made the branch (here .worktrees became
// read-only after the check, so the directory could not be made), the outcome
// says the branch was kept and names it, instead of "nothing was created",
// and that a rerun's name_unavailable is therefore explained. Why: the old
// report claimed nothing existed while refs/heads/rescued/topic did, so the
// user's next run failed with no account of why.
func TestExecuteReportsABranchLeftByAFailedCreate(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	wtDir := filepath.Join(f.Dir, ".worktrees")
	if err := os.Chmod(wtDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(wtDir, 0o755) })
	if err := os.WriteFile(filepath.Join(wtDir, "probe"), nil, 0o644); err == nil {
		t.Skip("the directory is writable despite chmod (running as root?)")
	}

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeCreateFailed {
		t.Fatalf("outcome = %+v", out)
	}
	if !out.BranchKept {
		t.Errorf("BranchKept is false although refs/heads/rescued/topic exists: %+v", out)
	}
	if strings.Contains(out.Reason, "nothing was created") || !strings.Contains(out.Reason, "rescued/topic") {
		t.Errorf("reason %q must name the kept branch and not say nothing was created", out.Reason)
	}
	gitIn(t, f.Dir, "rev-parse", "--verify", "refs/heads/rescued/topic")
}

// TestExecuteSaysNothingWasCreatedWhenCreateLeavesNothing checks a create that
// fails before git touches anything (the start commit is unknown) still says
// nothing was created and sets neither Created nor BranchKept. Why: the new
// leftover-branch report must not turn every create failure into a "kept".
func TestExecuteSaysNothingWasCreatedWhenCreateLeavesNothing(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	plan.Start = strings.Repeat("0", 40)

	out := eng.Execute(plan)

	if out.Code != CodeCreateFailed || out.Created || out.BranchKept || !strings.Contains(out.Reason, "nothing was created") {
		t.Fatalf("outcome = %+v", out)
	}
}

// dirMode is the permission bits of dir.
func dirMode(t *testing.T, dir string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// TestExecuteCarriesDirectoryModes checks the directories created for
// untracked files get the source directory's mode, a 0700 one included and a
// read-only one (so the copy had to write into it first), and that a
// directory git already made in the destination keeps its own. Why: a 0700
// directory used to become 0755, silently widening who can read the files
// the user kept private.
func TestExecuteCarriesDirectoryModes(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "private/deep/note.txt", "secret")
	f.write(p, "ro/file.txt", "read only dir")
	f.write(p, "tracked/new.txt", "new in a tracked dir")
	if err := os.Chmod(filepath.Join(p, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(p, "private/deep"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(p, "ro"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(p, "ro"), 0o700) })

	_, out := rescueOnce(t, f, p)
	dest := out.Plan.DestPath
	t.Cleanup(func() { os.Chmod(filepath.Join(dest, "ro"), 0o700) })

	if out.Status != StatusMoved {
		t.Fatalf("status %s (%s): %s", out.Status, out.Code, out.Reason)
	}
	for dir, want := range map[string]os.FileMode{"private": 0o700, "private/deep": 0o750, "ro": 0o500} {
		if got := dirMode(t, filepath.Join(dest, dir)); got != want {
			t.Errorf("%s has mode %o, want %o", dir, got, want)
		}
	}
}

// TestVerifyComparesDirectoryModes checks a destination directory whose mode
// drifted from the source's after the copy is a verify failure. Why: verify
// compared per-file modes only, so a widened directory passed as moved.
func TestVerifyComparesDirectoryModes(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "private/note.txt", "secret")
	if err := os.Chmod(filepath.Join(p, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { afterCarry = func(*Plan) {} })
	afterCarry = func(pl *Plan) { os.Chmod(filepath.Join(pl.DestPath, "private"), 0o755) }

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeVerifyFailed || !strings.Contains(out.Reason, "private") {
		t.Fatalf("outcome = %+v", out)
	}
}

// TestExecuteGivesCreatedDirectoriesTheirModesWhenTheCopyFailsMidway makes the
// copy of a second untracked file fail after the first one's directory (mode
// 0750) was created. What: the failure keeps the destination as before and
// the directory the copy made already has the source's mode, not the 0700 the
// copy writes with; a directory whose source vanished does not stop the
// others. Why: the modes used to be applied only after every file was in, so
// the kept half-built worktree widened or narrowed directories the user set.
func TestExecuteGivesCreatedDirectoriesTheirModesWhenTheCopyFailsMidway(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "shared/one.txt", "one")
	if err := os.Chmod(filepath.Join(p, "shared"), 0o750); err != nil {
		t.Fatal(err)
	}
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	plan.untracked = append(plan.untracked, "ghost/two.txt") // vanished after the check

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeCarryFailed || !out.Created {
		t.Fatalf("outcome = %+v", out)
	}
	if got := dirMode(t, filepath.Join(out.Plan.DestPath, "shared")); got != 0o750 {
		t.Errorf("shared has mode %o in the kept worktree, want 750", got)
	}
	// ghost/ was made in the copy but its source is gone, so its mode could not
	// be set: the keep reason says so rather than promising the modes.
	if !strings.Contains(out.Reason, "directory modes could not be restored") {
		t.Errorf("reason %q does not report the directory modes that could not be restored", out.Reason)
	}
}

// rescuedRefs lists the refs under the record prefix in the repository.
func rescuedRefs(t *testing.T, f *fixture) string {
	t.Helper()
	return f.git("for-each-ref", "refs/wt/rescued")
}

// TestExecuteWritesARecordAfterVerify checks, for commits-only, staged,
// unstaged, nested-untracked and detached sources, that a moved outcome
// carries a Record whose ref resolves to its commit, whose W tree is git's
// working-tree tree and whose I tree is the index tree, and that the source
// (raw index bytes included) is untouched. Why: the record is the proof the
// copy kept everything; a wrong tree or a touched source breaks that claim.
func TestExecuteWritesARecordAfterVerify(t *testing.T) {
	cases := []struct {
		name     string
		detached bool
		setup    func(f *fixture, p string)
	}{
		{"commits only", false, func(f *fixture, p string) {}},
		{"staged", false, func(f *fixture, p string) {
			f.write(p, "work.txt", "staged")
			gitIn(f.t, p, "add", "work.txt")
		}},
		{"unstaged", false, func(f *fixture, p string) { f.write(p, "work.txt", "unstaged") }},
		{"nested untracked", false, func(f *fixture, p string) { f.write(p, "a/b/c.txt", "deep") }},
		{"detached", true, func(f *fixture, p string) {
			f.write(p, "work.txt", "staged")
			gitIn(f.t, p, "add", "work.txt")
			f.write(p, "u.txt", "untracked")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			var p string
			if c.detached {
				p = f.Detached("d")
			} else {
				p = f.Worktree("topic")
			}
			f.Commit(p, "work.txt", "work", "work")
			c.setup(f, p)
			wantW, wantI := recOracle(t, p), copyIndexTree(t, p)
			before := snapshot(t, p)

			_, o := rescueOnce(t, f, p)

			if o.Status != StatusMoved {
				t.Fatalf("status %s (%s): %s", o.Status, o.Code, o.Reason)
			}
			if o.Record == nil {
				t.Fatalf("no record; warnings %q", o.Plan.Warnings)
			}
			if got := gitIn(t, p, "rev-parse", o.Record.Ref); got != o.Record.Commit {
				t.Errorf("ref points at %s, want %s", got, o.Record.Commit)
			}
			if got := gitIn(t, p, "rev-parse", o.Record.Commit+"^{tree}"); got != wantW {
				t.Errorf("W tree = %s, want %s", got, wantW)
			}
			if got := gitIn(t, p, "rev-parse", o.Record.Commit+"^^{tree}"); got != wantI {
				t.Errorf("I tree = %s, want %s", got, wantI)
			}
			if d := before.diff(snapshot(t, p)); d != "" {
				t.Errorf("the source changed (%s)", d)
			}
		})
	}
}

// TestExecuteWritesNoRecordWhenNothingMoved checks a refusal (nothing to
// keep), a taken name and a failed verify each leave no ref under
// refs/wt/rescued and no Record. Why: a record is a claim that the copy is
// verified; none may exist for a rescue that did not finish.
func TestExecuteWritesNoRecordWhenNothingMoved(t *testing.T) {
	t.Run("nothing to keep", func(t *testing.T) {
		f := newFixture(t)
		p := f.Worktree("topic")
		eng, find := f.engine(f.Dir)
		_, err := eng.Preflight(find(p), "")
		var r *Refusal
		if !errors.As(err, &r) || r.Code != CodeNothingToKeep {
			t.Fatalf("Preflight = %v, want nothing_to_keep", err)
		}
		if got := rescuedRefs(t, f); got != "" {
			t.Errorf("refs: %s", got)
		}
	})
	t.Run("name unavailable", func(t *testing.T) {
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Commit(p, "work.txt", "work", "work")
		eng, find := f.engine(f.Dir)
		plan, err := eng.Preflight(find(p), "")
		if err != nil {
			t.Fatal(err)
		}
		f.git("branch", plan.Branch) // taken while the prompt waited
		o := eng.Execute(plan)
		if o.Code != CodeNameUnavailable || o.Record != nil {
			t.Fatalf("outcome = %+v", o)
		}
		if got := rescuedRefs(t, f); got != "" {
			t.Errorf("refs: %s", got)
		}
	})
	t.Run("verify failed", func(t *testing.T) {
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Commit(p, "work.txt", "work", "work")
		f.write(p, "u.txt", "untracked")
		eng, find := f.engine(f.Dir)
		plan, err := eng.Preflight(find(p), "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { afterCarry = func(*Plan) {} })
		afterCarry = func(pl *Plan) { f.write(pl.DestPath, "u.txt", "drift") }
		o := eng.Execute(plan)
		if o.Code != CodeVerifyFailed || o.Record != nil {
			t.Fatalf("outcome = %+v", o)
		}
		if got := rescuedRefs(t, f); got != "" {
			t.Errorf("refs: %s", got)
		}
	})
}

// TestExecuteKeepsMovedWhenTheRecordCannotBeWritten checks a failing ref
// update leaves the outcome moved, with no Record, no ref and exactly one
// record_not_written warning. Why: the copy is verified and kept; only the
// extra proof is lost, and the user must be told.
func TestExecuteKeepsMovedWhenTheRecordCannotBeWritten(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "u.txt", "untracked")
	old := updateRecordRef
	t.Cleanup(func() { updateRecordRef = old })
	updateRecordRef = func(sgit, string, string) error { return errors.New("disk full") }

	_, o := rescueOnce(t, f, p)

	if o.Status != StatusMoved || o.Record != nil {
		t.Fatalf("outcome = %+v", o)
	}
	var n int
	for _, w := range o.Plan.Warnings {
		if strings.HasPrefix(w, RecordWarningPrefix) {
			n++
			if !strings.Contains(w, "disk full") {
				t.Errorf("warning does not say why: %q", w)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d record warnings in %q, want 1", n, o.Plan.Warnings)
	}
	if got := rescuedRefs(t, f); got != "" {
		t.Errorf("refs: %s", got)
	}
}

// TestExecuteWritesNoRecordWhenTheSourceChangedAfterVerify checks an edit to
// the source between verify and the record write gives a moved outcome, no
// record and a warning. Why: a record of a state the copy never held would
// be a false proof.
func TestExecuteWritesNoRecordWhenTheSourceChangedAfterVerify(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { afterVerify = func(*Plan) {} })
	afterVerify = func(*Plan) { f.write(p, "work.txt", "edited after verify") }

	o := eng.Execute(plan)

	if o.Status != StatusMoved || o.Record != nil {
		t.Fatalf("outcome = %+v", o)
	}
	if len(o.Plan.Warnings) == 0 || !strings.HasPrefix(o.Plan.Warnings[len(o.Plan.Warnings)-1], RecordWarningPrefix) ||
		!strings.Contains(strings.Join(o.Plan.Warnings, "\n"), "changed") {
		t.Errorf("warnings = %q", o.Plan.Warnings)
	}
	if got := rescuedRefs(t, f); got != "" {
		t.Errorf("refs: %s", got)
	}
}

// TestTwoRescuesOfOneSourceLeaveTwoValidRecords checks a second rescue of the
// unchanged source (to another branch) adds a second record beside the first,
// and their Wt-Dest-Branch trailers differ. Why: each rescue made its own
// destination; one record must not shadow or overwrite the other.
func TestTwoRescuesOfOneSourceLeaveTwoValidRecords(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "u.txt", "untracked")
	_, first := rescueOnce(t, f, p)
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "rescued/second")
	if err != nil {
		t.Fatal(err)
	}
	second := eng.Execute(plan)

	if first.Record == nil || second.Record == nil {
		t.Fatalf("records: %v %v; outcomes %+v / %+v", first.Record, second.Record, first, second)
	}
	if first.Record.Ref == second.Record.Ref {
		t.Fatalf("both records are %s", first.Record.Ref)
	}
	if n := len(strings.Split(rescuedRefs(t, f), "\n")); n != 2 {
		t.Errorf("%d refs, want 2", n)
	}
	for _, r := range []*Record{first.Record, second.Record} {
		if got := gitIn(t, p, "rev-parse", r.Ref); got != r.Commit {
			t.Errorf("%s points at %s, want %s", r.Ref, got, r.Commit)
		}
	}
	a := trailer(t, p, first.Record.Commit, "Wt-Dest-Branch")
	b := trailer(t, p, second.Record.Commit, "Wt-Dest-Branch")
	if a == b {
		t.Errorf("Wt-Dest-Branch trailers are equal: %q", a)
	}
}

// TestExecuteRecordOfADirectoryReplacedByASymlinkMatchesGit checks a source
// whose tracked directory was replaced by a symlink to a directory outside
// the repository (holding a file of the same name) is carried and recorded
// with git's own working-tree tree: the link kept as a link, the tracked
// children removed. Why: a record built by reading through the link would
// hold outside content under the old path and lose the symlink, a false
// proof of what the source held.
func TestExecuteRecordOfADirectoryReplacedByASymlinkMatchesGit(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.write(p, "x/y.txt", "tracked")
	gitIn(t, p, "add", "x/y.txt")
	gitIn(t, p, "commit", "-q", "-m", "x")
	out := t.TempDir()
	f.write(out, "y.txt", "outside content")
	if err := os.RemoveAll(filepath.Join(p, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(p, "x")); err != nil {
		t.Fatal(err)
	}
	want := recOracle(t, p)

	_, o := rescueOnce(t, f, p)

	if o.Status != StatusMoved {
		t.Fatalf("status %s (%s): %s", o.Status, o.Code, o.Reason)
	}
	if o.Record == nil {
		t.Fatalf("no record; warnings %q", o.Plan.Warnings)
	}
	if got := gitIn(t, p, "rev-parse", o.Record.Commit+"^{tree}"); got != want {
		t.Errorf("W tree = %s, want git's %s\n%s", got, want, gitIn(t, p, "ls-tree", "-r", o.Record.Commit))
	}
}

// TestExecuteWritesNoRecordWhenTheSourceChangesWhileTheRecordIsBuilt edits
// the source after the record's objects are built and before its ref is
// created (the beforeRecordRef seam): the outcome is moved, no ref exists and
// one record_not_written warning names the change. Why: the record is read
// after the copy was verified, so only a fingerprint taken after those reads
// proves the copy and the record describe the same state.
func TestExecuteWritesNoRecordWhenTheSourceChangesWhileTheRecordIsBuilt(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	t.Cleanup(func() { beforeRecordRef = func(*Plan) {} })
	beforeRecordRef = func(*Plan) { f.write(p, "work.txt", "edited while the record was built") }

	_, o := rescueOnce(t, f, p)

	if o.Status != StatusMoved || o.Record != nil {
		t.Fatalf("outcome = %+v", o)
	}
	var got []string
	for _, w := range o.Plan.Warnings {
		if strings.HasPrefix(w, RecordWarningPrefix) {
			got = append(got, w)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "changed") {
		t.Errorf("record warnings = %q (all: %q)", got, o.Plan.Warnings)
	}
	if refs := rescuedRefs(t, f); refs != "" {
		t.Errorf("refs: %s", refs)
	}
}

// TestExecuteRecordIgnoresAFailingReferenceTransactionHook installs a
// reference-transaction hook that exits 1 and rescues a dirty source: the
// outcome is moved with a record whose ref resolves. Why: the record's
// update-ref would otherwise run the repository's hook, which could refuse
// (or rewrite) the record; rescue runs no repository hook anywhere else.
func TestExecuteRecordIgnoresAFailingReferenceTransactionHook(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "u.txt", "untracked")
	hooks := f.git("rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, o := rescueOnce(t, f, p)

	if o.Status != StatusMoved {
		t.Fatalf("status %s (%s): %s", o.Status, o.Code, o.Reason)
	}
	if o.Record == nil {
		t.Fatalf("no record; warnings %q", o.Plan.Warnings)
	}
	if got := gitIn(t, p, "rev-parse", o.Record.Ref); got != o.Record.Commit {
		t.Errorf("ref points at %s, want %s", got, o.Record.Commit)
	}
}

// fileMode is the permission bits of path (not following a link).
func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// registered says git lists dest as a worktree of the repository at dir.
func registered(t *testing.T, dir, dest string) bool {
	t.Helper()
	want := "worktree " + cleanup.ResolvePath(dest)
	for _, l := range strings.Split(gitIn(t, dir, "worktree", "list", "--porcelain"), "\n") {
		if l == want {
			return true
		}
	}
	return false
}

// recordWarnings are the record_not_written warnings of o.
func recordWarnings(o Outcome) []string {
	var got []string
	for _, w := range o.Plan.Warnings {
		if strings.HasPrefix(w, RecordWarningPrefix) {
			got = append(got, w)
		}
	}
	return got
}

// TestExecuteIntoCarriesVerifiesAndRecords checks root mode end to end up to
// the record: a dirty root's plan carried by ExecuteInto into a detached
// worktree made by CreateDetachedAt is moved, the destination's status is
// byte-equal to the root's, modes are kept, a Record is written, Completed
// lists the carry, verify and record steps but no "created branch" step, the
// destination stays detached and the root is unchanged. Why: `wt restore-root`
// creates the destination itself (the branch already exists in the root), so
// rescue must carry, verify and record into a worktree it did not create,
// with the same proof `wt rescue` gives.
func TestExecuteIntoCarriesVerifiesAndRecords(t *testing.T) {
	f := newFixture(t)
	f.rootOnTopic()
	f.dirtyRoot()
	eng, plan, dest := f.rootInto()
	before := snapshot(t, f.Dir)

	o := eng.ExecuteInto(plan, dest)

	if o.Status != StatusMoved {
		t.Fatalf("status %s (%s): %s", o.Status, o.Code, o.Reason)
	}
	if o.Record == nil {
		t.Fatalf("no record; warnings %q", o.Plan.Warnings)
	}
	if got := gitIn(t, f.Dir, "rev-parse", o.Record.Ref); got != o.Record.Commit {
		t.Errorf("record ref points at %s, want %s", got, o.Record.Commit)
	}
	if got := trailer(t, f.Dir, o.Record.Commit, "Wt-Dest-Branch"); got != "topic" {
		t.Errorf("Wt-Dest-Branch = %q, want topic", got)
	}
	if got := trailer(t, f.Dir, o.Record.Commit, "Wt-Dest-Path"); got != dest {
		t.Errorf("Wt-Dest-Path = %q, want %q", got, dest)
	}
	got, err := sgit{dir: dest}.out(nil, cleanup.StatusArgs()...)
	if err != nil || string(got) != before.status {
		t.Errorf("destination status differs (err %v):\n%q\n%q", err, got, before.status)
	}
	for _, rel := range []string{"work.txt", "run.sh"} {
		if m := fileMode(t, filepath.Join(dest, rel)); m != 0o755 {
			t.Errorf("%s has mode %o, want 755", rel, m)
		}
	}
	if out, err := execGit(dest, "symbolic-ref", "-q", "HEAD").CombinedOutput(); err == nil {
		t.Errorf("the destination is on a branch (%s), want detached", out)
	}
	if !o.Created || o.Plan.DestPath != dest {
		t.Errorf("Created %v, DestPath %q, want true and %q", o.Created, o.Plan.DestPath, dest)
	}
	want := []string{"carried staged changes", "carried unstaged changes", "copied 2 untracked files", "verified status and file contents against the source", "recorded the source at " + o.Record.Ref}
	if strings.Join(o.Completed, "|") != strings.Join(want, "|") {
		t.Errorf("Completed = %q, want %q", o.Completed, want)
	}
	if d := before.diff(snapshot(t, f.Dir)); d != "" {
		t.Errorf("the root changed (%s)", d)
	}
	if got := f.git("symbolic-ref", "HEAD"); got != "refs/heads/topic" {
		t.Errorf("the root is on %s", got)
	}
}

// TestExecuteIntoFailureRows checks each way ExecuteInto stops short or
// loses only the record: (a) a carry failure is failed/carry_failed, (b) a
// root edit during the copy is failed/source_changed_while_copying, (c) a
// record ref that cannot be created (a ref-path conflict made in
// beforeRecordRef) is moved with one record_not_written warning and no
// Record, (d) a destination edit after verify is moved with no warning and a
// record (the engine verified what it carried and does not re-read), and
// DestinationMatches then names the edit. Each keeps the destination
// registered and leaves the root as it was (in (b), as the session left it).
// Why: rootmove's recovery table rows 2, 3 and 7 rest on exactly these
// outcomes; a failure that removed the destination or touched the root, or a
// moved outcome hiding a missing record, would break the release's proof.
func TestExecuteIntoFailureRows(t *testing.T) {
	cases := []struct {
		name   string
		arm    func(f *fixture, p *Plan, dest string, expect *snap)
		status Status
		code   string
		record bool
		check  func(t *testing.T, f *fixture, p *Plan, dest string, o Outcome)
	}{
		{"carry failure", func(f *fixture, p *Plan, dest string, expect *snap) {
			p.untracked = append(p.untracked, "ghost.txt") // vanished after the check
		}, StatusFailed, CodeCarryFailed, false, func(t *testing.T, f *fixture, p *Plan, dest string, o Outcome) {
			if !strings.Contains(o.Reason, "(the new worktree was kept at "+dest+")") {
				t.Errorf("reason %q does not keep the new worktree at %s", o.Reason, dest)
			}
		}},
		{"root edited while copying", func(f *fixture, p *Plan, dest string, expect *snap) {
			afterCarry = func(*Plan) {
				f.write(f.Dir, "README", "a session edited this")
				*expect = snapshot(f.t, f.Dir)
			}
		}, StatusFailed, CodeSourceChangedMid, false, func(t *testing.T, f *fixture, p *Plan, dest string, o Outcome) {
			if !strings.Contains(o.Reason, dest) {
				t.Errorf("reason %q does not name %s", o.Reason, dest)
			}
		}},
		{"record ref cannot be created", func(f *fixture, p *Plan, dest string, expect *snap) {
			beforeRecordRef = func(*Plan) {
				// refs/wt/rescued as a ref blocks every ref below it.
				f.git("update-ref", "refs/wt/rescued", f.git("rev-parse", "HEAD"))
			}
		}, StatusMoved, "", false, func(t *testing.T, f *fixture, p *Plan, dest string, o Outcome) {
			if w := recordWarnings(o); len(w) != 1 {
				t.Errorf("record warnings %q, want one", w)
			}
			if got := f.git("for-each-ref", "refs/wt/rescued/"); got != "" {
				t.Errorf("record refs: %s", got)
			}
		}},
		{"destination edited after verify", func(f *fixture, p *Plan, dest string, expect *snap) {
			afterVerify = func(*Plan) { f.write(dest, "README", "edited in the destination") }
		}, StatusMoved, "", true, func(t *testing.T, f *fixture, p *Plan, dest string, o Outcome) {
			if w := recordWarnings(o); len(w) != 0 {
				t.Errorf("record warnings %q, want none", w)
			}
			d, err := p.DestinationMatches(dest)
			if err != nil || !strings.Contains(d, "README") {
				t.Errorf("DestinationMatches = %q, %v; want the README edit named", d, err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(func() {
				afterCarry, afterVerify, beforeRecordRef = func(*Plan) {}, func(*Plan) {}, func(*Plan) {}
			})
			f := newFixture(t)
			f.rootOnTopic()
			f.dirtyRoot()
			eng, plan, dest := f.rootInto()
			expect := snapshot(t, f.Dir)
			c.arm(f, plan, dest, &expect)

			o := eng.ExecuteInto(plan, dest)

			if o.Status != c.status || o.Code != c.code || (o.Record != nil) != c.record {
				t.Fatalf("outcome %s/%s record %v, want %s/%s record %v: %s", o.Status, o.Code, o.Record, c.status, c.code, c.record, o.Reason)
			}
			if !o.Created || o.Plan.DestPath != dest {
				t.Errorf("Created %v, DestPath %q", o.Created, o.Plan.DestPath)
			}
			if _, err := os.Stat(dest); err != nil || !registered(t, f.Dir, dest) {
				t.Errorf("the destination was not kept: %v", err)
			}
			if d := expect.diff(snapshot(t, f.Dir)); d != "" {
				t.Errorf("the root changed (%s)", d)
			}
			c.check(t, f, plan, dest, o)
		})
	}
}

// TestPlanDestinationMatches checks DestinationMatches is "" for a
// destination ExecuteInto just verified and names a difference after a
// content edit, an extra untracked file and a restage there; and that a
// plan with no dirt is "" against an untouched detached worktree and
// non-empty once that worktree has a new file. Why: rootmove's read-back
// re-asks this exact comparison after the release and the switch, and a
// clean root (no copy) still needs a clean destination asserted.
func TestPlanDestinationMatches(t *testing.T) {
	edits := []struct {
		name string
		edit func(f *fixture, dest string)
	}{
		{"none", func(*fixture, string) {}},
		{"content edit", func(f *fixture, dest string) { f.write(dest, "staged.txt", "changed") }},
		{"extra untracked file", func(f *fixture, dest string) { f.write(dest, "extra.txt", "x") }},
		{"restage", func(f *fixture, dest string) { gitIn(f.t, dest, "add", "README") }},
	}
	for _, e := range edits {
		t.Run(e.name, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnTopic()
			f.dirtyRoot()
			eng, plan, dest := f.rootInto()
			if o := eng.ExecuteInto(plan, dest); o.Status != StatusMoved {
				t.Fatalf("status %s (%s): %s", o.Status, o.Code, o.Reason)
			}
			e.edit(f, dest)
			d, err := plan.DestinationMatches(dest)
			if err != nil {
				t.Fatal(err)
			}
			if (d == "") != (e.name == "none") {
				t.Errorf("DestinationMatches = %q", d)
			}
		})
	}
	t.Run("no dirt", func(t *testing.T) {
		f := newFixture(t)
		f.rootOnTopic()
		_, plan, dest := f.rootInto()
		if plan.HasDirt() {
			t.Fatalf("plan has dirt: %+v", plan.Counts)
		}
		if d, err := plan.DestinationMatches(dest); err != nil || d != "" {
			t.Errorf("untouched: DestinationMatches = %q, %v", d, err)
		}
		f.write(dest, "new.txt", "n")
		if d, err := plan.DestinationMatches(dest); err != nil || d == "" {
			t.Errorf("with a new file: DestinationMatches = %q, %v; want a difference", d, err)
		}
	})
}

// TestExecuteRefusesADetachedPlan checks Execute given a root-mode plan
// fails with create_failed, says to use ExecuteInto and creates nothing (no
// directory, no ref, no worktree). Why: Execute creates a new branch, which
// a root plan names as the root's existing one; it must never get as far as
// git worktree add with it.
func TestExecuteRefusesADetachedPlan(t *testing.T) {
	f := newFixture(t)
	f.rootOnTopic()
	f.dirtyRoot()
	eng, row := f.rootEngine()
	plan, err := eng.Preflight(row, "")
	if err != nil {
		t.Fatal(err)
	}
	before, world := snapshot(t, f.Dir), f.world()

	o := eng.Execute(plan)

	if o.Status != StatusFailed || o.Code != CodeCreateFailed || o.Created || o.BranchKept {
		t.Fatalf("outcome = %+v", o)
	}
	if !strings.Contains(o.Reason, "a root plan is executed with ExecuteInto") {
		t.Errorf("reason %q", o.Reason)
	}
	if _, err := os.Lstat(plan.DestPath); err == nil {
		t.Errorf("%s was created", plan.DestPath)
	}
	if w := f.world(); w != world {
		t.Errorf("refs or worktrees changed:\n%s\n--\n%s", world, w)
	}
	if d := before.diff(snapshot(t, f.Dir)); d != "" {
		t.Errorf("the root changed (%s)", d)
	}
}

// TestExecuteUnchangedForWorktrees checks a worktree rescue through the split
// Execute still starts Completed with the "created branch" step and still
// keeps "the new worktree and branch" on a carry failure. Why: Execute became
// start re-check + CreateAt + the shared carry; `wt rescue`'s observable
// output must not change (the rest of this file covers the rest of it).
func TestExecuteUnchangedForWorktrees(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "u.txt", "untracked")
	_, o := rescueOnce(t, f, p)
	if o.Status != StatusMoved || len(o.Completed) == 0 || !strings.HasPrefix(o.Completed[0], "created branch rescued/topic and its worktree at ") {
		t.Fatalf("outcome = %+v", o)
	}

	g := newFixture(t)
	q := g.Worktree("topic")
	g.Commit(q, "work.txt", "work", "work")
	g.write(q, "u.txt", "untracked")
	eng, find := g.engine(g.Dir)
	plan, err := eng.Preflight(find(q), "")
	if err != nil {
		t.Fatal(err)
	}
	plan.untracked = append(plan.untracked, "ghost.txt")
	o = eng.Execute(plan)
	if o.Code != CodeCarryFailed || !strings.HasSuffix(o.Reason, "(the new worktree and branch were kept at "+o.Plan.DestPath+")") {
		t.Fatalf("outcome = %+v", o)
	}
}

// TestExecuteIntoWithWorktreesDirNotIgnored records what root mode does
// today when the repository does not ignore .worktrees/, where the
// destination is created inside the root: (1) a dirty root whose plan was
// made before the destination existed; (2) a clean root, read with
// SourceUnchanged once the destination exists (the no-copy release path);
// (3) a root that already holds another linked worktree under .worktrees/,
// at Preflight. It asserts the behavior observed, not a wanted one. Why: an
// open design risk for PR B: the destination's directory shows up in the
// root's own status, so the source no longer reads as it did when checked.
func TestExecuteIntoWithWorktreesDirNotIgnored(t *testing.T) {
	t.Run("dirty root", func(t *testing.T) {
		f := newFixtureExcluding(t, false)
		f.rootOnTopic()
		f.write(f.Dir, "README", "edited")
		eng, plan, dest := f.rootInto()
		// The new worktree is an untracked directory of the root now.
		if d, err := plan.SourceUnchanged(); err != nil || d != "the status" {
			t.Errorf("SourceUnchanged after CreateDetachedAt = %q, %v", d, err)
		}

		o := eng.ExecuteInto(plan, dest)

		// Observed: the after-copy fingerprint sees ".worktrees/topic/" as a
		// new untracked entry, so the copy is reported stale and kept.
		if o.Status != StatusFailed || o.Code != CodeSourceChangedMid || !strings.Contains(o.Reason, "the status") {
			t.Errorf("outcome %s/%s: %s", o.Status, o.Code, o.Reason)
		}
		if o.Record != nil {
			t.Errorf("record written: %+v", o.Record)
		}
		if _, err := os.Stat(dest); err != nil {
			t.Errorf("destination not kept: %v", err)
		}
	})
	t.Run("clean root", func(t *testing.T) {
		f := newFixtureExcluding(t, false)
		f.rootOnTopic()
		_, plan, _ := f.rootInto()
		// Observed: the no-copy path's unchanged-fingerprint proof fails too.
		if d, err := plan.SourceUnchanged(); err != nil || d != "the status" {
			t.Errorf("SourceUnchanged = %q, %v", d, err)
		}
	})
	t.Run("another worktree under .worktrees", func(t *testing.T) {
		f := newFixtureExcluding(t, false)
		f.Worktree("other")
		f.rootOnTopic()
		f.write(f.Dir, "README", "edited")
		eng, row := f.rootEngine()

		plan, err := eng.Preflight(row, "")

		// Observed: Preflight refuses; the linked worktree reads as a nested
		// repository among the root's untracked files.
		var r *Refusal
		if !errors.As(err, &r) || r.Code != CodeNestedRepo {
			t.Errorf("Preflight = %+v, %v; want nested_repo", plan, err)
		}
	})
}
