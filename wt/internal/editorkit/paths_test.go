package editorkit

import (
	"strings"
	"testing"
)

// TestBreakPathNeverSplitsASegmentThatFits pins BreakPath: it breaks only
// after a slash, and cuts a segment only when it alone is wider than the width.
func TestBreakPathNeverSplitsASegmentThatFits(t *testing.T) {
	got := BreakPath("~/aaaa/bbbb/cccc/dddd", 12)
	want := []string{"~/aaaa/bbbb/", "cccc/dddd"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("BreakPath = %q, want %q", got, want)
	}
	if got := BreakPath("~/"+strings.Repeat("x", 30), 10); len(got) < 3 {
		t.Errorf("a segment wider than the width must still be cut: %q", got)
	}
	for _, l := range BreakPath("~/"+strings.Repeat("x", 30)+"/y", 10) {
		if len(l) > 10 {
			t.Errorf("line %q wider than 10", l)
		}
	}
}

// TestTildePathAbbreviatesOnlyUnderHome pins TildePath now that the home is
// a parameter: a path at or under the home gets ~, a sibling directory that
// merely shares the home's prefix never does, and no home (or /) abbreviates
// nothing.
func TestTildePathAbbreviatesOnlyUnderHome(t *testing.T) {
	for _, c := range []struct{ path, home, want string }{
		{"/Users/dev/x", "/Users/dev", "~/x"},
		{"/Users/dev", "/Users/dev", "~"},
		{"/Users/devx/y", "/Users/dev", "/Users/devx/y"},
		{"/a/b", "", "/a/b"},
		{"/a/b", "/", "/a/b"},
	} {
		if got := TildePath(c.path, c.home); got != c.want {
			t.Errorf("TildePath(%q, %q) = %q, want %q", c.path, c.home, got, c.want)
		}
	}
}

// TestHomePathPrefersTheObservedHome pins HomePath: the home the observed
// state names wins, and the user's home is only the fallback when the state
// names none (or /).
func TestHomePathPrefersTheObservedHome(t *testing.T) {
	for _, c := range []struct{ path, stateHome, userHome, want string }{
		{"/Users/dev/t", "/Users/dev", "/Users/other", "~/t"},
		{"/Users/other/t", "", "/Users/other", "~/t"},
		{"/Users/other/t", "/", "/Users/other", "~/t"},
		{"/srv/t", "/Users/dev", "/Users/dev", "/srv/t"},
		{"/Users/other/t", "/Users/dev", "/Users/other", "/Users/other/t"},
	} {
		if got := HomePath(c.path, c.stateHome, c.userHome); got != c.want {
			t.Errorf("HomePath(%q, %q, %q) = %q, want %q", c.path, c.stateHome, c.userHome, got, c.want)
		}
	}
}
