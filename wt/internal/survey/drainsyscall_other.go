//go:build !darwin && !linux

package survey

// drainTTYInput is a no-op on platforms without a known input-flush ioctl
// (wt's supported targets are darwin and linux; this keeps the build green
// everywhere else).
func drainTTYInput(fd int) error { return nil }
