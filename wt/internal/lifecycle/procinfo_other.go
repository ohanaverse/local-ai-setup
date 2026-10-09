//go:build !darwin

package lifecycle

import "errors"

// realDescribeProc has no implementation off macOS, where mtplx (MLX) does
// not run: wt then identifies no process, and so signals none.
func realDescribeProc(int) (procInfo, bool, error) {
	return procInfo{}, false, errors.New("reading the process table is supported on macOS only")
}
