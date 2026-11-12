package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

func cgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, b)
	}
	return strings.TrimSpace(string(b))
}

func cleanupCommit(t *testing.T, dir, file, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	cgit(t, dir, "add", file)
	cgit(t, dir, "commit", "-m", msg)
}

// cleanupRepo builds a fresh repository: a bare origin and a clone with main
// pushed and origin/HEAD set. Each test gets its own.
func cleanupRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin.git"), filepath.Join(root, "repo")
	if b, err := exec.Command("git", "init", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, b)
	}
	if b, err := exec.Command("git", "clone", origin, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, b)
	}
	cgit(t, dir, "config", "user.email", "t@example.com")
	cgit(t, dir, "config", "user.name", "T")
	cgit(t, dir, "config", "commit.gpgsign", "false")
	cgit(t, dir, "checkout", "-B", "main")
	cleanupCommit(t, dir, "README", "init")
	cgit(t, dir, "push", "-u", "origin", "main")
	cgit(t, dir, "remote", "set-head", "origin", "main")
	return dir
}

// stubCleanupGH makes every PR lookup answer "no PRs", so gh counts as ok.
func stubCleanupGH(t *testing.T) {
	t.Helper()
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
}

// runCleanup runs `wt cleanup args...` from dir and returns both streams and
// the error (its exit status is exitCodeOf(err)).
func runCleanup(t *testing.T, a *app, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)
	cmd := worktreesCmd(a)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{}, args...))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestCleanupCmdJSONIsOneDocument verifies that --json prints exactly one
// JSON document (the envelope) on stdout and nothing on stderr. A script
// parses stdout whole; a stray header line or a second document breaks it.
func TestCleanupCmdJSONIsOneDocument(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	cgit(t, dir, "branch", "topic")
	stdout, stderr, err := runCleanup(t, a, dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var env cleanup.Envelope
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout holds more than one document:\n%s", stdout)
	}
	if env.Version != cleanup.EnvelopeVersion || env.Error != nil || env.Base == nil || env.Base.Ref != "origin/main" || env.Fetch == nil || !env.Fetch.OK {
		t.Errorf("envelope = %+v", env)
	}
	found := false
	for _, r := range env.Rows {
		found = found || r.Name == "topic"
	}
	if !found {
		t.Errorf("no row for branch topic:\n%s", stdout)
	}
}

// TestCleanupCmdJSONIncludesRootDefaultAndHeldRows runs --json from a root on a
// feature branch: the root row carries hard_stop "primary", the default
// branch appears as a row, and a pushed branch folds its remote ref into an
// upstream field.
func TestCleanupCmdJSONIncludesRootDefaultAndHeldRows(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	cgit(t, dir, "checkout", "-b", "feature")
	cleanupCommit(t, dir, "f", "feature work")
	cgit(t, dir, "push", "-u", "origin", "feature")
	stdout, _, err := runCleanup(t, a, dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var env cleanup.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, stdout)
	}
	var root, def *cleanup.RowJSON
	for i, r := range env.Rows {
		switch {
		case r.Kind == "worktree" && r.HardStop == cleanup.BlockPrimary:
			root = &env.Rows[i]
		case r.Kind == "branch" && r.Name == "main":
			def = &env.Rows[i]
		}
	}
	if root == nil || root.Branch != "feature" {
		t.Errorf("no root row with hard_stop primary on feature:\n%s", stdout)
	}
	if def == nil {
		t.Fatalf("no row for the default branch main:\n%s", stdout)
	}
	if def.Upstream == nil || def.Upstream.Ref != "refs/remotes/origin/main" {
		t.Errorf("main upstream = %+v:\n%s", def.Upstream, stdout)
	}
}

// TestCleanupCmdPlainGroupsInOrder verifies the plain output prints the four
// group headings in the order proven, less sure, unique work, in use, and
// puts each row under its own heading. The order is the contract with the
// list: the user reads top-down from safest to blocked.
func TestCleanupCmdPlainGroupsInOrder(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	// proven: merged with a merge commit, so its tip is an ancestor of main.
	cgit(t, dir, "checkout", "-b", "merged")
	cleanupCommit(t, dir, "m", "merged work")
	cgit(t, dir, "checkout", "main")
	cgit(t, dir, "merge", "--no-ff", "--no-edit", "merged")
	cgit(t, dir, "push", "origin", "main")
	// less sure: pushed, unmerged, no unique-work signal.
	cgit(t, dir, "checkout", "-b", "spike")
	cleanupCommit(t, dir, "s", "spike work")
	cgit(t, dir, "push", "-u", "origin", "spike")
	// unique work: commits on no remote.
	cgit(t, dir, "checkout", "-b", "rescue")
	cleanupCommit(t, dir, "r", "rescue work")
	cgit(t, dir, "checkout", "main")

	// The branches are seconds old and --min-age has a 1m floor: classify
	// an hour from now so "merged" is old enough to be proven.
	old := cleanupClassify
	cleanupClassify = func(s cleanup.Snapshot, o cleanup.Options) cleanup.Report {
		o.Now = o.Now.Add(time.Hour)
		return old(s, o)
	}
	t.Cleanup(func() { cleanupClassify = old })
	stdout, stderr, err := runCleanup(t, a, dir, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q", stderr)
	}
	last := -1
	for _, h := range []string{"PROVEN MERGED", "LOOKS MERGED, LESS SURE", "HAS WORK THAT EXISTS NOWHERE ELSE", "IN USE / LOCKED (cannot remove)"} {
		i := strings.Index(stdout, h)
		if i < 0 || i < last {
			t.Fatalf("heading %q missing or out of order (at %d, previous at %d):\n%s", h, i, last, stdout)
		}
		last = i
	}
	section := func(from, to string) string {
		i := strings.Index(stdout, from)
		j := len(stdout)
		if to != "" {
			j = strings.Index(stdout, to)
		}
		return stdout[i:j]
	}
	for _, c := range []struct{ from, to, name string }{
		{"PROVEN MERGED", "LOOKS MERGED", "merged"},
		{"LOOKS MERGED", "HAS WORK", "spike"},
		{"HAS WORK", "IN USE", "rescue"},
	} {
		if !strings.Contains(section(c.from, c.to), c.name) {
			t.Errorf("row %q is not under %q:\n%s", c.name, c.from, stdout)
		}
	}
}

