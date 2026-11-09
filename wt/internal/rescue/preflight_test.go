package rescue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// preflightCase is one source state and what Preflight must answer.
type preflightCase struct {
	name  string
	setup func(f *fixture) string // returns the source worktree's path
	want  string                  // refusal code
}

// workTree is a worktree on branch "topic" with one unpushed commit, so it
// has something to keep whatever else a case adds.
func workTree(f *fixture) string {
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	return p
}

// TestPreflightRefusals checks that each state with no faithful copy (and
// each ineligible row) is refused with its stable code, and that a refusal
// changes nothing: the source's HEAD, refs, index bytes, status and file
// contents, and the set of refs and worktrees, are identical afterwards.
// Why: every refusal must happen before anything is created, and reading the
// source must not touch it (git status may rewrite the index unless optional
// locks are off).
func TestPreflightRefusals(t *testing.T) {
	cases := []preflightCase{
		{"nothing to keep", func(f *fixture) string { return f.Worktree("topic") }, CodeNothingToKeep},
		{"detached with nothing to keep", func(f *fixture) string { return f.Detached("d") }, CodeNothingToKeep},
		{"primary checkout", func(f *fixture) string {
			f.write(f.Dir, "dirty.txt", "x")
			return f.Dir
		}, CodePrimary},
		{"unmerged paths", func(f *fixture) string {
			p := workTree(f)
			h := gitIn(f.t, p, "hash-object", "-w", "work.txt")
			exec_(f.t, p, "100644 "+h+" 1\tconf\n100644 "+h+" 2\tconf\n100644 "+h+" 3\tconf\n", "update-index", "--index-info")
			return p
		}, CodeUnmerged},
		{"assume-unchanged file", func(f *fixture) string {
			p := workTree(f)
			gitIn(f.t, p, "update-index", "--assume-unchanged", "work.txt")
			return p
		}, CodeHiddenIndex},
		{"skip-worktree file", func(f *fixture) string {
			p := workTree(f)
			gitIn(f.t, p, "update-index", "--skip-worktree", "work.txt")
			return p
		}, CodeHiddenIndex},
		{"submodule change", func(f *fixture) string {
			p := workTree(f)
			gitIn(f.t, p, "update-index", "--add", "--cacheinfo", "160000,"+f.git("rev-parse", "HEAD")+",sub")
			return p
		}, CodeSubmodule},
		{"staged and modified", func(f *fixture) string {
			p := workTree(f)
			f.write(p, "work.txt", "staged")
			gitIn(f.t, p, "add", "work.txt")
			f.write(p, "work.txt", "staged then edited")
			return p
		}, CodeStagedAndEdited},
		{"intent to add", func(f *fixture) string {
			p := workTree(f)
			f.write(p, "new.txt", "new")
			gitIn(f.t, p, "add", "-N", "new.txt")
			return p
		}, CodeIntentToAdd},
		{"nested repository", func(f *fixture) string {
			p := workTree(f)
			gitIn(f.t, p, "init", "-q", filepath.Join(p, "inner"))
			return p
		}, CodeNestedRepo},
		{"CRLF in a text=auto repository", func(f *fixture) string {
			p := workTree(f)
			f.Commit(p, ".gitattributes", "* text=auto\n", "attrs")
			f.write(p, "crlf.txt", "l1\r\nl2\r\n")
			gitIn(f.t, p, "add", "crlf.txt")
			return p
		}, CodeLineEndings},
		{"eol=crlf attribute", func(f *fixture) string {
			p := workTree(f)
			f.Commit(p, ".gitattributes", "* text=auto eol=crlf\n", "attrs")
			f.write(p, "work.txt", "work\nmore\n")
			return p
		}, CodeLineEndings},
		{"clean/smudge filter on a changed file", func(f *fixture) string {
			p := workTree(f)
			f.git("config", "filter.shout.clean", "cat")
			f.git("config", "filter.shout.smudge", "cat")
			f.Commit(p, ".gitattributes", "*.dat filter=shout\n", "attrs")
			f.Commit(p, "a.dat", "one", "data")
			f.write(p, "a.dat", "two")
			return p
		}, CodeContentFilter},
		{"filter=lfs on a staged file", func(f *fixture) string {
			p := workTree(f)
			f.Commit(p, ".gitattributes", "*.bin filter=lfs diff=lfs merge=lfs -text\n", "attrs")
			f.write(p, "big.bin", "payload")
			gitIn(f.t, p, "add", "-f", "big.bin")
			return p
		}, CodeContentFilter},
		{"branch name taken", func(f *fixture) string {
			p := workTree(f)
			f.git("branch", "rescued/topic")
			return p
		}, CodeNameUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			src := c.setup(f)
			eng, find := f.engine(src)
			row := find(src)
			before := snapshot(t, src)
			world := f.world()

			p, err := eng.Preflight(row, "")
			var r *Refusal
			if !errors.As(err, &r) || r.Code != c.want {
				t.Fatalf("Preflight = %v, %v; want a refusal %q", p, err, c.want)
			}
			if d := before.diff(snapshot(t, src)); d != "" {
				t.Errorf("the source changed (%s)", d)
			}
			if w := f.world(); w != world {
				t.Errorf("refs or worktrees changed:\n%s\n--\n%s", world, w)
			}
		})
	}
}

