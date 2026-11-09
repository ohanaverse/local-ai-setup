package removal

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// hooksOffArg is the -c settings every root write carries: hooks off, and no
// submodule recursion whatever the user's submodule.recurse says.
var hooksOffArg = []string{"-c", "core.hooksPath=/dev/null", "-c", "submodule.recurse=false"}

// rootOnBranch switches the primary checkout to a new branch at HEAD and
// commits one file on it (unpushed): the root state `wt restore-root` moves.
func (f *fixture) rootOnBranch(branch string) {
	f.t.Helper()
	f.git("switch", "-q", "-c", branch)
	f.Commit(f.Dir, strings.ReplaceAll(branch, "/", "-")+".txt", "work", branch+" work")
}

// dirtyRoot gives the root a staged new file, an unstaged edit of the tracked
// README and two untracked files, one nested; it returns the untracked paths,
// relative to the root.
func (f *fixture) dirtyRoot() []string {
	f.t.Helper()
	f.write(f.Dir, "staged.txt", "s")
	f.git("add", "staged.txt")
	f.write(f.Dir, "README", "edited")
	f.write(f.Dir, "new.txt", "n")
	f.write(f.Dir, "sub/deep.txt", "d")
	return []string{"new.txt", "sub/deep.txt"}
}

// rootRescue is the root carried by the real rescue engine in root mode: the
// plan, the detached destination, the record ref, and the fingerprint check
// (plan.SourceUnchanged) a release compares against.
type rootRescue struct {
	Plan        *rescue.Plan
	Dest, Ref   string
	Fingerprint func() (string, error)
}

// rootPlan is rescue's root-mode Preflight of the primary checkout, as
// rootmove calls it.
func rootPlan(t *testing.T, f *fixture) (rescue.Engine, *rescue.Plan) {
	t.Helper()
	noGh(t)
	s, r := f.report()
	eng := rescue.Engine{G: cleanup.Git{Dir: f.Dir}, Prior: s, In: f.inputs(), Classify: cleanup.Options{Now: later()}, ForRoot: true}
	plan, err := eng.Preflight(rowAt(t, r, f.Dir), "")
	if err != nil {
		t.Fatalf("rescue root Preflight: %v", err)
	}
	return eng, plan
}

// rescueRoot runs the real rescue engine on the root in root mode
// (Preflight, CreateDetachedAt, ExecuteInto), requiring a moved outcome with a
// record, as rootmove will before it calls ReleaseRoot.
func rescueRoot(t *testing.T, f *fixture) rootRescue {
	t.Helper()
	eng, plan := rootPlan(t, f)
	dest, err := worktree.CreateDetachedAt(f.Dir, plan.DirName, plan.Branch, plan.Start)
	if err != nil {
		t.Fatalf("CreateDetachedAt: %v", err)
	}
	o := eng.ExecuteInto(plan, dest)
	if o.Status != rescue.StatusMoved || o.Record == nil {
		t.Fatalf("rescue root = %s %s %s, record %v; want moved with a record", o.Status, o.Code, o.Reason, o.Record)
	}
	return rootRescue{Plan: plan, Dest: dest, Ref: o.Record.Ref, Fingerprint: plan.SourceUnchanged}
}

// release is the RootRelease rootmove builds from a verified, recorded copy.
func (rr rootRescue) release(f *fixture) RootRelease {
	return RootRelease{Root: f.Dir, Default: "main", Verified: true, RecordRef: rr.Ref,
		Untracked: rr.Plan.UntrackedPaths(), Unchanged: rr.Fingerprint}
}

// rootCall is one git call ReleaseRoot made through the runGit seam.
type rootCall struct {
	Dir    string
	Config []string // the -c settings the call ran with (cleanup.Git.Config)
	Args   []string
}

// recordRootGit records the directory and argument vector of every git call
// made through runGit, then runs it for real.
func recordRootGit(t *testing.T) *[]rootCall {
	t.Helper()
	return recordRootGitAround(t, nil)
}

// recordRootGitAround is recordRootGit with around wrapping each call: it
// gets the arguments and the real run, and decides what the caller sees (a
// failure instead, or the real result after a change on disk). nil runs the
// call as is.
func recordRootGitAround(t *testing.T, around func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error)) *[]rootCall {
	t.Helper()
	var calls []rootCall
	stubRunGit(t, func(g cleanup.Git, args ...string) (cleanup.Out, error) {
		calls = append(calls, rootCall{Dir: g.Dir, Config: slices.Clone(g.Config), Args: slices.Clone(args)})
		run := func() (cleanup.Out, error) { return g.Run(args...) }
		if around == nil {
			return run()
		}
		return around(args, run)
	})
	return &calls
}

// isReset and isSwitch say whether a git argv is the root's reset or switch.
func isReset(args []string) bool  { return slices.Contains(args, "reset") }
func isSwitch(args []string) bool { return slices.Contains(args, "switch") }

// writes are the calls that change the checkout: every reset and switch.
func writes(calls []rootCall) [][]string {
	var out [][]string
	for _, c := range calls {
		if isReset(c.Args) || isSwitch(c.Args) || slices.Contains(c.Args, "clean") || slices.Contains(c.Args, "--hard") {
			out = append(out, c.Args)
		}
	}
	return out
}

// snapshot is dir's checkout as text: the branch, the full status (ignored
// entries too) and every file's and link's content, .git and .worktrees
// excepted. Two equal snapshots mean nothing in the checkout changed.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	// --abbrev-ref says HEAD for a detached checkout (the copy is one).
	fmt.Fprintf(&b, "head %s\nbranch %s\n", gitIn(t, dir, "rev-parse", "HEAD"), gitIn(t, dir, "rev-parse", "--abbrev-ref", "HEAD"))
	b.WriteString(gitIn(t, dir, "status", "--porcelain=v2", "--untracked-files=all", "--ignored=matching"))
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".worktrees") && p != dir {
			return filepath.SkipDir
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			tgt, err := os.Readlink(p)
			if err != nil {
				return err
			}
			lines = append(lines, "link "+rel+" -> "+tgt)
		case d.IsDir():
			lines = append(lines, "dir "+rel)
		default:
			c, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			lines = append(lines, fmt.Sprintf("file %s %x", rel, sha256.Sum256(c)))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	b.WriteString("\n" + strings.Join(lines, "\n"))
	return b.String()
}

// branchOf is the short name of the branch dir has checked out.
func branchOf(t *testing.T, dir string) string {
	t.Helper()
	return gitIn(t, dir, "symbolic-ref", "-q", "--short", "HEAD")
}

// readFile is p's content, or "<absent>".
func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "<absent>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lexists reports whether p is on disk (a dangling link counts).
func lexists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// requireReleasedClean fails unless dir is on main with a clean status
// (untracked files included).
func requireReleasedClean(t *testing.T, dir string) {
	t.Helper()
	if b := branchOf(t, dir); b != "main" {
		t.Errorf("root on %q, want main", b)
	}
	if st := gitIn(t, dir, "status", "--porcelain=v2", "--untracked-files=all"); st != "" {
		t.Errorf("root not clean after the release:\n%s", st)
	}
}

// failedAt asserts err is a *ReleaseFailed at stage and returns it.
func failedAt(t *testing.T, err error, stage string) *ReleaseFailed {
	t.Helper()
	var rf *ReleaseFailed
	if !errors.As(err, &rf) {
		t.Fatalf("err = %v (%T), want *ReleaseFailed at %s", err, err, stage)
	}
	if rf.Stage != stage {
		t.Fatalf("stage %q (%v), want %q", rf.Stage, rf.Err, stage)
	}
	return rf
}

