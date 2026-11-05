package cleanup

import (
	"errors"
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
	// The temp path is long, so the cap may cut the second line short.
	if !strings.Contains(r.Err, "repository fatal: Could not read") {
		t.Errorf("later lines of git's text were lost: %q", r.Err)
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