// exec_ runs git in dir with stdin and fails the test on error.
func exec_(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := execGit(dir, args...)
	cmd.Stdin = strings.NewReader(stdin)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return string(b)
}

// TestPreflightRefusesAnOperationInProgress checks a worktree stopped in the
// middle of a rebase is refused with the operation code. Why: the work is in
// git's own state files, which a copy cannot reproduce.
func TestPreflightRefusesAnOperationInProgress(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "a.txt", "one", "one")
	f.Commit(f.Dir, "a.txt", "two", "two")
	if out, err := execGit(p, "rebase", "main").CombinedOutput(); err == nil {
		t.Fatalf("the rebase was meant to stop on a conflict:\n%s", out)
	}
	eng, find := f.engine(p)

	_, err := eng.Preflight(find(p), "")
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeOperation {
		t.Fatalf("Preflight = %v, want a refusal %q", err, CodeOperation)
	}
}

// TestPreflightRefusesABranchRowAndPointsAtDashW checks a branch row (no
// worktree) is refused with the not-a-worktree code and the message names
// `wt -W`. Why: such a branch has no uncommitted work, and -W already gives
// it a worktree.
func TestPreflightRefusesABranchRowAndPointsAtDashW(t *testing.T) {
	f := newFixture(t)
	eng, _ := f.engine(f.Dir)

	_, err := eng.Preflight(cleanup.Row{Kind: cleanup.KindBranch, Name: "lone", Ref: "refs/heads/lone"}, "")
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeNotWorktree || !strings.Contains(r.Message, "wt -W lone") {
		t.Fatalf("Preflight = %v, want not_worktree naming `wt -W lone`", err)
	}
}

// TestPreflightRefusesTooMuchUntracked checks the size limits fail closed
// with a message naming what to do. Why: an untracked build directory that
// was never ignored would otherwise be copied silently.
func TestPreflightRefusesTooMuchUntracked(t *testing.T) {
	for _, tc := range []struct {
		name         string
		files, bytes int
	}{{"files", 2, 1 << 20}, {"bytes", 100, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := workTree(f)
			f.write(p, "u/a", "12345")
			f.write(p, "u/b", "12345")
			f.write(p, "u/c", "12345")
			oldF, oldB := limitFiles, limitBytes
			limitFiles, limitBytes = tc.files, int64(tc.bytes)
			t.Cleanup(func() { limitFiles, limitBytes = oldF, oldB })
			eng, find := f.engine(p)

			_, err := eng.Preflight(find(p), "")
			var r *Refusal
			if !errors.As(err, &r) || r.Code != CodeTooLarge {
				t.Fatalf("Preflight = %v, want too_large", err)
			}
		})
	}
}

// TestPreflightAllowsCurrentAndLockedSources checks the worktree the caller
// stands in and a locked one are accepted. Why: those block removal, not
// reading, and the first is the likeliest rescue ("I am in the worktree whose
// work I want to keep").
func TestPreflightAllowsCurrentAndLockedSources(t *testing.T) {
	for _, mode := range []string{"current", "locked"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			p := workTree(f)
			f.write(p, "scratch.txt", "x")
			cwd := f.Dir
			if mode == "current" {
				cwd = p
			} else {
				f.git("worktree", "lock", "--reason", "fixture", p)
			}
			eng, find := f.engine(cwd)

			plan, err := eng.Preflight(find(p), "")
			if err != nil {
				t.Fatal(err)
			}
			if plan.Branch != "rescued/topic" || plan.Counts.Commits != 1 || plan.Counts.Untracked != 1 {
				t.Errorf("plan = %+v", plan)
			}
		})
	}
}

