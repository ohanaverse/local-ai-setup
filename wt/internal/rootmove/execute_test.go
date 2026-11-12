package rootmove

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// soft runs a read in dir with optional locks off and returns its output,
// whatever the exit.
func soft(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	b, _ := cmd.Output()
	return string(b)
}

// statusOf is dir's full status the way rescue compares a copy with its
// source (no branch header).
func statusOf(dir string) string {
	return soft(dir, "status", "--porcelain=v2", "-z", "--untracked-files=all")
}

// headRef is dir's symbolic HEAD, "" when detached.
func headRef(dir string) string { return strings.TrimSpace(soft(dir, "symbolic-ref", "-q", "HEAD")) }

// rev is the commit ref names in dir ("" when it does not resolve).
func rev(dir, ref string) string {
	return strings.TrimSpace(soft(dir, "rev-parse", "-q", "--verify", ref+"^{commit}"))
}

// records lists the repository's rescue record refs.
func (f *fixture) records() []string {
	return strings.Fields(soft(f.Dir, "for-each-ref", "--format=%(refname)", rescue.RecordRefPrefix))
}

// rootCheckout is the root's checkout as the user sees it: the branch, the
// HEAD commit, the status, the raw index, the tracked changes' content and
// every untracked file's content. Equal before and after means the root was
// not touched (refs and .worktrees may differ: those are the other columns).
func (f *fixture) rootCheckout() string {
	f.t.Helper()
	raw := "absent"
	if b, err := os.ReadFile(filepath.Join(strings.TrimSpace(soft(f.Dir, "rev-parse", "--path-format=absolute", "--git-dir")), "index")); err == nil {
		raw = string(b)
	}
	parts := []string{
		headRef(f.Dir), rev(f.Dir, "HEAD"), statusOf(f.Dir), raw,
		soft(f.Dir, "diff", "--binary", "HEAD"),
	}
	for _, u := range strings.Split(soft(f.Dir, "ls-files", "-z", "--others", "--exclude-standard"), "\x00") {
		if u == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(f.Dir, u))
		parts = append(parts, u+"="+string(b)+"/"+errString(err))
	}
	return strings.Join(parts, "\n--\n")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// traced is one git process a test started: its argv and the worktree it
// ran in ("" when it set up none).
type traced struct {
	argv     []string
	worktree string
}