// TestCleanupCmdExit1WithoutDefaultBranch verifies that a repository with no
// resolvable default branch is exit 1, that --json then prints ONE error
// envelope (error set, no rows) and nothing on stderr, and that the plain
// form renders no rows. Classifying against a guessed base would risk a false
// "safe".
func TestCleanupCmdExit1WithoutDefaultBranch(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	mk := func() string {
		dir := t.TempDir()
		cgit(t, dir, "init", "-b", "trunk")
		cgit(t, dir, "config", "user.email", "t@example.com")
		cgit(t, dir, "config", "user.name", "T")
		cleanupCommit(t, dir, "f", "x")
		cgit(t, dir, "branch", "topic")
		return dir
	}
	stdout, stderr, err := runCleanup(t, a, mk(), "--json", "--no-fetch")
	if err == nil || exitCodeOf(err) != 1 {
		t.Fatalf("err = %v, want exit 1", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty under --json", stderr)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var env cleanup.Envelope
	if err := dec.Decode(&env); err != nil || dec.More() {
		t.Fatalf("stdout is not one envelope (%v):\n%s", err, stdout)
	}
	if env.Error == nil || env.Error.Message == "" || len(env.Rows) != 0 {
		t.Errorf("envelope = %+v, want error set and no rows", env)
	}

	stdout, _, err = runCleanup(t, a, mk(), "--plain", "--no-fetch")
	if err == nil || exitCodeOf(err) != 1 {
		t.Fatalf("plain err = %v, want exit 1", err)
	}
	if stdout != "" {
		t.Errorf("plain stdout = %q, want no rows rendered", stdout)
	}
}

// TestCleanupCmdFetchFailureExits0 verifies that an unreachable origin is
// data, not an exit code: exit 0, the header says the fetch FAILED and the
// JSON has fetch.ok false with the error. The user can still read the list
// and the header warns the base may be stale.
func TestCleanupCmdFetchFailureExits0(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	cgit(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nowhere.git"))

	stdout, _, err := runCleanup(t, a, dir, "--json")
	if err != nil {
		t.Fatalf("err = %v, want exit 0", err)
	}
	var env cleanup.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.Fetch == nil || env.Fetch.OK || env.Fetch.Error == "" {
		t.Errorf("fetch = %+v, want ok=false with an error", env.Fetch)
	}
	plain, _, err := runCleanup(t, a, dir, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "fetch: FAILED") {
		t.Errorf("header does not say the fetch failed:\n%s", plain)
	}
}

// TestCleanupCmdFetchFailureIsOneLine fetches from an origin that does not
// exist, so git prints "fatal: ... does not appear to be a git repository",
// "fatal: Could not read from remote repository.", a blank line and "Please
// make sure ...". Why: that text went raw into the --plain header, splitting
// the fetch line over several lines (some at column 0), and into a
// multi-line JSON fetch.error; both must be one line.
func TestCleanupCmdFetchFailureIsOneLine(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	cgit(t, dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nowhere.git"))

	stdout, _, err := runCleanup(t, a, dir, "--json")
	if err != nil {
		t.Fatalf("err = %v, want exit 0", err)
	}
	var env cleanup.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	if env.Fetch == nil || env.Fetch.Error == "" || strings.ContainsAny(env.Fetch.Error, "\n\r\t") {
		t.Errorf("fetch.error is not one line: %+v", env.Fetch)
	}
	plain, _, err := runCleanup(t, a, dir, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	var fetchLine string
	for _, line := range strings.Split(plain, "\n") {
		if strings.HasPrefix(line, "fetch:") {
			fetchLine = line
		}
		if strings.HasPrefix(line, "Please make sure") || strings.HasPrefix(line, "fatal:") {
			t.Errorf("git's text broke onto its own line %q:\n%s", line, plain)
		}
	}
	if !strings.Contains(fetchLine, "FAILED (fatal:") || !strings.HasSuffix(fetchLine, "merges since the last fetch may be missed") {
		t.Errorf("fetch line is not whole on one line: %q", fetchLine)
	}
}

// TestCleanupCmdGhFailureKeepsRowsOneLine makes every gh call fail with an
// unauthenticated gh's multi-line message, in a repository whose origin
// names a GitHub repo (so gh is asked), and prints --plain and --json. Why:
// the message was pasted into every row, so --plain grew lines at column 0
// ("Alternatively, populate the GH_TOKEN...") under each row and the JSON
// detail held newlines; the header already says gh is unavailable.
func TestCleanupCmdGhFailureKeepsRowsOneLine(t *testing.T) {
	a, _ := newTestApp(t)
	speech := "To get started with GitHub CLI, please run:  gh auth login\n" +
		"Alternatively, populate the GH_TOKEN environment variable with a GitHub API authentication token.\n"
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) {
		return nil, errors.New(speech)
	}))
	dir := cleanupRepo(t)
	cgit(t, dir, "checkout", "-b", "topic")
	cleanupCommit(t, dir, "t", "topic work")
	cgit(t, dir, "push", "-u", "origin", "topic")
	cgit(t, dir, "checkout", "main")
	// A GitHub origin URL so the PR lookup runs; --no-fetch keeps it offline.
	cgit(t, dir, "remote", "set-url", "origin", "https://github.com/o/r.git")

	plain, _, err := runCleanup(t, a, dir, "--plain", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "gh:    unavailable") || !strings.Contains(plain, "PR lookup failed") {
		t.Fatalf("plain output lacks the gh header or the short row detail:\n%s", plain)
	}
	headings := []string{"PROVEN MERGED", "LOOKS MERGED, LESS SURE", "HAS WORK THAT EXISTS NOWHERE ELSE", "IN USE / LOCKED (cannot remove)"}
	for _, line := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		switch {
		case line == "", strings.HasPrefix(line, "  "), slices.Contains(headings, line):
		case strings.HasPrefix(line, "repo:"), strings.HasPrefix(line, "base:"), strings.HasPrefix(line, "fetch:"), strings.HasPrefix(line, "gh:"):
		default:
			t.Errorf("line at column 0 that is no header or heading: %q\n%s", line, plain)
		}
	}
	if strings.Contains(plain, "Alternatively") {
		t.Errorf("gh's login speech reached the rows:\n%s", plain)
	}
	// The header's gh line is where the cause shows, on that one line.
	var ghLine string
	for _, line := range strings.Split(plain, "\n") {
		if strings.HasPrefix(line, "gh:") {
			ghLine = line
		}
	}
	if !strings.Contains(ghLine, "unavailable (") || !strings.Contains(ghLine, "gh auth login") {
		t.Errorf("gh line does not name the cause: %q", ghLine)
	}

	stdout, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	var env cleanup.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, r := range env.Rows {
		for _, re := range r.Reasons {
			if re.Code == cleanup.ReasonPRUnknown || re.Code == cleanup.ReasonCannotVerify {
				seen = true
			}
			if strings.ContainsAny(re.Detail, "\r\n") {
				t.Errorf("row %s reason %s detail is not one line: %q", r.Name, re.Code, re.Detail)
			}
		}
	}
	if !seen {
		t.Errorf("no row carries a PR lookup reason:\n%s", stdout)
	}
}

// TestCleanupCmdNoFetchOmitsFetch verifies --no-fetch runs no fetch: the JSON
// has no "fetch" key (not attempted is different from failed) and the plain
// header says skipped.
func TestCleanupCmdNoFetchOmitsFetch(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	stdout, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["fetch"]; ok {
		t.Errorf("--no-fetch envelope has a fetch key:\n%s", stdout)
	}
	plain, _, _ := runCleanup(t, a, dir, "--plain", "--no-fetch")
	if !strings.Contains(plain, "fetch: skipped") {
		t.Errorf("header does not say skipped:\n%s", plain)
	}
}

// TestCleanupCmdMinAge verifies --min-age: zero, negative and unparseable
// values are refused (a zero threshold would let a brand-new branch look
// merged), and a valid duration is what Classify is given.
func TestCleanupCmdMinAge(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	for _, bad := range []string{"0", "0s", "-5m", "soon"} {
		stdout, _, err := runCleanup(t, a, dir, "--json", "--no-fetch", "--min-age", bad)
		if err == nil || !strings.Contains(err.Error(), "--min-age") {
			t.Errorf("--min-age %q: err = %v, want a --min-age usage error", bad, err)
		}
		if stdout != "" {
			t.Errorf("--min-age %q printed %q", bad, stdout)
		}
	}
	var got cleanup.Options
	old := cleanupClassify
	cleanupClassify = func(s cleanup.Snapshot, o cleanup.Options) cleanup.Report { got = o; return old(s, o) }
	t.Cleanup(func() { cleanupClassify = old })
	if _, _, err := runCleanup(t, a, dir, "--json", "--no-fetch", "--min-age", "2h"); err != nil {
		t.Fatal(err)
	}
	if got.MinAge != 2*time.Hour || got.Now.IsZero() {
		t.Errorf("Options = %+v, want MinAge 2h and Now set", got)
	}
	got = cleanup.Options{}
	if _, _, err := runCleanup(t, a, dir, "--json", "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	if got.MinAge != 0 {
		t.Errorf("default MinAge = %v, want 0 (Classify applies its default)", got.MinAge)
	}
}

// TestCleanupCmdProtectedArtifactIsInUse verifies a registered local_path
// under a worktree reaches the classifier: that worktree row is in_use with
// the protected_artifact blocker. wt never deletes a user-produced model
// artifact, so cleanup must never offer its worktree. The registry is read
// through a.cfg and never written.
func TestCleanupCmdProtectedArtifactIsInUse(t *testing.T) {
	a, tmp := newTestApp(t)
	t.Setenv("WT_REGISTRY", filepath.Join(tmp, "local-ai", "registry.toml"))
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(tmp, "config.yaml"))
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	wtDir := filepath.Join(filepath.Dir(dir), "wt-topic")
	cgit(t, dir, "worktree", "add", "-b", "topic", wtDir)
	art := filepath.Join(wtDir, "models", "q4")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	a.cfg.Models = []config.Model{{ID: "omlx/q4", ProviderID: "omlx", ModelName: "q4", Fetch: config.ModelArtifact{LocalPath: art}}}

	stdout, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	var env cleanup.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	for _, r := range env.Rows {
		if r.Kind == "worktree" && r.Branch == "topic" {
			if r.Status != "in_use" {
				t.Fatalf("status = %q, want in_use", r.Status)
			}
			for _, b := range r.Blockers {
				if b.Code == cleanup.BlockProtectedArtifact {
					return
				}
			}
			t.Fatalf("no protected_artifact blocker: %+v", r.Blockers)
		}
	}
	t.Fatalf("no worktree row for topic:\n%s", stdout)
}