// TestReleaseRootHappyPath releases a dirty root the real rescue engine has
// copied and recorded: exactly two writes (the reset and the switch, both
// with hooks off), every call in the root, and afterwards the staged,
// unstaged and untracked work is gone from the root, which is clean on main,
// while an ignored file and an empty directory are left. Why: this is the
// one sequence that resets the user's checkout; any other argv or directory
// would be a different, unproven destruction.
func TestReleaseRootHappyPath(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	untracked := f.dirtyRoot()
	f.Ignored(f.Dir, "ign.log")
	if err := os.Mkdir(filepath.Join(f.Dir, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}
	rr := rescueRoot(t, f)
	if got := rr.Plan.UntrackedPaths(); !slices.Equal(got, untracked) {
		t.Fatalf("precondition: plan carries untracked %v, want %v", got, untracked)
	}
	calls := recordRootGit(t)

	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	if err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if want := (ReleaseResult{Reset: true, Removed: 2, Switched: true}); res != want {
		t.Errorf("result %+v, want %+v", res, want)
	}
	wantWrites := [][]string{
		append(slices.Clone(hooksOffArg), "reset", "--hard", "HEAD"),
		append(slices.Clone(hooksOffArg), "switch", "--no-overwrite-ignore", "main"),
	}
	if got := writes(*calls); !slices.EqualFunc(got, wantWrites, slices.Equal) {
		t.Errorf("writes %q, want %q", got, wantWrites)
	}
	for _, c := range *calls {
		if c.Dir != f.Dir {
			t.Errorf("git %q ran in %s, want the root %s", c.Args, c.Dir, f.Dir)
		}
	}
	requireReleasedClean(t, f.Dir)
	for _, p := range []string{"staged.txt", "new.txt", "sub/deep.txt", "topic.txt"} {
		if lexists(t, filepath.Join(f.Dir, p)) {
			t.Errorf("%s still in the root", p)
		}
	}
	if got := readFile(t, filepath.Join(f.Dir, "README")); got != "init" {
		t.Errorf("README = %q, want main's %q", got, "init")
	}
	if got := readFile(t, filepath.Join(f.Dir, "ign.log")); got != "ignored" {
		t.Errorf("ignored file = %q, want it left", got)
	}
	for _, d := range []string{"emptydir", "sub"} {
		if fi, err := os.Stat(filepath.Join(f.Dir, d)); err != nil || !fi.IsDir() {
			t.Errorf("directory %s not left (%v)", d, err)
		}
	}
	// The work is in the copy: the destination holds every carried change.
	if got := readFile(t, filepath.Join(rr.Dest, "README")); got != "edited" {
		t.Errorf("copy's README = %q, want the carried edit", got)
	}
	if got := readFile(t, filepath.Join(rr.Dest, "sub/deep.txt")); got != "d" {
		t.Errorf("copy's sub/deep.txt = %q, want the carried file", got)
	}
}

// TestReleaseRootRefusesWithoutAnyOfTheThreeProofs takes a dirty root with a
// real verified copy and record, then removes one proof per case (or breaks
// an input): each must be a *ReleaseRefused with no write run, the root's
// files, status and branch exactly as before. Why: the three proofs (verified
// copy, record, unchanged fingerprint) are the whole safety argument of the
// reset; any one missing means the root's work might exist nowhere else.
func TestReleaseRootRefusesWithoutAnyOfTheThreeProofs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *fixture, rr rootRescue, rel *RootRelease)
	}{
		{"not verified", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Verified = false }},
		{"empty record ref", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.RecordRef = "" }},
		{"record prefix only", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.RecordRef = cleanup.RecordRefPrefix
		}},
		{"foreign record ref", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.RecordRef = "refs/heads/main" }},
		{"another record's name", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.RecordRef = cleanup.RecordRefPrefix + "0123456789abcdef"
		}},
		{"record deleted", func(t *testing.T, f *fixture, rr rootRescue, _ *RootRelease) {
			gitIn(t, f.Dir, "update-ref", "-d", rr.Ref)
		}},
		{"fingerprint check nil", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Unchanged = nil }},
		{"fingerprint differs", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.Unchanged = func() (string, error) { return "the status", nil }
		}},
		{"fingerprint unreadable", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.Unchanged = func() (string, error) { return "", errors.New("injected read failure") }
		}},
		{"root edited after the record", func(t *testing.T, f *fixture, _ rootRescue, _ *RootRelease) {
			f.write(f.Dir, "README", "edited again")
		}},
		{"root edited, fingerprint blind (record gate)", func(t *testing.T, f *fixture, _ rootRescue, rel *RootRelease) {
			f.write(f.Dir, "README", "edited again")
			rel.Unchanged = func() (string, error) { return "", nil }
		}},
		{"new untracked file, fingerprint blind", func(t *testing.T, f *fixture, _ rootRescue, rel *RootRelease) {
			f.write(f.Dir, "late.txt", "late")
			rel.Unchanged = func() (string, error) { return "", nil }
		}},
		{"carried list misses a path", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Untracked = rel.Untracked[:1] }},
		{"carried list names a tracked file", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.Untracked = append(rel.Untracked, "README")
		}},
		{"carried list names an ignored file", func(t *testing.T, f *fixture, _ rootRescue, rel *RootRelease) {
			rel.Untracked = append(rel.Untracked, "ign.log")
		}},
		{"carried list repeats a path", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.Untracked = append(rel.Untracked, rel.Untracked[0])
		}},
		{"carried list escapes with ..", func(t *testing.T, f *fixture, _ rootRescue, rel *RootRelease) {
			rel.Untracked = append(rel.Untracked, "../escape.txt")
		}},
		{"carried list holds an absolute path", func(t *testing.T, f *fixture, _ rootRescue, rel *RootRelease) {
			rel.Untracked = []string{filepath.Join(f.Dir, "new.txt"), "sub/deep.txt"}
		}},
		{"relative root", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Root = "repo" }},
		{"empty root", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Root = "" }},
		{"root is a subdirectory", func(_ *testing.T, f *fixture, _ rootRescue, rel *RootRelease) { rel.Root = filepath.Join(f.Dir, "sub") }},
		{"root is a linked worktree", func(_ *testing.T, _ *fixture, rr rootRescue, rel *RootRelease) { rel.Root = rr.Dest }},
		{"default branch missing", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Default = "nope" }},
		{"default branch empty", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Default = "" }},
		{"default branch an option", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) { rel.Default = "-f" }},
		{"root on a detached HEAD", func(t *testing.T, f *fixture, _ rootRescue, _ *RootRelease) {
			f.git("switch", "-q", "--detach")
		}},
		{"default branch checked out in another worktree", func(t *testing.T, f *fixture, _ rootRescue, _ *RootRelease) {
			f.git("worktree", "add", "-q", filepath.Join(f.Dir, ".worktrees", "on-main"), "main")
		}},
		{"nothing uncommitted claimed on a dirty root", func(_ *testing.T, _ *fixture, _ rootRescue, rel *RootRelease) {
			rel.NothingUncommitted, rel.Untracked = true, nil
		}},
	}
	// refusalWhy pins, per case, the gate that must refuse it (a substring of
	// the reason), so an earlier gate cannot pass a case for the wrong cause.
	refusalWhy := map[string]string{
		"not verified":                                   "no verified copy",
		"empty record ref":                               "no rescue record",
		"record prefix only":                             "no rescue record",
		"foreign record ref":                             "no rescue record",
		"another record's name":                          "is not listed",
		"record deleted":                                 "is not listed",
		"fingerprint check nil":                          "no fingerprint",
		"fingerprint differs":                            "changed since its copy was verified",
		"fingerprint unreadable":                         "could not re-read the root",
		"root edited after the record":                   "changed since its copy was verified",
		"root edited, fingerprint blind (record gate)":   "does not hold the root's current index and working tree",
		"new untracked file, fingerprint blind":          "was not carried",
		"carried list misses a path":                     "was not carried",
		"carried list names a tracked file":              "is not an untracked file",
		"carried list names an ignored file":             "is not an untracked file",
		"carried list repeats a path":                    "listed twice",
		"carried list escapes with ..":                   "leaves the root",
		"carried list holds an absolute path":            "is absolute",
		"relative root":                                  "is not absolute",
		"empty root":                                     "is not absolute",
		"root is a subdirectory":                         "is not the top of a checkout",
		"root is a linked worktree":                      "not the primary checkout",
		"default branch missing":                         "does not exist locally",
		"default branch empty":                           "is not a branch name",
		"default branch an option":                       "is not a branch name",
		"nothing uncommitted claimed on a dirty root":    "has uncommitted changes",
		"default branch checked out in another worktree": "is checked out in another worktree",
		"root on a detached HEAD":                        "is not on a branch",
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnBranch("topic")
			f.dirtyRoot()
			f.Ignored(f.Dir, "ign.log")
			escape := filepath.Join(filepath.Dir(f.Dir), "escape.txt")
			if err := os.WriteFile(escape, []byte("outside"), 0o644); err != nil {
				t.Fatal(err)
			}
			rr := rescueRoot(t, f)
			rel := rr.release(f)
			c.mutate(t, f, rr, &rel)
			before := snapshot(t, f.Dir)
			destBefore := snapshot(t, rr.Dest)
			headBefore := gitIn(t, f.Dir, "rev-parse", "--abbrev-ref", "HEAD")
			calls := recordRootGit(t)

			res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rel)
			var refused *ReleaseRefused
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v (%T), want *ReleaseRefused", err, err)
			}
			if want := refusalWhy[c.name]; want == "" || !strings.Contains(refused.Reason, want) {
				t.Errorf("refused for %q, want the gate that says %q", refused.Reason, want)
			}
			if res != (ReleaseResult{}) {
				t.Errorf("result %+v on a refusal, want zero", res)
			}
			if w := writes(*calls); len(w) != 0 {
				t.Errorf("a refusal ran writes %q", w)
			}
			if after := snapshot(t, f.Dir); after != before {
				t.Errorf("root changed on a refusal:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if after := snapshot(t, rr.Dest); after != destBefore {
				t.Errorf("copy changed on a refusal")
			}
			if h := gitIn(t, f.Dir, "rev-parse", "--abbrev-ref", "HEAD"); h != headBefore {
				t.Errorf("root on %q after a refusal, want %q", h, headBefore)
			}
			if got := readFile(t, escape); got != "outside" {
				t.Errorf("file outside the root = %q", got)
			}
		})
	}
}

