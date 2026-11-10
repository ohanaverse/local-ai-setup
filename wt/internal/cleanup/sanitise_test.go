package cleanup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hostile is gh stderr carrying a terminal title escape (ESC ] ... BEL), a
// right-to-left override, a carriage return and a tab.
const hostile = "gh pr list: exit status 1: \x1b]0;x\x07evil ‮txt.exe\rmore\there"

// assertClean fails when s holds a control rune, a bidi control or a line
// break, i.e. anything that could drive the terminal or split the line.
func assertClean(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"\x1b", "\x07", "‮", "\r", "\t", "\n"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s holds %q: %q", what, bad, s)
		}
	}
}

// TestGHErrorTextIsSanitisedAtTheSource feeds a gh failure whose text holds
// ESC/BEL, a bidi override, CR and a tab through the real lookup, then shows
// it on the unavailable header, as a partial-state row detail and in the
// JSON. Why: gh's stderr reached the terminal raw through the header, so a
// hostile or garbled message could retitle the window or reorder the line.
func TestGHErrorTextIsSanitisedAtTheSource(t *testing.T) {
	t.Cleanup(StubPRListForTest(func(string, string) ([]byte, error) {
		return nil, errors.New(hostile)
	}))
	res := LookupPRs("", []string{"topic"}, "o/r", 1, GatherInputs{})["topic"]
	assertClean(t, "PRResult.Err", res.Err)
	if !strings.Contains(res.Err, "evil") || !strings.Contains(res.Err, "more here") {
		t.Errorf("sanitising lost the words: %q", res.Err)
	}

	r := ghDownReport(res.Err)
	assertClean(t, "gh line", ghLine(t, r))
	r.GH = GHPartial
	assertClean(t, "row text", RowText(r, r.Rows[0]))

	b, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, `\u001b`) || strings.Contains(s, `\u0007`) || strings.Contains(s, "‮") {
		t.Errorf("JSON detail keeps control text:\n%s", s)
	}
}

// TestHeaderGHCauseIsSanitisedAgain pins that the gh line sanitises a cause
// itself (a Detail not built by oneLine), and that a cause of only controls
// falls back to the old fixed line. Why: defence in depth for the one line
// that reaches the terminal from every gh failure.
func TestHeaderGHCauseIsSanitisedAgain(t *testing.T) {
	got := ghLine(t, ghDownReport("bad\x1b[31m⁦red\r\nnext"))
	assertClean(t, "gh line", got)
	if !strings.Contains(got, "bad[31mred next") {
		t.Errorf("gh line = %q", got)
	}
	const old = "gh:    unavailable; PR evidence is missing and rows that need it say so"
	if got := ghLine(t, ghDownReport("\x1b\x07‮")); got != old {
		t.Errorf("control-only cause: %q, want the old line", got)
	}
}

// TestFetchFailureIsOneSanitisedLine fetches from an origin that does not
// exist, so git prints several lines ("fatal: ... does not appear to be a
// git repository", "fatal: Could not read from remote repository.", a blank
// line, "Please make sure ..."). Why: that text went raw into the header's
// "fetch: FAILED (...)" and JSON fetch.error, splitting the header.
func TestFetchFailureIsOneSanitisedLine(t *testing.T) {
	f := newFixture(t)
	f.git("remote", "set-url", "origin", filepath.Join(t.TempDir(), "nowhere.git"))
	r := Fetch(Git{Dir: f.Dir})
	if r.OK || r.Err == "" {
		t.Fatalf("got %+v, want a failure", r)
	}
	assertClean(t, "fetch error", r.Err)
	// Only the first line's start is asserted: the temp path in it varies
	// in length, so the cap may cut anything after it.
	if !strings.HasPrefix(r.Err, "fatal:") {
		t.Errorf("fetch error does not start with git's first line: %q", r.Err)
	}
	if n := len([]rune(r.Err)); n > fetchErrMax {
		t.Errorf("fetch error is %d runes, want <= %d: %q", n, fetchErrMax, r.Err)
	}
}