// TestCleanupCmdTTYHandsOffToList verifies that on a terminal without --json
// or --plain the report goes to runCleanupList (Task 9's list) instead of the
// plain printer, and that --plain and --json each keep the plain path.
func TestCleanupCmdTTYHandsOffToList(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldTTY, oldList := isTerminalWriter, runCleanupList
	isTerminalWriter = func(io.Writer) bool { return true }
	calls := 0
	runCleanupList = func(r cleanup.Report, _ themes.Theme, _ tui.RemoveFuncs) error { calls++; return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList = oldTTY, oldList })

	if out, _, err := runCleanup(t, a, dir, "--no-fetch"); err != nil || calls != 1 || out != "" {
		t.Errorf("tty: err=%v calls=%d out=%q, want the list called once", err, calls, out)
	}
	for _, flag := range []string{"--plain", "--json"} {
		if _, _, err := runCleanup(t, a, dir, "--no-fetch", flag); err != nil || calls != 1 {
			t.Errorf("%s: err=%v calls=%d, want the list not called", flag, err, calls)
		}
	}
}

// TestCleanupListGetsTheRemovalEngine verifies the tick list is handed
// RemoveFuncs over the real engine for this repository: Preview previews the
// rows it is given, an Item confirmed from that preview is removed by Run,
// and Refresh re-reads the repository without the removed worktree. A wiring
// mistake (no engine, the wrong directory, a stale snapshot) would make the
// list's enter do nothing or act on the wrong repository.
func TestCleanupListGetsTheRemovalEngine(t *testing.T) {
	a, _ := removeApp(t)
	dir := cleanupRepo(t)
	p := mergedWorktree(t, dir, "topic")
	oldTTY, oldList := isTerminalWriter, runCleanupList
	isTerminalWriter = func(io.Writer) bool { return true }
	var got cleanup.Report
	var rm tui.RemoveFuncs
	runCleanupList = func(r cleanup.Report, _ themes.Theme, f tui.RemoveFuncs) error { got, rm = r, f; return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList = oldTTY, oldList })
	if _, _, err := runCleanup(t, a, dir, "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	if rm.Off != "" || rm.Preview == nil || rm.Run == nil || rm.Refresh == nil {
		t.Fatalf("RemoveFuncs not wired: %+v", rm)
	}
	var row cleanup.Row
	for _, r := range got.Rows {
		if r.Kind == cleanup.KindWorktree && r.Name == "topic" {
			row = r
		}
	}
	ps := rm.Preview([]cleanup.Row{row})
	if len(ps) != 1 || ps[0].Approval == removal.ApprovalRefused {
		t.Fatalf("preview: %+v", ps)
	}
	it, err := ps[0].Confirmed(ps[0].Needs)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	outs := rm.Run([]removal.Item{it}, func(removal.Outcome) { calls++ })
	if len(outs) != 1 || outs[0].Worktree != removal.WtRemoved || calls != 1 || exists(p) {
		t.Fatalf("run: %+v progress calls %d, worktree there %v", outs, calls, exists(p))
	}
	fresh, err := rm.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fresh.Rows {
		if r.Kind == cleanup.KindWorktree && r.Name == "topic" {
			t.Errorf("refreshed report still lists the removed worktree")
		}
	}
}

