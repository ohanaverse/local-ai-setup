package tui

import (
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

func TestRefreshedNote(t *testing.T) {
	now := time.Now()
	st := refreshStamp{At: now.Add(-3 * time.Minute), OK: true}
	cases := []struct {
		name string
		s    refreshStamp
		f    cleanup.FetchResult
		want string
	}{
		{"stamped", st, cleanup.FetchResult{}, "refreshed 3m ago"},
		{"no stamp", refreshStamp{}, cleanup.FetchResult{}, "not refreshed here"},
		{"ok fetch", st, cleanup.FetchResult{OK: true}, "refreshed 3m ago"},
		// git truncates FETCH_HEAD when a fetch starts, so a failed fetch
		// moves the stamp: no note may contradict "fetch: FAILED".
		{"failed fetch", st, cleanup.FetchResult{Err: "timed out"}, ""},
	}
	for _, c := range cases {
		if got := refreshedNote(c.s, now, c.f); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A report swap keeps the stamp the list was built with: no git read.
func TestWithReportKeepsStamp(t *testing.T) {
	st := refreshStamp{At: time.Now().Add(-time.Hour), OK: true}
	m := newRowList(cleanupFixture(), rowListOpts{Mode: modeLaunch, Now: time.Now(), Root: "/nonexistent", Stamp: &st})
	n := m.withReport(cleanupFixture())
	if n.stamp != st {
		t.Fatalf("stamp after swap = %+v, want %+v", n.stamp, st)
	}
}

// A report swap keeps the flow's whole state: the `R` restore-root choice
// (whose erasure a quit-window fill proved), the words that chose the
// flow's rows, the `d` seed and a fill's held funcs build. A dropped field
// here changes the screen under the user mid-decision or silently discards
// what they asked for.
func TestWithReportKeepsFlowState(t *testing.T) {
	st := refreshStamp{At: time.Now(), OK: true}
	m := newRowList(cleanupFixture(), rowListOpts{Mode: modeLaunch, Now: time.Now(), Root: "/nonexistent", Stamp: &st})
	m.restoreRoot, m.chosenByTicks, m.seedBranch = true, true, true
	m.pendingRM = func() RemoveFuncs { return RemoveFuncs{} }
	n := m.withReport(cleanupFixture())
	if !n.restoreRoot || !n.chosenByTicks || !n.seedBranch || n.pendingRM == nil {
		t.Errorf("restoreRoot=%v chosenByTicks=%v seedBranch=%v pendingRM set=%v after swap, want all kept",
			n.restoreRoot, n.chosenByTicks, n.seedBranch, n.pendingRM != nil)
	}
}
