package rescue

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// preflightPlan runs Preflight for src and fails the test on any error.
func preflightPlan(t *testing.T, f *fixture, src string) (Engine, *Plan) {
	t.Helper()
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(src), "")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	return eng, plan
}

// TestExecuteDoesNotClaimANameSomeoneElseTook checks that when the branch (or
// the directory) of the new worktree appears between Preflight and Execute,
// as it can while the y/N prompt waits, Execute refuses with name_unavailable
// and says nothing was created, never "kept" and never Created/BranchKept.
// Why: the old code judged leftovers by "the ref and the path exist", so it
// pointed `wt remove` at somebody else's branch or directory.
func TestExecuteDoesNotClaimANameSomeoneElseTook(t *testing.T) {
	for _, what := range []string{"branch", "directory"} {
		t.Run(what, func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Commit(p, "work.txt", "work", "work")
			f.write(p, "work.txt", "edit")
			eng, plan := preflightPlan(t, f, p)
			if what == "branch" {
				f.git("branch", plan.Branch, plan.Start)
			} else if err := os.MkdirAll(plan.DestPath, 0o755); err != nil {
				t.Fatal(err)
			}

			out := eng.Execute(plan)

			if out.Code != CodeNameUnavailable || out.Created || out.BranchKept {
				t.Fatalf("outcome = %+v, want name_unavailable with nothing created or kept", out)
			}
			if !strings.Contains(out.Reason, "nothing was created") || strings.Contains(out.Reason, "kept") {
				t.Errorf("reason %q must say nothing was created and claim nothing kept", out.Reason)
			}
		})
	}
}

// TestPreflightRefusesEolConversionOnTheChangedPathsOnly checks a changed file
// that git would write with CRLF in the new worktree (core.eol=crlf with a
// text or text=auto attribute) is refused with line_endings, naming that path
// and not an unaffected changed file, while core.eol=crlf alone (no text
// attribute, so no conversion) is accepted. Why: git apply smudges an LF
// source to CRLF, and verify used to find that only after the destination
// existed.
func TestPreflightRefusesEolConversionOnTheChangedPathsOnly(t *testing.T) {
	for _, tc := range []struct {
		name, attrs string
		refused     bool
	}{
		{"text", "*.txt text\n", true},
		{"text=auto", "*.txt text=auto\n", true},
		{"no attribute", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.git("config", "core.eol", "crlf")
			p := f.Worktree("topic")
			if tc.attrs != "" {
				f.Commit(p, ".gitattributes", tc.attrs, "attrs")
			}
			f.Commit(p, "work.txt", "work\n", "work")
			f.Commit(p, "other.dat", "data\n", "dat")
			f.write(p, "work.txt", "edit\n")
			f.write(p, "other.dat", "edit\n")
			eng, find := f.engine(f.Dir)

			_, err := eng.Preflight(find(p), "")

			var r *Refusal
			switch {
			case tc.refused && (!errors.As(err, &r) || r.Code != CodeLineEndings):
				t.Fatalf("Preflight = %v, want line_endings", err)
			case tc.refused && (!strings.Contains(r.Message, "work.txt") || strings.Contains(r.Message, "other.dat")):
				t.Errorf("message %q must name work.txt and not other.dat", r.Message)
			case !tc.refused && err != nil:
				t.Fatalf("Preflight = %v, want accepted", err)
			}
		})
	}
}

// TestExecuteGivesTrackedFilesTheSourcesExactMode checks a staged new file and
// an edited tracked file whose mode is not what git apply would write (0600,
// 0664) end up with the source's exact permission bits. Why: git apply writes
// 0666 &^ umask, verify compares every permission bit, so a private file used
// to fail verification after the destination existed.
func TestExecuteGivesTrackedFilesTheSourcesExactMode(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	if err := os.Chmod(filepath.Join(p, "work.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.write(p, "new.txt", "new")
	gitIn(t, p, "add", "new.txt")
	if err := os.Chmod(filepath.Join(p, "new.txt"), 0o664); err != nil {
		t.Fatal(err)
	}

	_, out := rescueOnce(t, f, p)

	if out.Status != StatusMoved {
		t.Fatalf("status %s (%s): %s", out.Status, out.Code, out.Reason)
	}
	for name, want := range map[string]os.FileMode{"work.txt": 0o600, "new.txt": 0o664} {
		if got := dirMode(t, filepath.Join(out.Plan.DestPath, name)); got != want {
			t.Errorf("%s has mode %o, want %o", name, got, want)
		}
	}
}

// TestPreflightCountsChangedTrackedFilesAgainstTheSizeLimit checks the 512 MiB
// limit (lowered here) is judged on the changed tracked files' size plus the
// untracked bytes, refusing with too_large before anything is created. Why:
// a huge staged file was never measured, and the whole diff is read into
// memory by Execute.
func TestPreflightCountsChangedTrackedFilesAgainstTheSizeLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tracked   int
		untracked int
	}{{"tracked alone", 200, 0}, {"combined", 60, 60}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			p := f.Worktree("topic")
			f.Commit(p, "work.txt", "work", "work")
			f.write(p, "work.txt", strings.Repeat("x", tc.tracked))
			if tc.untracked > 0 {
				f.write(p, "u.txt", strings.Repeat("y", tc.untracked))
			}
			old := limitBytes
			limitBytes = 100
			t.Cleanup(func() { limitBytes = old })
			eng, find := f.engine(f.Dir)
			world := f.world()

			_, err := eng.Preflight(find(p), "")

			var r *Refusal
			if !errors.As(err, &r) || r.Code != CodeTooLarge {
				t.Fatalf("Preflight = %v, want too_large", err)
			}
			if f.world() != world {
				t.Error("something was created")
			}
		})
	}
}

