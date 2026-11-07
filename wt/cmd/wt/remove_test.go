package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
)

// removeApp is a test app whose registry is a scratch file named by
// WT_REGISTRY, with WT_LITELLM_CONFIG redirected too (a redirected registry
// must name config.yaml as well), and the classification clock an hour ahead
// so the fixture's seconds-old branches clear the min-age rule. The terminal
// is "absent" unless a test says otherwise.
func removeApp(t *testing.T) (*app, string) {
	t.Helper()
	a, tmp := newTestApp(t)
	t.Setenv("WT_REGISTRY", filepath.Join(tmp, "local-ai", "registry.toml"))
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(tmp, "config.yaml"))
	stubCleanupGH(t)
	oldNow, oldTTY, oldProcs := removeNow, removeTTY, removeProcs
	removeNow = func() time.Time { return time.Now().Add(time.Hour) }
	removeTTY = func(io.Reader, io.Writer) bool { return false }
	removeProcs = func() removal.ProcFinder { return nil }
	t.Cleanup(func() { removeNow, removeTTY, removeProcs = oldNow, oldTTY, oldProcs })
	return a, tmp
}

// defaultRemoveProcs is removeProcs as the package initialised it, saved by
// TestMain before it stubs the seam.
var defaultRemoveProcs func() removal.ProcFinder

// onTTY makes the command see a terminal for the rest of the test.
func onTTY(t *testing.T) {
	t.Helper()
	removeTTY = func(io.Reader, io.Writer) bool { return true }
}

// runRemove runs `wt remove args...` from dir with stdin as the terminal's
// input and returns both streams and the error (exitCodeOf(err) is the
// status; nil is 0).
func runRemove(t *testing.T, a *app, dir, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	cmd := removeCmd(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{}, args...))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// exitOf is the process status for err.
func exitOf(err error) int {
	if err == nil {
		return 0
	}
	return exitCodeOf(err)
}

// mergedWorktree adds a linked worktree on a new branch, commits in it,
// pushes the branch and merges it into main with a merge commit (pushed):
// proven merged, nothing to lose. It returns the worktree's path.
func mergedWorktree(t *testing.T, dir, branch string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(dir), "wt-"+branch)
	cgit(t, dir, "worktree", "add", "-b", branch, p)
	cleanupCommit(t, p, branch+".txt", branch+" work")
	cgit(t, dir, "push", "-u", "origin", branch)
	cgit(t, dir, "merge", "--no-ff", "--no-edit", branch)
	cgit(t, dir, "push", "origin", "main")
	return p
}

// unmergedWorktree adds a linked worktree on a new branch with one commit,
// pushed but not merged: less sure, its branch not proven merged.
func unmergedWorktree(t *testing.T, dir, branch string) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(dir), "wt-"+branch)
	cgit(t, dir, "worktree", "add", "-b", branch, p)
	cleanupCommit(t, p, branch+".txt", branch+" work")
	cgit(t, dir, "push", "-u", "origin", branch)
	return p
}

// uniqueBranch makes a branch (no worktree) with a commit on no remote.
func uniqueBranch(t *testing.T, dir, branch string) {
	t.Helper()
	cgit(t, dir, "checkout", "-b", branch)
	cleanupCommit(t, dir, branch+".txt", branch+" work")
	cgit(t, dir, "checkout", "main")
}

// githubOrigin points origin at a GitHub URL once the fixture's pushes are
// done (--no-fetch keeps wt from reaching it), so the PR lookup knows which
// repository's PRs to ask about and an unmerged branch's state reads as
// known (otherwise it is pr_unknown and its branch retained_unknown).
func githubOrigin(t *testing.T, dir string) {
	t.Helper()
	cgit(t, dir, "remote", "set-url", "origin", "https://github.com/o/r.git")
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// hasBranch reports whether refs/heads/<b> exists, read with for-each-ref
// (exact spelling, never resolved through another case).
func hasBranch(t *testing.T, dir, b string) bool {
	t.Helper()
	return cgit(t, dir, "for-each-ref", "--format=%(refname)", "refs/heads/"+b) == "refs/heads/"+b
}

// decodeOne decodes stdout as exactly one removal envelope.
func decodeOne(t *testing.T, stdout string) removal.Envelope {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var env removal.Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not one envelope: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one document:\n%s", stdout)
	}
	return env
}

// TestRemoveCmdRemovesProvenWorktreeAndBranch verifies the basic path: a
// proven merged worktree named by its path, with --yes, is removed and its
// branch deleted, exit 0. It is the command's whole point; everything else
// guards it.
func TestRemoveCmdRemovesProvenWorktreeAndBranch(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", p)
	if exitOf(err) != 0 {
		t.Fatalf("exit %d: %v\nstdout=%s\nstderr=%s", exitOf(err), err, stdout, stderr)
	}
	if exists(p) || hasBranch(t, dir, "topic") {
		t.Errorf("worktree there=%v branch there=%v, want both gone\n%s", exists(p), hasBranch(t, dir, "topic"), stdout)
	}
	if !strings.Contains(stdout, "topic") {
		t.Errorf("stdout does not report topic: %q", stdout)
	}
}

