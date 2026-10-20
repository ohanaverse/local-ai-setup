package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// exitCodeError is an error that says which status the process exits with.
// Every command exits 1 on an error; `wt cloud-sync` is the one whose codes
// mean something to a caller (2 to 5 say why its ollama flow changed
// nothing, 1 that a step failed in either flow), so it wraps its error in
// this and main reads the code back with exitCodeOf. The message is what main
// prints after "wt:", unless quiet is set: then main prints nothing.
type exitCodeError struct {
	code  int
	err   error
	quiet bool
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// agentExitStatus is the error a launch from the TUI picker returns to main
// (#412): when running the agent ended in an *exec.ExitError, a quiet
// exitCodeError with the agent's own status, so wt exits as runAgentCmd's
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
	return &exitCodeError{code: code, err: err, quiet: true}
}

// reportExit prints err as main's "wt:" line on w, unless it is a quiet
// exitCodeError, and returns the status main exits with: 1 unless the error
// carries another status (exitCodeError): `wt cloud-sync` says with 2 to 5
// why its ollama flow changed nothing, and a picker launch passes on its
// agent's status.
func reportExit(err error, w io.Writer) int {
	var coded *exitCodeError
	if !errors.As(err, &coded) || !coded.quiet {
		fmt.Fprintln(w, "wt:", err)
	}
	return exitCodeOf(err)
}

// exitCodeOf is the status main exits with for err: the code an
// exitCodeError carries, anywhere in the chain, and 1 for every other error.
// A code outside 1 to 255 is 1: 0 would report a failure as success.
func exitCodeOf(err error) int {
	var coded *exitCodeError
	if errors.As(err, &coded) && coded.code >= 1 && coded.code <= 255 {
		return coded.code
	}
	return 1
}
