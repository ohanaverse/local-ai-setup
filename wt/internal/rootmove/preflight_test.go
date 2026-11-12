package rootmove

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// localOnly is a repository with no remote at all, on branch, holding one
// commit unless empty is set (an unborn branch).
func localOnly(t *testing.T, branch string, empty bool) *fixture {
	t.Helper()
	f := &fixture{t: t, Dir: filepath.Join(t.TempDir(), "repo")}
	if b, err := exec.Command("git", "init", "-q", "-b", branch, f.Dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, b)
	}
	f.git("config", "user.email", "t@example.com")
	f.git("config", "user.name", "T")
	f.git("config", "commit.gpgsign", "false")
	f.exclude(".worktrees/")
	if !empty {
		f.Commit(f.Dir, "README", "init", "init")
	}
	return f
}

// refusalOf returns err's *rescue.Refusal, failing the test when err is not
// one.
func refusalOf(t *testing.T, err error) *rescue.Refusal {
	t.Helper()
	var r *rescue.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v (%T), want a *rescue.Refusal", err, err)
	}
	return r
}

// commonDir is the root's absolute git common directory.
func (f *fixture) commonDir() string {
	f.t.Helper()
	return f.git("rev-parse", "--path-format=absolute", "--git-common-dir")
}

// claim writes a session claim on the root for pid, started at started.
func (f *fixture) claim(pid int, started int64) {
	f.t.Helper()
	dir := filepath.Join(f.commonDir(), "wt-sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	body := fmt.Sprintf("pid=%d\nstarted=%d\npath=%s\nbranch=topic\nagent=claude\n", pid, started, cleanup.ResolvePath(f.Dir))
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// liveOther starts a process that outlives the test's checks and claims the
// root for it: another live session.
func (f *fixture) liveOther() int {
	f.t.Helper()
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		f.t.Fatal(err)
	}
	started := time.Now().Unix()
	f.t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	f.claim(cmd.Process.Pid, started)
	return cmd.Process.Pid
}

// TestPreflightHappyPlan plans a dirty root on topic (one unpushed commit, a
// staged, an unstaged and an untracked file) whose origin/main is ahead of
// main, and checks every field of the plan: the counts, the destination
// under .worktrees (named after the branch, or after --to), the branch, the
// default, the HEAD and DefaultFF. Why: the plan is what the prompt shows and
// what Execute acts on; a wrong field moves the wrong thing.
func TestPreflightHappyPlan(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.onBranch("topic")
	f.dirty()
	f.originAhead(2)
	head := f.git("rev-parse", "HEAD")
	before := f.rootState()

	p, err := f.engine().Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if p.Branch != "topic" || p.Default != "main" || p.Head != head || p.DirName != "topic" {
		t.Errorf("plan = branch %q default %q head %q dir %q; want topic main %s topic", p.Branch, p.Default, p.Head, p.DirName, head)
	}
	if cleanup.ResolvePath(p.Repo) != cleanup.ResolvePath(f.Dir) {
		t.Errorf("Repo = %s, want %s", p.Repo, f.Dir)
	}
	if want := filepath.Join(f.Dir, ".worktrees", "topic"); cleanup.ResolvePath(p.DestPath) != cleanup.ResolvePath(want) {
		t.Errorf("DestPath = %s, want %s", p.DestPath, want)
	}
	if c := p.Counts; c.Commits != 1 || c.Staged != 1 || c.Unstaged != 1 || c.Untracked != 1 {
		t.Errorf("Counts = %+v, want 1 commit, 1 staged, 1 unstaged, 1 untracked", c)
	}
	if !p.DefaultFF || p.FFNote != "" {
		t.Errorf("DefaultFF = %v (%q), want true with no note", p.DefaultFF, p.FFNote)
	}
	if after := f.rootState(); after != before {
		t.Errorf("Preflight changed the root:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	p.SkipStartRecheck() // must reach rescue's plan, not panic

	p2, err := f.engine().Preflight("elsewhere")
	if err != nil {
		t.Fatalf("Preflight --to: %v", err)
	}
	if want := filepath.Join(f.Dir, ".worktrees", "elsewhere"); p2.DirName != "elsewhere" || cleanup.ResolvePath(p2.DestPath) != cleanup.ResolvePath(want) {
		t.Errorf("--to elsewhere: dir %q dest %s, want %s", p2.DirName, p2.DestPath, want)
	}
	if p2.Branch != "topic" {
		t.Errorf("--to elsewhere: Branch = %q, want topic (the root's own)", p2.Branch)
	}
}

// TestPreflightRefusals builds one root state per refusal code (rootmove's
// own, the addenda's and rescue's that root mode keeps) and requires that
// code with the root, the refs and .worktrees unchanged. Why: every refusal
// happens before anything exists; one that changed the root, or answered
// with another code, would break the "refused means untouched" promise and
// the JSON contract.
func TestPreflightRefusals(t *testing.T) {
	t.Parallel()
	topic := func(t *testing.T) *fixture {
		f := newFixture(t)
		f.onBranch("topic")
		return f
	}
	cases := []struct {
		name  string
		build func(t *testing.T) *fixture
		to    string
		code  string
		says  []string // substrings the message must hold
	}{
		{"detached root", func(t *testing.T) *fixture {
			f := topic(t)
			f.git("switch", "-q", "--detach")
			return f
		}, "", CodeDetachedRoot, []string{"git switch -c"}},
		{"already on the default", func(t *testing.T) *fixture { return newFixture(t) }, "", CodeAlreadyDefault, []string{"main"}},
		{"default in a linked worktree", func(t *testing.T) *fixture {
			f := topic(t)
			f.git("worktree", "add", "-q", filepath.Join(f.Dir, ".worktrees", "m"), "main")
			return f
		}, "", CodeDefaultInWorktree, []string{"main"}},
		{"no commit", func(t *testing.T) *fixture { return localOnly(t, "main", true) }, "", CodeNoCommit, nil},
		{"ignored file at a path only the default tracks", func(t *testing.T) *fixture {
			f := newFixture(t)
			f.Commit(f.Dir, "gen.txt", "generated upstream", "track gen.txt")
			f.git("switch", "-q", "-c", "topic", "HEAD~1")
			f.Commit(f.Dir, "work.txt", "w", "work")
			f.exclude("gen.txt")
			f.write(f.Dir, "gen.txt", "precious local")
			return f
		}, "", CodeIgnoredCollision, []string{"gen.txt"}},
		{"no default branch: no refs at all", func(t *testing.T) *fixture { return localOnly(t, "trunk", false) }, "", CodeNoDefaultBranch, nil},
		{"no default branch: remote-only default", func(t *testing.T) *fixture {
			f := topic(t)
			f.git("branch", "-q", "-D", "main")
			return f
		}, "", CodeNoDefaultBranch, []string{"origin/main"}},
		{".worktrees not ignored", func(t *testing.T) *fixture {
			f := newFixtureExcluding(t, false)
			f.onBranch("topic")
			return f
		}, "", CodeWorktreesNotIgnored, []string{"info/exclude", "echo '.worktrees/'"}},
		{".worktrees exclude removed, a linked worktree inside", func(t *testing.T) *fixture {
			f := topic(t)
			f.Worktree("side")
			ex := f.git("rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
			if err := os.WriteFile(ex, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return f
		}, "", CodeWorktreesNotIgnored, nil},
		{"assume-unchanged entry on a clean root", func(t *testing.T) *fixture {
			f := topic(t)
			f.git("update-index", "--assume-unchanged", "README")
			return f
		}, "", CodeHiddenIndex, []string{"assume-unchanged", "skip-worktree", "git update-index --no-assume-unchanged <path>", "git sparse-checkout disable"}},
		{"skip-worktree entry on a clean root", func(t *testing.T) *fixture {
			f := topic(t)
			f.git("update-index", "--skip-worktree", "README")
			return f
		}, "", CodeHiddenIndex, []string{"sparse", "git sparse-checkout disable", "git update-index --no-assume-unchanged <path>"}},
		{"operation in progress", func(t *testing.T) *fixture {
			f := topic(t)
			side := f.Worktree("side")
			f.Commit(f.Dir, "a.txt", "one", "one")
			f.Commit(side, "a.txt", "two", "two")
			if out, err := exec.Command("git", "-C", f.Dir, "merge", "side").CombinedOutput(); err == nil {
				t.Fatalf("the merge was meant to stop on a conflict:\n%s", out)
			}
			return f
		}, "", rescue.CodeOperation, nil},
		{"unmerged paths", func(t *testing.T) *fixture {
			f := topic(t)
			h := f.git("hash-object", "-w", "README")
			cmd := exec.Command("git", "-C", f.Dir, "update-index", "--index-info")
			cmd.Stdin = strings.NewReader("100644 " + h + " 1\tconf\n100644 " + h + " 2\tconf\n100644 " + h + " 3\tconf\n")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("update-index: %v\n%s", err, out)
			}
			return f
		}, "", rescue.CodeUnmerged, nil},
		{"staged and modified", func(t *testing.T) *fixture {
			f := topic(t)
			f.write(f.Dir, "README", "staged")
			f.git("add", "README")
			f.write(f.Dir, "README", "staged then edited")
			return f
		}, "", rescue.CodeStagedAndEdited, nil},
		{"too large", func(t *testing.T) *fixture {
			f := topic(t)
			// A sparse file: its size counts, its blocks are never written.
			fh, err := os.Create(filepath.Join(f.Dir, "big.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if err := fh.Truncate(rescue.MaxUntrackedBytes + 1); err != nil {
				t.Fatal(err)
			}
			fh.Close()
			return f
		}, "", rescue.CodeTooLarge, nil},
		{"destination name taken", func(t *testing.T) *fixture {
			f := topic(t)
			if err := os.MkdirAll(filepath.Join(f.Dir, ".worktrees", "taken"), 0o755); err != nil {
				t.Fatal(err)
			}
			return f
		}, "taken", rescue.CodeNameUnavailable, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := c.build(t)
			eng := f.engine()
			before := f.rootState()
			p, err := eng.Preflight(c.to)
			if p != nil {
				t.Errorf("a refusal returned a plan %+v", *p)
			}
			r := refusalOf(t, err)
			if r.Code != c.code {
				t.Errorf("code = %q (%s), want %q", r.Code, r.Message, c.code)
			}
			for _, s := range c.says {
				if !strings.Contains(r.Message, s) {
					t.Errorf("message %q does not say %q", r.Message, s)
				}
			}
			if after := f.rootState(); after != before {
				t.Errorf("a refusal changed the root:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestPreflightRefusesWhatTheReleaseGateWouldRefuse builds the state of
// internal/removal's ignored-file gate (a tracked README replaced by a
// readme/ directory holding an ignored file, with core.ignorecase on) and
// requires ignored_collision naming that file, with the root and the file
// untouched. Why: ReleaseRoot refuses it only after the copy and the record
// exist; the user must hear it before anything is created.
func TestPreflightRefusesWhatTheReleaseGateWouldRefuse(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.onBranch("topic")
	f.git("config", "core.ignorecase", "true")
	f.exclude("*.secret")
	if err := os.Remove(filepath.Join(f.Dir, "README")); err != nil {
		t.Fatal(err)
	}
	f.write(f.Dir, "readme/note.txt", "note")
	f.write(f.Dir, "readme/local.secret", "precious")
	eng := f.engine()
	before := f.rootState()
	_, err := eng.Preflight("")
	r := refusalOf(t, err)
	if r.Code != CodeIgnoredCollision || !strings.Contains(r.Message, "readme/local.secret") || !strings.Contains(r.Message, "the reset would") {
		t.Errorf("refusal = %s %q, want ignored_collision from the release gate's check naming readme/local.secret", r.Code, r.Message)
	}
	if after := f.rootState(); after != before {
		t.Errorf("a refusal changed the root:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if b, err := os.ReadFile(filepath.Join(f.Dir, "readme/local.secret")); err != nil || string(b) != "precious" {
		t.Errorf("the ignored file = %q, %v; want it kept", b, err)
	}
}

// TestPreflightClaims checks the live-session gate around the held warning: a
// claim of a dead pid and this process's own claim hold nothing (no held
// warning, the plan proceeds); an unreadable claims directory is a read
// failure, not a warning and not a pass. Why: a stale claim or one's own must
// not warn the user off a move, while claims nobody can read must not read as
// "no holder". A live claim of another process warns:
// TestPreflightHeldWarnsAndPlans.
func TestPreflightClaims(t *testing.T) {
	t.Parallel()
	t.Run("dead pid does not hold", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.onBranch("topic")
		cmd := exec.Command("true")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		f.claim(cmd.Process.Pid, time.Now().Unix())
		p, err := f.engine().Preflight("")
		if err != nil {
			t.Fatalf("Preflight: %v, want a plan (a stale claim does not hold)", err)
		}
		if ws := heldWarnings(p); len(ws) != 0 {
			t.Errorf("held warnings = %q, want none (a stale claim does not hold)", ws)
		}
	})
	t.Run("own claim does not hold", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.onBranch("topic")
		self, err := sessionclaim.Self(cleanup.ResolvePath(f.Dir), "topic", "wt")
		if err != nil {
			t.Fatal(err)
		}
		f.claim(self.PID, self.Started)
		p, err := f.engine().Preflight("")
		if err != nil {
			t.Fatalf("Preflight: %v, want a plan (our own claim does not hold)", err)
		}
		if ws := heldWarnings(p); len(ws) != 0 {
			t.Errorf("held warnings = %q, want none (our own claim does not hold)", ws)
		}
	})
	t.Run("unreadable claims directory is a read failure", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.onBranch("topic")
		eng := f.engine()
		dir := filepath.Join(f.commonDir(), "wt-sessions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		_, err := eng.Preflight("")
		var r *rescue.Refusal
		if err == nil || errors.As(err, &r) || !strings.Contains(err.Error(), "session claims") {
			t.Errorf("err = %v, want a read failure of the claims (not a refusal, not a pass)", err)
		}
	})
}

// heldWarnings returns p's warnings that are held lines (HeldWarningPrefix),
// which is what the held tests pin: rescue's own warnings ride the plan too.
func heldWarnings(p *Plan) []string {
	var out []string
	for _, w := range p.Warnings {
		if strings.HasPrefix(w, HeldWarningPrefix) {
			out = append(out, w)
		}
	}
	return out
}

// procStart returns pid's start time in epoch seconds — what a claim records
// (docs/contracts/session-claims.md: equal to the real start within 5 s). A
// fixture "other session" must read live under that rule, and this package
// cannot fake sessionclaim's process seams (unexported), so the claim names a
// real child's real start time, read from ps's lstart in the local zone.
// (Shape: rootclaim's holdlaunch_test, cleanup's snapshot_test, rescue's
// preflight_test.)
func procStart(t *testing.T, pid int) int64 {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C") // English month names for the parse
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps lstart for %d: %v", pid, err)
	}
	start, err := time.ParseInLocation(time.ANSIC, strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatalf("parse lstart %q: %v", out, err)
	}
	return start.Unix()
}

// holderClaim spawns a short-lived real process and claims path for it under
// commonDir — a fixture "other session" that reads live under the claim
// contract's liveness rule (real pid, real start time, never manufactured).
// The child and its claim are cleaned up. (Shape: rootclaim's holdlaunch_test,
// cleanup's snapshot_test, rescue's preflight_test.)
func holderClaim(t *testing.T, commonDir, path, branch, agent string) sessionclaim.Claim {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	c := sessionclaim.Claim{PID: child.Process.Pid, Started: procStart(t, child.Process.Pid), Path: path, Branch: branch, Agent: agent}
	release, err := sessionclaim.Hold(commonDir, c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return c
}

// TestPreflightHeldWarnsAndPlans checks a root another live wt session holds
// warns and still plans: Preflight appends exactly one HeldWarningPrefix
// warning naming the holder (pid included) to the plan and returns the plan —
// held is a label to acknowledge, never a refusal. The fixture is a real live
// claim (running process, real start time), and the line is pinned word for
// word: the established held rendering after the prefix. The symlink case
// claims a different spelling of the root's own directory (the /var and
// /private/var pair as one directory) and must fold into the same warning —
// cleanup.ResolvePath's keying, never raw string equality. Why: held stopped
// being a code and became a plan warning; a plan that refused it would block
// a move the user may still choose, and a re-worded line would drift from the
// rendering the refusals carried.
func TestPreflightHeldWarnsAndPlans(t *testing.T) {
	t.Parallel()
	// wantLine is the exact composed line at now: the prefix, the established
	// held wording and sessionclaim.Describe's entry for c.
	wantLine := func(c sessionclaim.Claim, now time.Time) string {
		return HeldWarningPrefix + "another wt session is running in the root: " +
			cleanup.OneLine(sessionclaim.Describe(c, now)) +
			"; end it first (Execute checks again just before the root changes)"
	}
	for _, c := range []struct {
		name string
		path func(t *testing.T, f *fixture) string // the spelling the claim records
	}{
		{"root", func(t *testing.T, f *fixture) string { return cleanup.ResolvePath(f.Dir) }},
		{"symlink spelling folds", func(t *testing.T, f *fixture) string {
			t.Helper()
			alias := filepath.Join(t.TempDir(), "root-alias")
			if err := os.Symlink(f.Dir, alias); err != nil {
				t.Fatal(err)
			}
			return alias
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.onBranch("topic")
			claim := holderClaim(t, f.commonDir(), c.path(t, f), "topic", "codex")
			eng := f.engine()
			t0 := time.Now()
			p, err := eng.Preflight("")
			t1 := time.Now()
			if err != nil {
				t.Fatalf("Preflight = %v; a held root warns and plans, it never refuses", err)
			}
			if p.Branch != "topic" || p.DirName != "topic" {
				t.Errorf("plan = branch %q dir %q, want the move planned (topic, topic)", p.Branch, p.DirName)
			}
			ws := heldWarnings(p)
			if len(ws) != 1 {
				t.Fatalf("held warnings = %q, want exactly one naming the holder", ws)
			}
			// The exact composed line. Describe's "running" term is the one
			// clocked piece (now minus the claim's start, rounded to seconds,
			// monotone in now), so the want walks exactly the renderings the
			// call could have made between t0 and t1 and the line must equal
			// one of them byte for byte.
			start := time.Unix(claim.Started, 0)
			matched := false
			for d := t0.Sub(start).Round(time.Second); d <= t1.Sub(start).Round(time.Second); d += time.Second {
				if ws[0] == wantLine(claim, start.Add(d)) {
					matched = true
				}
			}
			if !matched {
				t.Errorf("warning = %q, want the exact composed line (e.g. %q)", ws[0], wantLine(claim, t0))
			}
		})
	}
}

// TestPreflightHeldFromLinkedWorktreeClaimIsIgnored checks a real live claim
// on a LINKED worktree's path never warns about the root: Preflight plans with
// no warning at all. Why: "held" must describe the root's own holder — a
// session working in some other checkout says nothing about moving the root,
// and a warning naming it would send the user to end an unrelated session.
func TestPreflightHeldFromLinkedWorktreeClaimIsIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.onBranch("topic")
	side := f.Worktree("side")
	holderClaim(t, f.commonDir(), side, "side", "codex")
	p, err := f.engine().Preflight("")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("Warnings = %q, want none (the linked claim is not the root's)", p.Warnings)
	}
}

// TestDefaultFFTable runs Preflight over each relation of main to origin/main
// and requires DefaultFF only when origin/main is strictly ahead (main
// behind), with the reason in FFNote otherwise; none of them refuses. Why:
// the fast-forward after the move is `merge --ff-only` and must never be
// attempted (or reported as possible) when it would merge or lose commits.
func TestDefaultFFTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup func(f *fixture)
		ff    bool
		note  string
	}{
		{"strictly behind", func(f *fixture) { f.originAhead(1) }, true, ""},
		{"equal", func(f *fixture) {}, false, "already"},
		{"ahead", func(f *fixture) { f.Commit(f.Dir, "local.txt", "l", "local") }, false, "ahead"},
		{"diverged", func(f *fixture) {
			f.Commit(f.Dir, "local.txt", "l", "local")
			f.originAhead(1)
		}, false, "diverged"},
		{"no remote", func(f *fixture) {
			f.git("remote", "set-head", "origin", "-d")
			f.git("update-ref", "-d", "refs/remotes/origin/main")
		}, false, "does not exist"},
		{"origin/HEAD unset, main local", func(f *fixture) { f.git("remote", "set-head", "origin", "-d") }, false, "already"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			c.setup(f) // on main
			f.onBranch("topic")
			p, err := f.engine().Preflight("")
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			if p.DefaultFF != c.ff {
				t.Errorf("DefaultFF = %v (%q), want %v", p.DefaultFF, p.FFNote, c.ff)
			}
			if c.note == "" && p.FFNote != "" || !strings.Contains(p.FFNote, c.note) {
				t.Errorf("FFNote = %q, want it to say %q", p.FFNote, c.note)
			}
		})
	}
}

// TestPreflightNeverFetches traces every git process Preflight starts
// (GIT_TRACE, inherited by every child) and requires no fetch among them.
// Why: the one fetch of a run is cleanup.Gather's, before Preflight; a second
// one inside it would reach the network on --dry-run and --no-fetch too.
func TestPreflightNeverFetches(t *testing.T) {
	f := newFixture(t)
	f.onBranch("topic")
	f.dirty()
	eng := f.engine()
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	if _, err := eng.Preflight(""); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	b, err := os.ReadFile(trace)
	if err != nil || len(b) == 0 {
		t.Fatalf("no git trace (%v): the recording did not work", err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "fetch") {
			t.Errorf("Preflight ran a fetch: %s", l)
		}
	}
}

// TestPreflightWarnsOfAPostCheckoutHook installs an executable post-checkout
// hook and requires the plan to carry rescue's own warning about it, word for
// word, for a dirty root and a clean one, without running the hook; with no
// hook there is no such warning. Why: the destination is created with hooks
// off, so whatever the hook sets up is missing there and the user must be
// told; a re-worded copy would drift from rescue's.
func TestPreflightWarnsOfAPostCheckoutHook(t *testing.T) {
	t.Parallel()
	for _, dirty := range []bool{true, false} {
		t.Run(fmt.Sprintf("dirty=%v", dirty), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.onBranch("topic")
			if dirty {
				f.dirty()
			}
			marker := filepath.Join(t.TempDir(), "ran")
			f.hook("post-checkout", marker)
			eng := f.engine()
			p, err := eng.Preflight("")
			if err != nil {
				t.Fatalf("Preflight: %v", err)
			}
			rp, err := eng.Rescue.Preflight(cleanup.Row{Kind: cleanup.KindWorktree, Path: f.Dir, Ref: "refs/heads/topic"}, "")
			if err != nil {
				t.Fatalf("rescue Preflight: %v", err)
			}
			i := slices.IndexFunc(rp.Warnings, func(w string) bool { return strings.Contains(w, "post-checkout") })
			if i < 0 || !strings.Contains(rp.Warnings[i], "did not run in the new worktree") {
				t.Fatalf("rescue's warnings %q hold no post-checkout line", rp.Warnings)
			}
			if !slices.Contains(p.Warnings, rp.Warnings[i]) {
				t.Errorf("Warnings = %q, want rescue's line %q", p.Warnings, rp.Warnings[i])
			}
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("Preflight ran the hook")
			}
		})
	}
	t.Run("no hook", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.onBranch("topic")
		p, err := f.engine().Preflight("")
		if err != nil {
			t.Fatalf("Preflight: %v", err)
		}
		for _, w := range p.Warnings {
			if strings.Contains(w, "post-checkout") {
				t.Errorf("warning %q with no hook", w)
			}
		}
	})
}

// TestGitAtCopiesTimeoutConfigEnvButNotDir builds an Engine whose rescue Git
// stands in a linked worktree with a timeout, config and env, and requires
// gitAt(root) to run in the root with the same timeout and env and the config
// prefixed by core.fsmonitor=false. Why: rescue's G.Dir is the caller's
// directory, which may be a linked worktree; reading "the root" there would
// read the wrong checkout.
func TestGitAtCopiesTimeoutConfigEnvButNotDir(t *testing.T) {
	t.Parallel()
	e := Engine{Rescue: rescue.Engine{G: cleanup.Git{
		Dir:     "/repo/.worktrees/side",
		Timeout: 7 * time.Second,
		Config:  []string{"a.b=c", "d.e=f"},
		Env:     []string{"X=1"},
	}}}
	g := e.gitAt("/repo")
	if g.Dir != "/repo" {
		t.Errorf("Dir = %q, want /repo", g.Dir)
	}
	if g.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", g.Timeout)
	}
	if want := []string{"core.fsmonitor=false", "a.b=c", "d.e=f"}; !slices.Equal(g.Config, want) {
		t.Errorf("Config = %q, want %q", g.Config, want)
	}
	if !slices.Equal(g.Env, []string{"X=1"}) {
		t.Errorf("Env = %q", g.Env)
	}
	// The engine's own slices must not be shared with the copy.
	g.Config[1] = "changed"
	if e.Rescue.G.Config[0] != "a.b=c" {
		t.Errorf("gitAt shares Config with the engine")
	}
}