// TestReleaseRootNothingUncommittedPath releases a clean root with no copy
// and no record (only an unchanged fingerprint and an empty status are
// needed), and refuses one that claims nothing uncommitted while it has an
// untracked file or a carried list. Why: with nothing uncommitted rootmove
// makes no record, so the clean status is the proof; trusting the flag on a
// dirty root would reset work that exists nowhere else.
func TestReleaseRootNothingUncommittedPath(t *testing.T) {
	t.Run("clean root releases", func(t *testing.T) {
		f := newFixture(t)
		f.rootOnBranch("topic")
		_, plan := rootPlan(t, f)
		calls := recordRootGit(t)
		res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, RootRelease{Root: f.Dir, Default: "main", NothingUncommitted: true, Unchanged: plan.SourceUnchanged})
		if err != nil {
			t.Fatalf("ReleaseRoot: %v", err)
		}
		if want := (ReleaseResult{Reset: true, Switched: true}); res != want {
			t.Errorf("result %+v, want %+v", res, want)
		}
		if len(writes(*calls)) != 2 {
			t.Errorf("writes %q, want the reset and the switch", writes(*calls))
		}
		requireReleasedClean(t, f.Dir)
		if got := gitIn(t, f.Dir, "rev-parse", "refs/heads/topic"); got == "" {
			t.Error("topic branch gone")
		}
	})
	for _, c := range []struct {
		name      string
		untracked []string
	}{
		{"dirty root", nil},
		{"carried list given", []string{"new.txt"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnBranch("topic")
			if c.untracked == nil {
				f.Dirty(f.Dir, "new.txt")
			}
			_, plan := rootPlan(t, f)
			before := snapshot(t, f.Dir)
			calls := recordRootGit(t)
			_, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, RootRelease{Root: f.Dir, Default: "main", NothingUncommitted: true, Untracked: c.untracked, Unchanged: plan.SourceUnchanged})
			var refused *ReleaseRefused
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v, want *ReleaseRefused", err)
			}
			if w := writes(*calls); len(w) != 0 {
				t.Errorf("a refusal ran writes %q", w)
			}
			if after := snapshot(t, f.Dir); after != before {
				t.Errorf("root changed on a refusal")
			}
		})
	}
}

// TestReleaseRootRemovesOnlyTheCarriedList lets a second untracked file
// appear in a carried directory after the gates (during the reset): it, and
// an ignored file of the same directory, survive; the carried files go; no
// `clean` runs, and the leftover file makes the release stop at verify_clean
// with the root still on its branch. Why: removing anything beyond the exact
// carried list (a clean, a directory sweep) would delete a file the copy and
// the record do not hold.
func TestReleaseRootRemovesOnlyTheCarriedList(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.dirtyRoot()
	f.exclude("sub/ign.log")
	f.write(f.Dir, "sub/ign.log", "ignored")
	rr := rescueRoot(t, f)
	calls := recordRootGitAround(t, func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
		out, err := run()
		if isReset(args) {
			f.write(f.Dir, "sub/second.txt", "second")
		}
		return out, err
	})
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	rf := failedAt(t, err, StageVerifyClean)
	if want := (ReleaseResult{Reset: true, Removed: 2}); res != want || rf.Result != want {
		t.Errorf("result %+v / failure result %+v, want %+v", res, rf.Result, want)
	}
	for _, c := range *calls {
		if slices.Contains(c.Args, "clean") {
			t.Errorf("a clean ran: %q", c.Args)
		}
	}
	if got := readFile(t, filepath.Join(f.Dir, "sub/second.txt")); got != "second" {
		t.Errorf("sub/second.txt = %q, want it left", got)
	}
	if got := readFile(t, filepath.Join(f.Dir, "sub/ign.log")); got != "ignored" {
		t.Errorf("sub/ign.log = %q, want it left", got)
	}
	for _, p := range []string{"new.txt", "sub/deep.txt"} {
		if lexists(t, filepath.Join(f.Dir, p)) {
			t.Errorf("carried %s still in the root", p)
		}
	}
	if b := branchOf(t, f.Dir); b != "topic" {
		t.Errorf("root on %q, want topic (no switch after a failed read-back)", b)
	}
}