// TestRemoveCmdNeedsYesWithoutTTY verifies that without --yes and without a
// terminal the command exits 3 and removes nothing. A script that forgot
// --yes must not delete; there is nobody to ask.
func TestRemoveCmdNeedsYesWithoutTTY(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	_, _, err := runRemove(t, a, dir, "y\n", "--no-fetch", p)
	if exitOf(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitOf(err), err)
	}
	if !exists(p) || !hasBranch(t, dir, "topic") {
		t.Errorf("worktree there=%v branch there=%v, want both kept", exists(p), hasBranch(t, dir, "topic"))
	}
	// --json: the one envelope (an error, no items), still exit 3.
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--json", p)
	env := decodeOne(t, stdout)
	if exitOf(err) != 3 || env.Error == nil || len(env.Items) != 0 || !exists(p) {
		t.Errorf("json: exit %d env %+v there=%v, want 3, an error envelope, kept", exitOf(err), env, exists(p))
	}
}

// TestRemoveCmdUnmergedNeedsDeleteFlag verifies a branch with commits on no
// remote is refused without -D ("needs -D", exit 2, branch kept) and deleted
// with -D plus --accept-loss and --yes. -D is the one way to delete
// unmerged work; --yes alone must never do it.
func TestRemoveCmdUnmergedNeedsDeleteFlag(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	uniqueBranch(t, dir, "rescue")
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "--json", "rescue")
	env := decodeOne(t, stdout)
	if exitOf(err) != 2 || !hasBranch(t, dir, "rescue") {
		t.Fatalf("without -D: exit %d, branch there=%v, want 2 and kept\n%s", exitOf(err), hasBranch(t, dir, "rescue"), stdout)
	}
	if len(env.Items) != 1 || env.Items[0].Worktree != "refused" || !strings.Contains(env.Items[0].Reason, "needs -D") {
		t.Errorf("items = %+v, want one refused \"needs -D\"", env.Items)
	}
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "-D", "rescue")
	if exitOf(err) != 0 || hasBranch(t, dir, "rescue") {
		t.Errorf("with -D: exit %d (%v), branch there=%v, want 0 and gone\n%s\n%s", exitOf(err), err, hasBranch(t, dir, "rescue"), stdout, stderr)
	}
}

// TestRemoveCmdDirtyNeedsForce verifies a worktree with uncommitted changes
// is refused without --force ("needs --force", exit 2, the files kept) and
// removed with --force (plus --accept-loss, the loss lines' approval). The
// changes exist nowhere else; only the explicit flag may lose them.
func TestRemoveCmdDirtyNeedsForce(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	if err := os.WriteFile(filepath.Join(p, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "--json", p)
	env := decodeOne(t, stdout)
	if exitOf(err) != 2 || !exists(filepath.Join(p, "scratch.txt")) {
		t.Fatalf("without --force: exit %d, file there=%v, want 2 and kept\n%s", exitOf(err), exists(filepath.Join(p, "scratch.txt")), stdout)
	}
	if len(env.Items) != 1 || env.Items[0].Worktree != "refused" || !strings.Contains(env.Items[0].Reason, "needs --force") {
		t.Errorf("items = %+v, want one refused \"needs --force\"", env.Items)
	}
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "--force", p)
	if exitOf(err) != 0 || exists(p) {
		t.Errorf("with --force: exit %d (%v), worktree there=%v, want 0 and gone\n%s\n%s", exitOf(err), err, exists(p), stdout, stderr)
	}
}

// TestRemoveCmdDryRunMutatesNothing verifies --dry-run reports what a run
// would do (dry_run true, the worktree "removed" with a dry-run reason)
// without --yes and without a terminal, and changes nothing. A dry run is
// how a user reads the loss lines before approving them.
func TestRemoveCmdDryRunMutatesNothing(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--dry-run", "--json", p)
	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
	}
	env := decodeOne(t, stdout)
	if !env.DryRun || len(env.Items) != 1 || env.Items[0].Worktree != "removed" || !strings.Contains(env.Items[0].Reason, "dry run") || env.Items[0].BranchTip != "" {
		t.Errorf("envelope = %+v, want one dry-run item", env)
	}
	if !exists(p) || !hasBranch(t, dir, "topic") {
		t.Errorf("worktree there=%v branch there=%v, want both kept", exists(p), hasBranch(t, dir, "topic"))
	}
}

