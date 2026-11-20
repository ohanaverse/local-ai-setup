package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRoot runs the real command tree, built as main builds it, and reports
// the error the way main does: cobra's own output and reportExit's line share
// one buffer, in the order a terminal would show them.
func runRoot(t *testing.T, args ...string) (out string, code int) {
	t.Helper()
	return runRootWith(t, testHooks(), args...)
}

// runRootWith is runRoot over a command tree whose app carries h (testHooks() is the
// starting value for a test that overrides other hooks; the client is
// loopback-only either way, since hooks.client guards it in a test binary).
func runRootWith(t *testing.T, h hooks, args ...string) (out string, code int) {
	t.Helper()
	root := rootCmdWith(h)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return buf.String(), 0
	}
	code = reportExit(err, &buf)
	return buf.String(), code
}

// TestOneErrorLineThroughTheRealTree pins W2 (style guide M3): a failing
// invocation prints exactly one error line, "wt: <message>", never cobra's
// "Error:" line, and a command that shows usage on a usage mistake shows it
// but still prints the one line.
func TestOneErrorLineThroughTheRealTree(t *testing.T) {
	for name, tc := range map[string]struct {
		args  []string
		code  int
		usage bool // the usage block is shown before the line
	}{
		"unknown flag on a leaf":           {[]string{"litellm", "status", "--bogus"}, 1, false},
		"bad arg count on a leaf":          {[]string{"litellm", "status", "extra"}, 1, false},
		"unknown flag on the root":         {[]string{"--bogus"}, 1, true},
		"error from the root RunE":         {[]string{"-A", "nosuch"}, 1, false},
		"error from a leaf RunE":           {[]string{"model", "rm", "nosuch/nosuch", "--yes"}, 1, false},
		"smoke usage mistake":              {[]string{"smoke", "--tools", "--cwd"}, 1, true},
		"smoke bad arg count":              {[]string{"smoke", "a", "b"}, 1, true},
		"stop bad arg count":               {[]string{"stop", "a", "b"}, 1, true},
		"pointer swallows a usage mistake": {[]string{"model", "edit", "nosuch/nosuch"}, 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			out, code := runRoot(t, tc.args...)
			if strings.Contains(out, "Error:") {
				t.Errorf("output has cobra's Error: line:\n%s", out)
			}
			var lines int
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "wt: ") {
					lines++
				}
			}
			if lines != 1 {
				t.Errorf("%d \"wt: \" lines, want exactly 1:\n%s", lines, out)
			}
			if got := strings.Contains(out, "Usage:"); got != tc.usage {
				t.Errorf("usage shown = %v, want %v:\n%s", got, tc.usage, out)
			}
			if code != tc.code {
				t.Errorf("exit code = %d, want %d", code, tc.code)
			}
			if !tc.usage && strings.Count(strings.TrimRight(out, "\n"), "\n") != 0 {
				t.Errorf("want one line in all, got:\n%s", out)
			}
		})
	}
}

// TestEveryRunnableCommandSilencesUsage: a command that runs code and fails
// must not bury its error under usage text, so the walk in rootCmd silences
// usage on every runnable subcommand, except the ones that print usage
// themselves on a usage mistake (stop, smoke, model edit's dynamic mistakes
// are set in RunE). The exemptions are named here so a new one is a decision.
func TestEveryRunnableCommandSilencesUsage(t *testing.T) {
	exempt := map[string]bool{"wt stop": true, "wt smoke": true}
	var walk func(c *cobra.Command)
	seen := 0
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Runnable() {
				seen++
				path := sub.CommandPath()
				if exempt[path] {
					if sub.SilenceUsage {
						t.Errorf("%s silences usage statically; it shows usage on a usage mistake", path)
					}
				} else if !sub.SilenceUsage {
					t.Errorf("%s does not silence usage", path)
				}
			}
			walk(sub)
		}
	}
	root := rootCmdWith(testHooks())
	if !root.SilenceErrors {
		t.Error("the root does not silence cobra's Error: line")
	}
	walk(root)
	if seen < 20 {
		t.Fatalf("walked %d runnable commands; the walk is vacuous", seen)
	}
}

// TestStopPointerShowsNoUsage runs `wt stop` under a root built as rootCmd
// builds it (cobra's error line silenced, the usage walk applied): a provider
// name or --all, with or without a stray argument, is the moved pointer, so
// it prints exactly the one "wt: ... moved to wt-model" line, exit 1, and
// never cobra's Error: line or the usage text.
func TestStopPointerShowsNoUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"an unknown provider":    {"stop", "nosuch"},
		"--all":                  {"stop", "--all"},
		"--all with an argument": {"stop", "--all", "x"},
	} {
		t.Run(name, func(t *testing.T) {
			root := &cobra.Command{Use: "wt", SilenceErrors: true}
			root.AddCommand(stopCmd(&app{}))
			silenceUsageOnError(root)
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(args)
			err := root.Execute()
			if err == nil {
				t.Fatal("no error")
			}
			if code := reportExit(err, &buf); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			out := buf.String()
			if strings.Contains(out, "Error:") || strings.Count(out, "\nwt: ")+boolToInt(strings.HasPrefix(out, "wt: ")) != 1 || !strings.Contains(out, "moved to wt-model") {
				t.Errorf("want one \"wt: ... moved to wt-model\" line and no Error:\n%s", out)
			}
			if strings.Contains(out, "Usage:") {
				t.Errorf("usage shown:\n%s", out)
			}
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestCheckGuardNotInstalledIsOneCodedLine: --check-guard returns its
// "not installed" answer as an error (exit 1, the one "wt:" line) instead of
// exiting from RunE.
func TestCheckGuardNotInstalledIsOneCodedLine(t *testing.T) {
	t.Chdir(initTestRepo(t))
	out, code := runRoot(t, "--check-guard")
	if want := "wt: main guard is NOT installed in this repo.\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

// TestFailedStartupIsOneLine: an app that cannot start (newApp's error) goes
// through the same single path as any other failure: "wt: <message>", exit 1,
// no "error:" word, no usage.
func TestFailedStartupIsOneLine(t *testing.T) {
	root := failedRoot(errors.New("boom"))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--version"})
	err := root.Execute()
	if err == nil {
		t.Fatal("no error")
	}
	code := reportExit(err, &buf)
	if buf.String() != "wt: boom\n" || code != 1 {
		t.Errorf("output %q, code %d; want %q, 1", buf.String(), code, "wt: boom\n")
	}
}