// traceGit records, through GIT_TRACE2_EVENT, every git process the test
// starts from now on (children such as `worktree add`'s own checkout
// included); the returned func reads them.
func traceGit(t *testing.T) func() []traced {
	t.Helper()
	file := filepath.Join(t.TempDir(), "trace2.json")
	t.Setenv("GIT_TRACE2_EVENT", file)
	return func() []traced {
		fh, err := os.Open(file)
		if err != nil {
			return nil
		}
		defer fh.Close()
		var order []string
		by := map[string]*traced{}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var ev struct {
				Event    string   `json:"event"`
				SID      string   `json:"sid"`
				Argv     []string `json:"argv"`
				Worktree string   `json:"worktree"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			switch ev.Event {
			case "start":
				order = append(order, ev.SID)
				by[ev.SID] = &traced{argv: ev.Argv}
			case "def_repo":
				if tr := by[ev.SID]; tr != nil && tr.worktree == "" {
					tr.worktree = ev.Worktree
				}
			}
		}
		var all []traced
		for _, sid := range order {
			all = append(all, *by[sid])
		}
		return all
	}
}

// hasRun says some traced argv holds sub as a contiguous run.
func hasRun(all []traced, sub ...string) bool {
	for _, tr := range all {
		a := tr.argv
		for i := 0; i+len(sub) <= len(a); i++ {
			if slices.Equal(a[i:i+len(sub)], sub) {
				return true
			}
		}
	}
	return false
}

// resetArgv is the one reset of the root, as removal.ReleaseRoot runs it.
var resetArgv = []string{"-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false", "reset", "--hard", "HEAD"}

// noReset fails the test when a traced git ran a reset in root (or in no
// known worktree). `git worktree add` populates the new worktree with a
// reset of its own, which is not the root's.
func noReset(t *testing.T, all []traced, root string) {
	t.Helper()
	for _, tr := range all {
		if slices.Contains(tr.argv, "reset") && (tr.worktree == "" || cleanup.ResolvePath(tr.worktree) == cleanup.ResolvePath(root)) {
			t.Errorf("a reset ran in the root (worktree %q): %q", tr.worktree, tr.argv)
		}
	}
}

// otherCommit makes a commit on top of head (same tree) in dir's
// repository without moving any ref, and returns it.
func otherCommit(t *testing.T, dir, head string) string {
	t.Helper()
	return gitIn(t, dir, "commit-tree", head+"^{tree}", "-p", head, "-m", "elsewhere")
}

// dirtyPlan is a root on topic (one unpushed commit) with a staged file, an
// unstaged edit and an untracked file, and its checked plan.
func dirtyPlan(t *testing.T) (*fixture, Engine, *Plan) {
	t.Helper()
	f := newFixture(t)
	f.onBranch("topic")
	f.dirty()
	eng := f.engine()
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	return f, eng, p
}

// wantStatus fails unless o has status and code.
func wantStatus(t *testing.T, o Outcome, status rescue.Status, code string) {
	t.Helper()
	if o.Status != status || o.Code != code {
		t.Fatalf("outcome = %s %s (%s), want %s %s", o.Status, o.Code, o.Reason, status, code)
	}
}

// wantReason fails unless the reason holds every part.
func wantReason(t *testing.T, o Outcome, parts ...string) {
	t.Helper()
	for _, s := range parts {
		if !strings.Contains(o.Reason, s) {
			t.Errorf("reason %q lacks %q", o.Reason, s)
		}
	}
}

// wantCopy fails unless dest is a kept copy of the pre-move root: detached
// at tip (or on branch when onBranch) with the root's former status.
func wantCopy(t *testing.T, dest, tip, status, branch string) {
	t.Helper()
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("the new worktree is gone: %v", err)
	}
	if got := headRef(dest); got != branch {
		t.Errorf("destination HEAD ref = %q, want %q", got, branch)
	}
	if got := rev(dest, "HEAD"); got != tip {
		t.Errorf("destination HEAD = %s, want the branch tip %s", got, tip)
	}
	if got := statusOf(dest); got != status {
		t.Errorf("destination status = %q, want the root's former %q", got, status)
	}
}

// wantRootOnDefault fails unless the root is on main and clean.
func wantRootOnDefault(t *testing.T, f *fixture) {
	t.Helper()
	if got := headRef(f.Dir); got != "refs/heads/main" {
		t.Errorf("root HEAD = %q, want refs/heads/main", got)
	}
	if got := statusOf(f.Dir); got != "" {
		t.Errorf("root status = %q, want clean", got)
	}
}

// TestExecuteMovesCleanBranchWithUnpushedCommits moves a root on a branch
// with an upstream, one unpushed commit, branch config and nothing
// uncommitted. Why: the clean case makes no record and no copy (Review Focus
// 2), and "the same branch, not a copy" means the ref, its upstream and its
// config survive untouched.
func TestExecuteMovesCleanBranchWithUnpushedCommits(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.onBranch("topic")
	f.git("push", "-q", "-u", "origin", "topic")
	f.Commit(f.Dir, "unpushed.txt", "u", "unpushed")
	f.git("config", "branch.topic.description", "keep me")
	tip := f.git("rev-parse", "HEAD")
	configBefore := f.git("config", "--get-regexp", `^branch\.topic\.`)

	eng := f.engine()
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	if o.Record != nil || len(f.records()) != 0 {
		t.Errorf("record = %v, refs %q; want none for a clean root", o.Record, f.records())
	}
	if !o.Created || cleanup.ResolvePath(o.Plan.DestPath) != cleanup.ResolvePath(p.DestPath) {
		t.Errorf("Created %v at %s, want true at %s", o.Created, o.Plan.DestPath, p.DestPath)
	}
	if got := rev(f.Dir, "refs/heads/topic"); got != tip {
		t.Errorf("topic = %s, want its tip %s unmoved", got, tip)
	}
	if got := f.git("rev-parse", "--abbrev-ref", "topic@{u}"); got != "origin/topic" {
		t.Errorf("topic@{u} = %q, want origin/topic", got)
	}
	if got := f.git("config", "--get-regexp", `^branch\.topic\.`); got != configBefore {
		t.Errorf("branch config = %q, want %q", got, configBefore)
	}
	wantRootOnDefault(t, f)
	wantCopy(t, p.DestPath, tip, "", "refs/heads/topic")
	if _, err := os.Stat(filepath.Join(p.DestPath, "unpushed.txt")); err != nil {
		t.Errorf("the unpushed commit's file is not in the new worktree: %v", err)
	}
}

// TestExecuteMovesAllDirt moves a root holding a commit, a staged file, an
// unstaged edit, an unstaged mode change, an untracked executable and an
// ignored file, and (as a variant) the `git rm --cached foo` twin: a staged
// deletion with the same file untracked on disk. Why: the destination must
// read exactly as the root did (status equal), the record must exist, the
// root must end clean on the default, and the ignored file must stay where
// it was; the twin is the path the reset restores from HEAD.
func TestExecuteMovesAllDirt(t *testing.T) {
	t.Parallel()
	t.Run("all kinds", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.onBranch("topic")
		f.Commit(f.Dir, "tool.sh", "#!/bin/sh\n", "tool")
		f.dirty()
		if err := os.Chmod(filepath.Join(f.Dir, "tool.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		f.write(f.Dir, "bin/run", "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(f.Dir, "bin/run"), 0o755); err != nil {
			t.Fatal(err)
		}
		f.exclude("*.log")
		f.write(f.Dir, "debug.log", "ignored")
		tip := f.git("rev-parse", "HEAD")
		before := statusOf(f.Dir)

		eng := f.engine()
		p, err := eng.Preflight("")
		if err != nil {
			t.Fatalf("Preflight: %v", err)
		}
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusMoved, "")
		if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
			t.Fatalf("record = %v, want a written ref", o.Record)
		}
		wantRootOnDefault(t, f)
		wantCopy(t, p.DestPath, tip, before, "refs/heads/topic")
		for file, want := range map[string]string{"README": "edited", "new.txt": "new", "staged.txt": "s"} {
			if b, err := os.ReadFile(filepath.Join(p.DestPath, file)); err != nil || string(b) != want {
				t.Errorf("%s in the new worktree = %q, %v; want %q", file, b, err, want)
			}
		}
		for _, file := range []string{"tool.sh", "bin/run"} {
			if fi, err := os.Stat(filepath.Join(p.DestPath, file)); err != nil || fi.Mode().Perm()&0o100 == 0 {
				t.Errorf("%s in the new worktree is not executable (%v, %v)", file, fi, err)
			}
		}
		if b, err := os.ReadFile(filepath.Join(f.Dir, "debug.log")); err != nil || string(b) != "ignored" {
			t.Errorf("the ignored file in the root = %q, %v; want it left", b, err)
		}
		if _, err := os.Lstat(filepath.Join(p.DestPath, "debug.log")); err == nil {
			t.Errorf("the ignored file was carried")
		}
		if got := rev(f.Dir, "refs/heads/topic"); got != tip {
			t.Errorf("topic = %s, want %s", got, tip)
		}
	})
	t.Run("rm --cached twin", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.onBranch("topic")
		f.Commit(f.Dir, "foo", "foo v1", "foo")
		f.git("rm", "-q", "--cached", "foo")
		tip := f.git("rev-parse", "HEAD")
		before := statusOf(f.Dir)

		eng := f.engine()
		p, err := eng.Preflight("")
		if err != nil {
			t.Fatalf("Preflight: %v", err)
		}
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusMoved, "")
		wantRootOnDefault(t, f)
		wantCopy(t, p.DestPath, tip, before, "refs/heads/topic")
		if b, err := os.ReadFile(filepath.Join(p.DestPath, "foo")); err != nil || string(b) != "foo v1" {
			t.Errorf("foo in the new worktree = %q, %v", b, err)
		}
	})
}

// TestExecuteRecoveryRows drives a failure into each row of the spec's
// recovery table (1-5, and the read-back, 7) and checks the row's Root, New
// worktree and Work columns against a before/after snapshot, the outcome
// code and the row's sentence in the reason. Why: every failure must stop
// where it happens with the work reachable, and say where it is; a row that
// reset the root early, or claimed moved, loses work or lies.
func TestExecuteRecoveryRows(t *testing.T) {
	t.Run("1 create fails", func(t *testing.T) {
		t.Parallel()
		f, eng, p := dirtyPlan(t)
		wt := filepath.Join(f.Dir, ".worktrees")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(wt, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(wt, 0o755) })
		if fh, err := os.Create(filepath.Join(wt, "probe")); err == nil {
			fh.Close()
			t.Skip("a 0555 directory is still writable here (root?)")
		}
		before := f.rootState()
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusFailed, rescue.CodeCreateFailed)
		wantReason(t, o, "The root is untouched and all the work is still in it")
		if after := f.rootState(); after != before {
			t.Errorf("the root changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if _, err := os.Lstat(p.DestPath); err == nil || o.Created {
			t.Errorf("destination exists (%v) or Created %v", err, o.Created)
		}
	})

	t.Run("2 carry fails", func(t *testing.T) {
		t.Parallel()
		f, eng, p := dirtyPlan(t)
		fired := false
		eng.Hooks.ExecuteInto = func(_ rescue.Engine, rp *rescue.Plan, dest string) rescue.Outcome {
			fired = true
			return rescue.Outcome{Status: rescue.StatusFailed, Plan: *rp, Code: rescue.CodeCarryFailed, Reason: "planted carry failure", Created: true}
		}
		before := f.rootCheckout()
		o := eng.Execute(p)
		if !fired {
			t.Fatal("ExecuteInto hook did not fire")
		}
		wantStatus(t, o, rescue.StatusFailed, rescue.CodeCarryFailed)
		wantReason(t, o, "planted carry failure", "The root is untouched and all the work is still in it", p.DestPath)
		if after := f.rootCheckout(); after != before {
			t.Errorf("the root changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		wantCopy(t, p.DestPath, p.Head, "", "")
		if !o.Created || len(f.records()) != 0 {
			t.Errorf("Created %v, records %q; want the half-built worktree and no record", o.Created, f.records())
		}
	})

	t.Run("3 no record", func(t *testing.T) {
		f, eng, p := dirtyPlan(t)
		fired := false
		eng.Hooks.ExecuteInto = func(_ rescue.Engine, rp *rescue.Plan, dest string) rescue.Outcome {
			fired = true
			pl := *rp
			pl.Warnings = append(slices.Clone(pl.Warnings), rescue.RecordWarningPrefix+"planted")
			return rescue.Outcome{Status: rescue.StatusMoved, Plan: pl, Created: true}
		}
		trace := traceGit(t)
		before := f.rootCheckout()
		o := eng.Execute(p)
		if !fired {
			t.Fatal("ExecuteInto hook did not fire")
		}
		wantStatus(t, o, rescue.StatusFailed, CodeRecordNotWritten)
		wantReason(t, o, "planted", "The root is untouched", "not released", p.DestPath)
		if after := f.rootCheckout(); after != before {
			t.Errorf("the root changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		noReset(t, trace(), f.Dir)
		if _, err := os.Stat(p.DestPath); err != nil || !o.Created {
			t.Errorf("the new worktree was not kept: %v, Created %v", err, o.Created)
		}
	})

	t.Run("4 before the reset", func(t *testing.T) {
		t.Run("root edited", func(t *testing.T) {
			f, eng, p := dirtyPlan(t)
			pre := statusOf(f.Dir)
			var edited string
			fired := false
			eng.Hooks.BeforeRelease = func() {
				fired = true
				f.write(f.Dir, "README", "edited again")
				edited = f.rootCheckout()
			}
			trace := traceGit(t)
			o := eng.Execute(p)
			if !fired {
				t.Fatal("BeforeRelease hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, CodeReleaseRefused)
			wantReason(t, o, "nothing was reset or removed", "in the verified copy at "+p.DestPath)
			noReset(t, trace(), f.Dir)
			if after := f.rootCheckout(); after != edited {
				t.Errorf("the root changed after the edit:\nwant:\n%s\ngot:\n%s", edited, after)
			}
			wantCopy(t, p.DestPath, p.Head, pre, "")
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Errorf("record = %v, want it kept", o.Record)
			}
		})
		t.Run("live claim", func(t *testing.T) {
			f, eng, p := dirtyPlan(t)
			pre := statusOf(f.Dir)
			fired := false
			eng.Hooks.BeforeRelease = func() {
				fired = true
				f.liveOther()
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("BeforeRelease hook did not fire")
			}
			wantStatus(t, o, rescue.StatusMoved, "")
			wantRootOnDefault(t, f)
			wantCopy(t, p.DestPath, p.Head, pre, "refs/heads/topic")
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Errorf("record = %v, want it kept", o.Record)
			}
		})
		t.Run("release refused", func(t *testing.T) {
			t.Parallel()
			f, eng, p := dirtyPlan(t)
			before := f.rootCheckout()
			fired := false
			eng.Hooks.ReleaseRoot = func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error) {
				fired = true
				return removal.ReleaseResult{}, &removal.ReleaseRefused{Reason: "planted refusal"}
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("ReleaseRoot hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, CodeReleaseRefused)
			wantReason(t, o, "planted refusal", "nothing was reset or removed")
			if after := f.rootCheckout(); after != before {
				t.Errorf("the root changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Errorf("record = %v, want it kept", o.Record)
			}
		})
	})

	t.Run("4 after the reset", func(t *testing.T) {
		finish := []string{"git switch main", "git switch topic"}
		statusFirst := "first run `git status` in the root"
		stub := func(t *testing.T, rf *removal.ReleaseFailed) (*fixture, *Plan, Outcome) {
			f, eng, p := dirtyPlan(t)
			pre := statusOf(f.Dir)
			fired := false
			eng.Hooks.ReleaseRoot = func(cleanup.Git, removal.RootRelease) (removal.ReleaseResult, error) {
				fired = true
				return rf.Result, rf
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("ReleaseRoot hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, CodeReleaseFailed)
			wantCopy(t, p.DestPath, p.Head, pre, "")
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Errorf("record = %v, want it kept", o.Record)
			}
			wantReason(t, o, p.DestPath, o.Record.Ref)
			return f, p, o
		}
		t.Run("switch", func(t *testing.T) {
			t.Parallel()
			_, _, o := stub(t, &removal.ReleaseFailed{Stage: removal.StageSwitch, Result: removal.ReleaseResult{Reset: true, Removed: 1}, Err: errors.New("planted switch failure")})
			wantReason(t, o, append(finish, "planted switch failure", "reset and clean on topic")...)
			if strings.Contains(o.Reason, "partly changed") || strings.Contains(o.Reason, statusFirst) {
				t.Errorf("a certain switch failure says the root may be partly changed or dirty: %q", o.Reason)
			}
		})
		t.Run("switch uncertain", func(t *testing.T) {
			t.Parallel()
			_, _, o := stub(t, &removal.ReleaseFailed{Stage: removal.StageSwitch, Result: removal.ReleaseResult{Reset: true, Removed: 1}, Uncertain: true, Err: errors.New("killed")})
			wantReason(t, o, append(finish, "the root may be partly changed: run git status there", statusFirst)...)
		})
		t.Run("reset", func(t *testing.T) {
			t.Parallel()
			_, _, o := stub(t, &removal.ReleaseFailed{Stage: removal.StageReset, Uncertain: true, Err: errors.New("planted reset failure")})
			wantReason(t, o, append(finish, "the root may be partly changed: run git status there", "still on topic", statusFirst)...)
		})
		t.Run("verify_clean", func(t *testing.T) {
			t.Parallel()
			_, _, o := stub(t, &removal.ReleaseFailed{Stage: removal.StageVerifyClean, Result: removal.ReleaseResult{Reset: true, Removed: 1}, Err: errors.New("planted leftovers")})
			wantReason(t, o, append(finish, "was reset on topic but is not clean", statusFirst)...)
		})
		t.Run("real release, then the root back on the branch", func(t *testing.T) {
			f, eng, p := dirtyPlan(t)
			pre := statusOf(f.Dir)
			trace := traceGit(t)
			fired := false
			eng.Hooks.ReleaseRoot = func(g cleanup.Git, r removal.RootRelease) (removal.ReleaseResult, error) {
				fired = true
				res, err := removal.ReleaseRoot(g, r)
				if err != nil {
					t.Errorf("the real release failed: %v", err)
				}
				gitIn(t, f.Dir, "switch", "-q", "topic") // the root holds the branch again
				return removal.ReleaseResult{Reset: true, Removed: res.Removed},
					&removal.ReleaseFailed{Stage: removal.StageSwitch, Result: removal.ReleaseResult{Reset: true, Removed: res.Removed}, Err: errors.New("planted conflict")}
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("ReleaseRoot hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, CodeReleaseFailed)
			wantReason(t, o, append(finish, p.DestPath)...)
			if !hasRun(trace(), resetArgv...) {
				t.Errorf("no traced git ran %q", resetArgv)
			}
			// Root: on the branch, clean. New worktree: the verified copy.
			// Work: in the copy and the record.
			if got := headRef(f.Dir); got != "refs/heads/topic" {
				t.Errorf("root HEAD = %q, want topic", got)
			}
			if got := statusOf(f.Dir); got != "" {
				t.Errorf("root status = %q, want clean", got)
			}
			wantCopy(t, p.DestPath, p.Head, pre, "")
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Fatalf("record = %v, want it kept", o.Record)
			}
			// The two manual finishes the reason names do finish it.
			gitIn(t, f.Dir, "switch", "-q", "main")
			gitIn(t, p.DestPath, "switch", "-q", "topic")
			wantRootOnDefault(t, f)
			wantCopy(t, p.DestPath, p.Head, pre, "refs/heads/topic")
		})
		t.Run("remove_untracked, real", func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.onBranch("topic")
			f.dirty()
			f.write(f.Dir, "sub/left.txt", "left")
			eng := f.engine()
			p, err := eng.Preflight("")
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			pre := statusOf(f.Dir)
			sub := filepath.Join(f.Dir, "sub")
			fired := false
			eng.Hooks.BeforeRelease = func() {
				fired = true
				if err := os.Chmod(sub, 0o555); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
			o := eng.Execute(p)
			if !fired {
				t.Fatal("BeforeRelease hook did not fire")
			}
			if o.Code == CodeReleaseRefused {
				t.Skipf("the read-only directory stopped the release before the reset: %s", o.Reason)
			}
			wantStatus(t, o, rescue.StatusFailed, CodeReleaseFailed)
			wantReason(t, o, "was reset on topic", "carried untracked", "first run `git status` in the root", "git switch main", "git switch topic")
			if b, err := os.ReadFile(filepath.Join(sub, "left.txt")); err != nil || string(b) != "left" {
				t.Errorf("the leftover file = %q, %v; want it still in the root", b, err)
			}
			if got := headRef(f.Dir); got != "refs/heads/topic" {
				t.Errorf("root HEAD = %q, want topic", got)
			}
			if b, _ := os.ReadFile(filepath.Join(f.Dir, "README")); string(b) != "init" {
				t.Errorf("root README = %q, want the reset's init", b)
			}
			wantCopy(t, p.DestPath, p.Head, pre, "")
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Errorf("record = %v, want it kept", o.Record)
			}
		})
	})

	t.Run("5 destination switch fails", func(t *testing.T) {
		t.Parallel()
		f, eng, p := dirtyPlan(t)
		pre := statusOf(f.Dir)
		fired := false
		eng.Hooks.StepGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
			if slices.Contains(args, "switch") {
				fired = true
				return cleanup.Out{Code: 128, Stderr: "planted switch failure"}, nil
			}
			return g.Run(args...)
		}
		o := eng.Execute(p)
		if !fired {
			t.Fatal("StepGit hook did not fire at the switch")
		}
		wantStatus(t, o, rescue.StatusFailed, CodeSwitchFailed)
		wantReason(t, o, "planted switch failure", "on main, clean", "git switch topic", p.DestPath, "finishes it")
		wantRootOnDefault(t, f)
		wantCopy(t, p.DestPath, p.Head, pre, "")
		if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
			t.Errorf("record = %v, want it kept", o.Record)
		}
	})

	t.Run("5 branch moved before the switch", func(t *testing.T) {
		t.Parallel()
		f, eng, p := dirtyPlan(t)
		pre := statusOf(f.Dir)
		var other string
		fired := false
		eng.Hooks.AfterRelease = func() {
			fired = true
			other = otherCommit(t, f.Dir, p.Head)
			gitIn(t, f.Dir, "update-ref", "refs/heads/topic", other)
		}
		o := eng.Execute(p)
		if !fired {
			t.Fatal("AfterRelease hook did not fire")
		}
		wantStatus(t, o, rescue.StatusFailed, CodeSwitchFailed)
		wantReason(t, o, "moved to "+other, "detached at "+p.Head, "nothing was lost", p.DestPath)
		if strings.Contains(o.Reason, "finishes it") {
			t.Errorf("a moved branch is told the switch finishes it: %q", o.Reason)
		}
		wantRootOnDefault(t, f)
		wantCopy(t, p.DestPath, p.Head, pre, "")
	})

	t.Run("7 read-back", func(t *testing.T) {
		t.Parallel()
		t.Run("root not clean", func(t *testing.T) {
			t.Parallel()
			f, eng, p := dirtyPlan(t)
			fired := false
			eng.Hooks.AfterRelease = func() {
				fired = true
				f.write(f.Dir, "stray.txt", "after the release")
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("AfterRelease hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "the root is not clean", "stray.txt")
		})
		// afterSwitch arms StepGit to run the real destination switch, then
		// move in dest, and checks the hook fired at the switch when the test
		// ends (the helper only arms it; the run is the caller's).
		afterSwitch := func(t *testing.T, eng *Engine, move func(dest string)) {
			t.Helper()
			fired := false
			eng.Hooks.StepGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
				o, err := g.Run(args...)
				if err == nil && o.Code == 0 && slices.Contains(args, "switch") {
					fired = true
					move(g.Dir)
				}
				return o, err
			}
			t.Cleanup(func() {
				if !fired {
					t.Error("StepGit hook did not fire at the switch")
				}
			})
		}
		t.Run("destination HEAD moved", func(t *testing.T) {
			t.Parallel()
			f, eng, p := dirtyPlan(t)
			var other string
			afterSwitch(t, &eng, func(dest string) {
				other = otherCommit(t, f.Dir, p.Head)
				gitIn(t, dest, "update-ref", "--no-deref", "HEAD", other)
			})
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "is at "+other+", not the branch tip "+p.Head)
		})
		// The destination at the right commit but not on the branch: the
		// commit check passes, so only the branch check can catch it.
		t.Run("destination detached", func(t *testing.T) {
			t.Parallel()
			_, eng, p := dirtyPlan(t)
			afterSwitch(t, &eng, func(dest string) { gitIn(t, dest, "switch", "-q", "--detach") })
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "the new worktree "+p.DestPath+" is on a detached HEAD, not topic")
		})
		t.Run("destination on another branch", func(t *testing.T) {
			t.Parallel()
			_, eng, p := dirtyPlan(t)
			afterSwitch(t, &eng, func(dest string) { gitIn(t, dest, "switch", "-q", "-c", "other") })
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "is on refs/heads/other, not topic")
		})
		t.Run("branch moved", func(t *testing.T) {
			t.Parallel()
			f, eng, p := dirtyPlan(t)
			var other string
			afterSwitch(t, &eng, func(dest string) {
				other = otherCommit(t, f.Dir, p.Head)
				gitIn(t, dest, "update-ref", "refs/heads/topic", other)
			})
			o := eng.Execute(p)
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "the branch topic is at "+other+", not "+p.Head)
		})
		t.Run("destination edited", func(t *testing.T) {
			t.Parallel()
			f, eng, p := dirtyPlan(t)
			fired := false
			eng.Hooks.AfterRelease = func() {
				fired = true
				f.write(p.DestPath, "README", "changed after the release")
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("AfterRelease hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "README", p.DestPath)
			if o.Record == nil || rev(f.Dir, o.Record.Ref) == "" {
				t.Errorf("record = %v, want it kept", o.Record)
			}
		})
		t.Run("root switched away", func(t *testing.T) {
			t.Parallel()
			f, eng, p := dirtyPlan(t)
			fired := false
			eng.Hooks.AfterRelease = func() {
				fired = true
				gitIn(t, f.Dir, "switch", "-q", "-c", "other")
			}
			o := eng.Execute(p)
			if !fired {
				t.Fatal("AfterRelease hook did not fire")
			}
			wantStatus(t, o, rescue.StatusFailed, rescue.CodeVerifyFailed)
			wantReason(t, o, "refs/heads/other", "not main")
		})
	})
}

// TestExecuteRecordNotWritableBlocksRelease makes the record's ref path a
// regular file, so the create-only ref write fails, and requires
// record_not_written with the root untouched and no reset run. Why: the
// user's decision is that the record is required; `wt rescue` treats a
// missing record as a warning, here it must stop the release (Review Focus 5).
func TestExecuteRecordNotWritableBlocksRelease(t *testing.T) {
	f, eng, p := dirtyPlan(t)
	refs := filepath.Join(f.commonDir(), "refs", "wt")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refs, "rescued"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The engine's record listing (cleanup.ListRecords' for-each-ref over
	// rescue.RecordRefPrefix) sees nothing for the hand-built broken ref: the
	// read names nothing, so no reader can take the shape for a record. This
	// is the Anti-goal-6 premise check.
	gittest.RoundTrip(t, f.Dir, "", "for-each-ref", "--format=%(refname)", rescue.RecordRefPrefix)
	// Characterization of git's broken-ref treatment for the
	// prefix-without-slash pattern — not the engine's read, which is the
	// round-trip above: git warns and ignores the file. The two patterns
	// behave differently.
	gittest.RoundTrip(t, f.Dir, "warning: ignoring broken ref refs/wt/rescued\n", "for-each-ref", "--format=%(refname)", "refs/wt/rescued")
	before := f.rootCheckout()
	trace := traceGit(t)
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusFailed, CodeRecordNotWritten)
	wantReason(t, o, "not released", p.DestPath)
	noReset(t, trace(), f.Dir)
	if after := f.rootCheckout(); after != before {
		t.Errorf("the root changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err := os.Stat(p.DestPath); err != nil {
		t.Errorf("the new worktree was not kept: %v", err)
	}
}

// TestExecuteRunsNoHooks installs post-checkout, reference-transaction and
// post-merge hooks that each touch a marker, runs a full move of a dirty root
// whose default can fast-forward, and requires no marker, with Preflight's
// post-checkout warning still in the outcome. Why: hooks are arbitrary code,
// and the detached add, the root switch and the destination switch and the fast-forward all run with them off.
func TestExecuteRunsNoHooks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.onBranch("topic")
	f.dirty()
	f.originAhead(1)
	marks := t.TempDir()
	for _, h := range []string{"post-checkout", "reference-transaction", "post-merge"} {
		f.hook(h, filepath.Join(marks, h))
	}
	eng := f.engine()
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	if !o.FastForwarded {
		t.Errorf("FastForwarded = false, want true (warnings %q)", o.Plan.Warnings)
	}
	ents, _ := os.ReadDir(marks)
	for _, e := range ents {
		t.Errorf("the %s hook ran", e.Name())
	}
	if !slices.ContainsFunc(o.Plan.Warnings, func(w string) bool { return strings.Contains(w, "post-checkout") }) {
		t.Errorf("Warnings = %q, want Preflight's post-checkout line", o.Plan.Warnings)
	}
}

// TestExecuteStartRecheck edits the root after Preflight. Without
// SkipStartRecheck Execute refuses at the start with nothing created; with
// it, the later checks still catch the change before anything is reset.
// Why: the confirmation prompt is a window in which the root can change
// (Review Focus 1).
func TestExecuteStartRecheck(t *testing.T) {
	t.Run("edit, rechecked", func(t *testing.T) {
		t.Parallel()
		f, eng, p := dirtyPlan(t)
		f.write(f.Dir, "README", "edited during the prompt")
		before := f.rootState()
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusRefused, rescue.CodeSourceChanged)
		wantReason(t, o, "nothing was created")
		if after := f.rootState(); after != before || o.Created {
			t.Errorf("Execute changed the repository (Created %v):\nbefore:\n%s\nafter:\n%s", o.Created, before, after)
		}
	})
	t.Run("edit, skipped", func(t *testing.T) {
		f, eng, p := dirtyPlan(t)
		p.SkipStartRecheck()
		f.write(f.Dir, "README", "edited during the prompt")
		before := f.rootCheckout()
		trace := traceGit(t)
		o := eng.Execute(p)
		wantStatus(t, o, rescue.StatusFailed, rescue.CodeSourceChangedMid)
		noReset(t, trace(), f.Dir)
		if after := f.rootCheckout(); after != before {
			t.Errorf("the root changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

// TestExecuteIgnoresClaimsEntirely makes the repository's wt-sessions
// directory unreadable (mode 0) after Preflight has read it, then runs a full
// move of a dirty root and requires it to succeed. Why: Execute must not read
// the session claims at all — a held root rides the plan as a warning and the
// user decides — so an unreadable claims directory cannot refuse or fail a
// move; the one claim read left is Preflight's.
func TestExecuteIgnoresClaimsEntirely(t *testing.T) {
	t.Parallel()
	f, eng, p := dirtyPlan(t)
	sessions := filepath.Join(f.commonDir(), "wt-sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessions, 0); err != nil {
		t.Skipf("the platform refuses chmod 0 on a directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessions, 0o755) })
	if _, err := os.ReadDir(sessions); err == nil {
		t.Skip("a 0-mode claims directory is still readable here (root?)")
	}
	pre := statusOf(f.Dir)
	o := eng.Execute(p)
	wantStatus(t, o, rescue.StatusMoved, "")
	wantRootOnDefault(t, f)
	wantCopy(t, p.DestPath, p.Head, pre, "refs/heads/topic")
}

// TestExecuteRunsGitInRootAndDestFromALinkedWorktree runs a move with the
// rescue Git standing in another linked worktree and records the directory
// of every stepGit call and the Git and root handed to releaseRoot. Why:
// rescue's G.Dir is the caller's directory; a step run there would switch or
// reset the wrong checkout.
func TestExecuteRunsGitInRootAndDestFromALinkedWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	side := f.Worktree("side")
	f.onBranch("topic")
	f.dirty()
	eng := f.engine()
	eng.Rescue.G.Dir = side
	p, err := eng.Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	root, dest := cleanup.ResolvePath(f.Dir), cleanup.ResolvePath(p.DestPath)
	var dirs []string
	stepFired, releaseFired := false, false
	eng.Hooks.StepGit = func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		stepFired = true
		dirs = append(dirs, g.Dir)
		return g.Run(args...)
	}
	eng.Hooks.ReleaseRoot = func(g cleanup.Git, r removal.RootRelease) (removal.ReleaseResult, error) {
		releaseFired = true
		dirs = append(dirs, g.Dir, r.Root)
		return removal.ReleaseRoot(g, r)
	}
	o := eng.Execute(p)
	if !stepFired {
		t.Fatal("StepGit hook did not fire")
	}
	if !releaseFired {
		t.Fatal("ReleaseRoot hook did not fire")
	}
	wantStatus(t, o, rescue.StatusMoved, "")
	if len(dirs) < 3 {
		t.Fatalf("recorded only %q", dirs)
	}
	for _, d := range dirs {
		if r := cleanup.ResolvePath(d); r != root && r != dest {
			t.Errorf("a step ran in %s, neither the root %s nor the destination %s", d, root, dest)
		}
	}
}
