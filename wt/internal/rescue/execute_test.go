package rescue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
			got, err := sgit{dir: dest}.out(nil, statusArgs...)
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
