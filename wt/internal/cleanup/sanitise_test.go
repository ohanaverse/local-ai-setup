package cleanup

import (
	"errors"
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
	res := LookupPRs("", []string{"topic"}, "o/r", 1)["topic"]
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
	if n := len([]rune(got)); n > gitErrMax {
		t.Errorf("gitFailure is %d runes, want <= %d", n, gitErrMax)
	}
}

// TestGatesFailureDetailIsOneSanitisedLine corrupts a worktree's index so
// git status there fails with several lines of stderr ("error: bad
// signature ..." then "fatal: index file corrupt"), then gathers and
// classifies for real. Why: that stderr went whole into the row's
// gates_unavailable detail and from there to the terminal.
func TestGatesFailureDetailIsOneSanitisedLine(t *testing.T) {
	f := newFixture(t)
	p := f.Worktree("topic")
	index := strings.TrimSpace(gitIn(t, p, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	if err := os.WriteFile(index, []byte(strings.Repeat("not an index\x1b]0;pwn\x07\n", 8)), 0o644); err != nil {
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
	if n := len([]rune(detail)); n > gitErrMax {
		t.Errorf("gates detail is %d runes, want <= %d", n, gitErrMax)
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