// TestRemoveCmdUnknownArgIsUsageError verifies an argument matching no
// worktree path and no local branch is exit 1 naming the argument, with
// --json one error envelope. Guessing a target from a typo could remove the
// wrong thing.
func TestRemoveCmdUnknownArgIsUsageError(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	_, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", p, "nope")
	if exitOf(err) != 1 || err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("exit %d err %v, want 1 naming \"nope\"", exitOf(err), err)
	}
	if !exists(p) {
		t.Error("the valid argument's worktree was removed despite the usage error")
	}
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", "nope")
	env := decodeOne(t, stdout)
	if exitOf(err) != 1 || env.Error == nil || !strings.Contains(env.Error.Message, "nope") {
		t.Errorf("json: exit %d env %+v", exitOf(err), env)
	}
	// The empty argument names nothing (joined to the current directory it
	// would be the primary checkout).
	if _, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", ""); exitOf(err) != 1 || err == nil || !strings.Contains(err.Error(), `""`) {
		t.Errorf(`"": exit %d err %v, want 1 naming ""`, exitOf(err), err)
	}
}

// TestRemoveCmdRemoteRowIsNeverATarget verifies an unmerged origin branch
// with no local branch (a real, non-default remote row) is never deleted: a
// bare branch name or a refs/remotes/ spelling is no target (exit 1), and
// origin/topic, though a target, is refused (exit 2) however many approvals
// are given, --remote included, because only a proven row is removable. The
// remote ref stays.
func TestRemoveCmdRemoteRowIsNeverATarget(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	cgit(t, dir, "checkout", "-b", "topic")
	cleanupCommit(t, dir, "t.txt", "topic work")
	cgit(t, dir, "push", "-u", "origin", "topic")
	cgit(t, dir, "checkout", "main")
	cgit(t, dir, "branch", "-D", "topic")
	if got := cgit(t, dir, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/topic"); got != "refs/remotes/origin/topic" {
		t.Fatalf("fixture: origin/topic missing (%q)", got)
	}
	for arg, want := range map[string]int{"origin/topic": 2, "topic": 1, "refs/remotes/origin/topic": 1} {
		_, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--force", "--accept-loss", "-D", "--remote", arg)
		if exitOf(err) != want {
			t.Errorf("%s: exit %d (%v), want %d", arg, exitOf(err), err, want)
		}
	}
	if got := cgit(t, dir, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/topic"); got != "refs/remotes/origin/topic" {
		t.Errorf("origin/topic gone")
	}
}

// TestRemoveCmdDuplicateArgsAreOneItem verifies that naming the same
// worktree twice (its path, and its branch name, and the path again) acts on
// it once: one item, exit 0. A second item for the same target would read
// "no longer exists" and turn a clean run into exit 2.
func TestRemoveCmdDuplicateArgsAreOneItem(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", p, "topic", p)
	if exitOf(err) != 0 {
		t.Fatalf("exit %d (%v)\n%s", exitOf(err), err, stdout)
	}
	if env := decodeOne(t, stdout); len(env.Items) != 1 || env.Items[0].Worktree != "removed" {
		t.Errorf("items = %+v, want one removed", env.Items)
	}
}