// TestReleaseRootUntrackedSafety changes the carried paths on disk after the
// gates (during the reset), the way a racing process could, and checks each
// is refused or left alone as specified: a path that became a directory or
// sits under a parent replaced by a symlink out of the repository is a
// remove_untracked failure with nothing outside the list touched; a vanished
// path is skipped; a carried path the reset restored from HEAD (the
// `git rm --cached` case, with HEAD's content or an edit) is skipped, never
// removed, and the release completes clean on main. Why: file removal in the
// root is the step a race or an odd state could turn into deleting the wrong
// thing; it must fail closed. (A `..` or absolute path in the list is refused
// before the reset: TestReleaseRootRefusesWithoutAnyOfTheThreeProofs.)
func TestReleaseRootUntrackedSafety(t *testing.T) {
	type env struct {
		f       *fixture
		outside string
	}
	cases := []struct {
		name    string
		setup   func(t *testing.T, f *fixture) // dirties the root (default: dirtyRoot)
		atReset func(t *testing.T, e env)      // after the real reset
		stage   string                         // "" means the release completes
		check   func(t *testing.T, e env, res ReleaseResult)
	}{
		{
			name: "carried path became a directory",
			atReset: func(t *testing.T, e env) {
				p := filepath.Join(e.f.Dir, "new.txt")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				e.f.write(e.f.Dir, "new.txt/keep.txt", "keep")
			},
			stage: StageRemoveUntracked,
			check: func(t *testing.T, e env, res ReleaseResult) {
				if got := readFile(t, filepath.Join(e.f.Dir, "new.txt/keep.txt")); got != "keep" {
					t.Errorf("file in the new directory = %q, want it intact", got)
				}
			},
		},
		{
			name: "parent replaced by a symlink out of the repository",
			atReset: func(t *testing.T, e env) {
				if err := os.WriteFile(filepath.Join(e.outside, "deep.txt"), []byte("outside"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(e.f.Dir, "sub")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(e.outside, filepath.Join(e.f.Dir, "sub")); err != nil {
					t.Fatal(err)
				}
			},
			stage: StageRemoveUntracked,
			check: func(t *testing.T, e env, res ReleaseResult) {
				if got := readFile(t, filepath.Join(e.outside, "deep.txt")); got != "outside" {
					t.Errorf("file behind the symlink = %q, want it intact", got)
				}
			},
		},
		{
			name: "carried path vanished",
			atReset: func(t *testing.T, e env) {
				if err := os.Remove(filepath.Join(e.f.Dir, "new.txt")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, e env, res ReleaseResult) {
				if want := (ReleaseResult{Reset: true, Removed: 1, Switched: true}); res != want {
					t.Errorf("result %+v, want %+v (the vanished path skipped)", res, want)
				}
				requireReleasedClean(t, e.f.Dir)
			},
		},
		{
			name: "restored by the reset, HEAD content",
			setup: func(t *testing.T, f *fixture) {
				f.git("rm", "-q", "--cached", "README")
			},
			check: func(t *testing.T, e env, res ReleaseResult) {
				if want := (ReleaseResult{Reset: true, Removed: 0, Switched: true}); res != want {
					t.Errorf("result %+v, want %+v (README skipped)", res, want)
				}
				if got := readFile(t, filepath.Join(e.f.Dir, "README")); got != "init" {
					t.Errorf("README = %q, want HEAD's %q", got, "init")
				}
				gitIn(t, e.f.Dir, "ls-files", "--error-unmatch", "--", "README")
				requireReleasedClean(t, e.f.Dir)
			},
		},
		{
			name: "restored by the reset, edited",
			setup: func(t *testing.T, f *fixture) {
				f.git("rm", "-q", "--cached", "README")
				f.write(f.Dir, "README", "edited")
			},
			check: func(t *testing.T, e env, res ReleaseResult) {
				if want := (ReleaseResult{Reset: true, Removed: 0, Switched: true}); res != want {
					t.Errorf("result %+v, want %+v (README skipped)", res, want)
				}
				if got := readFile(t, filepath.Join(e.f.Dir, "README")); got != "init" {
					t.Errorf("README = %q, want HEAD's %q", got, "init")
				}
				requireReleasedClean(t, e.f.Dir)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			e := env{f: f, outside: t.TempDir()}
			f.rootOnBranch("topic")
			if c.setup != nil {
				c.setup(t, f)
			} else {
				f.dirtyRoot()
			}
			rr := rescueRoot(t, f)
			if c.setup != nil {
				if got := rr.Plan.UntrackedPaths(); !slices.Equal(got, []string{"README"}) {
					t.Fatalf("precondition: plan carries %v, want [README]", got)
				}
			}
			recordRootGitAround(t, func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
				out, err := run()
				if isReset(args) && c.atReset != nil {
					c.atReset(t, e)
				}
				return out, err
			})
			res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
			if c.stage == "" {
				if err != nil {
					t.Fatalf("ReleaseRoot: %v", err)
				}
			} else {
				rf := failedAt(t, err, c.stage)
				if !rf.Result.Reset || rf.Result.Switched || rf.Result != res {
					t.Errorf("failure result %+v (returned %+v), want reset, not switched", rf.Result, res)
				}
				if b := branchOf(t, f.Dir); b != "topic" {
					t.Errorf("root on %q after a removal failure, want topic", b)
				}
			}
			c.check(t, e, res)
		})
	}
}

// TestReleaseRootFailureStages injects a failure at each write stage through
// the runGit seam: a reset that exits non-zero or does not finish touches
// nothing and says reset with an empty result; a file appearing after the
// removal stops at verify_clean having reset and removed; a failed switch
// stops at switch with the root reset and clean on its branch. Why: the
// caller prints the recovery row from Stage and Result, so each must say
// exactly what already happened.
func TestReleaseRootFailureStages(t *testing.T) {
	cases := []struct {
		name      string
		around    func(f *fixture) func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error)
		stage     string
		result    ReleaseResult
		uncertain bool
		branch    string
		clean     bool
	}{
		{
			name: "reset exits non-zero",
			around: func(*fixture) func([]string, func() (cleanup.Out, error)) (cleanup.Out, error) {
				return func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
					if isReset(args) {
						return cleanup.Out{Code: 128, Stderr: "fatal: injected"}, nil
					}
					return run()
				}
			},
			stage: StageReset, result: ReleaseResult{}, branch: "topic", uncertain: true,
		},
		{
			name: "reset does not finish",
			around: func(*fixture) func([]string, func() (cleanup.Out, error)) (cleanup.Out, error) {
				return func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
					if isReset(args) {
						return cleanup.Out{}, errors.New("injected: signal: killed")
					}
					return run()
				}
			},
			stage: StageReset, result: ReleaseResult{}, branch: "topic", uncertain: true,
		},
		{
			name: "file left after the removal",
			around: func(f *fixture) func([]string, func() (cleanup.Out, error)) (cleanup.Out, error) {
				return func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
					out, err := run()
					if isReset(args) {
						f.write(f.Dir, "late.txt", "late")
					}
					return out, err
				}
			},
			stage: StageVerifyClean, result: ReleaseResult{Reset: true, Removed: 2}, branch: "topic",
		},
		{
			name: "switch fails",
			around: func(*fixture) func([]string, func() (cleanup.Out, error)) (cleanup.Out, error) {
				return func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
					if isSwitch(args) {
						return cleanup.Out{Code: 1, Stderr: "error: injected"}, nil
					}
					return run()
				}
			},
			stage: StageSwitch, result: ReleaseResult{Reset: true, Removed: 2}, branch: "topic", clean: true, uncertain: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnBranch("topic")
			f.dirtyRoot()
			rr := rescueRoot(t, f)
			before := snapshot(t, f.Dir)
			recordRootGitAround(t, c.around(f))
			res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
			rf := failedAt(t, err, c.stage)
			if rf.Uncertain != c.uncertain {
				t.Errorf("Uncertain = %v, want %v (a failed reset or switch call may have written part of the index or tree)", rf.Uncertain, c.uncertain)
			}
			if rf.Result != c.result || res != c.result {
				t.Errorf("failure result %+v (returned %+v), want %+v", rf.Result, res, c.result)
			}
			if rf.Err == nil || !strings.Contains(err.Error(), c.stage) {
				t.Errorf("error %q has no cause or does not name the stage", err)
			}
			if b := branchOf(t, f.Dir); b != c.branch {
				t.Errorf("root on %q, want %q", b, c.branch)
			}
			if c.stage == StageReset {
				if after := snapshot(t, f.Dir); after != before {
					t.Errorf("root changed though the reset never ran")
				}
			}
			if c.clean {
				if st := gitIn(t, f.Dir, "status", "--porcelain=v2", "--untracked-files=all"); st != "" {
					t.Errorf("root not clean after the reset:\n%s", st)
				}
			}
		})
	}
}

