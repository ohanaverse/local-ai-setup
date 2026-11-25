package rootmove

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// The tests here cover a root on a detached HEAD (#584): E, a branch already
// holds HEAD (the new worktree stays detached, no ref is made), and C, none
// does and --branch NAME is created in the new worktree before the release.

// detachedOnBranch detaches the root at a pushed commit of topic, which
// local topic and origin/topic both hold.
func detachedOnBranch(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.onBranch("topic")
	f.git("push", "-q", "-u", "origin", "topic")
	f.git("switch", "-q", "--detach")
	return f
}

// detachedRemoteOnly detaches the root at a commit only origin/topic holds.
func detachedRemoteOnly(t *testing.T) *fixture {
	t.Helper()
	f := detachedOnBranch(t)
	f.git("branch", "-q", "-D", "topic")
	return f
}

// detachedOrphan detaches the root and commits: no branch holds HEAD.
func detachedOrphan(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.git("switch", "-q", "--detach")
	f.Commit(f.Dir, "orphan.txt", "o", "only here")
	return f
}

// heads lists every ref outside refs/wt/rescued and the worktrees' metadata:
// the refs a move must not add to.
func (f *fixture) refsExceptRecords() []string {
	var out []string
	for _, l := range strings.Split(f.git("for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if l != "" && !strings.HasPrefix(l, rescue.RecordRefPrefix) {
			out = append(out, l)
		}
	}
	return out
}

func detachedPlan(t *testing.T, f *fixture, name string) (Engine, *Plan) {
	t.Helper()
	eng := f.engine()
	eng.NewBranch = name
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	return eng, p
}

// TestPreflightDetachedReachablePlansE: a detached root a branch holds is
// planned with no branch, a detached-<sha8> directory and the holders named,
// and a clean one needs no worktree at all. Why: E leaves the new worktree
// detached, inventing no ref or name.
func TestPreflightDetachedReachablePlansE(t *testing.T) {
	t.Parallel()
	f := detachedOnBranch(t)
	f.dirty()
	_, p := detachedPlan(t, f, "")
	if !p.Detached || p.Branch != "" || p.NewBranch != "" || p.DirName != "detached-"+p.Head[:8] || p.DestPath == "" {
		t.Errorf("plan %+v", p)
	}
	if !slices.Contains(p.ReachableFrom, "topic") || p.Counts.Commits != 0 {
		t.Errorf("ReachableFrom %q, commits %d", p.ReachableFrom, p.Counts.Commits)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warnings %q, want none for a local holder", p.Warnings)
	}

	g := detachedOnBranch(t)
	_, c := detachedPlan(t, g, "")
	if !c.Detached || c.DirName != "" || c.DestPath != "" {
		t.Errorf("clean plan %+v, want no worktree", c)
	}
}

// TestPreflightRemoteOnlyReachabilityWarns: HEAD held only by a remote-tracking
// ref still plans, with a warning. Why: a prune could orphan the commits of a
// clean move, which has no record.
func TestPreflightRemoteOnlyReachabilityWarns(t *testing.T) {
	t.Parallel()
	f := detachedRemoteOnly(t)
	_, p := detachedPlan(t, f, "")
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "origin/topic") || !strings.Contains(p.Warnings[0], "pruned") {
		t.Errorf("warnings %q", p.Warnings)
	}
}

// TestPreflightDetachedUnreachableRefusesNamingFlag: HEAD on no branch and no
// --branch is refused naming the flag, changing nothing; a commit only a tag,
// the stash or a rescue record holds still counts as on no branch.
func TestPreflightDetachedUnreachableRefusesNamingFlag(t *testing.T) {
	t.Parallel()
	for _, hold := range []string{"none", "tag", "record"} {
		t.Run(hold, func(t *testing.T) {
			t.Parallel()
			f := detachedOrphan(t)
			head := f.git("rev-parse", "HEAD")
			switch hold {
			case "tag":
				f.git("tag", "keep", head)
			case "record":
				f.git("update-ref", rescue.RecordRefPrefix+"x", head)
			}
			before := f.rootState()
			p, err := f.engine().Preflight("")
			r := refusalOf(t, err)
			if p != nil || r.Code != CodeDetachedRoot || !strings.Contains(r.Message, "--branch NAME") {
				t.Errorf("plan %v code %q message %q", p, r.Code, r.Message)
			}
			if after := f.rootState(); after != before {
				t.Errorf("a refusal changed the root")
			}
		})
	}
}

