package rescue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// recOracle is git's own working-tree tree: HEAD in a throwaway index, then
// `add -A` and `write-tree`.
func recOracle(t *testing.T, dir string) string {
	t.Helper()
	idx := filepath.Join(t.TempDir(), "idx")
	run := func(args ...string) string {
		cmd := execGit(dir, args...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+idx)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("read-tree", "HEAD")
	run("add", "-A")
	return run("write-tree")
}

// copyIndexTree is git's write-tree run on a copy of the index, so the real
// index is not touched.
func copyIndexTree(t *testing.T, dir string) string {
	t.Helper()
	idx := gitIn(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "index")
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "idx")
	if err := os.WriteFile(cp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := execGit(dir, "write-tree")
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+cp)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("write-tree: %v\n%s", err, b)
	}
	return strings.TrimSpace(string(b))
}

// recPlan builds the Plan a real Preflight would for the worktree at p.
func recPlan(t *testing.T, f *fixture, p string) *Plan {
	t.Helper()
	eng, find := f.engine(f.Dir)
	plan, err := eng.Preflight(find(p), "")
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	return plan
}

func trailer(t *testing.T, dir, commit, key string) string {
	t.Helper()
	return gitIn(t, dir, "log", "-1", "--format=%(trailers:key="+key+",valueonly)", commit)
}

// TestWriteRecordBuildsTheChain pins the record's shape: ref name, the I and
// W commits with their trees and parents, and the six trailers. It runs for a
// branch and a detached source; a record that mis-binds is a false claim.
func TestWriteRecordBuildsTheChain(t *testing.T) {
	for _, detached := range []bool{false, true} {
		f := newFixture(t)
		var p string
		if detached {
			p = f.Detached("d")
		} else {
			p = f.Worktree("topic")
		}
		f.Commit(p, "c.txt", "c", "c")
		f.write(p, "README", "staged")
		gitIn(t, p, "add", "README")
		f.write(p, "c.txt", "unstaged")
		f.write(p, "new/u.txt", "untracked")
		plan := recPlan(t, f, p)

		rec, err := writeRecord(sgit{dir: p}, plan)
		if err != nil {
			t.Fatal(err)
		}
		if want := RecordRefPrefix + rec.Commit[:16]; rec.Ref != want {
			t.Errorf("Ref = %q, want %q", rec.Ref, want)
		}
		if got := gitIn(t, p, "rev-parse", rec.Ref); got != rec.Commit {
			t.Errorf("ref points at %s, want %s", got, rec.Commit)
		}
		i := gitIn(t, p, "rev-parse", rec.Commit+"^")
		head := gitIn(t, p, "rev-parse", "HEAD")
		if got := gitIn(t, p, "rev-parse", i+"^"); got != head {
			t.Errorf("I's parent = %s, want HEAD %s", got, head)
		}
		if got, want := gitIn(t, p, "rev-parse", i+"^{tree}"), copyIndexTree(t, p); got != want {
			t.Errorf("I tree = %s, want index tree %s", got, want)
		}
		if got, want := gitIn(t, p, "rev-parse", rec.Commit+"^{tree}"), recOracle(t, p); got != want {
			t.Errorf("W tree = %s, want oracle %s", got, want)
		}
		wantRef := "refs/heads/topic"
		if detached {
			wantRef = "detached"
		}
		want := map[string]string{
			"Wt-Version":     "1",
			"Wt-Source-Path": cleanup.ResolvePath(p),
			"Wt-Source-Ref":  wantRef,
			"Wt-Source-Head": head,
			"Wt-Dest-Branch": plan.Branch,
			"Wt-Dest-Path":   plan.DestPath,
		}
		for _, c := range []string{rec.Commit, i} {
			for k, v := range want {
				if got := trailer(t, p, c, k); got != v {
					t.Errorf("%s trailer %s = %q, want %q", c[:8], k, got, v)
				}
			}
		}
	}
}

// TestWriteRecordUsesItsOwnIdentity proves the record needs no git identity:
// with user.useConfigOnly set and no name configured, a normal commit would
// fail, the record must not.
func TestWriteRecordUsesItsOwnIdentity(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.write(p, "x.txt", "x")
	plan := recPlan(t, f, p)
	f.git("config", "user.useConfigOnly", "true")
	f.git("config", "--unset", "user.name")
	f.git("config", "--unset", "user.email")

	rec, err := writeRecord(sgit{dir: p}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, p, "log", "-1", "--format=%an <%ae> %cn <%ce>", rec.Commit); got != "wt rescue <wt@invalid> wt rescue <wt@invalid>" {
		t.Errorf("identity = %q", got)
	}
}

