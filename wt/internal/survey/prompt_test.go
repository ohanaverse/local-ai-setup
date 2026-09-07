package survey

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func withTTY(t *testing.T, val bool) {
	t.Helper()
	old := stdinTTY
	stdinTTY = func() bool { return val }
	t.Cleanup(func() { stdinTTY = old })
}

// TestPromptRunNoopWhenNotTTY verifies the survey silently does nothing on
// a non-interactive launch (piped stdin, CI) — the design explicitly
// rejects a config toggle in favor of this guard.
func TestPromptRunNoopWhenNotTTY(t *testing.T) {
	withTTY(t, false)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\n"), &out, store, "claude", config.Model{ID: "m"})
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty (non-TTY guard)", out.String())
	}
	if len(store.Events()) != 0 {
		t.Fatalf("Events() = %v, want empty", store.Events())
	}
}

// TestPromptRunNoopForCommandAgent verifies command agents (m.ID == "",
// e.g. shell) are never surveyed — there's no model verdict to record.
func TestPromptRunNoopForCommandAgent(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\n"), &out, store, "shell", config.Model{})
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty (command-agent guard)", out.String())
	}
}

// TestPromptRunYesFlow verifies the full y -> speed -> quality path
// records a worked event with both ratings and prints the after-survey
// stats block.
func TestPromptRunYesFlow(t *testing.T) {
	withTTY(t, true)
	now = func() time.Time { return time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\n"), &out, store, "claude", config.Model{ID: "ollama/gemma4:9b"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	e := events[0]
	if e.Worked == nil || !*e.Worked {
		t.Fatalf("Worked = %v, want true", e.Worked)
	}
	if e.Speed == nil || *e.Speed != 4 {
		t.Fatalf("Speed = %v, want 4", e.Speed)
	}
	if e.Quality == nil || *e.Quality != 5 {
		t.Fatalf("Quality = %v, want 5", e.Quality)
	}
	if !strings.Contains(out.String(), "survey saved") {
		t.Errorf("output = %q, want it to contain \"survey saved\"", out.String())
	}
	if !strings.Contains(out.String(), "model") {
		t.Errorf("output = %q, want the after-survey stats block", out.String())
	}
}

// TestPromptRunNoFlowStopsAfterQ1 verifies "n" records a failed verdict and
// never prompts for ratings — a failed run has nothing to rate.
func TestPromptRunNoFlowStopsAfterQ1(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("n\n"), &out, store, "claude", config.Model{ID: "m"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	if events[0].Worked == nil || *events[0].Worked {
		t.Fatalf("Worked = %v, want false", events[0].Worked)
	}
	if strings.Contains(out.String(), "speed") {
		t.Errorf("output = %q, should not prompt for speed after \"no\"", out.String())
	}
}

// TestPromptRunSkipFlow verifies both "s" and a bare Enter record a skip
// event with no verdict.
func TestPromptRunSkipFlow(t *testing.T) {
	for _, input := range []string{"s\n", "\n"} {
		withTTY(t, true)
		store := NewStoreAt(t.TempDir())
		var out bytes.Buffer
		PromptRun(strings.NewReader(input), &out, store, "claude", config.Model{ID: "m"})

		events := store.Events()
		if len(events) != 1 {
			t.Fatalf("input %q: Events() = %d, want 1", input, len(events))
		}
		if !events[0].Skipped {
			t.Fatalf("input %q: Skipped = false, want true", input)
		}
		if events[0].Worked != nil {
			t.Fatalf("input %q: Worked = %v, want nil", input, events[0].Worked)
		}
	}
}

// TestPromptRunInvalidQ1Reprompts verifies an unrecognized Q1 answer
// re-prompts instead of silently defaulting, so a mistyped key doesn't
// record the wrong verdict.
func TestPromptRunInvalidQ1Reprompts(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("x\ny\n\n\n"), &out, store, "claude", config.Model{ID: "m"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	if events[0].Worked == nil || !*events[0].Worked {
		t.Fatalf("Worked = %v, want true (recovered after reprompt)", events[0].Worked)
	}
	if !strings.Contains(out.String(), "please answer y, n, s") {
		t.Errorf("output = %q, want a reprompt message for the invalid \"x\"", out.String())
	}
}

// TestPromptRunRatingReprompts verifies an out-of-range rating (e.g. "9")
// re-prompts instead of silently recording a bad value, and that a bare
// Enter still leaves the rating unrated afterward.
func TestPromptRunRatingReprompts(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n9\n3\n\n"), &out, store, "claude", config.Model{ID: "m"})

	events := store.Events()
	if len(events) != 1 {
		t.Fatalf("Events() = %d, want 1", len(events))
	}
	e := events[0]
	if e.Speed == nil || *e.Speed != 3 {
		t.Fatalf("Speed = %v, want 3 (recovered after reprompt on invalid \"9\")", e.Speed)
	}
	if e.Quality != nil {
		t.Fatalf("Quality = %v, want nil (Enter-skipped)", e.Quality)
	}
	if !strings.Contains(out.String(), "please answer 1-5") {
		t.Errorf("output = %q, want a reprompt message for the invalid \"9\"", out.String())
	}
}