// TestReleaseRootRunsNoHook installs post-checkout, reference-transaction,
// post-index-change and pre-commit hooks after the copy and releases the
// root: none may run. It then proves the hooks were live by running a plain
// reset itself. Why: the reset and switch fire those hooks by default, and a
// hook is arbitrary code running inside the destructive step (it could
// refuse a ref update half-way or rewrite the files being reset).
func TestReleaseRootRunsNoHook(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.dirtyRoot()
	rr := rescueRoot(t, f)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hooks := gitIn(t, f.Dir, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"post-checkout", "reference-transaction", "post-index-change", "pre-commit"} {
		script := fmt.Sprintf("#!/bin/sh\necho %s >> '%s'\n", h, marker)
		if err := os.WriteFile(filepath.Join(hooks, h), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f)); err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if got := readFile(t, marker); got != "<absent>" {
		t.Errorf("hooks ran during the release:\n%s", got)
	}
	requireReleasedClean(t, f.Dir)
	gitIn(t, f.Dir, "reset", "--hard", "HEAD")
	if got := readFile(t, marker); got == "<absent>" {
		t.Error("the hooks never fire, so this test proves nothing")
	}
}

// TestReleaseRootIgnoresCallerDir passes a g whose Dir is a linked worktree
// holding staged, unstaged and untracked work: that worktree and its HEAD
// must survive untouched, the root must be reset and switched, and every git
// call must run with Dir == the root. Why: rootmove calls from the user's
// directory, which may be any worktree; a reset run there would destroy work
// no proof covers.
func TestReleaseRootIgnoresCallerDir(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.dirtyRoot()
	rr := rescueRoot(t, f)
	w := f.Worktree("other")
	f.write(w, "w-staged.txt", "s")
	gitIn(t, w, "add", "w-staged.txt")
	f.write(w, "README", "w edit")
	f.write(w, "w-new.txt", "n")
	before := snapshot(t, w)
	calls := recordRootGit(t)

	res, err := ReleaseRoot(cleanup.Git{Dir: w, Timeout: time.Minute}, rr.release(f))
	if err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if !res.Reset || !res.Switched {
		t.Errorf("result %+v, want reset and switched", res)
	}
	if after := snapshot(t, w); after != before {
		t.Errorf("caller's worktree changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	requireReleasedClean(t, f.Dir)
	if len(*calls) == 0 {
		t.Fatal("no git call recorded")
	}
	for _, c := range *calls {
		if c.Dir != f.Dir {
			t.Errorf("git %q ran in %s, want the root %s", c.Args, c.Dir, f.Dir)
		}
	}
}

// TestReleaseRootIgnoredCollisionKeepsFile puts an ignored file at a path
// main tracks and the root's branch does not, then releases a clean root:
// the switch must fail (ReleaseFailed at switch, reset done) and the ignored
// file keep its content. Why: ignored files are never overwritten; a plain
// `git switch` would replace it with main's copy silently.
func TestReleaseRootIgnoredCollisionKeepsFile(t *testing.T) {
	f := newFixture(t)
	f.Commit(f.Dir, "cfg.txt", "tracked on main", "cfg")
	f.rootOnBranch("topic")
	f.git("rm", "-q", "cfg.txt")
	f.git("commit", "-q", "-m", "drop cfg")
	f.exclude("cfg.txt")
	f.write(f.Dir, "cfg.txt", "mine")
	_, plan := rootPlan(t, f)
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, RootRelease{Root: f.Dir, Default: "main", NothingUncommitted: true, Unchanged: plan.SourceUnchanged})
	rf := failedAt(t, err, StageSwitch)
	if want := (ReleaseResult{Reset: true}); res != want || rf.Result != want {
		t.Errorf("result %+v / failure result %+v, want %+v", res, rf.Result, want)
	}
	if got := readFile(t, filepath.Join(f.Dir, "cfg.txt")); got != "mine" {
		t.Errorf("ignored cfg.txt = %q, want it kept", got)
	}
	if b := branchOf(t, f.Dir); b != "topic" {
		t.Errorf("root on %q, want topic", b)
	}
}

// ignoredRiskState is one state where `git reset --hard HEAD` would delete or
// overwrite an ignored file, built on a root on topic that also tracks d/a.txt,
// foo/x and a/b/c and has new.txt untracked.
type ignoredRiskState struct {
	name string
	// real says the real rescue engine moves this state and writes its record,
	// so the release runs with the real proofs. Otherwise rescue's verify fails
	// on it (rootmove never reaches the release), and the test releases with a
	// record of the root before the change and a blind fingerprint check, so
	// ReleaseRoot's own gates are what must refuse (the record check first).
	real  bool
	build func(t *testing.T, f *fixture) (path, content, named string)
}

// ignoredRiskStates are the states, each returning the ignored file's path
// (relative to the root), its content, and the name the refusal must give.
var ignoredRiskStates = []ignoredRiskState{
	{"ignored file inside a directory where HEAD tracks a file", true, func(t *testing.T, f *fixture) (string, string, string) {
		f.exclude("*.secret")
		if err := os.Remove(filepath.Join(f.Dir, "README")); err != nil {
			t.Fatal(err)
		}
		f.write(f.Dir, "README/note.txt", "note")
		f.write(f.Dir, "README/local.secret", "precious")
		return "README/local.secret", "precious", "README/local.secret"
	}},
	{"ignored file inside a case-folded directory where HEAD tracks a file", true, func(t *testing.T, f *fixture) (string, string, string) {
		// core.ignorecase is what git sets on a case-insensitive volume (the
		// macOS default); set it so the state is the same on any filesystem.
		f.git("config", "core.ignorecase", "true")
		f.exclude("*.secret")
		if err := os.Remove(filepath.Join(f.Dir, "README")); err != nil {
			t.Fatal(err)
		}
		f.write(f.Dir, "readme/note.txt", "note")
		f.write(f.Dir, "readme/local.secret", "precious")
		return "readme/local.secret", "precious", "readme/local.secret"
	}},
	{"ignored file at an index-deleted path", false, func(t *testing.T, f *fixture) (string, string, string) {
		f.exclude("README")
		f.git("rm", "-q", "--cached", "README")
		f.write(f.Dir, "README", "precious edit")
		return "README", "precious edit", "README"
	}},
	{"ignored file replacing a tracked directory", false, func(t *testing.T, f *fixture) (string, string, string) {
		if err := os.RemoveAll(filepath.Join(f.Dir, "d")); err != nil {
			t.Fatal(err)
		}
		f.exclude("d")
		f.write(f.Dir, "d", "precious")
		return "d", "precious", "d"
	}},
	{"edited file at an index-deleted path in a directory-pattern-ignored directory", false, func(t *testing.T, f *fixture) (string, string, string) {
		f.git("rm", "-q", "--cached", "foo/x")
		f.exclude("foo/")
		f.write(f.Dir, "foo/x", "precious edit")
		return "foo/x", "precious edit", "foo"
	}},
	{"ignored file where HEAD tracks a directory, in a directory-pattern-ignored directory", false, func(t *testing.T, f *fixture) (string, string, string) {
		f.git("rm", "-q", "-r", "--cached", "a")
		f.exclude("a/")
		if err := os.RemoveAll(filepath.Join(f.Dir, "a/b")); err != nil {
			t.Fatal(err)
		}
		f.write(f.Dir, "a/b", "precious")
		return "a/b", "precious", "a"
	}},
}

// ignoredRiskBase is the root every ignoredRiskStates state starts from.
func ignoredRiskBase(f *fixture) {
	f.rootOnBranch("topic")
	f.Commit(f.Dir, "d/a.txt", "a", "tracked dir")
	f.Commit(f.Dir, "foo/x", "orig", "foo")
	f.Commit(f.Dir, "a/b/c", "orig", "abc")
}

// TestReleaseRootRefusesWhenTheResetWouldLoseIgnoredFiles builds each state
// where `git reset --hard HEAD` would delete or overwrite an ignored file
// (inside a directory where HEAD tracks a file, also under another case with
// core.ignorecase; at an index-deleted path; replacing a tracked directory;
// under a directory-pattern ignore that hides paths HEAD tracks) and requires
// a *ReleaseRefused with no write run and the root (the ignored file
// included) unchanged. A real-rescue state passes the copy's verify and
// RecordCoversDirt and must be refused by the ignored-file gate, naming the
// entry; a state rescue cannot record is refused by the record check, which
// runs first (TestIgnoredAtRiskSeesEachState pins that the detector sees it
// too). Why: ignored files are in neither the copy nor the record, so a reset
// that removes one destroys data that exists nowhere else.
func TestReleaseRootRefusesWhenTheResetWouldLoseIgnoredFiles(t *testing.T) {
	for _, st := range ignoredRiskStates {
		t.Run(st.name, func(t *testing.T) {
			f := newFixture(t)
			ignoredRiskBase(f)
			f.write(f.Dir, "new.txt", "n")
			var rel RootRelease
			var path, content, named string
			if st.real {
				path, content, named = st.build(t, f)
				rel = rescueRoot(t, f).release(f)
			} else {
				rel = rescueRoot(t, f).release(f)
				path, content, named = st.build(t, f)
				rel.Unchanged = func() (string, error) { return "", nil }
			}
			before := snapshot(t, f.Dir)
			calls := recordRootGit(t)
			_, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rel)
			var refused *ReleaseRefused
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v (%T), want *ReleaseRefused", err, err)
			}
			// The record check runs before the ignored-file check, so a state
			// rescue cannot record is refused there (the record does not hold
			// it); a real-rescue state passes the record and must meet the
			// ignored-file gate.
			if st.real {
				if !strings.Contains(refused.Reason, "ignored") || !strings.Contains(refused.Reason, named) {
					t.Errorf("refused for %q, want the ignored-file gate naming %s", refused.Reason, named)
				}
			} else if !strings.Contains(refused.Reason, "does not hold the root's current index and working tree") {
				t.Errorf("refused for %q, want the record gate", refused.Reason)
			}
			if w := writes(*calls); len(w) != 0 {
				t.Errorf("a refusal ran writes %q", w)
			}
			if after := snapshot(t, f.Dir); after != before {
				t.Errorf("root changed on a refusal:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if got := readFile(t, filepath.Join(f.Dir, path)); got != content {
				t.Errorf("ignored %s = %q, want %q", path, got, content)
			}
		})
	}
}