// TestPreflightNamesTheDestination checks the default and the --to names: a
// branch worktree gets rescued/<branch>, a detached one rescued/<directory>,
// and an asked-for name is used as given, flattened into .worktrees. Why: the
// plan is what the user confirms, so it must show the real destination.
func TestPreflightNamesTheDestination(t *testing.T) {
	f := newFixture(t)
	p := f.Detached("scratchdir")
	f.write(p, "x.txt", "x")
	eng, find := f.engine(f.Dir)

	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "rescued/scratchdir" || plan.DestPath != filepath.Join(cleanup.ResolvePath(f.Dir), ".worktrees", "rescued-scratchdir") {
		t.Errorf("default: branch %q at %q", plan.Branch, plan.DestPath)
	}
	if plan.Start != plan.Source.Head || plan.Source.Ref != "" {
		t.Errorf("start %q, source %+v", plan.Start, plan.Source)
	}
	plan, err = eng.Preflight(find(p), "keep/me")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "keep/me" || plan.DestPath != filepath.Join(cleanup.ResolvePath(f.Dir), ".worktrees", "keep-me") {
		t.Errorf("--to: branch %q at %q", plan.Branch, plan.DestPath)
	}
	if _, err := os.Stat(plan.DestPath); err == nil {
		t.Error("Preflight created the destination")
	}
}

// TestCheckUntrackedRefusesWhatIsNotAFileOrLink checks a fifo (which git
// itself never lists, so it normally cannot reach the plan) and an unsafe
// path are refused if one ever does. Why: the copy opens each file, and
// opening a fifo would block forever.
func TestCheckUntrackedRefusesWhatIsNotAFileOrLink(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	for _, rel := range []string{"pipe", "../out", "a/../../b", ".git/hooks/x", "/abs"} {
		err := Engine{}.checkUntracked(dir, rel, &Plan{})
		var r *Refusal
		if !errors.As(err, &r) || r.Code != CodeSpecialFile {
			t.Errorf("checkUntracked(%q) = %v, want special_file", rel, err)
		}
	}
}

// TestPreflightStillAcceptsASecondRescueOfAPreservedOriginal checks that after
// one real rescue the original reads preserved, yet Preflight with a distinct
// --to name still returns a plan (no nothing_to_keep) and executing it writes
// a second, distinct record ref. Why: Preflight reads the raw losses, so
// the user can still keep a second copy of work a record already covers.
func TestPreflightStillAcceptsASecondRescueOfAPreservedOriginal(t *testing.T) {
	f := newFixture(t)
	p := workTree(f)
	if _, out := rescueOnce(t, f, p); out.Status != StatusMoved {
		t.Fatalf("first rescue: %+v", out)
	}
	if n := len(strings.Fields(f.git("for-each-ref", "--format=%(refname)", "refs/wt/rescued"))); n != 1 {
		t.Fatalf("after the first rescue %d record refs, want 1", n)
	}

	eng, find := f.engine(f.Dir)
	row := find(p)
	if row.Preserved == nil {
		t.Fatalf("original is not preserved after a rescue: %+v", row)
	}
	plan, err := eng.Preflight(row, "keep/again")
	if err != nil {
		t.Fatalf("second Preflight: %v", err)
	}
	if plan.Branch != "keep/again" {
		t.Errorf("plan branch = %q, want keep/again", plan.Branch)
	}
	if out := eng.Execute(plan); out.Status != StatusMoved {
		t.Fatalf("second rescue: %+v", out)
	}
	if n := len(strings.Fields(f.git("for-each-ref", "--format=%(refname)", "refs/wt/rescued"))); n != 2 {
		t.Errorf("after the second rescue %d record refs, want 2:\n%s", n, rescuedRefs(t, f))
	}
}

// wantRefusal fails the test unless err is a *Refusal with code.
func wantRefusal(t *testing.T, p *Plan, err error, code string) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || r.Code != code {
		t.Fatalf("Preflight = %+v, %v; want a refusal %q", p, err, code)
	}
	return r
}