// TestPreflightDetachedOperationWinsOverDetachedRoot: an operation in
// progress is what a detached unreachable root refuses with, not a misleading
// "name a branch".
func TestPreflightDetachedOperationWinsOverDetachedRoot(t *testing.T) {
	t.Parallel()
	f := detachedOrphan(t)
	head := f.git("rev-parse", "HEAD")
	gd := f.git("rev-parse", "--path-format=absolute", "--git-dir")
	if err := os.WriteFile(filepath.Join(gd, "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.engine().Preflight("")
	if r := refusalOf(t, err); r.Code != rescue.CodeOperation {
		t.Errorf("code %q (%s), want %q", r.Code, r.Message, rescue.CodeOperation)
	}
}

// TestPreflightBranchFlagRefusals: --branch on a root on a branch, an invalid
// name and an existing one are each refused with their own code, nothing made.
func TestPreflightBranchFlagRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, flag, code string
		build            func(t *testing.T) *fixture
	}{
		{"on a branch", "x", CodeBranchUnneeded, func(t *testing.T) *fixture { f := newFixture(t); f.onBranch("topic"); return f }},
		{"invalid", "bad..name", CodeBranchInvalid, detachedOrphan},
		{"at sign", "@", CodeBranchInvalid, detachedOrphan},
		{"refs prefix", "refs/x", CodeBranchInvalid, detachedOrphan},
		{"exists", "main", CodeBranchExists, detachedOrphan},
		{"exists on a reachable root", "topic", CodeBranchExists, detachedOnBranch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := c.build(t)
			eng := f.engine()
			eng.NewBranch = c.flag
			before := f.rootState()
			p, err := eng.Preflight("")
			if r := refusalOf(t, err); p != nil || r.Code != c.code {
				t.Errorf("plan %v code %q (%s), want %q", p, r.Code, r.Message, c.code)
			}
			if after := f.rootState(); after != before {
				t.Errorf("a refusal changed the root")
			}
		})
	}
}

// TestPreflightBranchFlagOnReachableDetachedHonoursFlag: --branch on a
// reachable detached root plans the branch (the C flow).
func TestPreflightBranchFlagOnReachableDetachedHonoursFlag(t *testing.T) {
	t.Parallel()
	f := detachedOnBranch(t)
	_, p := detachedPlan(t, f, "spike")
	if p.Branch != "spike" || p.NewBranch != "spike" || p.DirName != "spike" {
		t.Errorf("plan %+v", p)
	}
}

// TestExecuteDetachedReachableDirtyMoves (E): the dirty root's work is carried
// into a worktree left detached at HEAD, the root ends clean on main, a record
// exists, and no ref besides it is created.
func TestExecuteDetachedReachableDirtyMoves(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(*testing.T) *fixture{"local branch": detachedOnBranch, "remote only": detachedRemoteOnly} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := build(t)
			f.dirty()
			status := statusOf(f.Dir)
			refs := f.refsExceptRecords()
			eng, p := detachedPlan(t, f, "")
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusMoved, "")
			wantRootOnDefault(t, f)
			wantCopy(t, o.Plan.DestPath, p.Head, status, "")
			if o.Record == nil || len(f.records()) != 1 {
				t.Errorf("record %v refs %q, want one", o.Record, f.records())
			}
			if !slices.Equal(f.refsExceptRecords(), refs) {
				t.Errorf("refs changed:\n%q\n--\n%q", refs, f.refsExceptRecords())
			}
			if !strings.Contains(f.git("log", "-1", "--format=%B", o.Record.Ref+"^"), "Wt-Dest-Branch: detached") {
				t.Errorf("record lacks Wt-Dest-Branch: detached")
			}
			recs, err := cleanup.ListRecords(cleanup.Git{Dir: f.Dir})
			if err != nil || len(recs) != 1 {
				t.Errorf("ListRecords %v err %v", recs, err)
			}
		})
	}
}