// TestCleanupListRemovalOffWithoutRegistry verifies that a registry that did
// not load turns the list's removal off (plan call 7): the RemoveFuncs say
// why and hold no engine, so enter can preview or remove nothing.
func TestCleanupListRemovalOffWithoutRegistry(t *testing.T) {
	rm := cleanupRemoveFuncs(nil, cleanup.Snapshot{}, cleanup.GatherInputs{}, 0, errRegistryUnavailable)
	if !strings.Contains(rm.Off, "the registry failed to load") || rm.Preview != nil || rm.Run != nil || rm.Refresh != nil {
		t.Errorf("RemoveFuncs = %+v, want removal off naming the registry", rm)
	}
}

// TestWorktreesCmdRegisteredWithCleanupAlias verifies `wt worktrees` is the
// root subcommand and `wt cleanup` the same command object under an alias.
func TestWorktreesCmdRegisteredWithCleanupAlias(t *testing.T) {
	newTestApp(t)
	root := rootCmd()
	w, _, err := root.Find([]string{"worktrees"})
	if err != nil || w == nil || w.Name() != "worktrees" {
		t.Fatalf("Find(worktrees) = %v, %v", w, err)
	}
	c, _, err := root.Find([]string{"cleanup"})
	if err != nil || c != w {
		t.Errorf("Find(cleanup) = %v, %v; want the worktrees command", c, err)
	}
	if !slices.Contains(w.Aliases, "cleanup") {
		t.Errorf("Aliases = %v, want cleanup", w.Aliases)
	}
}

// TestWorktreesCmdTTYHandsOffToManageList is the interactive hand-off: one
// call with the report on a terminal; --json and --plain never call it.
func TestWorktreesCmdTTYHandsOffToManageList(t *testing.T) {
	newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldTTY, oldList := isTerminalWriter, runCleanupList
	isTerminalWriter = func(io.Writer) bool { return true }
	calls := 0
	runCleanupList = func(cleanup.Report, themes.Theme, tui.RemoveFuncs) error { calls++; return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList = oldTTY, oldList })
	for _, name := range []string{"worktrees", "cleanup"} {
		calls = 0
		root := rootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{name, "--no-fetch"})
		t.Chdir(dir)
		if err := root.Execute(); err != nil || calls != 1 || out.Len() != 0 {
			t.Errorf("%s on a terminal: err=%v calls=%d out=%q, want the list handed off once and no output", name, err, calls, out.String())
		}
	}
}

// TestWorktreesCmdTTYDoesNotFetchOnOpen verifies the interactive path gathers
// without fetching and with every remote's branches, while --json fetches.
func TestWorktreesCmdTTYDoesNotFetchOnOpen(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldTTY, oldList, oldGather := isTerminalWriter, runCleanupList, cleanupGather
	var ins []cleanup.GatherInputs
	cleanupGather = func(g cleanup.Git, in cleanup.GatherInputs) (cleanup.Snapshot, error) {
		ins = append(ins, in)
		return oldGather(g, in)
	}
	runCleanupList = func(cleanup.Report, themes.Theme, tui.RemoveFuncs) error { return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList, cleanupGather = oldTTY, oldList, oldGather })

	isTerminalWriter = func(io.Writer) bool { return true }
	if _, _, err := runCleanup(t, a, dir); err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || ins[0].Fetch || !ins[0].AllRemotes {
		t.Errorf("tty gather = %+v, want one gather with Fetch false and AllRemotes", ins)
	}
	ins = nil
	if _, _, err := runCleanup(t, a, dir, "--json"); err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || !ins[0].Fetch || ins[0].AllRemotes {
		t.Errorf("--json gather = %+v, want Fetch true and no AllRemotes", ins)
	}
}

// TestWorktreesCmdJSONSameUnderBothNames verifies both names print the same
// bytes and accept the same flags.
func TestWorktreesCmdJSONSameUnderBothNames(t *testing.T) {
	newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	// No extra branch: a fresh branch's reason text carries its age in seconds,
	// which the comparison below normalizes.
	t.Chdir(dir)
	run := func(name string) string {
		root := rootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&bytes.Buffer{})
		root.SetArgs([]string{name, "--json", "--no-fetch", "--min-age", "20m"})
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out.String()
	}
	// A row's reason names its age in seconds, which can tick between the runs.
	age := regexp.MustCompile(`only \d+[smh]\d* old`)
	norm := func(s string) string { return age.ReplaceAllString(s, "only N old") }
	w, c := norm(run("worktrees")), norm(run("cleanup"))
	if w == "" || w != c {
		t.Errorf("worktrees and cleanup --json differ:\n%s\n---\n%s", w, c)
	}
}

