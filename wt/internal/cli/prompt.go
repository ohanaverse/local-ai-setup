package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReadYesNo reads one line from r and answers it: y/yes is true, n/no is
// false (any case, surrounding space ignored), and anything else, an empty
// line or EOF keeps def.
func ReadYesNo(r io.Reader, def bool) (bool, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return def, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return def, nil
	}
}

// AskOnTTY asks question with a "[y/N]" suffix on the controlling terminal
// open returns, and reads the answer back from it, defaulting to No: the
// question is asked on the terminal itself, never on stdin, so piped input
// can never answer it. When the terminal cannot be opened it returns noTTY,
// the caller's own error naming the flag that applies without asking. open
// is the caller's seam, so a test can take the terminal away.
func AskOnTTY(open func() (*os.File, error), question string, noTTY error) (bool, error) {
	f, err := open()
	if err != nil {
		return false, noTTY
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	return ReadYesNo(f, false)
}