// hostileGitDetail is git stderr as it reaches a row detail: a terminal
// title escape (ESC ] ... BEL), a right-to-left override, a carriage return
// and a second line.
const hostileGitDetail = "cleanup: git status: exit 128: fatal: x\x1b]0;pwn\x07 ‮evil\rmore\nline2"

// hostileDetailReport is a hand-built report with one row per reason whose
// detail is built from git's stderr or an error's text.
func hostileDetailReport() Report {
	r := Report{Base: Base{Ref: "origin/main"}, GH: GHOK}
	for _, code := range []string{ReasonUnpushedUnknown, ReasonGatesUnavailable, ReasonClaimsUnavailable} {
		r.Rows = append(r.Rows, Row{Kind: KindBranch, Name: "topic", Status: StatusLessSure, Unknown: true,
			Reasons: []Reason{{Code: code, Detail: hostileGitDetail}}})
	}
	return r
}

// TestRowTextSanitisesToolDetails pins that ReasonText and RowText drop
// ESC/BEL, bidi controls, CR and LF from the unpushed, gates and claims
// unavailable details, keeping the words on one line. Why: those details
// carry git's stderr, and RowText is what --plain and the TUI print, so a
// hostile or garbled message could retitle the terminal or split the row.
func TestRowTextSanitisesToolDetails(t *testing.T) {
	r := hostileDetailReport()
	for _, row := range r.Rows {
		got := RowText(r, row)
		assertClean(t, "RowText "+row.Reasons[0].Code, got)
		if !strings.Contains(got, "fatal: x]0;pwn evil more line2") {
			t.Errorf("RowText %s lost the words: %q", row.Reasons[0].Code, got)
		}
		assertClean(t, "ReasonText", ReasonText(row.Reasons[0].Code, row.Reasons[0].Detail))
	}
}

// TestGitFailureIsOneSanitisedLine pins that a failed git command's error is
// one printable line that keeps the command and exit code and is capped.
// Why: it becomes a row detail (unpushed count, gates) and reaches the
// terminal; git's stderr is several lines and may hold anything.
func TestGitFailureIsOneSanitisedLine(t *testing.T) {
	o := Out{Code: 128, Stderr: "fatal: x\x1b]0;pwn\x07\n‮evil\r\n" + strings.Repeat("word ", 100)}
	got := gitFailure(o, "status", "--porcelain").Error()
	assertClean(t, "gitFailure", got)
	if !strings.HasPrefix(got, "cleanup: git status --porcelain: exit 128: fatal: x]0;pwn evil word") {
		t.Errorf("gitFailure = %q", got)
	}
	if n := len([]rune(stderrPart(t, got, "128"))); n > gitErrMax {
		t.Errorf("gitFailure's stderr part is %d runes, want <= %d", n, gitErrMax)
	}
}