// TestCleanupCmdContainsNoDeletion scans the non-test sources of the cleanup
// package and of this command for any deletion call. The classifier is
// read-only by design: removal lives in internal/removal alone (held module-
// wide by TestDeletionStringsOnlyInRemovalPackage), and must not slip in here
// unreviewed. Besides worktree and branch deletion it bans the two ways to
// delete a remote branch: a push (refspec ":<branch>") and `gh api -X DELETE`.
func TestCleanupCmdContainsNoDeletion(t *testing.T) {
	files, _ := filepath.Glob("../../internal/cleanup/*.go")
	more, _ := filepath.Glob("cleanup*.go")
	files = append(files, more...)
	banned := []string{"worktree remove", `"worktree", "remove"`, `"worktree", "prune"`, "update-ref", `branch", "-d`, `branch", "-D`, "--delete", "os.RemoveAll", "os.Remove(",
		`":"+`, `":" +`, `"push"`, "-X DELETE", `"-X", "DELETE"`, `"--method", "DELETE"`, "-XDELETE"}
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// The one exemption: in held.go only, the sequencer state file
		// HeldRefs reads (a read-only path whose name contains "update-ref"),
		// exact spelling. Every "update-ref" there must be that spelling.
		src := string(b)
		if filepath.Base(f) == "held.go" && strings.Contains(filepath.ToSlash(f), "internal/cleanup/") {
			const stateFile = "rebase-merge/update-refs"
			if n, m := strings.Count(src, "update-ref"), strings.Count(src, stateFile); n != m {
				t.Errorf("%s: %d \"update-ref\" but only %d %q", f, n, m, stateFile)
			}
			src = strings.ReplaceAll(src, stateFile, "")
		}
		for _, s := range banned {
			if strings.Contains(src, s) {
				t.Errorf("%s contains %q", f, s)
			}
		}
	}
	if scanned < 5 {
		t.Errorf("scanned only %d sources", scanned)
	}
}

// TestWorktreesCmdTTYMarksFetchNotAttempted: the terminal list does not fetch
// on open and says so (FetchResult.NotAttempted), so origin rows do not
// blame a --no-fetch the user never passed; with --no-fetch it stays the
// flag's plain skip.
func TestWorktreesCmdTTYMarksFetchNotAttempted(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldTTY, oldList := isTerminalWriter, runCleanupList
	var got []cleanup.FetchResult
	runCleanupList = func(r cleanup.Report, _ themes.Theme, _ tui.RemoveFuncs) error {
		got = append(got, r.Fetch)
		return nil
	}
	t.Cleanup(func() { isTerminalWriter, runCleanupList = oldTTY, oldList })
	isTerminalWriter = func(io.Writer) bool { return true }
	if _, _, err := runCleanup(t, a, dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCleanup(t, a, dir, "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].NotAttempted || got[1].NotAttempted {
		t.Errorf("fetch results %+v, want NotAttempted without --no-fetch only", got)
	}
}

// TestCleanupCmdTerminalDoesNotFetchOnOpen: pin — the terminal list gathers
// with no fetch at all (a fetch is the user's `--fetch` now, no automatic
// behaviors), marks the fetch "not run" rather than blaming a --no-fetch the
// user never passed, and leaves the repository's fetch stamp untouched.
// Already true at the base commit (the list stopped fetching on open before
// this task); written as a pin so `--fetch` cannot regress it — no RED.
func TestCleanupCmdTerminalDoesNotFetchOnOpen(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	before, hadBefore := worktree.LastFetched(dir)
	oldTTY, oldList, oldGather := isTerminalWriter, runCleanupList, cleanupGather
	var ins []cleanup.GatherInputs
	cleanupGather = func(g cleanup.Git, in cleanup.GatherInputs) (cleanup.Snapshot, error) {
		ins = append(ins, in)
		return oldGather(g, in)
	}
	runCleanupList = func(cleanup.Report, themes.Theme, tui.RemoveFuncs) error { return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList, cleanupGather = oldTTY, oldList, oldGather })
	isTerminalWriter = func(io.Writer) bool { return true }

	if _, _, err := runCleanup(t, a, dir); err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || ins[0].Fetch || !ins[0].Fetched.NotAttempted {
		t.Errorf("open gather = %+v, want one gather with Fetch false and the fetch marked not attempted", ins)
	}
	ins = nil
	if _, _, err := runCleanup(t, a, dir, "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	if len(ins) != 1 || ins[0].Fetch || ins[0].Fetched.NotAttempted || !ins[0].Fetched.At.IsZero() {
		t.Errorf("--no-fetch gather = %+v, want the flag's plain skip (Fetch false, no NotAttempted marker)", ins)
	}
	after, hadAfter := worktree.LastFetched(dir)
	if hadAfter != hadBefore || (hadAfter && !after.Equal(before)) {
		t.Errorf("opening the list moved the fetch stamp (%v/%v -> %v/%v); it fetches nothing", hadBefore, before, hadAfter, after)
	}
}

