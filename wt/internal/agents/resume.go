package agents

import "github.com/ohanaverse/local-ai-setup/wt/internal/session"

// ResumeSession returns the newest resumable session for agent at path, or nil
// when there is none. The second return value is a user-facing warning to show
// when the lookup failed, and is empty otherwise.
//
// A failed lookup is deliberately not an error the caller must handle. Resume
// is a convenience, and letting an unreadable session index — a corrupt
// database, an agent binary that cannot be found — block the launch would make
// the agent unlaunchable over a feature the user did not ask for. Callers warn
// and launch fresh.
//
// A native model never resumes: the session stores the model it ran with, so
// resuming would silently override the user's "native" choice (for claude,
// routing a gateway model at the real Anthropic API).
//
// Both launch paths go through here so they cannot drift: the TUI used to
// abort the launch on a lookup error while the non-TUI path discarded the same
// error and launched silently — two behaviours for one failure.
func ResumeSession(agent string, native bool, path string) (*session.Session, string) {
	if native {
		return nil, ""
	}
	r, ok := ByName(agent).(Resumer)
	if !ok {
		return nil, ""
	}
	s, err := r.LatestSession(path)
	if err != nil {
		return nil, "resume check failed, starting fresh: " + err.Error()
	}
	return s, ""
}
