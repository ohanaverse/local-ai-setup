package agents

import (
	"errors"
	"strings"
	"testing"
)

// TestResumeSessionReturnsTheSession asserts the happy path: a resumable
// session for the path comes back with no warning to show.
func TestResumeSessionReturnsTheSession(t *testing.T) {
	stubOpenCodeQuery(t, `[{"id":"ses_a","time_updated":1000}]`, nil)

	s, warning := ResumeSession("opencode", false, "/work/repo")
	if s == nil || s.ID != "ses_a" {
		t.Fatalf("ResumeSession = %+v, want ses_a", s)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
}

// TestResumeSessionWarnsInsteadOfFailingOnLookupError asserts a failed lookup
// degrades to a fresh launch with a warning rather than an error. Resume is a
// convenience: a corrupt session index or a missing CLI must not make the
// agent unlaunchable, which is how the TUI behaved before this — it aborted
// the whole launch on an error the non-TUI path silently discarded.
func TestResumeSessionWarnsInsteadOfFailingOnLookupError(t *testing.T) {
	stubOpenCodeQuery(t, "", errors.New("opencode: not found"))

	s, warning := ResumeSession("opencode", false, "/work/repo")
	if s != nil {
		t.Fatalf("session = %+v, want nil on a failed lookup", s)
	}
	if !strings.Contains(warning, "opencode: not found") {
		t.Errorf("warning = %q, want it to name the underlying failure", warning)
	}
}

// TestResumeSessionSkipsNativeModels asserts a native model never even
// queries for a session: resuming would restore the session's stored model and
// silently override the user's "native" choice.
func TestResumeSessionSkipsNativeModels(t *testing.T) {
	got := stubOpenCodeQuery(t, `[{"id":"ses_a","time_updated":1000}]`, nil)

	s, warning := ResumeSession("opencode", true, "/work/repo")
	if s != nil || warning != "" {
		t.Fatalf("ResumeSession(native) = %+v, %q; want nil, \"\"", s, warning)
	}
	if len(*got) != 0 {
		t.Errorf("native model queried for a session %d time(s), want 0", len(*got))
	}
}

// TestResumeSessionIgnoresAgentsWithoutResume asserts an agent that cannot
// resume (codex) yields no session and no warning, rather than an error.
func TestResumeSessionIgnoresAgentsWithoutResume(t *testing.T) {
	got := stubOpenCodeQuery(t, `[{"id":"ses_a","time_updated":1000}]`, nil)

	s, warning := ResumeSession("codex", false, "/work/repo")
	if s != nil || warning != "" {
		t.Fatalf("ResumeSession(codex) = %+v, %q; want nil, \"\"", s, warning)
	}
	if len(*got) != 0 {
		t.Errorf("non-resuming agent queried for a session %d time(s), want 0", len(*got))
	}
}
