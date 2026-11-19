package main

import (
	"errors"
	"io"
	"os/exec"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// agentExitStatus is the error a launch from the TUI picker returns to main
// (#412): when running the agent ended in an *exec.ExitError, a quiet
// exitcode.Error with the agent's own status, so wt exits as runAgentCmd's
// os.Exit(ee.ExitCode()) makes a launch without the picker exit. An agent a
// signal killed has ExitCode -1, which os.Exit turns into 255, so it carries
// 255. Any other error is returned as it is: main prints it and exits 1, as
// runAgentCmd's return does.
func agentExitStatus(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	code := ee.ExitCode()
	if code < 0 {
		code = 255
	}
	return exitcode.New(code, err, true)
}

// reportExit prints err as main's "wt:" line on w, unless it is a quiet
// exitcode.Error, and returns the status main exits with (exitcode.Report).
// A nil error is the one case where it returns 1, not 0: main only calls it
// with an error, and the 1 is what a missing error has always meant here.
func reportExit(err error, w io.Writer) int {
	if err == nil {
		return 1
	}
	return exitcode.Report("wt", err, w)
}