// TestRemoveCmdRegistryCheckedBeforeAnythingIsRead verifies the registry
// refusal comes before the working directory is read as a repository and
// before any fetch: outside a repository the error still names the registry,
// and inside one (without --no-fetch) origin's new commit is not fetched.
// Removal must not even start reading with artifact protection off.
func TestRemoveCmdRegistryCheckedBeforeAnythingIsRead(t *testing.T) {
	_, tmp := removeApp(t)
	if err := os.WriteFile(filepath.Join(tmp, "local-ai", "registry.toml"), []byte("models = [ broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil || a.loadErr == nil {
		t.Fatalf("newApp: %v, loadErr %v; want a load error", err, a.loadErr)
	}
	if _, _, err := runRemove(t, a, t.TempDir(), "", "--yes", "topic"); exitOf(err) != 1 || !strings.Contains(fmtErr(err), "registry") {
		t.Errorf("outside a repository: exit %d err %v, want 1 naming the registry", exitOf(err), err)
	}

	dir := cleanupRepo(t)
	other := filepath.Join(filepath.Dir(dir), "other")
	origin := cgit(t, dir, "remote", "get-url", "origin")
	cgit(t, filepath.Dir(dir), "clone", "-q", origin, other)
	cgit(t, other, "config", "user.email", "t@example.com")
	cgit(t, other, "config", "user.name", "T")
	cgit(t, other, "config", "commit.gpgsign", "false")
	cleanupCommit(t, other, "new.txt", "new upstream work")
	cgit(t, other, "push", "origin", "main")
	before := cgit(t, dir, "rev-parse", "refs/remotes/origin/main")
	if _, _, err := runRemove(t, a, dir, "", "--yes", "main"); exitOf(err) != 1 {
		t.Errorf("in a repository: exit %d (%v), want 1", exitOf(err), err)
	}
	if after := cgit(t, dir, "rev-parse", "refs/remotes/origin/main"); after != before {
		t.Errorf("origin/main moved %s -> %s: a fetch ran before the registry refusal", before, after)
	}
}

// fmtErr is err's text, "" for nil.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestRemoveDefaultProcFinderIsLsof verifies the command's default
// live-process finder is removal.LsofFinder (by type; it is not run, no test
// scans the machine). A nil default would silently drop the "processes
// running in the worktree" warning.
func TestRemoveDefaultProcFinderIsLsof(t *testing.T) {
	got, want := reflect.TypeOf(defaultRemoveProcs()), reflect.TypeOf(removal.LsofFinder())
	if got == nil || got != want {
		t.Errorf("default finder type = %v, want %v", got, want)
	}
}

// TestCleanupInputsLabelsTheCommand verifies the session claim names the
// command that gathered (wt-remove for remove, wt-cleanup for cleanup), so a
// claim seen by another process says who holds the root.
func TestCleanupInputsLabelsTheCommand(t *testing.T) {
	dir := t.TempDir()
	for _, label := range []string{"wt-remove", "wt-cleanup"} {
		if got := cleanupInputs(dir, false, nil, label).Claim.Agent; got != label {
			t.Errorf("label %s: claim agent %q", label, got)
		}
	}
}

// TestRemoveCmdPromptShowsEveryItemFirst verifies that with two items on a
// terminal the prompt prints BOTH items' loss lines and processes, and the
// -D line naming the offered branch, all before the [y/N] question. Seeing
// only the first item's losses would approve the second blind.
func TestRemoveCmdPromptShowsEveryItemFirst(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	first := mergedWorktree(t, dir, "topic")
	second := unmergedWorktree(t, dir, "kept")
	githubOrigin(t, dir)
	excl := filepath.Join(filepath.Dir(dir), "excl")
	if err := os.WriteFile(excl, []byte("cache/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cgit(t, dir, "config", "core.excludesFile", excl)
	// Different losses per item (1 and 2 ignored entries), so one item's
	// lines cannot stand in for the other's.
	for n, p := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(p, "cache"), 0o755); err != nil {
			t.Fatal(err)
		}
		for k := 0; k <= n; k++ {
			if err := os.WriteFile(filepath.Join(p, "cache", "blob"+string(rune('a'+k))), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	removeProcs = func() removal.ProcFinder {
		return dirProcs{first: {PID: 111, Command: "vim"}, second: {PID: 222, Command: "make"}}
	}
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--dry-run", "--accept-loss", "-D", "--json", first, second)
	env := decodeOne(t, stdout)
	if err != nil || len(env.Items) != 2 || len(env.Items[0].Losses) == 0 || len(env.Items[1].Losses) == 0 {
		t.Fatalf("dry run: err %v items %+v, want two items with losses", err, env.Items)
	}

	onTTY(t)
	_, stderr, err := runRemove(t, a, dir, "n\n", "--no-fetch", "--accept-loss", "-D", first, second)
	if exitOf(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitOf(err), err)
	}
	q := strings.Index(stderr, "[y/N]")
	want := append(append([]string{}, env.Items[0].Losses...), env.Items[1].Losses...)
	want = append(want, "pid 111 vim", "pid 222 make", "-D: will also delete unmerged branch kept")
	for _, w := range want {
		if i := strings.Index(stderr, w); i < 0 || i > q {
			t.Errorf("%q missing or after the question (at %d, question at %d):\n%s", w, i, q, stderr)
		}
	}
	// Each item's losses and process under its own heading.
	i, j := strings.Index(stderr, "worktree topic"), strings.Index(stderr, "worktree kept")
	if i < 0 || j < i || q < j {
		t.Fatalf("headings out of order:\n%s", stderr)
	}
	for k, sec := range []string{stderr[i:j], stderr[j:q]} {
		proc := []string{"pid 111 vim", "pid 222 make"}[k]
		for _, l := range append(append([]string{}, env.Items[k].Losses...), proc) {
			if !strings.Contains(sec, l) {
				t.Errorf("item %d: %q not under its heading:\n%s", k, l, sec)
			}
		}
	}
	if env.Items[0].Losses[0] == env.Items[1].Losses[0] {
		t.Fatalf("fixture: the two items' losses are the same (%q)", env.Items[0].Losses)
	}
}

// dirProcs is a ProcFinder that finds one process per worktree directory.
type dirProcs map[string]removal.Proc

func (d dirProcs) InDir(dir string) ([]removal.Proc, error) {
	for k, p := range d {
		if cleanup.ResolvePath(k) == cleanup.ResolvePath(dir) {
			return []removal.Proc{p}, nil
		}
	}
	return nil, nil
}

// TestRemoveCmdClosedStdinIsNo verifies a terminal whose input ends without
// an answer (EOF) counts as No: exit 3, nothing removed. A closed stdin must
// never read as consent.
func TestRemoveCmdClosedStdinIsNo(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	onTTY(t)
	for _, in := range []string{"", "y"} { // "y" with no newline is still read
		_, _, err := runRemove(t, a, dir, in, "--no-fetch", p)
		if in == "" && (exitOf(err) != 3 || !exists(p)) {
			t.Errorf("EOF: exit %d (%v), there=%v, want 3 and kept", exitOf(err), err, exists(p))
		}
		if in == "y" && (exitOf(err) != 0 || exists(p)) {
			t.Errorf("y then EOF: exit %d (%v), there=%v, want 0 and gone", exitOf(err), err, exists(p))
		}
	}
}

// TestRemoveCmdBranchItemDeleteLine verifies that for a branch-only item -D
// is described as deleting that branch itself ("-D: deletes unmerged branch
// rescue"), never as "will also delete" something besides it, and that the
// item's notes are not hidden. The prompt must say plainly what -D does to
// the item named.
func TestRemoveCmdBranchItemDeleteLine(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	uniqueBranch(t, dir, "rescue")
	githubOrigin(t, dir)
	onTTY(t)
	_, stderr, err := runRemove(t, a, dir, "n\n", "--no-fetch", "--accept-loss", "-D", "rescue")
	if exitOf(err) != 3 {
		t.Fatalf("exit %d (%v), want 3", exitOf(err), err)
	}
	if !strings.Contains(stderr, "-D: deletes unmerged branch rescue") || strings.Contains(stderr, "will also delete") {
		t.Errorf("prompt:\n%s", stderr)
	}
}

// TestRemoveCmdDeleteFlagWithoutEffectIsSaid verifies that when -D is given
// for a worktree whose branch is kept by rule (another worktree has it
// checked out), the result says -D had no effect on that branch. A user who
// asked for the branch to go must not assume it did.
func TestRemoveCmdDeleteFlagWithoutEffectIsSaid(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := unmergedWorktree(t, dir, "kept")
	twin := filepath.Join(filepath.Dir(dir), "wt-twin")
	cgit(t, dir, "worktree", "add", "-f", twin, "kept")
	githubOrigin(t, dir)
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "-D", "--json", p)
	env := decodeOne(t, stdout)
	if len(env.Items) != 1 {
		t.Fatalf("items = %+v (exit %d, %s)", env.Items, exitOf(err), stderr)
	}
	it := env.Items[0]
	if it.Worktree != "removed" || it.Branch == "deleted" || !hasBranch(t, dir, "kept") {
		t.Fatalf("item = %+v, want the worktree removed and the branch kept", it)
	}
	said := false
	for _, w := range it.Warnings {
		said = said || strings.Contains(w, "-D had no effect") && strings.Contains(w, "kept")
	}
	if !said {
		t.Errorf("warnings %q do not say -D had no effect on kept", it.Warnings)
	}
}

// TestRemoveCmdAmbiguousArgIsUsageError verifies an argument that is both a
// worktree's path (relative to the current directory) and another local
// branch's name is exit 1 naming it, and removes neither. Picking one would
// be a guess.
func TestRemoveCmdAmbiguousArgIsUsageError(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	// A worktree at <repo>/topic on branch other, and a branch named topic.
	p := filepath.Join(dir, "topic")
	cgit(t, dir, "worktree", "add", "-b", "other", p)
	cgit(t, dir, "branch", "topic")
	_, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--accept-loss", "-D", "topic")
	if exitOf(err) != 1 || err == nil || !strings.Contains(err.Error(), `"topic"`) || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("exit %d err %v, want 1 naming \"topic\" as matching more than one", exitOf(err), err)
	}
	if !exists(p) || !hasBranch(t, dir, "topic") || !hasBranch(t, dir, "other") {
		t.Error("something was removed")
	}
}

// TestRemoveCmdRegistryFailedRefuses verifies a registry that fails to load
// stops the command with exit 1 before it touches anything: the artifacts
// the registry names are unknown, so none could be protected.
func TestRemoveCmdRegistryFailedRefuses(t *testing.T) {
	_, tmp := removeApp(t)
	reg := filepath.Join(tmp, "local-ai", "registry.toml")
	if err := os.WriteFile(reg, []byte("models = [ this is not toml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	if a.loadErr == nil {
		t.Fatal("the broken registry loaded")
	}
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	_, _, err = runRemove(t, a, dir, "", "--no-fetch", "--yes", p)
	if exitOf(err) != 1 || err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("exit %d err %v, want 1 naming the registry", exitOf(err), err)
	}
	if !exists(p) || !hasBranch(t, dir, "topic") {
		t.Error("removed despite the registry failure")
	}
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", p)
	if env := decodeOne(t, stdout); exitOf(err) != 1 || env.Error == nil || len(env.Items) != 0 {
		t.Errorf("json: exit %d env %+v", exitOf(err), env)
	}
}

// TestRemoveCmdPartialFailureExits2WithJSON verifies two named items where
// one is approved (proven, --yes) and one is not (dirty, no --force): the
// first is removed, the second refused, the one envelope lists both in
// argument order and the exit is 2. A run that did part of its work must
// say so, and one item's missing flag must not stop or leak into another.
func TestRemoveCmdPartialFailureExits2WithJSON(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	dirty := mergedWorktree(t, dir, "dirty")
	if err := os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean := mergedWorktree(t, dir, "topic")
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", dirty, clean)
	if exitOf(err) != 2 {
		t.Fatalf("exit %d (%v), want 2\n%s", exitOf(err), err, stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty under --json", stderr)
	}
	env := decodeOne(t, stdout)
	if len(env.Items) != 2 || env.Items[0].Name != "dirty" || env.Items[0].Worktree != "refused" ||
		env.Items[1].Name != "topic" || env.Items[1].Worktree != "removed" || env.Items[1].Branch != "deleted" {
		t.Errorf("items = %+v", env.Items)
	}
	if !exists(dirty) || exists(clean) {
		t.Errorf("dirty there=%v clean there=%v, want kept / gone", exists(dirty), exists(clean))
	}
}

// TestRemoveCmdJSONIsOneDocument verifies --json prints exactly one JSON
// document on stdout, valid against the shape the contract pins, and nothing
// on stderr. A script parses stdout whole.
func TestRemoveCmdJSONIsOneDocument(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	stdout, stderr, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", p)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q", stderr)
	}
	env := decodeOne(t, stdout)
	if env.Version != removal.EnvelopeVersion || env.Error != nil || env.DryRun || len(env.Items) != 1 {
		t.Fatalf("envelope = %+v", env)
	}
	it := env.Items[0]
	if it.Kind != "worktree" || it.Name != "topic" || it.Saw != "proven" || it.Worktree != "removed" || it.Branch != "deleted" || len(it.BranchTip) != 40 {
		t.Errorf("item = %+v", it)
	}
	if cleanup.ResolvePath(env.Repo) != cleanup.ResolvePath(dir) {
		t.Errorf("repo = %q, want %q", env.Repo, dir)
	}
}

