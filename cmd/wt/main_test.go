package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestLegacyShortFlagRejected verifies that the old `-w` arity is captured
// and rejected with the migration message. Users coming from the bash
// engine expect `-w name`, `-wname`, and `-w=name` to all parse; if pflag
// treats them as unknown flags instead, the migration message never fires.
func TestLegacyShortFlagRejected(t *testing.T) {
	for _, args := range []struct {
		name string
		args []string
	}{
		{"space separated", []string{"-w", "my-branch"}},
		{"no space", []string{"-wmy-branch"}},
		{"equals", []string{"-w=my-branch"}},
	} {
		t.Run(args.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(args.args)
			err := root.Execute()
			if err == nil {
				t.Fatal("expected error for legacy -w flag, got nil")
			}
			if !strings.Contains(err.Error(), "-w is removed") {
				t.Errorf("error %q does not contain migration message", err.Error())
			}
		})
	}
}
