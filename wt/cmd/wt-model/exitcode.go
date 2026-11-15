package main

import (
	"fmt"
	"io"
)

// reportExit prints err as wt-model's one error line on w and returns the
// status main exits with: 0 for nil, 1 for any error.
func reportExit(err error, w io.Writer) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(w, "wt-model:", err)
	return 1
}
