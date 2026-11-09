package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
)

// cleanWorktree adds a worktree on "spike" beside the repo with exactly one
// unpushed commit and no dirt, and returns its path: the shape a rescue
// leaves removable.
func cleanWorktree(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(dir), "wt-spike")
	cgit(t, dir, "worktree", "add", "-b", "spike", p)
	cleanupCommit(t, p, "spike.txt", "spike work")
	return p
}

// rescueClean rescues "spike" with --yes (plus any extra args) and returns
// the decoded result, failing unless it moved and wrote a record.
func rescueClean(t *testing.T, a *app, dir string, extra ...string) rescue.Envelope {
	t.Helper()
	args := append([]string{"spike", "--yes", "--json", "--no-fetch"}, extra...)
	stdout, stderr, err := runRescue(t, a, dir, "", args...)
	if exitOf(err) != 0 {
		t.Fatalf("rescue exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	env := decodeRescue(t, stdout)
	if env.Outcome != "moved" || env.Record == nil || env.Destination == nil {
		t.Fatalf("rescue did not move with a record: %+v\n%s", env, stdout)
	}
	return env
}

// listRows runs `wt cleanup --json --no-fetch` and returns its rows.
func listRows(t *testing.T, a *app, dir string) []cleanup.RowJSON {
	t.Helper()
	stdout, stderr, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if exitOf(err) != 0 {
		t.Fatalf("cleanup exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	var env cleanup.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("cleanup stdout: %v\n%s", err, stdout)
	}
	return env.Rows
}

// rowFor is the worktree row on branch (not the branch row).
func rowFor(t *testing.T, rows []cleanup.RowJSON, branch string) cleanup.RowJSON {
	t.Helper()
	for _, r := range rows {
		if r.Kind == "worktree" && r.Branch == branch {
			return r
		}
	}
	t.Fatalf("no worktree row for %q in %+v", branch, rows)
	return cleanup.RowJSON{}
}

func reasonCodes(r cleanup.RowJSON) []string {
	var out []string
	for _, x := range r.Reasons {
		out = append(out, x.Code)
	}
	return out
}

func hasRef(t *testing.T, dir, ref string) bool {
	t.Helper()
	return cgit(t, dir, "for-each-ref", "--format=%(refname)", ref) == ref
}

// expectNotPreserved asserts the source row is unique_work, not preserved.
func expectNotPreserved(t *testing.T, a *app, dir, want string) {
	t.Helper()
	r := rowFor(t, listRows(t, a, dir), "spike")
	if r.Status != want || r.Evidence == "rescued" || slices.Contains(reasonCodes(r), "preserved") {
		t.Fatalf("source row = status %q evidence %q reasons %v, want status %q and no rescued/preserved", r.Status, r.Evidence, reasonCodes(r), want)
	}
}

// TestRescuedOriginalReadsRemovableAndGoesWithItsBranch drives rescue, list
// and remove together on a clean worktree: after the rescue the original
// lists proven (evidence rescued, reason preserved) while the copy stays
// unique_work, and `wt remove --yes` takes the worktree and its branch but
// leaves the copy and the record, with no gc caveat. Why: this is the
// feature's whole promise, and only the real commands together prove the
// classifier, the engine and the branch stage agree.
func TestRescuedOriginalReadsRemovableAndGoesWithItsBranch(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	src := cleanWorktree(t, dir)
	githubOrigin(t, dir)
	env := rescueClean(t, a, dir)

	rows := listRows(t, a, dir)
	r := rowFor(t, rows, "spike")
	if r.Status != "proven" || r.Evidence != "rescued" || !slices.Contains(reasonCodes(r), "preserved") {
		t.Fatalf("source row = status %q evidence %q reasons %v", r.Status, r.Evidence, reasonCodes(r))
	}
	d := rowFor(t, rows, "rescued/spike")
	if d.Status != "unique_work" || d.Evidence == "rescued" || slices.Contains(reasonCodes(d), "preserved") {
		t.Fatalf("destination row = status %q evidence %q reasons %v, want unique_work", d.Status, d.Evidence, reasonCodes(d))
	}

	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", src)
	if exitOf(err) != 0 {
		t.Fatalf("remove exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	out := decodeOne(t, stdout)
	if len(out.Items) != 1 {
		t.Fatalf("items = %+v", out.Items)
	}
	it := out.Items[0]
	if it.Worktree != "removed" || it.Branch != "deleted" {
		t.Errorf("worktree %q branch %q, want removed/deleted (%s)", it.Worktree, it.Branch, it.Reason)
	}
	for _, w := range it.Warnings {
		if strings.Contains(w, "git gc") {
			t.Errorf("gc caveat in warnings: %q", w)
		}
	}
	if exists(src) {
		t.Errorf("source worktree %s still exists", src)
	}
	if hasBranch(t, dir, "spike") {
		t.Error("branch spike survives")
	}
	if !hasBranch(t, dir, "rescued/spike") {
		t.Error("rescued/spike is gone")
	}
	if !hasRef(t, dir, env.Record.Ref) {
		t.Errorf("record %s is gone", env.Record.Ref)
	}
	if !exists(env.Destination.Path) {
		t.Errorf("destination worktree %s is gone", env.Destination.Path)
	}
}

// TestCleanupReadsWhatRescueWrites checks the preserved reason on the real
// listing names the destination path and record ref the rescue result
// reported. Why: internal/cleanup duplicates the record ref prefix and
// trailer keys of internal/rescue (it cannot import it); only a real rescue
// read back through the command catches the two drifting apart.
func TestCleanupReadsWhatRescueWrites(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	cleanWorktree(t, dir)
	githubOrigin(t, dir)
	env := rescueClean(t, a, dir)

	r := rowFor(t, listRows(t, a, dir), "spike")
	var detail string
	for _, x := range r.Reasons {
		if x.Code == "preserved" {
			detail = x.Detail
		}
	}
	if detail == "" {
		t.Fatalf("no preserved reason: %+v", r.Reasons)
	}
	dest, err := filepath.EvalSymlinks(env.Destination.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, env.Record.Ref) || !(strings.Contains(detail, env.Destination.Path) || strings.Contains(detail, dest)) {
		t.Errorf("preserved detail %q lacks record %q or destination %q", detail, env.Record.Ref, env.Destination.Path)
	}
}

// TestRescuedOriginalStopsReadingRemovableAndRefuses covers the cases
// that must leave the original as it reads today: a commit made after the
// rescue, the record deleted, an edit after a clean rescue, an ignored file
// (less_sure, and remove --yes refuses), and gh unavailable (less_sure).
// Why: the rescued signal is only safe while the record still proves every
// commit and nothing else is at risk; each case fails for its own reason.
func TestRescuedOriginalStopsReadingRemovableAndRefuses(t *testing.T) {
	t.Run("new commit after the rescue", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := cleanWorktree(t, dir)
		rescueClean(t, a, dir)
		cleanupCommit(t, src, "later.txt", "later work")
		expectNotPreserved(t, a, dir, "unique_work")
	})
	t.Run("record deleted", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		cleanWorktree(t, dir)
		env := rescueClean(t, a, dir)
		cgit(t, dir, "update-ref", "-d", env.Record.Ref)
		expectNotPreserved(t, a, dir, "unique_work")
	})
	t.Run("edited after a clean rescue", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := cleanWorktree(t, dir)
		rescueClean(t, a, dir)
		if err := os.WriteFile(filepath.Join(src, "spike.txt"), []byte("edited"), 0o644); err != nil {
			t.Fatal(err)
		}
		expectNotPreserved(t, a, dir, "unique_work")
	})
	t.Run("ignored file", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := cleanWorktree(t, dir)
		if err := os.WriteFile(filepath.Join(src, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cgit(t, src, "add", ".gitignore")
		cgit(t, src, "commit", "-m", "ignore logs")
		githubOrigin(t, dir)
		if err := os.WriteFile(filepath.Join(src, "build.log"), []byte("log"), 0o644); err != nil {
			t.Fatal(err)
		}
		rescueClean(t, a, dir)
		r := rowFor(t, listRows(t, a, dir), "spike")
		if r.Status != "less_sure" || slices.Contains(reasonCodes(r), "pr_unknown") {
			t.Fatalf("status = %q evidence %q reasons %v, want less_sure for the ignored file alone", r.Status, r.Evidence, reasonCodes(r))
		}
		_, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", src)
		if exitOf(err) == 0 {
			t.Error("remove --yes of an original with an ignored file exited 0")
		}
		if !exists(src) || !hasBranch(t, dir, "spike") {
			t.Error("the original or its branch was removed without --accept-loss")
		}
	})
	t.Run("gh unavailable", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := cleanWorktree(t, dir)
		githubOrigin(t, dir)
		rescueClean(t, a, dir)
		t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh: not installed") }))
		t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return nil, errors.New("gh: not installed") }))
		r := rowFor(t, listRows(t, a, dir), "spike")
		if r.Status != "less_sure" || !slices.Contains(reasonCodes(r), "pr_unknown") {
			t.Fatalf("status = %q evidence %q reasons %v, want less_sure with pr_unknown", r.Status, r.Evidence, reasonCodes(r))
		}
		stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", src)
		if exitOf(err) == 0 {
			t.Errorf("remove --yes exit 0 with the PR state unknown; stderr %q\n%s", stderr, stdout)
		}
		if !exists(src) || !hasBranch(t, dir, "spike") {
			t.Error("original or branch removed with the PR state unknown")
		}
	})
}

