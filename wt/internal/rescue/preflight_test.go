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
	if n := len(strings.Split(strings.TrimSpace(rescuedRefs(t, f)), "\n")); n != 1 {
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
	if n := len(strings.Split(strings.TrimSpace(rescuedRefs(t, f)), "\n")); n != 2 {
		t.Errorf("after the second rescue %d record refs, want 2:\n%s", n, rescuedRefs(t, f))
	}
}
