//go:build !darwin

package lifecycle

import "errors"

// realReadWiredLimit has no limit to read off macOS, where Sushi (MLX) does
// not run: the preflight then skips with "not macOS".
func realReadWiredLimit() (mb int64, ok bool, err error) {
	return 0, false, errors.New("not macOS")
}