// TestCleanupCmdTerminalFetchFlagFetches: `--fetch` is the terminal list's
// explicit fetch — every remote is fetched once before the list opens (the
// gather itself never fetches, so the run fetches exactly once), the fetched
// refs reach the report, and the header's fetch note reads the result (the
// wording contract the picker's F established). It fetches at the report's
// ROOT — the primary worktree — even when the command runs from a linked
// worktree: the root's fetch stamp (worktree.LastFetched there, what feeds
// the header's refreshed line) advances and the cwd worktree's own stamp does
// not. FETCH_HEAD stamps are per-worktree, so a fetch at cwd would move the
// wrong stamp, leave the refreshed line unmoved and fetch at the wrong place
// altogether — this runs from a linked worktree so the two are told apart.
func TestCleanupCmdTerminalFetchFlagFetches(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	// "late" and "uplate" exist only on the remotes: a second clone puts
	// them there, so only a fetch can bring their remote-tracking refs into
	// dir. Two remotes prove every remote is fetched, not just origin.
	other := filepath.Join(t.TempDir(), "other")
	if b, err := exec.Command("git", "clone", "-q", filepath.Join(filepath.Dir(dir), "origin.git"), other).CombinedOutput(); err != nil {
		t.Fatalf("clone other: %v\n%s", err, b)
	}
	cgit(t, other, "config", "user.email", "t@example.com")
	cgit(t, other, "config", "user.name", "T")
	cgit(t, other, "push", "origin", "HEAD:refs/heads/late")
	up := filepath.Join(t.TempDir(), "up.git")
	if b, err := exec.Command("git", "init", "--bare", "-b", "main", up).CombinedOutput(); err != nil {
		t.Fatalf("init up: %v\n%s", err, b)
	}
	cgit(t, other, "push", up, "HEAD:refs/heads/uplate")
	cgit(t, dir, "remote", "add", "up", up)
	// The command runs from a LINKED worktree: cwd is not the report's root
	// (that is the primary), so where the fetch runs shows in which
	// worktree's fetch stamp moves.
	linked := filepath.Join(t.TempDir(), "linked")
	cgit(t, dir, "worktree", "add", "-b", "linked", linked)
	rootBefore, rootHad := worktree.LastFetched(dir)
	cwdBefore, cwdHad := worktree.LastFetched(linked)

	oldTTY, oldList, oldGather := isTerminalWriter, runCleanupList, cleanupGather
	var ins []cleanup.GatherInputs
	cleanupGather = func(g cleanup.Git, in cleanup.GatherInputs) (cleanup.Snapshot, error) {
		ins = append(ins, in)
		return oldGather(g, in)
	}
	var got cleanup.Report
	runCleanupList = func(r cleanup.Report, _ themes.Theme, _ tui.RemoveFuncs) error { got = r; return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList, cleanupGather = oldTTY, oldList, oldGather })
	isTerminalWriter = func(io.Writer) bool { return true }

	if _, _, err := runCleanup(t, a, linked, "--fetch"); err != nil {
		t.Fatal(err)
	}
	// The report is the primary's whatever directory the command ran from
	// (compared resolved: macOS spells t.TempDir() /var where git answers
	// /private/var).
	if cleanup.ResolvePath(got.Repo) != cleanup.ResolvePath(dir) {
		t.Fatalf("report root = %q, want the primary worktree %q (run from the linked worktree %q)", got.Repo, dir, linked)
	}
	// The gather itself never fetches (exactly one fetch, the explicit one)
	// and the fetch's outcome rides the snapshot for the header to render.
	if len(ins) != 1 || ins[0].Fetch || !ins[0].Fetched.OK || ins[0].Fetched.NotAttempted {
		t.Errorf("gather = %+v, want one gather with Fetch false and a successful explicit fetch recorded", ins)
	}
	// The fetched refs reached the report that was listed.
	names := map[string]bool{}
	for _, r := range got.Rows {
		names[r.Name] = true
	}
	if !names["late"] || !names["uplate"] {
		t.Errorf("rows lack the fetched branches (late=%v uplate=%v); --fetch must fetch before listing", names["late"], names["uplate"])
	}
	// The header's fetch note reflects the fetch.
	if hdr := strings.Join(cleanup.HeaderLines(got, time.Now()), "\n"); !strings.Contains(hdr, "fetch: ok") {
		t.Errorf("header does not read the fetch:\n%s", hdr)
	}
	// The refreshed line is fed by LastFetched at the report's root: the
	// explicit fetch must advance the ROOT's stamp — and only that one (it
	// runs exactly once, there).
	rootAfter, rootHadAfter := worktree.LastFetched(got.Repo)
	if !rootHadAfter || !rootAfter.After(rootBefore) {
		t.Errorf("the root's fetch stamp did not advance (%v/%v -> %v/%v); the refreshed line would not show the fetch", rootHad, rootBefore, rootHadAfter, rootAfter)
	}
	cwdAfter, cwdHadAfter := worktree.LastFetched(linked)
	if cwdHadAfter != cwdHad || (cwdHadAfter && !cwdAfter.Equal(cwdBefore)) {
		t.Errorf("the cwd worktree's fetch stamp moved (%v/%v -> %v/%v); --fetch fetches at the report's root only", cwdHad, cwdBefore, cwdHadAfter, cwdAfter)
	}
}

// TestCleanupCmdTerminalFetchFailureStillOpensTheList: pin — a failing --fetch
// is a recorded value, never an error. With a remote that cannot be reached,
// `wt worktrees --fetch` still opens the list (exit 0) and the header reads
// "fetch: FAILED (...)" — the wording contract (present.go's fetch note) the
// picker's F and Refetch already render. A script reading the list finds the
// failure in the header, never in the exit status (exit 1 is reserved for a
// repository that could not be classified at all).
func TestCleanupCmdTerminalFetchFailureStillOpensTheList(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	// A remote whose URL cannot be reached: the one `git fetch --all --prune`
	// fails on it (fast — a missing local path, not a network).
	cgit(t, dir, "remote", "add", "dead", filepath.Join(t.TempDir(), "no-such.git"))
	oldTTY, oldList := isTerminalWriter, runCleanupList
	isTerminalWriter = func(io.Writer) bool { return true }
	var got cleanup.Report
	listed := 0
	runCleanupList = func(r cleanup.Report, _ themes.Theme, _ tui.RemoveFuncs) error { got, listed = r, listed+1; return nil }
	t.Cleanup(func() { isTerminalWriter, runCleanupList = oldTTY, oldList })

	if _, _, err := runCleanup(t, a, dir, "--fetch"); err != nil {
		t.Fatalf("a failing fetch must still open the list (exit 0): %v", err)
	}
	if listed != 1 {
		t.Errorf("list calls = %d, want the list opened exactly once", listed)
	}
	if got.Fetch.OK || got.Fetch.Err == "" {
		t.Errorf("report.Fetch = %+v, want the fetch failure recorded", got.Fetch)
	}
	if hdr := strings.Join(cleanup.HeaderLines(got, time.Now()), "\n"); !strings.Contains(hdr, "fetch: FAILED (") {
		t.Errorf("the header does not read the failure:\n%s", hdr)
	}
}