// TestExecuteDetachedReachableCleanOnlySwitchesRoot (E, clean): no worktree
// directory, no record, Created false, the root on main. Also the commit stays
// where it was.
func TestExecuteDetachedReachableCleanOnlySwitchesRoot(t *testing.T) {
	t.Parallel()
	f := detachedOnBranch(t)
	refs := f.refsExceptRecords()
	eng, p := detachedPlan(t, f, "")
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	wantRootOnDefault(t, f)
	if o.Created || o.Record != nil || len(f.records()) != 0 || o.Plan.DestPath != "" {
		t.Errorf("Created %v record %v dest %q, want none", o.Created, o.Record, o.Plan.DestPath)
	}
	if ents, _ := os.ReadDir(filepath.Join(f.Dir, ".worktrees")); len(ents) != 0 {
		t.Errorf(".worktrees holds %d entries, want none", len(ents))
	}
	if !slices.Equal(f.refsExceptRecords(), refs) {
		t.Errorf("refs changed")
	}
}

// TestExecuteDetachedCleanEditedAfterPreflightIsNotLost: an edit made after
// Preflight of a clean detached root is refused (the fingerprint, or, with the
// start re-check skipped, the release's own empty-status proof); the edit stays.
func TestExecuteDetachedCleanEditedAfterPreflightIsNotLost(t *testing.T) {
	t.Parallel()
	for _, skip := range []bool{false, true} {
		f := detachedOnBranch(t)
		eng, p := detachedPlan(t, f, "")
		if skip {
			p.SkipStartRecheck()
		}
		f.write(f.Dir, "late.txt", "late")
		o := eng.Execute(p)
		if o.Status == rescue.StatusMoved {
			t.Fatalf("skip=%v: moved, the late edit would be lost", skip)
		}
		if got, _ := os.ReadFile(filepath.Join(f.Dir, "late.txt")); string(got) != "late" {
			t.Errorf("skip=%v: late.txt = %q", skip, got)
		}
		if headRef(f.Dir) != "" {
			t.Errorf("skip=%v: the root moved off its detached HEAD", skip)
		}
	}
}

// TestExecuteDetachedUnreachableWithBranchCreatesNameBeforeRelease (C): the
// branch exists in the new worktree, at HEAD, before the root is released.
func TestExecuteDetachedUnreachableWithBranchCreatesNameBeforeRelease(t *testing.T) {
	t.Parallel()
	for _, dirty := range []bool{true, false} {
		f := detachedOrphan(t)
		if dirty {
			f.dirty()
		}
		status := statusOf(f.Dir)
		eng, p := detachedPlan(t, f, "spike/one")
		if p.Counts.Commits != 1 {
			t.Fatalf("parked commits %d, want 1", p.Counts.Commits)
		}
		seen := false
		eng.Hooks.BeforeRelease = func() {
			seen = true
			if got := rev(f.Dir, "refs/heads/spike/one"); got != p.Head {
				t.Errorf("before the release spike/one = %q, want %s", got, p.Head)
			}
			if headRef(f.Dir) != "" {
				t.Errorf("before the release the root is on %q, want detached", headRef(f.Dir))
			}
		}
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusMoved, "")
		if !seen {
			t.Error("BeforeRelease did not fire")
		}
		wantRootOnDefault(t, f)
		wantCopy(t, o.Plan.DestPath, p.Head, status, "refs/heads/spike/one")
		if got := rev(f.Dir, "refs/heads/spike/one"); got != p.Head {
			t.Errorf("spike/one = %s, want %s", got, p.Head)
		}
		if dirty != (o.Record != nil) {
			t.Errorf("dirty=%v record %v", dirty, o.Record)
		}
		if got := soft(f.Dir, "config", "--get-regexp", `^branch\.spike/one\.`); got != "" {
			t.Errorf("the new branch tracks something: %q", got)
		}
	}
}

