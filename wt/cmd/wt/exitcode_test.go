package main

import (
	"errors"
	"fmt"
	"testing"
)

// TestExitCodeOf pins the mapping main uses: the code a cloud-sync error
// carries, however deeply wrapped, and 1 for every other error — so no other
// command's exit status moved when cloud-sync got its own codes, and a
// carried 0 can never turn a failure into a success.
func TestExitCodeOf(t *testing.T) {
	coded := &exitCodeError{code: 4, err: errors.New("refused")}
	cases := []struct {
		err  error
		want int
	}{
		{errors.New("plain"), 1},
		{coded, 4},
		{fmt.Errorf("wrapped: %w", coded), 4},
		{&exitCodeError{code: 0, err: errors.New("zero")}, 1},
		{&exitCodeError{code: 300, err: errors.New("too big")}, 1},
	}
	for _, tc := range cases {
		if got := exitCodeOf(tc.err); got != tc.want {
			t.Errorf("exitCodeOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
	if coded.Error() != "refused" || !errors.Is(fmt.Errorf("x: %w", coded), coded) {
		t.Error("an exitCodeError must print and unwrap as the error it carries")
	}
}