// TestGatesFailureDetailIsOneSanitisedLine corrupts a worktree's index so
// git status there fails with several lines of stderr ("error: bad
// signature ..." then "fatal: index file corrupt"), then gathers and
// classifies for real. Why: that stderr went whole into the row's
// gates_unavailable detail and from there to the terminal. git never
// echoes the index bytes, so the index holds plain bytes: this test covers
// the multi-line shape only (TestGitFailureIsOneSanitisedLine covers
// control and bidi runes).
func TestGatesFailureDetailIsOneSanitisedLine(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	index := strings.TrimSpace(gitIn(t, p, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	if err := os.WriteFile(index, []byte(strings.Repeat("not an index\n", 8)), 0o644); err != nil {
		t.Fatal(err)
	}
	noGh(t)
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	var detail string
	for _, re := range row.Reasons {
		if re.Code == ReasonGatesUnavailable {
			detail = re.Detail
		}
	}
	if detail == "" {
		t.Fatalf("no %s reason: %v%s", ReasonGatesUnavailable, reasonCodes(row.Reasons), dumpRows(r.Rows))
	}
	assertClean(t, "gates detail", detail)
	// git's first line ("error: bad signature ...") precedes the long
	// index path, so it survives the cap whatever the temp directory is.
	if !strings.Contains(detail, "exit 128: error: bad signature") {
		t.Errorf("gates detail lost git's words: %q", detail)
	}
	if n := len([]rune(stderrPart(t, detail, "128"))); n > gitErrMax {
		t.Errorf("gates detail's stderr part is %d runes, want <= %d", n, gitErrMax)
	}
}

// TestFetchErrTextFlattensAndCaps pins the fetch-error cleaner: controls and
// bidi dropped, every line joined by one space, cut at fetchErrMax runes
// ending in "…", and the header's fetch line one line. Why: git's stderr is
// tool output that can hold anything, and the header shows it whole.
func TestFetchErrTextFlattensAndCaps(t *testing.T) {
	got := fetchErrText("fatal: a\x1b]0;x\x07\n\n‮b\r\n\tc\n")
	if got != "fatal: a]0;x b c" {
		t.Errorf("fetchErrText = %q", got)
	}
	long := fetchErrText(strings.Repeat("word ", 100))
	if n := len([]rune(long)); n != fetchErrMax || !strings.HasSuffix(long, "…") {
		t.Errorf("long text is %d runes (%q), want %d ending in …", n, long, fetchErrMax)
	}
	r := Report{Base: Base{Ref: "origin/main"}, Fetch: FetchResult{Err: "x\ny\x1b", At: time.Now()}}
	var fetchLine string
	for _, l := range HeaderLines(r, r.Fetch.At) {
		if strings.HasPrefix(l, "fetch:") {
			fetchLine = l
		}
	}
	assertClean(t, "fetch line", fetchLine)
	if !strings.Contains(fetchLine, "FAILED (x y);") {
		t.Errorf("fetch line = %q", fetchLine)
	}
}

// longArgvCases are the two git commands whose message prefix (command line
// and exit code) alone is longer than gitErrMax: operationsInProgress's
// rev-parse (built from operationMarkers, as the code builds it) and
// readRefs's for-each-ref.
func longArgvCases() map[string][]string {
	ops := []string{"rev-parse", "--path-format=absolute", "--show-toplevel"}
	for _, m := range operationMarkers {
		ops = append(ops, "--git-path", m)
	}
	return map[string][]string{
		"operationsInProgress": ops,
		"readRefs":             {"for-each-ref", "--format=" + refFormat, "refs/heads", "refs/remotes"},
	}
}

// stderrPart is what follows the ": exit <code>: " that gitFailure writes.
func stderrPart(t *testing.T, msg, code string) string {
	t.Helper()
	sep := ": exit " + code + ": "
	i := strings.Index(msg, sep)
	if i < 0 {
		t.Fatalf("no %q in %q", sep, msg)
	}
	return msg[i+len(sep):]
}

// TestGitFailureKeepsLongCommandAndExit calls gitFailure with the real
// operationsInProgress and readRefs argument lists, whose command and exit
// code alone exceed gitErrMax. Why: the cap once covered the whole message,
// so for these commands the exit code and git's stderr were cut off, and a
// gates_unavailable row or a failed Gather named only part of a command
// line, not why it failed.
func TestGitFailureKeepsLongCommandAndExit(t *testing.T) {
	for name, args := range longArgvCases() {
		if n := len([]rune("cleanup: git " + strings.Join(args, " ") + ": exit 128: ")); n <= gitErrMax {
			t.Fatalf("%s prefix is %d runes, not longer than gitErrMax: the case no longer covers the regression", name, n)
		}
		got := gitFailure(Out{Code: 128, Stderr: "fatal: not a git repository\n"}, args...).Error()
		assertClean(t, name, got)
		if !strings.HasPrefix(got, "cleanup: git "+strings.Join(args, " ")+": ") {
			t.Errorf("%s: command line cut: %q", name, got)
		}
		if !strings.HasSuffix(got, ": exit 128: fatal: not a git repository") {
			t.Errorf("%s: exit code or stderr lost: %q", name, got)
		}
		if n := len([]rune(stderrPart(t, got, "128"))); n > gitErrMax {
			t.Errorf("%s: stderr part is %d runes, want <= %d", name, n, gitErrMax)
		}
	}
}

// TestGitFailureCapsOnlyStderr feeds 500 runes of stderr behind the long
// operationsInProgress command. Why: the cap bounds git's text, which can be
// any length, and must never eat the command or exit code that lead it.
func TestGitFailureCapsOnlyStderr(t *testing.T) {
	args := longArgvCases()["operationsInProgress"]
	got := gitFailure(Out{Code: 128, Stderr: strings.Repeat("x", 500)}, args...).Error()
	prefix := "cleanup: git " + strings.Join(args, " ") + ": exit 128: "
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("prefix cut: %q", got)
	}
	tail := strings.TrimPrefix(got, prefix)
	if n := len([]rune(tail)); n != gitErrMax || !strings.HasSuffix(tail, "…") {
		t.Errorf("stderr part is %d runes (%q), want %d ending in …", n, tail, gitErrMax)
	}
}

// TestGitDetailKeepsGitFailureIntact passes the operationsInProgress failure
// through gitDetail, the wrapper that builds GatesErr, ClaimsErr and the
// unpushed texts, bare and wrapped with %w, and a non-git error too. Why:
// those wrappers once re-cut an already capped gitFailure message at
// gitErrMax, dropping its exit code and stderr again; other text must still
// be sanitised and capped.
func TestGitDetailKeepsGitFailureIntact(t *testing.T) {
	err := gitFailure(Out{Code: 128, Stderr: "fatal: not a git repository"}, longArgvCases()["operationsInProgress"]...)
	if got := gitDetail(err); got != err.Error() {
		t.Errorf("gitDetail changed a gitFailure message:\n got %q\nwant %q", got, err.Error())
	}
	wrapped := fmt.Errorf("cleanup: gates: %w", err)
	if got := gitDetail(wrapped); got != wrapped.Error() {
		t.Errorf("gitDetail changed a wrapped gitFailure message:\n got %q\nwant %q", got, wrapped.Error())
	}
	other := gitDetail(errors.New("a\x1b\n" + strings.Repeat("y", 500)))
	assertClean(t, "other", other)
	if n := len([]rune(other)); n != gitErrMax {
		t.Errorf("other error is %d runes, want the cap %d", n, gitErrMax)
	}
}

// TestGatesDetailKeepsOperationsFailureEndToEnd points a linked worktree's
// .git file at a git dir that does not exist, so the operationsInProgress
// rev-parse there exits 128, then gathers and classifies for real. Why: the
// gates_unavailable detail for that command once named only part of its
// command line, not the exit code or git's reason.
func TestGatesDetailKeepsOperationsFailureEndToEnd(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	gitdir := "gitdir: " + filepath.Join(t.TempDir(), "nowhere") + "\n"
	if err := os.WriteFile(filepath.Join(p, ".git"), []byte(gitdir), 0o644); err != nil {
		t.Fatal(err)
	}
	noGh(t)
	s, err := Gather(Git{Dir: f.Dir}, GatherInputs{CWD: f.Dir, OwnRepo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	var gatesErr string
	for _, ws := range s.Worktrees {
		if resolve(ws.Fact.Path) == resolve(p) {
			gatesErr = ws.GatesErr
		}
	}
	if !strings.Contains(gatesErr, "--git-path sequencer: exit 128: fatal:") {
		t.Fatalf("GatesErr lost the command end, exit code or stderr: %q", gatesErr)
	}
	r := Classify(s, Options{Now: later()})
	row := findRow(t, r, KindWorktree, "topic")
	var detail string
	for _, re := range row.Reasons {
		if re.Code == ReasonGatesUnavailable {
			detail = re.Detail
		}
	}
	if detail != gatesErr {
		t.Errorf("row detail %q, want GatesErr %q", detail, gatesErr)
	}
	assertClean(t, "gates detail", detail)
}

// TestFetchErrTextJoinsSecondLine pins that fetchErrText keeps the words of
// git's second line. Why: "Could not read from remote repository" often says
// more than the first line, so the lines are joined, not dropped.
func TestFetchErrTextJoinsSecondLine(t *testing.T) {
	got := fetchErrText("fatal: 'origin' does not appear to be a git repository\nfatal: Could not read from remote repository.\n")
	if !strings.Contains(got, "repository fatal: Could not read from remote repository.") {
		t.Errorf("fetchErrText dropped the second line: %q", got)
	}
}