// TestIgnoredAtRiskSeesEachState calls the detector directly on each
// ignoredRiskStates state, and on look-alikes that are safe (an ignored file
// beside tracked ones, an ignored file in a tracked directory, a directory
// replacing a tracked file with no ignored entry in it). Why: the end-to-end
// test above can reach the gate only through the gates before it; this pins
// what the detector itself sees.
func TestIgnoredAtRiskSeesEachState(t *testing.T) {
	for _, st := range ignoredRiskStates {
		t.Run(st.name, func(t *testing.T) {
			f := newFixture(t)
			ignoredRiskBase(f)
			_, _, named := st.build(t, f)
			why, err := ignoredAtRisk(cleanup.Git{Dir: f.Dir}, f.Dir)
			if err != nil || !strings.Contains(why, named) {
				t.Errorf("ignoredAtRisk = %q, %v; want it to name %s", why, err, named)
			}
		})
	}
	t.Run("safe look-alikes", func(t *testing.T) {
		f := newFixture(t)
		f.rootOnBranch("topic")
		f.Commit(f.Dir, "d/a.txt", "a", "tracked dir")
		f.Ignored(f.Dir, "top.log")
		f.Ignored(f.Dir, "d/b.log")
		if err := os.Remove(filepath.Join(f.Dir, "README")); err != nil {
			t.Fatal(err)
		}
		f.write(f.Dir, "README/note.txt", "note")
		// A directory-pattern-ignored directory holding a force-added, edited
		// tracked file: git lists the ignored file inside, not "build/".
		f.exclude("build/")
		f.write(f.Dir, "build/.keep", "k")
		f.git("add", "-f", "build/.keep")
		f.git("commit", "-q", "-m", "keep")
		f.write(f.Dir, "build/.keep", "edited")
		f.write(f.Dir, "build/out.o", "o")
		why, err := ignoredAtRisk(cleanup.Git{Dir: f.Dir}, f.Dir)
		if err != nil || why != "" {
			t.Errorf("ignoredAtRisk = %q, %v; want nothing at risk", why, err)
		}
	})
}

// TestReleaseRootTrackedFileBecameDirectory releases a root whose tracked
// README was replaced by a directory holding only an untracked, carried
// note.txt: the reset restores README as a file, the carried README/note.txt
// is then gone with its directory and is skipped, and the release completes
// clean on main with README back and the other carried file removed. Why:
// the parent is now a tracked file, which is the reset's doing, not a race;
// failing there would strand the root half-released for no reason.
func TestReleaseRootTrackedFileBecameDirectory(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	if err := os.Remove(filepath.Join(f.Dir, "README")); err != nil {
		t.Fatal(err)
	}
	f.write(f.Dir, "README/note.txt", "note")
	f.write(f.Dir, "new.txt", "n")
	rr := rescueRoot(t, f)
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	if err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if want := (ReleaseResult{Reset: true, Removed: 1, Switched: true}); res != want {
		t.Errorf("result %+v, want %+v", res, want)
	}
	if got := readFile(t, filepath.Join(f.Dir, "README")); got != "init" {
		t.Errorf("README = %q, want main's %q", got, "init")
	}
	requireReleasedClean(t, f.Dir)
	if got := readFile(t, filepath.Join(rr.Dest, "README/note.txt")); got != "note" {
		t.Errorf("copy's README/note.txt = %q, want the carried file", got)
	}
}

