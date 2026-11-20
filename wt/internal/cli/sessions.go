package cli

import "fmt"

// WithSessions is a prompt line about one model with the number of live wt
// sessions using it: s alone when none does, else s and the count in
// parentheses. Shared by the TUI's dialog and the non-TUI driver's prompts,
// which the docs promise name an occupant the same way (#412).
func WithSessions(s string, sessions int) string {
	if sessions <= 0 {
		return s
	}
	return fmt.Sprintf("%s (in use by %d wt session(s))", s, sessions)
}