// TestRemoveCmdProtectedArtifactRefused verifies a worktree holding a
// registered local_path model artifact is refused (exit 2) and stays, even
// with every flag. wt never deletes a user-produced model artifact.
func TestRemoveCmdProtectedArtifactRefused(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	art := filepath.Join(p, "models", "q4")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	a.cfg.Models = []config.Model{{ID: "omlx/q4", ProviderID: "omlx", ModelName: "q4", Fetch: config.ModelArtifact{LocalPath: art}}}
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--force", "--accept-loss", "-D", "--json", p)
	if exitOf(err) != 2 || !exists(art) {
		t.Fatalf("exit %d (%v), artifact there=%v, want 2 and kept\n%s", exitOf(err), err, exists(art), stdout)
	}
	env := decodeOne(t, stdout)
	if len(env.Items) != 1 || env.Items[0].Worktree != "refused" || !strings.Contains(env.Items[0].Reason, cleanup.BlockProtectedArtifact) {
		t.Errorf("items = %+v, want refused naming %s", env.Items, cleanup.BlockProtectedArtifact)
	}
}

// fakeProcs is a ProcFinder that finds the same processes in every
// directory.
type fakeProcs []removal.Proc

func (f fakeProcs) InDir(string) ([]removal.Proc, error) { return f, nil }