// TestExecuteBranchCreateFailureLeavesRootUntouched: step 3b failing (a git
// failure, or the name appearing meanwhile) leaves the root detached with its
// work, the copy and the record kept, and says how to give the commits a branch.
func TestExecuteBranchCreateFailureLeavesRootUntouched(t *testing.T) {
	t.Parallel()
	t.Run("git fails", func(t *testing.T) {
		t.Parallel()
		f := detachedOrphan(t)
		f.dirty()
		eng, p := detachedPlan(t, f, "spike")
		eng.Hooks.StepGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
			if slices.Contains(args, "-c") && slices.Contains(args, "spike") {
				return cleanup.Out{Code: 128, Stderr: "planted"}, nil
			}
			return g.Run(args...)
		}
		before := f.rootCheckout()
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusFailed, CodeBranchCreateFailed)
		wantReason(t, o, "planted", "still detached", "git switch -c <name>", o.Record.Ref)
		if f.rootCheckout() != before {
			t.Error("the root changed")
		}
		wantCopy(t, p.DestPath, p.Head, statusOf(f.Dir), "")
		if rev(f.Dir, "refs/heads/spike") != "" {
			t.Error("spike exists")
		}
	})
	t.Run("name appears at another commit", func(t *testing.T) {
		t.Parallel()
		f := detachedOrphan(t)
		f.dirty()
		eng, p := detachedPlan(t, f, "spike")
		other := otherCommit(t, f.Dir, p.Head)
		eng.Hooks.ExecuteInto = func(re rescue.Engine, rp *rescue.Plan, dest string) rescue.Outcome {
			o := re.ExecuteInto(rp, dest)
			f.git("branch", "spike", other)
			return o
		}
		before := f.rootCheckout()
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusFailed, CodeBranchCreateFailed)
		wantReason(t, o, "appeared", other, "not touched")
		if f.rootCheckout() != before {
			t.Error("the root changed")
		}
		if got := rev(f.Dir, "refs/heads/spike"); got != other {
			t.Errorf("spike = %s, want the racer's %s", got, other)
		}
	})
	t.Run("name appears before the creating step (start re-check)", func(t *testing.T) {
		t.Parallel()
		f := detachedOrphan(t)
		eng, p := detachedPlan(t, f, "spike")
		f.git("branch", "spike", otherCommit(t, f.Dir, p.Head))
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusRefused, rescue.CodeSourceChanged)
		if o.Created {
			t.Error("a refusal created a worktree")
		}
	})
}

// TestExecuteDetachedFailureTextNeverSwitchesTheDestination: every release
// failure stage, for E and C, tells the user to switch only the root; the
// new worktree is final.
func TestExecuteDetachedFailureTextNeverSwitchesTheDestination(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"E", "C"} {
		for _, stage := range []string{removal.StageReset, removal.StageRemoveUntracked, removal.StageVerifyClean, removal.StageSwitch} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				t.Parallel()
				var f *fixture
				name := ""
				if mode == "E" {
					f = detachedOnBranch(t)
				} else {
					f, name = detachedOrphan(t), "spike"
				}
				f.dirty()
				eng, p := detachedPlan(t, f, name)
				eng.Hooks.ReleaseRoot = func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error) {
					return removal.ReleaseResult{}, &removal.ReleaseFailed{Stage: stage, Err: os.ErrInvalid}
				}
				o := eng.Execute(p)
				wantStatus(t, o, rescue.StatusFailed, CodeReleaseFailed)
				if strings.Contains(o.Reason, "running `wt restore-root` again") || strings.Contains(o.Reason, "in "+p.DestPath+" (") {
					t.Errorf("reason sends the user to switch the new worktree: %s", o.Reason)
				}
				wantReason(t, o, "git switch main")
			})
		}
	}
	t.Run("a release refusal is named for a branch-less checkout", func(t *testing.T) {
		t.Parallel()
		f := detachedOrphan(t)
		eng, p := detachedPlan(t, f, "spike")
		eng.Hooks.ReleaseRoot = func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error) {
			return removal.ReleaseResult{}, &removal.ReleaseRefused{Reason: "HEAD is not on any branch"}
		}
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusFailed, CodeReleaseRefused)
		wantReason(t, o, "HEAD is not on any branch", "clean checkout of "+p.Head[:8])
	})
}

// TestExecuteDetachedReleaseGetsDetachedAt: the release is asked for a
// detached root only with the plan's commit named.
func TestExecuteDetachedReleaseGetsDetachedAt(t *testing.T) {
	t.Parallel()
	f := detachedOnBranch(t)
	f.dirty()
	eng, p := detachedPlan(t, f, "")
	var got removal.RootRelease
	eng.Hooks.ReleaseRoot = func(g cleanup.Git, r removal.RootRelease) (removal.ReleaseResult, error) {
		got = r
		return removal.ReleaseRoot(g, r)
	}
	wantStatus(t, eng.Execute(p), rescue.StatusMoved, "")
	if got.DetachedAt != p.Head {
		t.Errorf("DetachedAt %q, want %q", got.DetachedAt, p.Head)
	}
}
