package survey

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
)

// syncBuf is a buffer the test reads while Select's reader goroutine may
// still be writing the prompt.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestSelectReturnsTheChosenIndices proves Select, the prompt the stop picker
// and `wt stop` on the model server share, prints the header and numbered
// labels and returns the indices a line chooses in list order, or nothing for
// an empty line.
func TestSelectReturnsTheChosenIndices(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want []int
	}{"numbers": {"3 1\n", []int{0, 2}}, "all": {"a\n", []int{0, 1, 2}}, "none": {"\n", nil}} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			got, cancelled := Select(context.Background(), strings.NewReader(tc.in), &out, "Pick?", []string{"a", "b", "c"})
			if cancelled || !slices.Equal(got, tc.want) {
				t.Errorf("got %v cancelled=%v, want %v", got, cancelled, tc.want)
			}
			if !strings.Contains(out.String(), "Pick?\n  1  a\n  2  b\n  3  c\n") {
				t.Errorf("output = %q", out.String())
			}
		})
	}
}

// TestSelectCancelledByContext proves a context that ends while the read is
// blocked ends the prompt with "cancelled" printed and cancelled=true, so
// Ctrl+C reaches the caller.
func TestSelectCancelledByContext(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out syncBuf
	got, cancelled := Select(ctx, pr, &out, "Pick?", []string{"a"})
	if !cancelled || got != nil || !strings.Contains(out.String(), "cancelled") {
		t.Errorf("got %v cancelled=%v out=%q", got, cancelled, out.String())
	}
}
