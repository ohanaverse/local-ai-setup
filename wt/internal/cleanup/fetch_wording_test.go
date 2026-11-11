package cleanup

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// cannotVerifyDetail is the cannot_verify detail of s's origin/topic row.
func cannotVerifyDetail(t *testing.T, s Snapshot) string {
	t.Helper()
	row := rowByRef(t, Classify(s, Options{Now: later()}), "refs/remotes/origin/topic")
	if row.Status == StatusProven {
		t.Fatalf("origin row proven without a fetch: %v", reasonCodes(row.Reasons))
	}
	i := slices.IndexFunc(row.Reasons, func(r Reason) bool { return r.Code == ReasonCannotVerify })
	if i < 0 {
		t.Fatalf("no %s reason: %v", ReasonCannotVerify, reasonCodes(row.Reasons))
	}
	return row.Reasons[i].Detail
}

// TestOriginNotFetchedWording: an origin row is never proven without this
// run's fetch, and the reason names the cause truly: --no-fetch only when the
// user passed it; the terminal list, which never fetches on open, says origin
// was not refreshed in this list and how to remove it with a fetch.
func TestOriginNotFetchedWording(t *testing.T) {
	noFetch := cannotVerifyDetail(t, remoteSnap(func(s *Snapshot, _ string) { s.Fetch = FetchResult{} }))
	if !strings.Contains(noFetch, "--no-fetch") {
		t.Errorf("--no-fetch run: detail %q does not name --no-fetch", noFetch)
	}
	list := cannotVerifyDetail(t, remoteSnap(func(s *Snapshot, _ string) { s.Fetch = FetchResult{NotAttempted: true} }))
	if strings.Contains(list, "--no-fetch") {
		t.Errorf("list: detail %q names a flag the user did not pass", list)
	}
	want := "origin was not refreshed in this list; open pull requests that target this branch may be missed (wt remove origin/topic --remote re-checks after a fetch)"
	if list != want {
		t.Errorf("list detail\n got %q\nwant %q", list, want)
	}
}

// TestHeaderFetchNotAttempted: the header says the list did not fetch, not
// that a fetch was skipped by a flag.
func TestHeaderFetchNotAttempted(t *testing.T) {
	r := Report{Repo: "/r", Base: Base{Default: "main", Ref: "origin/main"}, Fetch: FetchResult{NotAttempted: true}}
	lines := strings.Join(HeaderLines(r, time.Now()), "\n")
	if !strings.Contains(lines, "fetch: not run (this list does not fetch when it opens)") {
		t.Errorf("header:\n%s", lines)
	}
}

// TestOriginFetchReasonIsNotAGhReason: the origin-fetch reason reuses the
// cannot_verify code, but it is not about gh: the row shows its own words
// (no "needs gh" label, not folded into "PR lookup failed") and the header's
// gh line does not list it as a gh cause.
func TestOriginFetchReasonIsNotAGhReason(t *testing.T) {
	for _, f := range []FetchResult{{NotAttempted: true}, {}} {
		s := remoteSnap(func(s *Snapshot, _ string) { s.Fetch = f })
		rep := Classify(s, Options{Now: later()})
		row := rowByRef(t, rep, "refs/remotes/origin/topic")
		text := RowText(rep, row)
		if strings.Contains(text, "needs gh") || !strings.Contains(text, "origin was not") {
			t.Errorf("fetch %+v: row text %q, want the fetch reason without a gh label", f, text)
		}
		// gh down too: the fetch reason still reads as itself, and the gh
		// line names only gh's causes.
		s.PRs = map[string]PRResult{"topic": {Err: "gh: not found"}}
		rep = Classify(s, Options{Now: later()})
		row = rowByRef(t, rep, "refs/remotes/origin/topic")
		if text := RowText(rep, row); !strings.Contains(text, "origin was not") {
			t.Errorf("fetch %+v, gh down: row text %q lost the fetch reason", f, text)
		}
		for _, l := range HeaderLines(rep, time.Now()) {
			if strings.HasPrefix(l, "gh:") && strings.Contains(l, "origin was not") {
				t.Errorf("fetch %+v: gh line %q names the fetch as a gh cause", f, l)
			}
		}
	}
}