// TestRescueTwiceThenRemove rescues the same clean original twice (two
// records, two copies) and removes it: the original and its branch go, both
// copies and both records stay. Why: more than one matching record must not
// make the original ambiguous, and a second rescue of a preserved original
// stays possible.
func TestRescueTwiceThenRemove(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	src := cleanWorktree(t, dir)
	githubOrigin(t, dir)
	first := rescueClean(t, a, dir)
	second := rescueClean(t, a, dir, "--to", "rescued/spike-2")
	if first.Record.Ref == second.Record.Ref || first.Destination.Branch == second.Destination.Branch {
		t.Fatalf("second rescue reused the first: %+v / %+v", first, second)
	}
	r := rowFor(t, listRows(t, a, dir), "spike")
	if r.Status != "proven" || r.Evidence != "rescued" {
		t.Fatalf("source row = status %q evidence %q", r.Status, r.Evidence)
	}
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", src)
	if exitOf(err) != 0 {
		t.Fatalf("remove exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	if exists(src) || hasBranch(t, dir, "spike") {
		t.Error("original or its branch survives")
	}
	for _, e := range []rescue.Envelope{first, second} {
		if !hasRef(t, dir, e.Record.Ref) || !hasBranch(t, dir, e.Destination.Branch) {
			t.Errorf("record %s or branch %s is gone", e.Record.Ref, e.Destination.Branch)
		}
	}
}

// dirtyWorktree is cleanWorktree plus all three kinds of uncommitted work: a
// staged edit, an unstaged edit and an untracked file.
func dirtyWorktree(t *testing.T, dir string) string {
	t.Helper()
	p := cleanWorktree(t, dir)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(p, "staged.txt"), []byte("staged"), 0o644))
	cgit(t, p, "add", "staged.txt")
	must(os.WriteFile(filepath.Join(p, "spike.txt"), []byte("edited"), 0o644))
	must(os.WriteFile(filepath.Join(p, "new.txt"), []byte("untracked"), 0o644))
	return p
}

