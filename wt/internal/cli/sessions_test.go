package cli

import "testing"

// TestWithSessionsCountsOnlyWhenUsed verifies the one wording the start
// prompts share for an occupant's session count (#412): the line alone when
// nobody (or a negative count) uses the model, the count in parentheses after it otherwise.
func TestWithSessionsCountsOnlyWhenUsed(t *testing.T) {
	for _, tc := range []struct {
		line string
		n    int
		want string
	}{
		{"omlx/a", 0, "omlx/a"},
		{"omlx/a", -1, "omlx/a"},
		{"omlx/a", 2, "omlx/a (in use by 2 wt session(s))"},
		{"omlx/a — 4.0 GB loaded", 1, "omlx/a — 4.0 GB loaded (in use by 1 wt session(s))"},
	} {
		if got := WithSessions(tc.line, tc.n); got != tc.want {
			t.Errorf("WithSessions(%q, %d) = %q, want %q", tc.line, tc.n, got, tc.want)
		}
	}
}
