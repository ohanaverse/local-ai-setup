// Prompt and result helpers shared by the commands that ask y/N questions and
// print a result: cleanup, rescue, remove and restore-root.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

// isTerminalStream says whether s (a reader or writer) is a terminal file.
func isTerminalStream(s any) bool {
	f, ok := s.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

// bothTerminals says whether a y/N prompt can be asked: both its input and
// the stream it is written to are terminals. The *TTY test seams default to it.
func bothTerminals(in io.Reader, out io.Writer) bool {
	return isTerminalStream(in) && isTerminalStream(out)
}

// confirmYN reads one line from r and reports whether it is y or yes (any
// case); anything else, including the end of input, is no.
func confirmYN(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// writeResult emits a rescue-style result: the JSON document from encode or
// the text from text on out, the text on errOut when the result cannot be
// written, and a non-zero code as an exitCodeError carrying notDone (quiet
// after JSON, whose document already says so). encode also reports partial: a
// JSON document that carries an error beside its items is printed whole and
// raises the exit status to 2 (remove's Ruling O).
func writeResult(out, errOut io.Writer, asJSON bool, code int, notDone string, encode func() ([]byte, bool, error), text func(io.Writer) error) error {
	lost := func(err error) error {
		text(errOut)
		return &exitCodeError{code: 2, err: fmt.Errorf("could not write the result to stdout (it is on stderr): %w", err)}
	}
	if asJSON {
		b, partial, err := encode()
		if err != nil {
			text(errOut)
			return &exitCodeError{code: 2, err: fmt.Errorf("could not write the JSON result: %w", err)}
		}
		if _, err := out.Write(b); err != nil {
			return lost(err)
		}
		if partial {
			code = 2
		}
		if code != 0 {
			return &exitCodeError{code: code, err: errors.New(notDone), quiet: true}
		}
		return nil
	}
	if err := text(out); err != nil {
		return lost(err)
	}
	if code != 0 {
		return &exitCodeError{code: code, err: errors.New(notDone)}
	}
	return nil
}
