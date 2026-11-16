package main

import (
	"errors"
	"fmt"
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

// exitApprovalMissing is C9's code 3: the command needed consent it did not
// get (no --yes and no terminal, or the prompt was declined) and changed
// nothing.
const exitApprovalMissing = 3

// errNoTerminal is what a y/N prompt returns when there is no terminal to ask
// on. The command that asked turns it into approvalMissing, naming its own
// flag and what it did not do.
var errNoTerminal = errors.New("there is no terminal to ask")

// approvalMissing is P4's refusal, exit 3: "approval missing: pass <flag>
// (there is no terminal to ask); <state>", where state says what did not
// happen ("nothing was stopped").
func approvalMissing(flag, state string) error {
	return exitcode.New(exitApprovalMissing, fmt.Errorf("approval missing: pass %s (%s); %s", flag, errNoTerminal.Error(), state), false)
}

// noTerminalRefusal turns a prompt's errNoTerminal into approvalMissing(flag,
// state) and passes every other error through.
func noTerminalRefusal(err error, flag, state string) error {
	if errors.Is(err, errNoTerminal) {
		return approvalMissing(flag, state)
	}
	return err
}

// declined is an answered "no": exit 3, like a missing approval, on one line
// "approval missing: declined; <state>", where state says what did not happen
// or what is still true ("nothing was stopped", "omlx/x still running").
func declined(state string) error {
	return exitcode.New(exitApprovalMissing, errors.New("approval missing: declined; "+state), false)
}