// TestRemoveCmdPromptShowsLosses verifies the terminal prompt prints every
// loss line the dry run reports and every live process for each item before
// asking, defaults to No (an empty answer removes nothing, exit 3), and
// removes on "y". The user approves a specific loss, so they must see it.
func TestRemoveCmdPromptShowsLosses(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	cgit(t, dir, "config", "core.excludesFile", filepath.Join(filepath.Dir(dir), "excl"))
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "excl"), []byte("cache/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p, "cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "cache", "blob"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeProcs = func() removal.ProcFinder { return fakeProcs{{PID: 4242, Command: "vim"}} }

	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--dry-run", "--accept-loss", "--json", p)
	env := decodeOne(t, stdout)
	if err != nil || len(env.Items) != 1 || len(env.Items[0].Losses) == 0 {
		t.Fatalf("dry run: err %v items %+v, want one item with loss lines", err, env.Items)
	}
	losses := env.Items[0].Losses

	onTTY(t)
	_, stderr, err := runRemove(t, a, dir, "\n", "--no-fetch", "--accept-loss", p)
	if exitOf(err) != 3 || !exists(p) {
		t.Fatalf("empty answer: exit %d (%v), there=%v, want 3 and kept", exitOf(err), err, exists(p))
	}
	for _, l := range append(losses, "vim", "4242", "[y/N]") {
		if !strings.Contains(stderr, l) {
			t.Errorf("prompt lacks %q:\n%s", l, stderr)
		}
	}
	if strings.Index(stderr, losses[0]) > strings.Index(stderr, "[y/N]") {
		t.Errorf("loss line printed after the question:\n%s", stderr)
	}
	if _, _, err := runRemove(t, a, dir, "y\n", "--no-fetch", "--accept-loss", p); exitOf(err) != 0 || exists(p) {
		t.Errorf("answer y: exit %d (%v), there=%v, want 0 and gone", exitOf(err), err, exists(p))
	}
}