// TestPreflightForRootAcceptsPrimary checks root mode takes the primary
// checkout on a branch with a commit, a staged, an unstaged and an untracked
// file: the plan keeps the branch itself (Branch "topic", a detached
// destination), names the directory after it or after --to, counts the work,
// and nothing is created or touched. Why: `wt restore-root` moves the same
// branch, so the plan must name the real branch (the record trailer
// Wt-Dest-Branch reads it) and a detached directory, never rescued/<branch>.
func TestPreflightForRootAcceptsPrimary(t *testing.T) {
	f := newFixture(t)
	f.rootOnTopic()
	f.write(f.Dir, "staged.txt", "s")
	f.git("add", "staged.txt")
	f.write(f.Dir, "README", "edited")
	f.write(f.Dir, "new.txt", "n")
	eng, row := f.rootEngine()
	before, world := snapshot(t, f.Dir), f.world()

	for _, tc := range []struct{ name, dir string }{{"", "topic"}, {"spike", "spike"}, {"a/b", "a/b"}} {
		plan, err := eng.Preflight(row, tc.name)
		if err != nil {
			t.Fatalf("Preflight(%q): %v", tc.name, err)
		}
		if !plan.Detached || plan.Branch != "topic" || plan.DirName != tc.dir {
			t.Errorf("Preflight(%q): detached %v, branch %q, dir %q", tc.name, plan.Detached, plan.Branch, plan.DirName)
		}
		if want := filepath.Join(cleanup.ResolvePath(f.Dir), ".worktrees", strings.ReplaceAll(tc.dir, "/", "-")); plan.DestPath != want {
			t.Errorf("Preflight(%q): DestPath %q, want %q", tc.name, plan.DestPath, want)
		}
		if c := plan.Counts; c.Commits != 1 || c.Staged != 1 || c.Unstaged != 1 || c.Untracked != 1 {
			t.Errorf("Preflight(%q): counts %+v", tc.name, c)
		}
		if !plan.HasDirt() || plan.Start != plan.Source.Head || plan.Source.Ref != "refs/heads/topic" {
			t.Errorf("Preflight(%q): plan %+v", tc.name, plan)
		}
		if _, err := os.Lstat(plan.DestPath); err == nil {
			t.Errorf("Preflight(%q) created %s", tc.name, plan.DestPath)
		}
	}
	if d := before.diff(snapshot(t, f.Dir)); d != "" {
		t.Errorf("the root changed (%s)", d)
	}
	if w := f.world(); w != world {
		t.Errorf("refs or worktrees changed:\n%s\n--\n%s", world, w)
	}
}

// TestPreflightForRootCleanRootIsNotNothingToKeep checks a clean root on a
// fully pushed branch, and one with unpushed commits only, gets a plan with
// no dirt in root mode, while the same row without ForRoot is still refused
// as the primary checkout. Why: moving the branch off the root is the point
// even when nothing is uncommitted (Review Focus 2), and the zero value must
// keep `wt rescue` exactly as it was.
func TestPreflightForRootCleanRootIsNotNothingToKeep(t *testing.T) {
	for _, pushed := range []bool{true, false} {
		t.Run(map[bool]string{true: "pushed", false: "unpushed"}[pushed], func(t *testing.T) {
			f := newFixture(t)
			f.rootOnTopic()
			if pushed {
				f.git("push", "-q", "-u", "origin", "topic")
			}
			eng, row := f.rootEngine()

			plan, err := eng.Preflight(row, "")
			if err != nil {
				t.Fatalf("ForRoot: %v", err)
			}
			if plan.HasDirt() || plan.Branch != "topic" || !plan.Detached {
				t.Errorf("plan %+v", plan)
			}
			if want := map[bool]int{true: 0, false: 1}[pushed]; plan.Counts.Commits != want {
				t.Errorf("commits %d, want %d", plan.Counts.Commits, want)
			}
			eng.ForRoot = false
			p, err := eng.Preflight(row, "")
			wantRefusal(t, p, err, CodePrimary)
		})
	}
}