// TestRescueDirtyOriginalIsRemovableWithAcceptLoss drives the real commands
// on a worktree with commits plus staged, unstaged and untracked work:
// after `rescue --yes` the original lists less_sure with reason preserved,
// its loss lines no longer mention uncommitted changes, plain `remove --yes`
// exits 2 (refused) with "needs --accept-loss" and removes nothing, and
// `--accept-loss` removes the worktree and branch while the copy and the
// record stay. Why: the feature's promise for dirty work, and only the real
// commands together prove the classifier, the engine's re-check and the
// approval ladder agree (dirt never reads proven and never needs --force).
func TestRescueDirtyOriginalIsRemovableWithAcceptLoss(t *testing.T) {
	a := rescueApp(t)
	dir := cleanupRepo(t)
	src := dirtyWorktree(t, dir)
	githubOrigin(t, dir)
	env := rescueClean(t, a, dir)

	r := rowFor(t, listRows(t, a, dir), "spike")
	if r.Status != "less_sure" || !slices.Contains(reasonCodes(r), "preserved") || slices.Contains(reasonCodes(r), "dirty") {
		t.Fatalf("source row = status %q evidence %q reasons %v, want less_sure with preserved and no dirty", r.Status, r.Evidence, reasonCodes(r))
	}

	// The dry run without --accept-loss exits 2 (refused), but still reports
	// the effective loss lines.
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--dry-run", "--json", src)
	if items := decodeOne(t, stdout).Items; len(items) != 1 {
		t.Fatalf("dry-run exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	for _, l := range decodeOne(t, stdout).Items[0].Losses {
		if strings.Contains(l, "uncommitted changes") {
			t.Errorf("effective loss lines still say %q", l)
		}
	}

	stdout, stderr, err = runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", src)
	out := decodeOne(t, stdout)
	if exitOf(err) != 2 || len(out.Items) != 1 || !strings.Contains(out.Items[0].Reason, "needs --accept-loss") {
		t.Fatalf("remove --yes exit %d, items %+v, stderr %q, want 2 (refused) and \"needs --accept-loss\"", exitOf(err), out.Items, stderr)
	}
	if !exists(src) || !hasBranch(t, dir, "spike") {
		t.Fatal("the original or its branch was removed without --accept-loss")
	}

	stdout, stderr, err = runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "--json", src)
	if exitOf(err) != 0 {
		t.Fatalf("remove --accept-loss exit %d, stderr %q\n%s", exitOf(err), stderr, stdout)
	}
	if exists(src) || hasBranch(t, dir, "spike") {
		t.Error("original or its branch survives --accept-loss")
	}
	if !hasBranch(t, dir, "rescued/spike") || !exists(env.Destination.Path) || !hasRef(t, dir, env.Record.Ref) {
		t.Error("the copy, its worktree or the record is gone")
	}
}

