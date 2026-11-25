package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// cleanupRecord writes the chain `wt rescue` writes (a commit with the six
// trailers, parent srcHead) and points refs/wt/rescued/<16 hex> at it. The
// source path is only a trailer value: pass a worktree's path for a record
// that worktree still matches, a missing path for one whose source is gone.
func cleanupRecord(t *testing.T, dir, srcPath, srcRef, srcHead string) (ref, commit string) {
	t.Helper()
	msg := "wt rescue record\n\n" + strings.Join([]string{
		"Wt-Version: 1", "Wt-Source-Path: " + srcPath, "Wt-Source-Ref: " + srcRef,
		"Wt-Source-Head: " + srcHead, "Wt-Dest-Branch: rescue/x", "Wt-Dest-Path: /tmp/dest",
	}, "\n") + "\n"
	cmd := exec.Command("git", "-C", dir, "commit-tree", srcHead+"^{tree}", "-p", srcHead, "-F", "-")
	cmd.Stdin = strings.NewReader(msg)
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	commit = strings.TrimSpace(string(b))
	ref = "refs/wt/rescued/" + commit[:16]
	cgit(t, dir, "update-ref", ref, commit)
	return ref, commit
}

// recordRefExists reads the ref with for-each-ref (exact spelling).
func recordRefExists(t *testing.T, dir, ref string) bool {
	t.Helper()
	return cgit(t, dir, "for-each-ref", "--format=%(refname)", ref) == ref
}

// recordRemoveApp is removeApp with the clock days ahead, so a record made
// now is that old.
func recordRemoveApp(t *testing.T, days int) *app {
	t.Helper()
	a, _ := removeApp(t)
	removeNow = func() time.Time { return time.Now().Add(time.Duration(days) * 24 * time.Hour) }
	return a
}

// TestRemoveRecordPrunesAnOldUnusedRecord checks wt remove --record <id>
// --yes prunes a record whose source is gone and which is over a week old:
// exit 0, the ref gone, the text naming the commit and the restore command.
// Why: this is the supported replacement for a hand-typed update-ref -d.
func TestRemoveRecordPrunesAnOldUnusedRecord(t *testing.T) {
	a := recordRemoveApp(t, 30)
	dir := cleanupRepo(t)
	ref, commit := cleanupRecord(t, dir, "/nonexistent/src", "refs/heads/old", cgit(t, dir, "rev-parse", "HEAD"))
	stdout, stderr, err := runRemove(t, a, dir, "", "--record", "--yes", ref[len("refs/wt/rescued/"):])
	if exitOf(err) != 0 {
		t.Fatalf("exit %d: %v\n%s\n%s", exitOf(err), err, stdout, stderr)
	}
	if recordRefExists(t, dir, ref) {
		t.Fatal("the ref is still there")
	}
	for _, want := range []string{"pruned", "record commit was " + commit, "git update-ref " + ref + " " + commit} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

// TestRemoveRecordByFullRefAndJSON checks the full ref names a record too and
// that --json prints one envelope with items [] and the records section.
// Why: scripts read this document; the shape is the additive contract.
func TestRemoveRecordByFullRefAndJSON(t *testing.T) {
	a := recordRemoveApp(t, 30)
	dir := cleanupRepo(t)
	ref, commit := cleanupRecord(t, dir, "/nonexistent/src", "refs/heads/old", cgit(t, dir, "rev-parse", "HEAD"))
	stdout, _, err := runRemove(t, a, dir, "", "--record", "--yes", "--json", ref, ref)
	env := decodeOne(t, stdout)
	if exitOf(err) != 0 || len(env.Items) != 0 || len(env.Records) != 1 || env.Records[0].Outcome != "pruned" || env.Records[0].Commit != commit {
		t.Fatalf("exit %d env %+v\n%s", exitOf(err), env, stdout)
	}
	if recordRefExists(t, dir, ref) {
		t.Fatal("the ref is still there")
	}
}

// TestRemoveRecordRecentNeedsAcceptLoss checks a young record is retained
// (exit 2, ref kept) without --accept-loss and pruned with it. Why: a fresh
// record is usually the only copy of just-rescued work.
func TestRemoveRecordRecentNeedsAcceptLoss(t *testing.T) {
	a := recordRemoveApp(t, 0)
	dir := cleanupRepo(t)
	ref, _ := cleanupRecord(t, dir, "/nonexistent/src", "refs/heads/old", cgit(t, dir, "rev-parse", "HEAD"))
	stdout, _, err := runRemove(t, a, dir, "", "--record", "--yes", "--json", ref)
	env := decodeOne(t, stdout)
	if exitOf(err) != 2 || len(env.Records) != 1 || env.Records[0].Outcome != "retained_recent" || !strings.Contains(env.Records[0].Reason, "--accept-loss") {
		t.Fatalf("exit %d env %+v", exitOf(err), env)
	}
	if !recordRefExists(t, dir, ref) {
		t.Fatal("the young record was pruned")
	}
	_, _, err = runRemove(t, a, dir, "", "--record", "--yes", "--accept-loss", ref)
	if exitOf(err) != 0 || recordRefExists(t, dir, ref) {
		t.Fatalf("with --accept-loss: exit %d, ref there=%v", exitOf(err), recordRefExists(t, dir, ref))
	}
}

// TestRemoveRecordInUseIsRefusedWhateverTheFlags checks a record whose source
// worktree still matches is refused with every approval flag, exit 2, ref
// kept. Why: that record is the only copy of the worktree's rescued state.
func TestRemoveRecordInUseIsRefusedWhateverTheFlags(t *testing.T) {
	a := recordRemoveApp(t, 30)
	dir := cleanupRepo(t)
	ref, _ := cleanupRecord(t, dir, cleanup.ResolvePath(dir), "refs/heads/main", cgit(t, dir, "rev-parse", "HEAD"))
	stdout, _, err := runRemove(t, a, dir, "", "--record", "--yes", "--accept-loss", "--json", ref)
	env := decodeOne(t, stdout)
	if exitOf(err) != 2 || len(env.Records) != 1 || env.Records[0].Outcome != "refused_in_use" {
		t.Fatalf("exit %d env %+v", exitOf(err), env)
	}
	if !recordRefExists(t, dir, ref) {
		t.Fatal("an in-use record was pruned")
	}
}

// TestRemoveRecordDryRunAndApproval checks --dry-run changes nothing and says
// so (exit 0, no --yes needed), a run with no --yes and no terminal exits 3,
// and on a terminal "y" prunes while "n" does not, the prompt naming source,
// destination branch and age. Why: the same approval rules as every removal.
func TestRemoveRecordDryRunAndApproval(t *testing.T) {
	a := recordRemoveApp(t, 30)
	dir := cleanupRepo(t)
	ref, _ := cleanupRecord(t, dir, "/nonexistent/src", "refs/heads/old", cgit(t, dir, "rev-parse", "HEAD"))
	stdout, _, err := runRemove(t, a, dir, "", "--record", "--dry-run", ref)
	if exitOf(err) != 0 || !strings.Contains(stdout, "dry run: nothing was changed") || !recordRefExists(t, dir, ref) {
		t.Fatalf("dry run: exit %d there=%v\n%s", exitOf(err), recordRefExists(t, dir, ref), stdout)
	}
	if _, _, err := runRemove(t, a, dir, "y\n", "--record", ref); exitOf(err) != 3 || !recordRefExists(t, dir, ref) {
		t.Fatalf("no terminal: exit %d", exitOf(err))
	}
	onTTY(t)
	_, stderr, err := runRemove(t, a, dir, "n\n", "--record", ref)
	if exitOf(err) != 3 || !recordRefExists(t, dir, ref) {
		t.Fatalf("declined: exit %d", exitOf(err))
	}
	for _, want := range []string{"/nonexistent/src", "rescue/x", "30 days old", "Prune 1 record? [y/N]"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("prompt lacks %q:\n%s", want, stderr)
		}
	}
	if _, _, err := runRemove(t, a, dir, "y\n", "--record", ref); exitOf(err) != 0 || recordRefExists(t, dir, ref) {
		t.Fatalf("approved: exit %d there=%v", exitOf(err), recordRefExists(t, dir, ref))
	}
}