// TestPreflightForRootKeepsEveryOtherRefusal checks each state rescue refuses
// in a worktree is refused with the same code in the root, and the root, its
// refs and the worktrees are untouched. Why: root mode lifts only `primary`
// and `nothing_to_keep`; a root with no faithful copy must stop before
// anything exists, exactly as a worktree does.
func TestPreflightForRootKeepsEveryOtherRefusal(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		to    string
		want  string
	}{
		{"operation in progress", func(f *fixture) {
			side := f.Worktree("side")
			f.Commit(f.Dir, "a.txt", "one", "one")
			f.Commit(side, "a.txt", "two", "two")
			if out, err := execGit(f.Dir, "merge", "side").CombinedOutput(); err == nil {
				f.t.Fatalf("the merge was meant to stop on a conflict:\n%s", out)
			}
		}, "", CodeOperation},
		{"unmerged paths", func(f *fixture) {
			h := gitIn(f.t, f.Dir, "hash-object", "-w", "work.txt")
			exec_(f.t, f.Dir, "100644 "+h+" 1\tconf\n100644 "+h+" 2\tconf\n100644 "+h+" 3\tconf\n", "update-index", "--index-info")
		}, "", CodeUnmerged},
		{"assume-unchanged file", func(f *fixture) { f.git("update-index", "--assume-unchanged", "work.txt") }, "", CodeHiddenIndex},
		{"submodule change", func(f *fixture) {
			f.git("update-index", "--add", "--cacheinfo", "160000,"+f.git("rev-parse", "HEAD")+",sub")
		}, "", CodeSubmodule},
		{"staged and modified", func(f *fixture) {
			f.write(f.Dir, "work.txt", "staged")
			f.git("add", "work.txt")
			f.write(f.Dir, "work.txt", "staged then edited")
		}, "", CodeStagedAndEdited},
		{"intent to add", func(f *fixture) {
			f.write(f.Dir, "new.txt", "new")
			f.git("add", "-N", "new.txt")
		}, "", CodeIntentToAdd},
		{"nested repository", func(f *fixture) { f.git("init", "-q", filepath.Join(f.Dir, "inner")) }, "", CodeNestedRepo},
		{"too large", func(f *fixture) {
			f.write(f.Dir, "u/a", "12345")
			f.write(f.Dir, "u/b", "12345")
			oldF := limitFiles
			limitFiles = 1
			f.t.Cleanup(func() { limitFiles = oldF })
		}, "", CodeTooLarge},
		{"line endings", func(f *fixture) {
			f.Commit(f.Dir, ".gitattributes", "* text=auto eol=crlf\n", "attrs")
			f.write(f.Dir, "work.txt", "work\nmore\n")
		}, "", CodeLineEndings},
		{"content filter", func(f *fixture) {
			f.git("config", "filter.shout.clean", "cat")
			f.git("config", "filter.shout.smudge", "cat")
			f.Commit(f.Dir, ".gitattributes", "*.dat filter=shout\n", "attrs")
			f.Commit(f.Dir, "a.dat", "one", "data")
			f.write(f.Dir, "a.dat", "two")
		}, "", CodeContentFilter},
		{"directory taken", func(f *fixture) {
			if err := os.MkdirAll(filepath.Join(f.Dir, ".worktrees", "topic"), 0o755); err != nil {
				f.t.Fatal(err)
			}
		}, "", CodeNameUnavailable},
		{"--to name taken", func(f *fixture) { f.Worktree("spike") }, "spike", CodeNameUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnTopic()
			c.setup(f)
			eng, row := f.rootEngine()
			before, world := snapshot(t, f.Dir), f.world()

			p, err := eng.Preflight(row, c.to)
			wantRefusal(t, p, err, c.want)
			if d := before.diff(snapshot(t, f.Dir)); d != "" {
				t.Errorf("the root changed (%s)", d)
			}
			if w := f.world(); w != world {
				t.Errorf("refs or worktrees changed:\n%s\n--\n%s", world, w)
			}
		})
	}
	// special_file: git never lists a fifo, so no source state reaches it
	// through Preflight; the check it runs is the same in root mode.
	t.Run("special file", func(t *testing.T) {
		dir := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
			t.Skip("mkfifo:", err)
		}
		err := Engine{ForRoot: true}.checkUntracked(dir, "pipe", &Plan{})
		var r *Refusal
		if !errors.As(err, &r) || r.Code != CodeSpecialFile {
			t.Errorf("checkUntracked = %v, want special_file", err)
		}
	})
}