// TestReleaseRootReadsTheIndexOnce carries 40 untracked files and requires
// one `ls-files` after the reset, not one per path. Why: the time between the
// reset and the last removal is a window in which the root holds neither its
// work nor its default branch; a process per path (up to 20000) stretches it.
func TestReleaseRootReadsTheIndexOnce(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	for i := range 40 {
		f.write(f.Dir, fmt.Sprintf("many/f%02d.txt", i), "x")
	}
	rr := rescueRoot(t, f)
	calls := recordRootGit(t)
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	if err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if res.Removed != 40 {
		t.Errorf("removed %d, want 40", res.Removed)
	}
	afterReset, lsFiles := false, 0
	for _, c := range *calls {
		if isReset(c.Args) {
			afterReset = true
		}
		if afterReset && slices.Contains(c.Args, "ls-files") {
			lsFiles++
		}
	}
	if lsFiles != 1 {
		t.Errorf("%d ls-files calls after the reset, want 1", lsFiles)
	}
	requireReleasedClean(t, f.Dir)
}

// TestReleaseRootKeepsACarriedFileEditedAfterTheGates edits a carried
// untracked file after the gates (during the reset): its content no longer
// matches the record, so it is not removed and the release fails at
// remove_untracked with the new content intact. Why: an edit made in the
// window is in neither the copy nor the record; removing the file would
// destroy it.
func TestReleaseRootKeepsACarriedFileEditedAfterTheGates(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.dirtyRoot()
	rr := rescueRoot(t, f)
	recordRootGitAround(t, func(args []string, run func() (cleanup.Out, error)) (cleanup.Out, error) {
		out, err := run()
		if isReset(args) {
			f.write(f.Dir, "sub/deep.txt", "edited in the window")
		}
		return out, err
	})
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	rf := failedAt(t, err, StageRemoveUntracked)
	if !rf.Result.Reset || rf.Result.Switched || res != rf.Result {
		t.Errorf("result %+v (returned %+v), want reset, not switched", rf.Result, res)
	}
	if got := readFile(t, filepath.Join(f.Dir, "sub/deep.txt")); got != "edited in the window" {
		t.Errorf("sub/deep.txt = %q, want the edit kept", got)
	}
}

// TestReleaseRootRefusesWithRepoEnvironment sets GIT_DIR and GIT_INDEX_FILE
// to a decoy repository and releases a verified root: it must refuse before
// running anything, leaving the root and the decoy unchanged. Why: those
// variables override `git -C <root>`, so every gate would read, and the reset
// would write, the decoy (or a mix of the two) instead of the root.
func TestReleaseRootRefusesWithRepoEnvironment(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.dirtyRoot()
	rr := rescueRoot(t, f)
	decoy := t.TempDir()
	gitIn(t, decoy, "init", "-q")
	f.write(decoy, "decoy.txt", "decoy")
	gitIn(t, decoy, "add", "decoy.txt")
	f.write(decoy, "decoy.txt", "decoy edit")
	before, decoyBefore := snapshot(t, f.Dir), gitIn(t, decoy, "status", "--porcelain=v2", "--untracked-files=all")
	calls := recordRootGit(t)

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(decoy, ".git", "index"))
	_, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_INDEX_FILE")

	var refused *ReleaseRefused
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "GIT_DIR") {
		t.Fatalf("err = %v, want *ReleaseRefused naming GIT_DIR", err)
	}
	if len(*calls) != 0 {
		t.Errorf("git ran with the variables set: %q", *calls)
	}
	if after := snapshot(t, f.Dir); after != before {
		t.Errorf("root changed")
	}
	if got := gitIn(t, decoy, "status", "--porcelain=v2", "--untracked-files=all"); got != decoyBefore || readFile(t, filepath.Join(decoy, "decoy.txt")) != "decoy edit" {
		t.Errorf("decoy changed:\n%s", got)
	}
}

// TestReleaseRootTrackedFileBecameCaseFoldedDirectory is
// TestReleaseRootTrackedFileBecameDirectory with the directory named readme/
// and core.ignorecase on: on a case-insensitive volume the reset writes
// README over readme/, and the carried readme/note.txt must be skipped as
// gone, not fail at remove_untracked. Why: the parent's on-disk name differs
// from the tracked one only by case, which git (and the volume) treat as the
// same path.
func TestReleaseRootTrackedFileBecameCaseFoldedDirectory(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.git("config", "core.ignorecase", "true")
	if err := os.Remove(filepath.Join(f.Dir, "README")); err != nil {
		t.Fatal(err)
	}
	f.write(f.Dir, "readme/note.txt", "note")
	f.write(f.Dir, "new.txt", "n")
	rr := rescueRoot(t, f)
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	if err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if !res.Reset || !res.Switched || res.Removed < 1 {
		t.Errorf("result %+v, want reset, switched, new.txt removed", res)
	}
	requireReleasedClean(t, f.Dir)
	if got := readFile(t, filepath.Join(rr.Dest, "readme/note.txt")); got != "note" {
		t.Errorf("copy's readme/note.txt = %q, want the carried file", got)
	}
}

// TestReleaseRootTrackedSymlinkBecameDirectory releases a root whose tracked
// symlink lnk was replaced by a directory holding a carried note.txt: the
// reset restores the symlink, so the carried lnk/note.txt is gone (skipped,
// nothing followed through the link), and the release completes clean on
// main. Why: a parent the index tracks was put back by the reset; only a
// symlink parent the index does not track is a race to fail on.
func TestReleaseRootTrackedSymlinkBecameDirectory(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	if err := os.Symlink("README", filepath.Join(f.Dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	f.git("add", "lnk")
	f.git("commit", "-q", "-m", "lnk")
	if err := os.Remove(filepath.Join(f.Dir, "lnk")); err != nil {
		t.Fatal(err)
	}
	f.write(f.Dir, "lnk/note.txt", "note")
	f.write(f.Dir, "new.txt", "n")
	rr := rescueRoot(t, f)
	res, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rr.release(f))
	if err != nil {
		t.Fatalf("ReleaseRoot: %v", err)
	}
	if want := (ReleaseResult{Reset: true, Removed: 1, Switched: true}); res != want {
		t.Errorf("result %+v, want %+v", res, want)
	}
	requireReleasedClean(t, f.Dir)
	if got := readFile(t, filepath.Join(f.Dir, "README")); got != "init" {
		t.Errorf("README (the link's target) = %q, want %q", got, "init")
	}
	if got := readFile(t, filepath.Join(rr.Dest, "lnk/note.txt")); got != "note" {
		t.Errorf("copy's lnk/note.txt = %q, want the carried file", got)
	}
}

// TestIgnoredAtRiskOnDiskPassCatchesAliasedNames turns git's own name
// matching off (core.ignorecase=false, core.precomposeunicode=false) and
// replaces a tracked file by a directory whose name differs only by case, or
// only by Unicode normalization (NFD for an NFC name), holding an ignored
// file: the name pass cannot see it, so the on-disk pass (the volume resolves
// the tracked name to the directory) must. Each case skips on a volume that
// does not alias those names, where the reset would not touch the directory.
// Why: folding does not cover normalization, and a config that understates
// how the volume compares names must not let the reset destroy the file.
func TestIgnoredAtRiskOnDiskPassCatchesAliasedNames(t *testing.T) {
	for _, c := range []struct{ name, tracked, onDisk string }{
		{"case", "README", "readme"},
		{"unicode normalization", "café", "café"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnBranch("topic")
			f.git("config", "core.ignorecase", "false")
			f.git("config", "core.precomposeunicode", "false")
			if c.tracked != "README" {
				f.Commit(f.Dir, c.tracked, "tracked", "tracked")
			}
			if err := os.Remove(filepath.Join(f.Dir, c.tracked)); err != nil {
				t.Fatal(err)
			}
			f.exclude("*.secret")
			f.write(f.Dir, c.onDisk+"/local.secret", "precious")
			if !lexists(t, filepath.Join(f.Dir, c.tracked)) {
				t.Skipf("this volume does not alias %q and %q", c.tracked, c.onDisk)
			}
			why, err := ignoredAtRisk(cleanup.Git{Dir: f.Dir}, f.Dir)
			if err != nil || !strings.Contains(why, "local.secret") {
				t.Errorf("ignoredAtRisk = %q, %v; want the ignored local.secret named", why, err)
			}
		})
	}
}