// TestPreflightRefusesASourceThatChangesWhileItIsRead checks an edit made
// after the status was read but before Preflight finishes is refused with
// source_changed. Why: the fingerprint used to be taken after the reads, so
// the later state was baked in as "the checked one" and the counts the user
// confirmed described a state that no longer existed.
func TestPreflightRefusesASourceThatChangesWhileItIsRead(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, find := f.engine(f.Dir)
	t.Cleanup(func() { afterReadWork = func() {} })
	afterReadWork = func() { f.write(p, "work.txt", "edited while being read") }

	_, err := eng.Preflight(find(p), "")

	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeSourceChanged {
		t.Fatalf("Preflight = %v, want source_changed", err)
	}
}

// TestExecuteSkipsTheStartRecheckWhenToldNoOneWasAsked checks that a plan
// marked SkipStartRecheck (Execute follows Preflight with no prompt between)
// does not re-read the source at the start, so a source edited in between is
// caught by the after-copy comparison instead (failed, source changed while
// copying), while an unmarked plan is still refused at the start. Why: the
// start check hashed every changed file a second time right after Preflight
// had; it is kept only when a prompt gave the source time to change.
func TestExecuteSkipsTheStartRecheckWhenToldNoOneWasAsked(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	f.write(p, "work.txt", "edit")
	eng, plan := preflightPlan(t, f, p)
	f.write(p, "work.txt", "edited again")
	plan.SkipStartRecheck()

	out := eng.Execute(plan)

	if out.Status != StatusFailed || out.Code != CodeSourceChangedMid {
		t.Fatalf("outcome = %+v, want the after-copy check to catch the edit", out)
	}
}

// TestPreflightFailsClosedOnAnUnreadableCommitCount checks a rev-list --count
// answer that is not a number is an error, not a plan claiming 0 commits.
// Why: the Sscan error was ignored, so Counts.Commits silently became 0 in
// the prompt and the JSON while the gate said commits exist.
func TestPreflightFailsClosedOnAnUnreadableCommitCount(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *\"--count HEAD --not --remotes\"*) echo many; exit 0;; esac\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	eng, find := f.engine(f.Dir)
	row := find(p)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	plan, err := eng.Preflight(row, "")

	if err == nil {
		t.Fatalf("Preflight accepted an unreadable count; plan says %d commits", plan.Counts.Commits)
	}
}

