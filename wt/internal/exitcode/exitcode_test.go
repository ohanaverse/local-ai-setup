package exitcode

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestReport(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		out  string
	}{
		{"nil", nil, 0, ""},
		{"plain", errors.New("boom"), 1, "p: boom\n"},
		{"coded", New(4, errors.New("refused"), false), 4, "p: refused\n"},
		{"wrapped coded", fmt.Errorf("outer: %w", New(3, errors.New("in"), false)), 3, "p: outer: in\n"},
		{"quiet", New(2, errors.New("said"), true), 2, ""},
		{"zero", New(0, errors.New("zero"), false), 1, "p: zero\n"},
		{"too big", New(300, errors.New("big"), false), 1, "p: big\n"},
		{"negative", New(-1, errors.New("neg"), true), 1, ""},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		if got := Report("p", c.err, &buf); got != c.code {
			t.Errorf("%s: code = %d, want %d", c.name, got, c.code)
		}
		if buf.String() != c.out {
			t.Errorf("%s: printed %q, want %q", c.name, buf.String(), c.out)
		}
	}
}

func TestErrorUnwrapsToItsCause(t *testing.T) {
	cause := errors.New("cause")
	err := New(5, cause, false)
	if err.Error() != "cause" || !errors.Is(err, cause) {
		t.Errorf("err = %v, want it to print and unwrap as its cause", err)
	}
	if Of(nil) != 1 {
		t.Errorf("Of(nil) = %d, want 1", Of(nil))
	}
}
