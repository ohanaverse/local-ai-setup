//go:build darwin

package survey

import "golang.org/x/sys/unix"

// fread is the FREAD flag from sys/ttycom.h (not exported by x/sys on
// darwin); TIOCFLUSH flushes the input queue when set, output when FWRITE
// is set.
const fread = 0x1

// drainTTYInput discards any buffered TTY input. A multi-line paste that the
// survey doesn't fully consume would otherwise sit in the kernel's input
// queue and execute as shell commands in the parent terminal after wt exits.
// TIOCFLUSH with FREAD flushes the input queue only — output is untouched.
func drainTTYInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TIOCFLUSH, fread)
}