// TestPreflightForRootRefusesDetachedRoot checks root mode refuses a primary
// checkout with a detached HEAD as not_worktree, and a linked worktree as
// not_worktree too, creating and changing nothing. Why: the move keeps a
// branch, and a detached root has none; a worktree is `wt rescue`'s source,
// never a root move's.
func TestPreflightForRootRefusesDetachedRoot(t *testing.T) {
	f := newFixture(t)
	f.git("switch", "-q", "--detach")
	f.Commit(f.Dir, "work.txt", "work", "work")
	f.write(f.Dir, "new.txt", "n")
	eng, row := f.rootEngine()
	before, world := snapshot(t, f.Dir), f.world()

	p, err := eng.Preflight(row, "")
	if r := wantRefusal(t, p, err, CodeNotWorktree); !strings.Contains(r.Message, "a detached root has no branch to keep") {
		t.Errorf("message %q", r.Message)
	}
	if d := before.diff(snapshot(t, f.Dir)); d != "" {
		t.Errorf("the root changed (%s)", d)
	}
	if w := f.world(); w != world {
		t.Errorf("refs or worktrees changed:\n%s\n--\n%s", world, w)
	}

	g := newFixture(t)
	wt := workTree(g)
	geng, find := g.engine(g.Dir)
	geng.ForRoot = true
	p, err = geng.Preflight(find(wt), "")
	wantRefusal(t, p, err, CodeNotWorktree)
}

// TestPreflightForRootAcceptsStagedDeleteWithUntrackedTwin checks a root
// with `git rm --cached foo` and foo left on disk gets a plan counting one
// staged and one untracked path. Why: rescue accepts this state and carries
// both halves faithfully; root mode must not refuse what rescue copies (the
// release later skips the twin the reset restores, Review Focus 3).
func TestPreflightForRootAcceptsStagedDeleteWithUntrackedTwin(t *testing.T) {
	f := newFixture(t)
	f.rootOnTopic()
	f.git("rm", "-q", "--cached", "work.txt")
	eng, row := f.rootEngine()

	plan, err := eng.Preflight(row, "")
	if err != nil {
		t.Fatal(err)
	}
	if c := plan.Counts; c.Staged != 1 || c.Untracked != 1 || c.Unstaged != 0 {
		t.Errorf("counts %+v", c)
	}
	if got := plan.UntrackedPaths(); len(got) != 1 || got[0] != "work.txt" {
		t.Errorf("UntrackedPaths = %q", got)
	}
}

// TestPlanAccessors checks UntrackedPaths returns a copy of the carried list
// and SourceUnchanged is "" for an untouched source and names the change
// after an edit. Why: removal's ReleaseRoot removes exactly the carried
// untracked paths and refuses unless the source still matches what was
// verified, so neither may be altered by a caller or blind to an edit.
func TestPlanAccessors(t *testing.T) {
	f := newFixture(t)
	f.rootOnTopic()
	f.write(f.Dir, "new.txt", "n")
	f.write(f.Dir, "README", "edited")
	eng, row := f.rootEngine()
	plan, err := eng.Preflight(row, "")
	if err != nil {
		t.Fatal(err)
	}

	got := plan.UntrackedPaths()
	if len(got) != 1 || got[0] != "new.txt" {
		t.Fatalf("UntrackedPaths = %q", got)
	}
	got[0] = "changed"
	if again := plan.UntrackedPaths(); again[0] != "new.txt" {
		t.Errorf("UntrackedPaths shares its backing array: %q", again)
	}

	if d, err := plan.SourceUnchanged(); err != nil || d != "" {
		t.Fatalf("SourceUnchanged on an untouched root = %q, %v", d, err)
	}
	f.write(f.Dir, "README", "edited again")
	if d, err := plan.SourceUnchanged(); err != nil || !strings.Contains(d, "README") {
		t.Errorf("SourceUnchanged after a README edit = %q, %v; want it named", d, err)
	}
	f.write(f.Dir, "README", "edited")
	f.write(f.Dir, "other.txt", "o")
	if d, err := plan.SourceUnchanged(); err != nil || d == "" {
		t.Errorf("SourceUnchanged after a new file = %q, %v; want a difference", d, err)
	}
}