// TestCleanupCmdJSONStillFetchesByDefault: pin — --json's fetch-first (every
// remote, then one JSON document) is its documented default, not the terminal
// list's new opt-in: it runs through the gather's own fetch unless --no-fetch
// opts out. --json --fetch is pinned redundant-but-harmless: it fetches
// exactly as much as --json alone (one gather per run, the outcome recorded
// once, in the report; --fetch adds no fetch and no record of its own).
// Already true at the base; written as a pin so `--fetch` cannot regress it —
// no RED.
func TestCleanupCmdJSONStillFetchesByDefault(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldGather := cleanupGather
	var ins []cleanup.GatherInputs
	cleanupGather = func(g cleanup.Git, in cleanup.GatherInputs) (cleanup.Snapshot, error) {
		ins = append(ins, in)
		return oldGather(g, in)
	}
	t.Cleanup(func() { cleanupGather = oldGather })

	alone, _, err := runCleanup(t, a, dir, "--json")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCleanup(t, a, dir, "--json", "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	redundant, _, err := runCleanup(t, a, dir, "--json", "--fetch")
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 3 || !ins[0].Fetch || ins[1].Fetch || !ins[2].Fetch {
		t.Errorf("gathers = %+v, want the default fetch on, --no-fetch off, --fetch adding nothing", ins)
	}
	if r := ins[2].Fetched; r.OK || r.NotAttempted || r.Err != "" || !r.At.IsZero() {
		t.Errorf("--json --fetch also recorded an explicit fetch: %+v; the json path's outcome lives in the report only", r)
	}
	var envAlone, envFetch cleanup.Envelope
	if err := json.Unmarshal([]byte(alone), &envAlone); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(redundant), &envFetch); err != nil {
		t.Fatal(err)
	}
	if envAlone.Fetch == nil || envFetch.Fetch == nil || envAlone.Fetch.OK != envFetch.Fetch.OK || len(envAlone.Fetch.Remotes) != len(envFetch.Fetch.Remotes) {
		t.Errorf("fetched differently: --json = %+v, --json --fetch = %+v; --fetch must fetch exactly as much and no more", envAlone.Fetch, envFetch.Fetch)
	}
}

