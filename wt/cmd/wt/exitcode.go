package main

import "errors"

// exitCodeError is an error that says which status the process exits with.
// Every command exits 1 on an error; `wt cloud-sync` is the one whose codes
// mean something to a caller (2 to 5 say why its catalog flow changed
// nothing, 1 that a step failed in either flow), so it wraps its error in
// this and main reads the code back with exitCodeOf. The message is what main
// prints after "wt:".
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

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