// TestRescueDirtyOriginalNegatives checks the cases where a dirty original
// must not read covered: an edit after the rescue, an assume-unchanged flag
// hiding an edit, and an operation in progress. Each keeps the row off the
// preserved/less_sure path, and `remove --yes --accept-loss` still needs
// --force (exit non-zero, nothing removed). Why: the record covers exactly
// the state rescued; anything the record cannot prove must read as today.
func TestRescueDirtyOriginalNegatives(t *testing.T) {
	check := func(t *testing.T, a *app, dir, src string) {
		t.Helper()
		r := rowFor(t, listRows(t, a, dir), "spike")
		if r.Status == "less_sure" && slices.Contains(reasonCodes(r), "preserved") && !slices.Contains(reasonCodes(r), "dirty") {
			t.Fatalf("row still reads covered: status %q reasons %v", r.Status, reasonCodes(r))
		}
		if r.Status != "unique_work" && r.Status != "in_use" {
			t.Fatalf("status = %q reasons %v, want unique_work or in_use", r.Status, reasonCodes(r))
		}
		_, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "--json", src)
		if exitOf(err) == 0 || !exists(src) || !hasBranch(t, dir, "spike") {
			t.Errorf("remove --accept-loss exit %d, removed=%v; want a refusal that keeps everything", exitOf(err), !exists(src))
		}
	}
	t.Run("edit after the rescue", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := dirtyWorktree(t, dir)
		githubOrigin(t, dir)
		rescueClean(t, a, dir)
		if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("changed after"), 0o644); err != nil {
			t.Fatal(err)
		}
		check(t, a, dir, src)
	})
	t.Run("hidden by assume-unchanged", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := dirtyWorktree(t, dir)
		githubOrigin(t, dir)
		rescueClean(t, a, dir)
		cgit(t, src, "update-index", "--assume-unchanged", "spike.txt")
		if err := os.WriteFile(filepath.Join(src, "spike.txt"), []byte("hidden edit"), 0o644); err != nil {
			t.Fatal(err)
		}
		check(t, a, dir, src)
	})
	t.Run("operation in progress", func(t *testing.T) {
		a := rescueApp(t)
		dir := cleanupRepo(t)
		src := dirtyWorktree(t, dir)
		githubOrigin(t, dir)
		rescueClean(t, a, dir)
		gd := cgit(t, src, "rev-parse", "--absolute-git-dir")
		if err := os.WriteFile(filepath.Join(gd, "MERGE_HEAD"), []byte(cgit(t, src, "rev-parse", "HEAD")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		check(t, a, dir, src)
	})
}