// TestWriteRecordRefusesAControlCharacter proves a value that cannot be a
// trailer writes nothing at all: no ref and no new commit reachable.
func TestWriteRecordRefusesAControlCharacter(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.write(p, "x.txt", "x")
	plan := recPlan(t, f, p)
	before := gitIn(t, p, "rev-list", "--all", "--count")
	plan.DestPath += "\nWt-Source-Path: /elsewhere"

	if _, err := writeRecord(sgit{dir: p}, plan); err == nil {
		t.Fatal("want an error")
	}
	if got := gitIn(t, p, "for-each-ref", "refs/wt"); got != "" {
		t.Errorf("refs written: %q", got)
	}
	if got := gitIn(t, p, "rev-list", "--all", "--count"); got != before {
		t.Errorf("reachable commits %s, was %s", got, before)
	}
}

// TestWriteRecordHandlesAPathWithANewlineOrNonASCII proves odd untracked
// names reach W's tree exactly as git itself would add them.
func TestWriteRecordHandlesAPathWithANewlineOrNonASCII(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.write(p, "a\nb.txt", "newline")
	f.write(p, "café.txt", "accent")
	plan := recPlan(t, f, p)

	rec, err := writeRecord(sgit{dir: p}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gitIn(t, p, "rev-parse", rec.Commit+"^{tree}"), recOracle(t, p); got != want {
		t.Errorf("W tree = %s, want oracle %s", got, want)
	}
}

// TestUpdateRecordRefIsCreateOnly pins the seam: the same commit twice is
// written, a different commit at an existing ref is an error and the ref
// stays put.
func TestUpdateRecordRefIsCreateOnly(t *testing.T) {
	f := newFixture(t)
	a := f.git("rev-parse", "HEAD")
	b := f.Commit(f.Dir, "more", "m", "more")
	src := sgit{dir: f.Dir}
	ref := RecordRefPrefix + "abc"
	for i := 0; i < 2; i++ {
		if err := updateRecordRef(src, ref, a); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if err := updateRecordRef(src, ref, b); err == nil {
		t.Fatal("want an error for a different commit")
	}
	if got := f.git("rev-parse", ref); got != a {
		t.Errorf("ref moved to %s, want %s", got, a)
	}
}

// TestWriteRecordReadsButNeverChangesTheSource proves the writer leaves the
// source exactly as found, the raw index bytes included: I's tree is derived
// in memory, so no cache-tree extension is stored in the source's index.
func TestWriteRecordReadsButNeverChangesTheSource(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	f.write(p, "README", "edit")
	gitIn(t, p, "add", "README")
	f.write(p, "u.txt", "u")
	plan := recPlan(t, f, p)
	before := snapshot(t, p)

	if _, err := writeRecord(sgit{dir: p}, plan); err != nil {
		t.Fatal(err)
	}
	if d := before.diff(snapshot(t, p)); d != "" {
		t.Errorf("source changed: %s", d)
	}
}

// TestTheRecordOutlivesTheBranchTheReflogAndGC rescues a source holding an
// unpushed commit, a staged change, an unstaged change and an untracked file
// in a nested directory, then removes the destination worktree and its branch
// with plain git, expires every reflog and runs `gc --prune=now`. The record
// ref must still resolve, I and W must still exist, and W must still hold the
// untracked file's exact content; the source is unchanged by all of it. Why:
// "preserved" must not depend on the destination, and a bare hash with no ref
// would be pruned.
func TestTheRecordOutlivesTheBranchTheReflogAndGC(t *testing.T) {
	f := newFixture(t)
	src := f.Worktree("topic")
	f.Commit(src, "committed.txt", "unpushed", "unpushed work")
	f.write(src, "README", "staged edit")
	gitIn(t, src, "add", "README")
	f.write(src, "committed.txt", "unstaged edit")
	const untracked = "deep/er/only-here.txt"
	const content = "this exists nowhere else\n"
	f.write(src, untracked, content)

	plan, out := rescueOnce(t, f, src)
	if out.Status != StatusMoved || out.Record == nil {
		t.Fatalf("status %v record %v, want moved with a record", out.Status, out.Record)
	}
	rec := *out.Record
	before := snapshot(t, src)

	gitIn(t, f.Dir, "worktree", "remove", "--force", plan.DestPath)
	gitIn(t, f.Dir, "branch", "-D", plan.Branch)
	gitIn(t, f.Dir, "reflog", "expire", "--expire=now", "--all")
	gitIn(t, f.Dir, "gc", "--prune=now")

	if got := gitIn(t, f.Dir, "rev-parse", rec.Ref); got != rec.Commit {
		t.Errorf("%s = %s, want %s", rec.Ref, got, rec.Commit)
	}
	gitIn(t, f.Dir, "cat-file", "-e", rec.Commit)
	gitIn(t, f.Dir, "cat-file", "-e", rec.Commit+"^")
	if got := gitIn(t, f.Dir, "show", rec.Commit+":"+untracked); got != strings.TrimSpace(content) {
		t.Errorf("untracked file in W = %q, want %q", got, strings.TrimSpace(content))
	}
	if d := before.diff(snapshot(t, src)); d != "" {
		t.Errorf("source changed: %s", d)
	}
}