// TestRemoveRecordUsageErrors checks an argument that is no record (a branch
// or worktree target mixed in), and the flags --record does not take, are
// usage errors (exit 1, an error envelope under --json) that remove nothing.
// Why: --record must never be read as a worktree or branch removal.
func TestRemoveRecordUsageErrors(t *testing.T) {
	a := recordRemoveApp(t, 30)
	dir := cleanupRepo(t)
	ref, _ := cleanupRecord(t, dir, "/nonexistent/src", "refs/heads/old", cgit(t, dir, "rev-parse", "HEAD"))
	uniqueBranch(t, dir, "topic")
	for name, args := range map[string][]string{
		"branch target": {"--record", "--yes", ref, "topic"},
		"unknown id":    {"--record", "--yes", "deadbeefdeadbeef"},
		"-D":            {"--record", "--yes", "-D", ref},
		"--force":       {"--record", "--yes", "--force", ref},
		"--remote":      {"--record", "--yes", "--remote", ref},
	} {
		stdout, _, err := runRemove(t, a, dir, "", append([]string{"--json"}, args...)...)
		env := decodeOne(t, stdout)
		if exitOf(err) != 1 || env.Error == nil || len(env.Records) != 0 {
			t.Errorf("%s: exit %d env %+v", name, exitOf(err), env)
		}
		if !recordRefExists(t, dir, ref) || !hasBranch(t, dir, "topic") {
			t.Errorf("%s: something was removed", name)
		}
	}
}

// TestWorktreesPlainListsRescueRecords checks wt worktrees --plain prints the
// Rescue records block with the id wt remove --record takes, and --json the
// records section. Why: the id has to be discoverable where users look.
func TestWorktreesPlainListsRescueRecords(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	ref, _ := cleanupRecord(t, dir, "/nonexistent/src", "refs/heads/old", cgit(t, dir, "rev-parse", "HEAD"))
	id := ref[len("refs/wt/rescued/"):]
	stdout, stderr, err := runCleanup(t, a, dir, "--plain", "--no-fetch")
	if err != nil || !strings.Contains(stdout, "Rescue records") || !strings.Contains(stdout, id) || !strings.Contains(stdout, "from /nonexistent/src") {
		t.Fatalf("plain: %v\n%s\n%s", err, stdout, stderr)
	}
	stdout, _, err = runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil || !strings.Contains(stdout, `"records"`) || !strings.Contains(stdout, `"id": "`+id+`"`) {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
}

// TestWithRecordNoteIsOnlyForTheListHeader checks the tick list's header
// pointer counts the records and a report without them is unchanged. Why: the
// list shows no record rows, so the note is how a user learns they exist.
func TestWithRecordNoteIsOnlyForTheListHeader(t *testing.T) {
	if got := withRecordNote(cleanup.Report{}); len(got.Notes) != 0 {
		t.Fatalf("notes = %v, want none", got.Notes)
	}
	got := withRecordNote(cleanup.Report{Records: make([]cleanup.RecordRow, 2)})
	if len(got.Notes) != 1 || got.Notes[0] != "2 rescue records, see wt worktrees --plain / wt remove --record" {
		t.Fatalf("notes = %v", got.Notes)
	}
}
