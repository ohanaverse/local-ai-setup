package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// TestOneErrorLineThroughTheWtModelTree pins the error shape of the moved
// registry commands on wt-model: one "wt-model: " line, never cobra's
// "Error:" line, and the usage block before it exactly where wt showed one
// (a model edit usage mistake), so the move changes no output shape.
func TestOneErrorLineThroughTheWtModelTree(t *testing.T) {
	for name, tc := range map[string]struct {
		args  []string
		code  int
		usage bool // the usage block is shown before the line
	}{
		"model edit usage mistake": {[]string{"model", "edit", "nosuch/nosuch"}, 1, true},
		"error from a leaf RunE":   {[]string{"model", "rm", "nosuch/nosuch", "--yes"}, 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			modelHome(t, writeRegistry)
			asWTModel(t)
			// testHost, not rootCmd's real host: neither row reaches a seam,
			// and a regression that did must not reach the machine.
			root := rootCmdWith(fakeDeps, testHost())
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(tc.args)
			err := root.Execute()
			code := exitcode.Report("wt-model", err, &buf)
			out := buf.String()
			if strings.Contains(out, "Error:") {
				t.Errorf("output has cobra's Error: line:\n%s", out)
			}
			var lines int
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "wt-model: ") {
					lines++
				}
			}
			if lines != 1 {
				t.Errorf("%d \"wt-model: \" lines, want exactly 1:\n%s", lines, out)
			}
			if got := strings.Contains(out, "Usage:"); got != tc.usage {
				t.Errorf("usage shown = %v, want %v:\n%s", got, tc.usage, out)
			}
			if code != tc.code {
				t.Errorf("exit code = %d, want %d", code, tc.code)
			}
		})
	}
}
