package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestLegacyShortFlagRejected verifies that `wt -w foo` errors out with
// the migration message. This is the hard-error path for users still on
// the old `-w` short flag.
func TestLegacyShortFlagRejected(t *testing.T) {
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"-w", "my-branch"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for legacy -w flag, got nil")
	}
	if !strings.Contains(err.Error(), "-w is removed") {
		t.Errorf("error %q does not contain migration message", err.Error())
	}
}