// TestRemoveCmdDeleteFlagNamesOfferedBranch verifies -D on a worktree whose
// unmerged branch is merely offered: the prompt names that branch before
// asking, and on "y" the branch is deleted with the worktree; without -D the
// worktree goes and the branch is kept (retained_unmerged, still exit 0).
// The user must see exactly which unmerged branch -D would delete.
func TestRemoveCmdDeleteFlagNamesOfferedBranch(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := unmergedWorktree(t, dir, "kept")
	onTTY(t)
	_, stderr, err := runRemove(t, a, dir, "n\n", "--no-fetch", "--accept-loss", "-D", p)
	if exitOf(err) != 3 || !exists(p) || !hasBranch(t, dir, "kept") {
		t.Fatalf("answer n: exit %d (%v), want 3 and nothing removed", exitOf(err), err)
	}
	if !strings.Contains(stderr, "-D") || !strings.Contains(stderr, "delete") || !strings.Contains(stderr, "branch kept") {
		t.Errorf("prompt does not name the offered branch:\n%s", stderr)
	}
	if _, stderr, err := runRemove(t, a, dir, "y\n", "--no-fetch", "--accept-loss", "-D", p); exitOf(err) != 0 || exists(p) || hasBranch(t, dir, "kept") {
		t.Errorf("answer y: exit %d (%v), worktree there=%v branch there=%v, want 0 and both gone\n%s", exitOf(err), err, exists(p), hasBranch(t, dir, "kept"), stderr)
	}

	// Without -D the branch stays.
	q := unmergedWorktree(t, dir, "keep2")
	githubOrigin(t, dir)
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--accept-loss", "--yes", "--json", q)
	env := decodeOne(t, stdout)
	if exitOf(err) != 0 || exists(q) || !hasBranch(t, dir, "keep2") || env.Items[0].Branch != "retained_unmerged" {
		t.Errorf("without -D: exit %d, worktree there=%v branch there=%v items %+v", exitOf(err), exists(q), hasBranch(t, dir, "keep2"), env.Items)
	}
}

