package agents

import (
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestSummaryModelAgent asserts the full three-segment line for a model
// agent. Without this, a regression that drops the model segment for
// non-empty IDs would silently hide which model the user just ran
// against — the most useful piece of post-run context.
func TestSummaryModelAgent(t *testing.T) {
	got := Summary("claude", config.Model{ID: "claude/sonnet"}, 3*time.Minute+42*time.Second)
	want := "wt: claude · claude/sonnet · 3m42s"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestSummaryCommandAgentOmitsModel asserts command agents (empty model
// ID) drop the model segment. Shell/npm/test commands have no model
// layer — emitting "wt: shell · · 1s" would be confusing, and a
// regression here would put a stray separator pair on the user's
// terminal.
func TestSummaryCommandAgentOmitsModel(t *testing.T) {
	got := Summary("shell", config.Model{}, 850*time.Millisecond)
	want := "wt: shell · 850ms"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestSummaryRoundsSubSecond asserts 1.2s rounds down to 1s. The
// ms/seconds display boundary is what makes fast agent invocations
// readable; a regression that always shows seconds (or always shows
// ms) would make every short run either noisy or premature.
func TestSummaryRoundsSubSecond(t *testing.T) {
	got := Summary("shell", config.Model{}, 1200*time.Millisecond)
	want := "wt: shell · 1s"
	if got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

// TestSummaryDurationBoundaries pins the 1s crossover so a `>=` → `>`
// regression (or off-by-one in the rounding rule) can't silently flip
// the formatting. The 999ms/1000ms/1500ms cases are the load-bearing
// boundaries: a regression here would change the visible duration unit
// for every sub-1.5s launch without any other test catching it.
func TestSummaryDurationBoundaries(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"999ms still ms", 999 * time.Millisecond, "wt: shell · 999ms"},
		{"1000ms crosses to seconds", 1000 * time.Millisecond, "wt: shell · 1s"},
		{"1499ms rounds down to 1s", 1499 * time.Millisecond, "wt: shell · 1s"},
		{"1500ms rounds up to 2s", 1500 * time.Millisecond, "wt: shell · 2s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Summary("shell", config.Model{}, tc.d)
			if got != tc.want {
				t.Errorf("Summary(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
