package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// TestOneErrorLineThroughTheWtModelTree pins the error shape of the moved
// registry commands (and served and warm) on wt-model: one "wt-model: " line, never cobra's
// "Error:" line, and the usage block before it exactly where wt showed one
// (a model edit usage mistake), so the move changes no output shape.
func TestOneErrorLineThroughTheWtModelTree(t *testing.T) {
	for name, tc := range map[string]struct {
		args  []string
		code  int
		usage bool   // the usage block is shown before the line
		line  string // the "wt-model: " line names this, when set
	}{
		"model edit usage mistake": {[]string{"model", "edit", "nosuch/nosuch"}, 1, true, ""},
		"error from a leaf RunE":   {[]string{"model", "rm", "nosuch/nosuch", "--yes"}, 1, false, ""},
		// served and warm moved here from wt: both rows refuse before any
		// probe, and the line is the command's own refusal, not cobra's
		// unknown command, so both fail if the root stops adding it.
		"served unknown provider":  {[]string{"served", "nosuch"}, 1, false, `wt-model: unknown provider "nosuch": wt-model served answers for `},
		"warm a non-omlx provider": {[]string{"warm", "ollama", "m"}, 1, false, `wt-model: unknown provider "ollama": wt-model warm loads models into omlx only`},
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
			if tc.line != "" && !strings.Contains(out, tc.line) {
				t.Errorf("output does not name %q:\n%s", tc.line, out)
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

// TestWtModelServesServedAndWarm pins that wt-model's root adds `served` and
// `warm`, visible, with served's --json: `served` and `warm` under `wt` point
// users here and llmbench's bridge calls these names, so the root dropping
// either must fail here and not only on a machine.
func TestWtModelServesServedAndWarm(t *testing.T) {
	root := rootCmdWith(fakeDeps, testHost())
	for _, name := range []string{"served", "warm"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == root || cmd.Name() != name {
			t.Fatalf("wt-model %s: Find gave %q (err %v), want the %s command", name, cmd.CommandPath(), err, name)
		}
		if cmd.Hidden {
			t.Errorf("wt-model %s is hidden, want it listed", name)
		}
		if name == "served" && cmd.Flags().Lookup("json") == nil {
			t.Errorf("wt-model served has no --json flag")
		}
	}
}