// TestRemoveCmdForceDoesNotImplyDelete verifies --force never deletes an
// unmerged branch: a dirty worktree on one is removed with --force and its
// branch kept, and a branch-only unmerged item is still refused "needs -D".
// The two flags each mean one thing (plan call 2).
func TestRemoveCmdForceDoesNotImplyDelete(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := unmergedWorktree(t, dir, "kept")
	if err := os.WriteFile(filepath.Join(p, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	uniqueBranch(t, dir, "rescue")
	githubOrigin(t, dir)
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--force", "--accept-loss", "--json", p, "rescue")
	if exitOf(err) != 2 {
		t.Fatalf("exit %d (%v), want 2\n%s", exitOf(err), err, stdout)
	}
	env := decodeOne(t, stdout)
	if len(env.Items) != 2 || env.Items[0].Worktree != "removed" || env.Items[0].Branch != "retained_unmerged" ||
		env.Items[1].Worktree != "refused" || !strings.Contains(env.Items[1].Reason, "needs -D") {
		t.Errorf("items = %+v", env.Items)
	}
	if exists(p) || !hasBranch(t, dir, "kept") || !hasBranch(t, dir, "rescue") {
		t.Errorf("worktree there=%v kept=%v rescue=%v, want gone / kept / kept", exists(p), hasBranch(t, dir, "kept"), hasBranch(t, dir, "rescue"))
	}
}

// TestRemoveCmdNeedsAcceptLoss verifies an item that loses something no
// flag-scoped approval covers (an unmerged, pushed worktree) is refused
// "needs --accept-loss" with --yes alone. Ruling B: --yes approves proven
// rows only.
func TestRemoveCmdNeedsAcceptLoss(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := unmergedWorktree(t, dir, "kept")
	stdout, _, err := runRemove(t, a, dir, "", "--no-fetch", "--yes", "--json", p)
	env := decodeOne(t, stdout)
	if exitOf(err) != 2 || !exists(p) || len(env.Items) != 1 || !strings.Contains(env.Items[0].Reason, "needs --accept-loss") {
		t.Errorf("exit %d there=%v items %+v, want 2, kept, refused needing --accept-loss", exitOf(err), exists(p), env.Items)
	}
}

// TestRemoveExitCode verifies the exit-code mapping over outcomes: 0 when
// every item was removed (a worktree's kept unmerged branch included), 2 when
// any item was refused, skipped or failed, or its branch could not be
// settled. Scripts branch on it.
func TestRemoveExitCode(t *testing.T) {
	wt := func(w removal.WorktreeOutcome, b removal.BranchOutcome) removal.Outcome {
		return removal.Outcome{Item: removal.Item{Kind: cleanup.KindWorktree}, Worktree: w, Branch: b}
	}
	br := func(b removal.BranchOutcome) removal.Outcome {
		return removal.Outcome{Item: removal.Item{Kind: cleanup.KindBranch}, Worktree: removal.WtNotRequested, Branch: b}
	}
	for _, c := range []struct {
		name string
		out  []removal.Outcome
		want int
	}{
		{"removed+deleted", []removal.Outcome{wt(removal.WtRemoved, removal.BrDeleted)}, 0},
		{"detached", []removal.Outcome{wt(removal.WtRemoved, removal.BrNotAttempted)}, 0},
		{"kept unmerged", []removal.Outcome{wt(removal.WtRemoved, removal.BrRetainedUnmerged)}, 0},
		{"branch deleted", []removal.Outcome{br(removal.BrDeleted)}, 0},
		{"kept: checked out elsewhere", []removal.Outcome{wt(removal.WtRemoved, removal.BrRetainedCheckedOut)}, 0},
		{"kept: default", []removal.Outcome{wt(removal.WtRemoved, removal.BrRetainedDefault)}, 0},
		{"branch item checked out", []removal.Outcome{br(removal.BrRetainedCheckedOut)}, 2},
		{"branch item default", []removal.Outcome{br(removal.BrRetainedDefault)}, 2},
		{"refused", []removal.Outcome{wt(removal.WtRemoved, removal.BrDeleted), wt(removal.WtRefused, removal.BrNotAttempted)}, 2},
		{"skipped", []removal.Outcome{wt(removal.WtSkippedChanged, removal.BrNotAttempted)}, 2},
		{"failed", []removal.Outcome{wt(removal.WtFailed, removal.BrNotAttempted)}, 2},
		{"branch moved", []removal.Outcome{wt(removal.WtRemoved, removal.BrRetainedMoved)}, 2},
		{"branch unknown", []removal.Outcome{wt(removal.WtRemoved, removal.BrRetainedUnknown)}, 2},
		{"branch item kept", []removal.Outcome{br(removal.BrRetainedUnmerged)}, 2},
		{"branch item refused", []removal.Outcome{{Item: removal.Item{Kind: cleanup.KindBranch}, Worktree: removal.WtRefused, Branch: removal.BrNotAttempted}}, 2},
	} {
		if got := removeExitCode(c.out); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// TestRemoveCmdPartialReportExits2 verifies an envelope that carries both
// items and an error (Ruling O's partial report) is printed whole and exits
// 2, even when every item succeeded: the caller must treat the listed
// changes as made and still learn something is off.
func TestRemoveCmdPartialReportExits2(t *testing.T) {
	var out bytes.Buffer
	o := removal.Outcome{Item: removal.Item{Kind: cleanup.KindWorktree, Name: "x", Saw: "weird"}, Worktree: removal.WtRemoved, Branch: removal.BrDeleted}
	err := writeRemoveResult(&out, io.Discard, true, removal.Meta{Repo: "/r"}, []removal.Outcome{o})
	env := decodeOne(t, out.String())
	if exitOf(err) != 2 || env.Error == nil || len(env.Items) != 1 {
		t.Errorf("exit %d env %+v, want 2 with the partial report", exitOf(err), env)
	}
	var coded *exitCodeError
	if !errors.As(err, &coded) || !coded.quiet {
		t.Errorf("err = %v, want a quiet exit code (the document already said it)", err)
	}
}

// TestRemoveCmdRegisteredOnRoot verifies `wt remove` is a root subcommand.
func TestRemoveCmdRegisteredOnRoot(t *testing.T) {
	newTestApp(t)
	c, _, err := rootCmd().Find([]string{"remove"})
	if err != nil || c == nil || c.Name() != "remove" {
		t.Errorf("Find(remove) = %v, %v", c, err)
	}
}

// TestRemoveJSONUsageErrorsPrintTheEnvelope verifies that `wt remove --json`
// with no target, or with an unknown flag after --json, prints the one error
// document on stdout, nothing on stderr, and exits 1; without --json the
// error is the plain exit-1 error. Why: the help promises the document "on
// failure too", and a script must not have to parse cobra's usage text.
func TestRemoveJSONUsageErrorsPrintTheEnvelope(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	for name, args := range map[string][]string{
		"no target": {"--json"},
		"bad flag":  {"--json", "--bogus", "x"},
	} {
		stdout, stderr, err := runRemove(t, a, dir, "", args...)
		if exitOf(err) != 1 {
			t.Errorf("%s: exit %d, want 1 (%v)", name, exitOf(err), err)
		}
		var doc struct {
			Error *struct{ Message string }
			Items []any
		}
		if jerr := json.Unmarshal([]byte(stdout), &doc); jerr != nil || doc.Error == nil || doc.Error.Message == "" {
			t.Errorf("%s: stdout is not the error envelope: %q (%v)", name, stdout, jerr)
		}
		if stderr != "" {
			t.Errorf("%s: stderr = %q, want none", name, stderr)
		}
	}
	stdout, _, err := runRemove(t, a, dir, "")
	if exitOf(err) != 1 || stdout != "" {
		t.Errorf("no --json, no target: exit %d, stdout %q; want 1 and none", exitOf(err), stdout)
	}
}
