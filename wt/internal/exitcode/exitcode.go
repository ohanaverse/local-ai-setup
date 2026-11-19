// Package exitcode carries a process exit status inside an error, and turns
// an error into the status and the one error line a binary's main prints.
package exitcode

import (
	"errors"
	"fmt"
	"io"
)

// Error is an error that says which status the process exits with. Every
// command exits 1 on an error; a few carry a code that means something to a
// caller (`wt-model cloud-sync`'s 2 to 5, a picker launch's agent status), so they
// wrap their error with New and main reads the code back with Of. The
// message is what Report prints after the binary's prefix, unless the error
// is quiet: then Report prints nothing.
type Error struct {
	code  int
	err   error
	quiet bool
}

// New wraps err so the process exits with code. A quiet error prints nothing
// in Report (something else already said it).
func New(code int, err error, quiet bool) error {
	return &Error{code: code, err: err, quiet: quiet}
}

func (e *Error) Error() string { return e.err.Error() }
func (e *Error) Unwrap() error { return e.err }

// IsQuiet reports whether err carries a quiet Error anywhere in its chain.
func IsQuiet(err error) bool {
	var coded *Error
	return errors.As(err, &coded) && coded.quiet
}

// Code is the code an Error carries anywhere in err's chain, as it was given
// to New (not clamped), and false when err carries none.
func Code(err error) (int, bool) {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.code, true
	}
	return 0, false
}

// Of is the status main exits with for err: the code an Error carries,
// anywhere in the chain, and 1 for every other error. A code outside 1 to
// 255 is 1: 0 would report a failure as success.
func Of(err error) int {
	var coded *Error
	if errors.As(err, &coded) && coded.code >= 1 && coded.code <= 255 {
		return coded.code
	}
	return 1
}

// Report prints err as main's "<prefix>: <err>" line on w, unless it is nil
// or a quiet Error, and returns the status main exits with: 0 for nil,
// otherwise Of(err).
func Report(prefix string, err error, w io.Writer) int {
	if err == nil {
		return 0
	}
	if !IsQuiet(err) {
		fmt.Fprintln(w, prefix+":", err)
	}
	return Of(err)
}
