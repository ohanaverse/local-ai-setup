package agents

import "testing"

// TestIsCommand verifies the agent/command distinction: shell is a command (no model layer); every other registered driver
// is an agent. This matters because the picker shows a different screen for
// commands (skip the model layer) vs. agents (show the model list).
func TestIsCommand(t *testing.T) {
	commands := map[string]bool{"shell": true}
	for _, n := range Names() {
		if commands[n] {
			if !IsCommand(n) {
				t.Errorf("IsCommand(%q) = false, want true (command)", n)
			}
			continue
		}
		if IsCommand(n) {
			t.Errorf("IsCommand(%q) = true, want false (agent)", n)
		}
	}
}

// TestIsCommandUnknown verifies that IsCommand is safe on unregistered
// names — it must return false rather than panicking on a missing entry.
// This guards callers that resolve an agent name from user/config input
// before the picker runs the registry check.
func TestIsCommandUnknown(t *testing.T) {
	if IsCommand("does-not-exist") {
		t.Error(`IsCommand("does-not-exist") = true, want false`)
	}
}
