package tui

import "testing"

// TestOppositeTagSwapsCodeAndDesign asserts the helper swaps the two
// known tag group names. The cross-tag skip in rotation calls this to
// know which other tag's last-used model to avoid.
func TestOppositeTagSwapsCodeAndDesign(t *testing.T) {
	cases := map[string]string{
		"code":          "design",
		"design":        "code",
		"":              "",
		"anything-else": "",
	}
	for in, want := range cases {
		if got := oppositeTag(in); got != want {
			t.Errorf("oppositeTag(%q) = %q, want %q", in, got, want)
		}
	}
}
