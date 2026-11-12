package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// TestReportExitNil guards the one path where reportExit is reachable with a
// nil error today: it must not panic or print, and it returns the same code
// exitCodeOf would (1, since nil carries no code).
func TestReportExitNil(t *testing.T) {
	var buf bytes.Buffer
	if got := reportExit(nil, &buf); got != 1 {
		t.Errorf("reportExit(nil) = %d, want 1", got)
	}
	if buf.Len() != 0 {
		t.Errorf("reportExit(nil) printed %q, want nothing", buf.String())
	}
}

// TestExitCodeOf pins the mapping main uses: the code a cloud-sync error
// carries, however deeply wrapped, and 1 for every other error — so no other
// command's exit status moved when cloud-sync got its own codes, and a
// carried 0 can never turn a failure into a success.
func TestExitCodeOf(t *testing.T) {
	coded := &exitCodeError{code: 4, err: errors.New("refused")}
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("plain"), 1},
		{coded, 4},
		{fmt.Errorf("wrapped: %w", coded), 4},
		{&exitCodeError{code: 0, err: errors.New("zero")}, 1},
		{&exitCodeError{code: 300, err: errors.New("too big")}, 1},
	}
	for _, tc := range cases {
		if got := exitCodeOf(tc.err); got != tc.want {
			t.Errorf("exitCodeOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
	if coded.Error() != "refused" || !errors.Is(fmt.Errorf("x: %w", coded), coded) {
		t.Error("an exitCodeError must print and unwrap as the error it carries")
	}
}

// TestAgentExitStatusPropagatesThroughTheTail pins that a non-TUI launch whose
// agent exits 3 runs the whole post-exit tail and then exits 3 — the exit
// routing fires only after the tail — so a script reading the status sees the
// agent's own code, never wt's tail outcome. The launch runs in a child
// process because runAgentCmd ends in os.Exit. This is a pin of surviving
// behavior (no red→green, R303a): verified by mutation — forcing os.Exit(0) at
// the exit routing makes this test fail (the child then exits 0, not 3).
func TestAgentExitStatusPropagatesThroughTheTail(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if os.Getenv("WT_TEST_AGENT_EXIT_CHILD") == "1" {
		runStopPicker = func(*config.Config) {}
		_ = runAgentCmd(exec.Command("sh", "-c", "exit 3"), "shell", config.Model{}, &config.Config{}, nil)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentExitStatusPropagatesThroughTheTail$")
	cmd.Env = append(os.Environ(), "WT_TEST_AGENT_EXIT_CHILD=1")
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 3 {
		t.Fatalf("child err = %v, want exit status 3\n%s", err, out)
	}
	if !strings.Contains(string(out), "wt: shell") {
		t.Errorf("child output = %q; want the summary line (the tail ran) before the exit", out)
	}
}
