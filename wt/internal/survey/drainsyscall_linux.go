//go:build linux

package survey

import "golang.org/x/sys/unix"

// drainTTYInput discards any buffered TTY input. A multi-line paste that the
// survey doesn't fully consume would otherwise sit in the kernel's input
// queue and execute as shell commands in the parent terminal after wt exits.
// TCFLSH with TCIFLUSH flushes the input queue only — output is untouched.
func drainTTYInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
