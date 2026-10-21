package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

// TestStartErrorMessageMapsEngineFailures verifies each engine failure
// produces the message the spec promises: a down daemon names the origin, the
// engine's own typed errors are shown verbatim, a server stopped again after
// it started is "not running" rather than a failed start (#343), and anything
// else is prefixed with the model — a user must be able to tell what to fix. It lives here,
// with the function, so the TUI and the non-TUI driver are tested against one
// shared wording.
func TestStartErrorMessageMapsEngineFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		id   string
		want []string // substrings the message must contain
	}{
		{"daemon down", &DaemonDownError{Provider: "omlx", Origin: "http://localhost:8000"}, "omlx/q", []string{"is not answering at", "http://localhost:8000"}},
		{"binary missing", &BinaryMissingError{Binary: "omlx"}, "omlx/q", []string{"omlx"}},
		{"port busy", &PortBusyError{Port: 8000}, "omlx/q", []string{"8000"}},
		{"mtplx address", &MtplxAddressError{Origin: "http://mybox"}, "mtplx/q", []string{"http://mybox", "names no port", "127.0.0.1"}},
		{"generic", errors.New("boom"), "omlx/q", []string{"failed to start omlx/q: boom"}},
		{"stopped after it started", &StoppedError{Why: "mtplx no longer answers at http://127.0.0.1:8003"}, "mtplx/q", []string{"mtplx/q is not running: it started, and was stopped while wt updated the LiteLLM routes (mtplx no longer answers at http://127.0.0.1:8003)"}},
	}
	for _, tc := range cases {
		got := StartErrorMessage(tc.id, tc.err)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: message = %q, want it to contain %q", tc.name, got, want)
			}
		}
	}
}

// TestWithSessionsCountsOnlyWhenUsed verifies the one wording the picker's
// dialog and the non-TUI prompts share for an occupant's session count (#412):
// the line alone when nobody uses the model, the count in parentheses after it
// when sessions do, whatever the line is (an id, or an id and its figure).
func TestWithSessionsCountsOnlyWhenUsed(t *testing.T) {
	for _, tc := range []struct {
		line string
		n    int
		want string
	}{
		{"omlx/a", 0, "omlx/a"},
		{"omlx/a", 2, "omlx/a (in use by 2 wt session(s))"},
		{"omlx/a — 4.0 GB loaded", 1, "omlx/a — 4.0 GB loaded (in use by 1 wt session(s))"},
	} {
		if got := WithSessions(tc.line, tc.n); got != tc.want {
			t.Errorf("WithSessions(%q, %d) = %q, want %q", tc.line, tc.n, got, tc.want)
		}
	}
}

// TestStageLabelCoversEveryStage verifies every declared Stage has its own
// phrase and the zero value falls back to a generic one. A missing case would
// render an empty progress segment while the user waits on a slow warmup.
func TestStageLabelCoversEveryStage(t *testing.T) {
	stages := []Stage{StageStoppingOthers, StageStoppingOccupant, StageStarting, StageWaiting, StageWarming, StageRouting}
	seen := map[string]bool{}
	for _, s := range stages {
		label := StageLabel(s)
		if label == "" || label == StageLabel("") {
			t.Errorf("StageLabel(%q) = %q, want its own non-generic phrase", s, label)
		}
		if seen[label] {
			t.Errorf("StageLabel(%q) = %q, duplicated with an earlier stage", s, label)
		}
		seen[label] = true
	}
	if got := StageLabel(Stage("")); got != "starting" {
		t.Errorf("StageLabel(zero) = %q, want %q", got, "starting")
	}
}
