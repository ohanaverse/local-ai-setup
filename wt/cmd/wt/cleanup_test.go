package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tui"
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
	stubCleanupGH(t) // an unknown PR lookup is never proven; tests that want gh down stub it again
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
	// A github.com URL names the own repository, which PR lookups need; insteadOf keeps git on the bare origin.
	cgit(t, dir, "config", "url."+origin+".insteadOf", "https://github.com/o/r.git")
	cgit(t, dir, "remote", "set-url", "origin", "https://github.com/o/r.git")
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
	cmd := cleanupCmd(a)
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
	dir := cleanupRepo(t) // stubs gh ok; the failing stub below replaces it
	t.Cleanup(cleanup.StubPRListForTest(func(string, string) ([]byte, error) {
		return nil, errors.New(speech)
	}))
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

// TestCleanupCmdRegisteredOnRoot verifies `wt cleanup` is a root subcommand.
func TestCleanupCmdRegisteredOnRoot(t *testing.T) {
	newTestApp(t)
	c, _, err := rootCmd().Find([]string{"cleanup"})
	if err != nil || c == nil || c.Name() != "cleanup" {
		t.Errorf("Find(cleanup) = %v, %v", c, err)
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