// TestCleanupCmdPlainStillFetchesByDefault: pin — --plain fetches first like
// --json (its documented default), and --no-fetch is its opt-out. --plain
// --fetch is pinned redundant-but-harmless exactly like --json --fetch: one
// gather per run, the outcome recorded once in the report's header, and
// --fetch adding no fetch and no record of its own. Already true at the base;
// written as a pin, not RED.
func TestCleanupCmdPlainStillFetchesByDefault(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldGather := cleanupGather
	var ins []cleanup.GatherInputs
	cleanupGather = func(g cleanup.Git, in cleanup.GatherInputs) (cleanup.Snapshot, error) {
		ins = append(ins, in)
		return oldGather(g, in)
	}
	t.Cleanup(func() { cleanupGather = oldGather })

	alone, _, err := runCleanup(t, a, dir, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCleanup(t, a, dir, "--plain", "--no-fetch"); err != nil {
		t.Fatal(err)
	}
	redundant, _, err := runCleanup(t, a, dir, "--plain", "--fetch")
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 3 || !ins[0].Fetch || ins[1].Fetch || !ins[2].Fetch {
		t.Errorf("gathers = %+v, want the default fetch on, --no-fetch off, --fetch adding nothing", ins)
	}
	if r := ins[2].Fetched; r.OK || r.NotAttempted || r.Err != "" || !r.At.IsZero() {
		t.Errorf("--plain --fetch also recorded an explicit fetch: %+v; the plain path's outcome lives in the header only", r)
	}
	for name, out := range map[string]string{"--plain": alone, "--plain --fetch": redundant} {
		if !strings.Contains(out, "fetch: ok") {
			t.Errorf("%s does not read the fetch in its header:\n%s", name, out)
		}
	}
}

// TestCleanupCmdFetchAndNoFetchConflict: --fetch asks for the fetch and
// --no-fetch refuses it, so the pair is a usage error naming both flags — in
// any output mode, before anything runs.
func TestCleanupCmdFetchAndNoFetchConflict(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	oldGather := cleanupGather
	gathers := 0
	cleanupGather = func(cleanup.Git, cleanup.GatherInputs) (cleanup.Snapshot, error) {
		gathers++
		return cleanup.Snapshot{}, errors.New("gather must not run")
	}
	t.Cleanup(func() { cleanupGather = oldGather })

	for _, args := range [][]string{
		{"--fetch", "--no-fetch"},
		{"--json", "--fetch", "--no-fetch"},
		{"--plain", "--no-fetch", "--fetch"},
	} {
		stdout, _, err := runCleanup(t, a, dir, args...)
		if err == nil {
			t.Fatalf("%v: no usage error", args)
		}
		if msg := err.Error(); !strings.Contains(msg, "--fetch") || !strings.Contains(msg, "--no-fetch") {
			t.Errorf("%v: error %q, want it naming both --fetch and --no-fetch", args, msg)
		}
		if stdout != "" || gathers != 0 {
			t.Errorf("%v: ran despite the conflict (out %q, gathers %d)", args, stdout, gathers)
		}
	}
}

// TestCleanupRemoveFuncsRefetchFetches: the manage list's `F` reaches
// RemoveFuncs.Refetch, which fetches once at the report's root — the same
// root the report was built from (a fetch at another worktree stamps that
// one, and the header's refreshed line would not move) — and re-reads, so the
// fresh report's header carries the fetch's result: the note advances from
// "fetch: not run" to "fetch: ok". A failing fetch is that contract's other
// half: a recorded value, never an error — Refetch's error is the re-read's
// own and the header reads "fetch: FAILED (...)".
func TestCleanupRemoveFuncsRefetchFetches(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	var roots []string
	in := cleanupListInputs(dir, false, nil, "wt-cleanup")
	in.Fetched = cleanup.FetchResult{NotAttempted: true} // the list's open state
	// The hook stands in for worktree.FetchRemotes (cleanupRemoveFuncs takes
	// its inputs by value, so it is set once): the first F fetches clean, the
	// second fails for the failure half of the contract below.
	fails := false
	in.FetchRemotes = func(root string) error {
		roots = append(roots, root)
		if fails {
			return errors.New("fetch exploded")
		}
		return nil
	}
	snap, err := cleanupGather(cleanup.Git{Dir: dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	rep := cleanupClassify(snap, cleanup.Options{Now: time.Now()})
	before := strings.Join(cleanup.HeaderLines(rep, time.Now()), "\n")
	rm := cleanupRemoveFuncs(a, snap, in, 0, nil)
	if rm.Refetch == nil {
		t.Fatal("RemoveFuncs.Refetch is nil; the list's F has no fetch")
	}
	fresh, err := rm.Refetch()
	if err != nil {
		t.Fatalf("Refetch: %v", err)
	}
	if len(roots) != 1 || roots[0] != rep.Repo {
		t.Errorf("fetch roots = %v, want exactly one at the report's root %q", roots, rep.Repo)
	}
	if !strings.Contains(before, "fetch: not run") {
		t.Fatalf("the old header does not read as not-run:\n%s", before)
	}
	if after := strings.Join(cleanup.HeaderLines(fresh, time.Now()), "\n"); !strings.Contains(after, "fetch: ok") {
		t.Errorf("the fresh header did not advance past the not-run note:\n%s", after)
	}
	// The failure half of the contract: the fetch's own error must never be
	// Refetch's error (F just re-reads whatever the fetch left), it travels
	// in report.Fetch for the header to render.
	fails = true
	failed, err := rm.Refetch()
	if err != nil {
		t.Fatalf("Refetch returned the fetch's own error %v; it must travel in report.Fetch instead", err)
	}
	if failed.Fetch.OK || failed.Fetch.Err == "" {
		t.Errorf("report.Fetch = %+v, want the fetch failure recorded", failed.Fetch)
	}
	if hdr := strings.Join(cleanup.HeaderLines(failed, time.Now()), "\n"); !strings.Contains(hdr, "fetch: FAILED (") {
		t.Errorf("the header does not read the failure:\n%s", hdr)
	}
	if len(roots) != 2 || roots[1] != rep.Repo {
		t.Errorf("fetch roots = %v, want exactly one more at the report's root %q", roots, rep.Repo)
	}
}

// squashMergedOriginRepo is a repository whose origin (a local bare repo that
// git reaches through url.insteadOf, so wt reads a GitHub-shaped URL and knows
// the own repository) holds a branch "topic" whose one commit the base holds
// as a squashed copy: the content is in the base but the tip is neither the
// base's commit nor its ancestor, so the row reads proven on the trees-match
// rung — which, unlike same-commit and ancestor, the fresh-branch floor does
// not gate. It also answers gh: topic's one PR is merged (#8, head the topic
// tip, merged by the squash commit) and nothing is based on topic.
func squashMergedOriginRepo(t *testing.T) string {
	t.Helper()
	dir := cleanupRepo(t)
	bare := filepath.Join(filepath.Dir(dir), "origin.git")
	cgit(t, dir, "checkout", "-b", "topic")
	cleanupCommit(t, dir, "topic.txt", "topic work")
	tip := cgit(t, dir, "rev-parse", "HEAD")
	cgit(t, dir, "push", "-u", "origin", "topic")
	cgit(t, dir, "checkout", "main")
	// A squashed copy of topic's commit: same content, its own commit, so
	// the tip is not an ancestor and the proof is the tree comparison.
	cgit(t, dir, "cherry-pick", "-n", "topic")
	cgit(t, dir, "commit", "-m", "topic work (squash)")
	squash := cgit(t, dir, "rev-parse", "HEAD")
	cgit(t, dir, "push", "origin", "main")
	cgit(t, dir, "config", "url."+bare+".insteadOf", "https://github.com/o/r.git")
	cgit(t, dir, "remote", "set-url", "origin", "https://github.com/o/r.git")
	merged := `[{"number":8,"state":"MERGED","headRefOid":"` + tip + `","baseRefName":"main","mergeCommit":{"oid":"` + squash + `"},"isCrossRepository":false,"headRepository":{"name":"r","nameWithOwner":"o/r"}}]`
	t.Cleanup(cleanup.StubPRListForTest(func(_, b string) ([]byte, error) {
		if b == "topic" {
			return []byte(merged), nil
		}
		return []byte("[]"), nil
	}))
	t.Cleanup(cleanup.StubPRBaseListForTest(func(string, string) ([]byte, error) { return []byte("[]"), nil }))
	return dir
}

// TestCleanupRemoveFuncsRefetchRepointsTheRemovalEngine: the list's `F` must
// leave the removal engine standing on the snapshot it just fetched for. The
// engine's re-check before any removal (removal.Engine's Rescan) reuses the
// fetch state of the snapshot it was built with, so a Refetch that re-reads
// but does not repoint the engine makes remoteGate refuse the very origin row
// the fetch just proved ("origin was not refreshed in this list"). What: a
// squash-merged origin row is proven once the fetch is recorded (it is not
// proven in the list that opens without one), and Preview right after Refetch
// must offer it (needs_remote), not refuse it. Why: F is the user's fetch; a
// removal straight after must see it, or F proves rows the tick list cannot
// act on and the fetch looks useless.
func TestCleanupRemoveFuncsRefetchRepointsTheRemovalEngine(t *testing.T) {
	a, _ := removeApp(t)
	dir := squashMergedOriginRepo(t)
	in := cleanupListInputs(dir, false, nil, "wt-cleanup")
	in.Fetched = cleanup.FetchResult{NotAttempted: true} // the list's open state
	in.FetchRemotes = func(string) error { return nil }  // the fetch itself is not under test here
	snap, err := cleanupGather(cleanup.Git{Dir: dir}, in)
	if err != nil {
		t.Fatal(err)
	}
	rm := cleanupRemoveFuncs(a, snap, in, 0, nil)
	fresh, err := rm.Refetch()
	if err != nil {
		t.Fatalf("Refetch: %v", err)
	}
	var row cleanup.Row
	found := false
	for _, r := range fresh.AllRows() {
		if r.Kind == cleanup.KindRemote && r.Name == "topic" {
			row, found = r, true
		}
	}
	if !found {
		t.Fatalf("the fresh report holds no origin/topic row: %+v", fresh.AllRows())
	}
	if row.Status != cleanup.StatusProven {
		t.Fatalf("F did not prove origin/topic (status %q, reasons %+v); the re-check below would prove nothing", row.Status, row.Reasons)
	}
	ps := rm.Preview([]cleanup.Row{row})
	if len(ps) != 1 {
		t.Fatalf("preview: %+v", ps)
	}
	// The re-check right before a removal must read the fetch F recorded: a
	// proven, loss-free origin row passes the gate and asks for --remote.
	if ps[0].Approval != removal.ApprovalNeedsRemote {
		t.Errorf("preview after F = %+v, want needs_remote (the re-check must see the fresh snapshot F left)", ps)
	}
}