// TestExecuteRunsNoHooksAndPreflightWarnsOfAPostCheckoutHook checks the new
// worktree is added with hooks disabled (a post-checkout hook does not run),
// that the plan warns when such a hook exists, and that no warning appears
// when none does. Why: a hook that rewrites files or fails would break the
// byte-for-byte copy; the user must be told it was skipped.
func TestExecuteRunsNoHooksAndPreflightWarnsOfAPostCheckoutHook(t *testing.T) {
	for _, hook := range []bool{true, false} {
		f := newFixture(t)
		marker := filepath.Join(t.TempDir(), "ran")
		p := f.Worktree("topic")
		f.Commit(p, "work.txt", "work", "work")
		f.write(p, "work.txt", "edit")
		if hook {
			dir := filepath.Join(gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir"), "hooks")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "post-checkout"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		plan, out := rescueOnce(t, f, p)

		if out.Status != StatusMoved {
			t.Fatalf("hook=%v: status %s (%s): %s", hook, out.Status, out.Code, out.Reason)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("hook=%v: the post-checkout hook ran in the new worktree", hook)
		}
		warned := strings.Contains(strings.Join(plan.Warnings, "\n"), "post-checkout")
		if warned != hook {
			t.Errorf("hook=%v: warned=%v, warnings %q", hook, warned, plan.Warnings)
		}
	}
}

// TestPreflightAcceptsAZeroCountForADetachedHeadOnARemoteRef checks a detached
// worktree whose HEAD a remote-tracking ref already holds, but which is not in
// the base, is planned with 0 commits instead of failing. Why: DetachedUnique
// means "not an ancestor of the base" while the count means "reachable from no
// remote-tracking ref", so 0 is a true answer there; the zero-count guard
// belongs to Unpushed only (a regression the F7 fix introduced).
func TestPreflightAcceptsAZeroCountForADetachedHeadOnARemoteRef(t *testing.T) {
	f := newFixture(t)
	p := f.Detached("scratch")
	sha := f.Commit(p, "a.txt", "a", "a")
	f.git("update-ref", "refs/remotes/origin/feat", sha)
	f.write(p, "a.txt", "dirty")
	eng, find := f.engine(f.Dir)

	plan, err := eng.Preflight(find(p), "")

	if err != nil {
		t.Fatalf("Preflight = %v, want a plan", err)
	}
	if plan.Counts.Commits != 0 {
		t.Fatalf("commits = %d, want 0", plan.Counts.Commits)
	}
}

// TestPreflightRefusesAZeroCountWhenTheGateSaysCommitsExist checks the
// zero-count guard on its own: unpushed commits per the classifier and a count
// of 0 is an error. Why: without it a plan would tell the user it carries no
// commits while the gate says some exist; the parse check alone does not
// cover a well-formed 0.
func TestPreflightRefusesAZeroCountWhenTheGateSaysCommitsExist(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "work.txt", "work", "work")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *\"--count HEAD --not --remotes\"*) echo 0; exit 0;; esac\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	eng, find := f.engine(f.Dir)
	row := find(p)
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	if plan, err := eng.Preflight(row, ""); err == nil {
		t.Fatalf("Preflight accepted a 0 count against unpushed commits; plan says %d", plan.Counts.Commits)
	}
}

// bigPathList is enough long, mostly nonexistent paths that passing them on
// the git command line would exceed ARG_MAX on macOS (1 MiB) and Linux, with
// the one real path last.
func bigPathList(real string) []string {
	long := strings.Repeat("d", 120) + "/" + strings.Repeat("f", 120)
	paths := make([]string, 0, 13000)
	for i := 0; i < 12000; i++ {
		paths = append(paths, fmt.Sprintf("%s%05d", long, i))
	}
	return append(paths, real)
}

// TestAttributeChecksSurviveAVeryLargeChangeSet runs checkContentFilters and
// checkLineEndings over about 3 MB of paths with one filtered and one
// eol=crlf file at the end. What: both still refuse that file, with no
// "argument list too long" error. Why: the paths used to ride the git argv, so
// a big change set (a generated tree) failed the rescue with an exec error, or
// worse a refusal that named nothing, instead of the right verdict.
func TestAttributeChecksSurviveAVeryLargeChangeSet(t *testing.T) {
	f := newFixture(t)
	f.write(f.Dir, ".gitattributes", "*.lfs filter=lfs\n*.crlf eol=crlf\n")
	f.git("add", ".gitattributes")
	f.git("commit", "-m", "attrs")
	f.Commit(f.Dir, "big.lfs", "x", "lfs")
	f.Commit(f.Dir, "big.crlf", "x\n", "crlf")
	sg := sgit{dir: f.Dir}

	err := checkContentFilters(sg, bigPathList("big.lfs"))
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeContentFilter || !strings.Contains(r.Message, "big.lfs") {
		t.Errorf("checkContentFilters = %v, want a content_filter refusal naming big.lfs", err)
	}
	err = checkLineEndings(sg, bigPathList("big.crlf"))
	if !errors.As(err, &r) || r.Code != CodeLineEndings || !strings.Contains(r.Message, "big.crlf") {
		t.Errorf("checkLineEndings = %v, want a line_endings refusal naming big.crlf", err)
	}
	if err := checkContentFilters(sg, bigPathList("README")); err != nil {
		t.Errorf("unfiltered large set: %v", err)
	}
	if err := checkLineEndings(sg, bigPathList("README")); err != nil {
		t.Errorf("unconverted large set: %v", err)
	}
}

// TestBlobSizesCountTheStagedAndCommittedBlob checks blobSizes reports the
// larger of a path's index blob and its HEAD blob, whatever the working file
// holds. What: a file staged big and then truncated in the working tree, and a
// big committed file whose deletion is staged, both report the big size; a
// path git has no blob for reports 0. Why: the tracked-size bound used the
// working file's size, so these counted about 0 although the diff held the
// whole blob in memory.
func TestBlobSizesCountTheStagedAndCommittedBlob(t *testing.T) {
	f := newFixture(t)
	f.Commit(f.Dir, "gone.bin", strings.Repeat("g", 300), "big committed")
	f.git("rm", "-q", "gone.bin")
	f.write(f.Dir, "staged.bin", strings.Repeat("s", 200))
	f.git("add", "staged.bin")
	f.write(f.Dir, "staged.bin", "") // truncated after staging
	sg := sgit{dir: f.Dir}

	got, err := blobSizes(sg, []string{"gone.bin", "staged.bin", "nothing.bin", "with\nnewline"})

	if err != nil {
		t.Fatal(err)
	}
	if got["gone.bin"] != 300 || got["staged.bin"] != 200 || got["nothing.bin"] != 0 {
		t.Errorf("blobSizes = %v, want gone 300, staged 200, nothing 0", got)
	}
}

// TestPreflightCountsAStagedDeletionAgainstTheSizeLimit stages the deletion of
// a big committed file (the working file is gone, so it measures 0) with the
// limit below its size. What: Preflight refuses with too_large and creates
// nothing. Why: `diff --cached --binary` of a deletion carries the whole old
// blob, held in memory by Execute, while the working-file size said 0.
func TestPreflightCountsAStagedDeletionAgainstTheSizeLimit(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.Commit(p, "gone.bin", strings.Repeat("g", 300), "big committed")
	gitIn(t, p, "rm", "-q", "gone.bin")
	old := limitBytes
	limitBytes = 100
	t.Cleanup(func() { limitBytes = old })
	eng, find := f.engine(f.Dir)
	world := f.world()

	_, err := eng.Preflight(find(p), "")

	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeTooLarge {
		t.Fatalf("Preflight = %v, want too_large", err)
	}
	if f.world() != world {
		t.Error("something was created")
	}
}

// TestBlobSizesCoverAPathWithANewline: a big committed file whose name holds
// a newline has its deletion staged. What: blobSizes reports the big size and
// Preflight refuses with too_large. Why: cat-file's line-based stdin cannot
// name such a path, so it used to count 0.
func TestBlobSizesCoverAPathWithANewline(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	name := "gone\nname.bin"
	f.Commit(p, name, strings.Repeat("g", 300), "big committed")
	gitIn(t, p, "rm", "-q", name)

	got, err := blobSizes(sgit{dir: p}, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if got[name] != 300 {
		t.Errorf("blobSizes = %v, want %q: 300", got, name)
	}
	old := limitBytes
	limitBytes = 100
	t.Cleanup(func() { limitBytes = old })
	eng, find := f.engine(f.Dir)
	_, err = eng.Preflight(find(p), "")
	var r *Refusal
	if !errors.As(err, &r) || r.Code != CodeTooLarge {
		t.Fatalf("Preflight = %v, want too_large", err)
	}
}

// TestPreflightCountsAStagedRenameOnce: a staged rename of a file just under
// the limit passes (old and new path are one file), while deleting one big
// file and adding a different big one counts both and is refused. Why: the
// rename's old path (HEAD blob) and new path (index blob) were both counted,
// so a file under the limit was refused as twice its size.
func TestPreflightCountsAStagedRenameOnce(t *testing.T) {
	old := limitBytes
	limitBytes = 100
	t.Cleanup(func() { limitBytes = old })

	t.Run("rename passes", func(t *testing.T) {
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Commit(p, "a.bin", strings.Repeat("a", 80), "big committed")
		gitIn(t, p, "mv", "a.bin", "b.bin")
		eng, find := f.engine(f.Dir)
		if _, err := eng.Preflight(find(p), ""); err != nil {
			t.Fatalf("Preflight = %v, want the rename to pass", err)
		}
	})
	t.Run("delete plus different add counts both", func(t *testing.T) {
		f := newFixture(t)
		p := f.Worktree("topic")
		f.Commit(p, "a.bin", strings.Repeat("aaaaaaa\n", 10), "big committed")
		gitIn(t, p, "rm", "-q", "a.bin")
		f.write(p, "c.bin", strings.Repeat("zzzzzzz\n", 10)) // too different for rename detection
		gitIn(t, p, "add", "c.bin")
		eng, find := f.engine(f.Dir)
		_, err := eng.Preflight(find(p), "")
		var r *Refusal
		if !errors.As(err, &r) || r.Code != CodeTooLarge {
			t.Fatalf("Preflight = %v, want too_large", err)
		}
	})
}