// hideEdit edits README under an assume-unchanged or skip-worktree flag, so
// `git status` (and rescue's fingerprint, which hashes `ls-files --stage`
// and the status's paths) cannot see the edit.
func hideEdit(t *testing.T, f *fixture, flag string) {
	t.Helper()
	f.git("update-index", flag, "README")
	f.write(f.Dir, "README", "hidden edit")
	if st := gitIn(t, f.Dir, "status", "--porcelain=v2", "--", "README"); st != "" {
		t.Fatalf("precondition: status sees README under %s:\n%s", flag, st)
	}
}

// TestReleaseRootRefusesHiddenIndexFlags takes a fingerprint (a clean root,
// or a dirty one with a real copy and record), then edits README under an
// assume-unchanged or skip-worktree flag: status and the fingerprint stay
// the same, and ReleaseRoot must still refuse with no write run and the edit
// intact. Why: with nothing uncommitted an empty status is the only content
// proof, and a hidden edit would be reset away with no copy and no record.
func TestReleaseRootRefusesHiddenIndexFlags(t *testing.T) {
	for _, mode := range []string{"nothing uncommitted", "dirty"} {
		for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
			t.Run(mode+" "+flag, func(t *testing.T) {
				f := newFixture(t)
				f.rootOnBranch("topic")
				var rel RootRelease
				if mode == "dirty" {
					f.write(f.Dir, "new.txt", "n")
					rel = rescueRoot(t, f).release(f)
				} else {
					_, plan := rootPlan(t, f)
					rel = RootRelease{Root: f.Dir, Default: "main", NothingUncommitted: true, Unchanged: plan.SourceUnchanged}
				}
				hideEdit(t, f, flag)
				if d, err := rel.Unchanged(); err != nil || d != "" {
					t.Fatalf("precondition: the fingerprint sees the hidden edit (%q, %v)", d, err)
				}
				calls := recordRootGit(t)
				_, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rel)
				var refused *ReleaseRefused
				if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "assume-unchanged or skip-worktree") {
					t.Fatalf("err = %v, want *ReleaseRefused naming the hidden index flag", err)
				}
				if w := writes(*calls); len(w) != 0 {
					t.Errorf("a refusal ran writes %q", w)
				}
				if got := readFile(t, filepath.Join(f.Dir, "README")); got != "hidden edit" {
					t.Errorf("README = %q, want the hidden edit kept", got)
				}
			})
		}
	}
}

// TestReleaseRootRefusesADetachedRoot detaches a clean root and commits on
// it, then releases it with nothing uncommitted and a fingerprint check that
// reports no change: it must refuse, the commit still HEAD. Why: switching a
// detached root away orphans its commits, which no branch, copy or record
// holds.
func TestReleaseRootRefusesADetachedRoot(t *testing.T) {
	f := newFixture(t)
	f.rootOnBranch("topic")
	f.git("switch", "-q", "--detach")
	tip := f.Commit(f.Dir, "orphan.txt", "o", "only here")
	calls := recordRootGit(t)
	_, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, RootRelease{Root: f.Dir, Default: "main", NothingUncommitted: true,
		Unchanged: func() (string, error) { return "", nil }})
	var refused *ReleaseRefused
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "is not on a branch") {
		t.Fatalf("err = %v, want *ReleaseRefused for a detached root", err)
	}
	if w := writes(*calls); len(w) != 0 {
		t.Errorf("a refusal ran writes %q", w)
	}
	if got := f.git("rev-parse", "HEAD"); got != tip {
		t.Errorf("HEAD %s, want the detached commit %s", got, tip)
	}
}

// staleFsmonitor points the root's core.fsmonitor at a hook that always
// answers "nothing changed" (protocol v2: a token and an empty path list),
// lets git record that answer in the index, then edits README: a git that
// trusts the hook no longer sees the edit.
func staleFsmonitor(t *testing.T, f *fixture) {
	t.Helper()
	hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'tok\\0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.git("config", "core.fsmonitor", hook)
	f.git("status", "--porcelain") // writes the fsmonitor extension into the index
	f.write(f.Dir, "README", "edit fsmonitor hides")
	if st := f.git("status", "--porcelain", "--", "README"); st != "" {
		t.Skipf("this git does not trust the fake fsmonitor (status sees %q); nothing to test", st)
	}
}

// TestReleaseRootIgnoresAStaleFsmonitor edits README behind a core.fsmonitor
// hook that reports no changes, so a plain `git status` and `ls-files -v` see
// a clean file, and releases the root in both modes with a fingerprint check
// that reports no change (only ReleaseRoot's own gates stand): it must refuse,
// run no write and keep the edit; every git call must carry
// core.fsmonitor=false. Why: the reset trusts fsmonitor too, and the switch
// would then overwrite the edit, which no copy or record holds.
func TestReleaseRootIgnoresAStaleFsmonitor(t *testing.T) {
	for _, mode := range []string{"nothing uncommitted", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.rootOnBranch("topic")
			var rel RootRelease
			if mode == "dirty" {
				f.write(f.Dir, "new.txt", "n")
				rel = rescueRoot(t, f).release(f)
			} else {
				rel = RootRelease{Root: f.Dir, Default: "main", NothingUncommitted: true}
			}
			rel.Unchanged = func() (string, error) { return "", nil }
			staleFsmonitor(t, f)
			calls := recordRootGit(t)
			_, err := ReleaseRoot(cleanup.Git{Dir: f.Dir}, rel)
			var refused *ReleaseRefused
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v (%T), want *ReleaseRefused (the edit is visible with fsmonitor off)", err, err)
			}
			if w := writes(*calls); len(w) != 0 {
				t.Errorf("a refusal ran writes %q", w)
			}
			if got := readFile(t, filepath.Join(f.Dir, "README")); got != "edit fsmonitor hides" {
				t.Errorf("README = %q, want the edit kept", got)
			}
			if len(*calls) == 0 {
				t.Fatal("no git call recorded")
			}
			for _, c := range *calls {
				if !slices.Contains(c.Config, "core.fsmonitor=false") {
					t.Errorf("git %q ran without core.fsmonitor=false (config %q)", c.Args, c.Config)
				}
			}
		})
	}
}
