package survey

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Select is the numbered prompt of a client session's `wt stop` picker
// (cmd/wt-session's model_cmds_remote.go): it prints header and labels, reads
// one line on its own goroutine and races that read against ctx. It returns
// the chosen indices in list order (see chooseLabeled for what a line may
// say); nothing chosen is an empty result, and a ctx that ends first prints
// "cancelled" and reports cancelled=true.
//
// The menu read blocks in a syscall a signal does not interrupt (the runtime
// restarts it), so a cancelled menu abandons that goroutine still blocked in
// Scan: harmless, wt exits shortly after. It is still printing the prompt
// until it gets there, which is the race lockedWriter covers: w is wrapped
// in one unless the caller already did.
func Select(ctx context.Context, r io.Reader, w io.Writer, header string, labels []string) (selected []int, cancelled bool) {
	if _, ok := w.(*lockedWriter); !ok {
		w = &lockedWriter{w: w}
	}
	answer := make(chan []int, 1)
	go func() { answer <- chooseLabeled(bufio.NewScanner(r), w, header, labels) }()
	select {
	case selected = <-answer:
		return selected, false
	case <-ctx.Done():
		fmt.Fprintln(w, "\ncancelled")
		return nil, true
	}
}

// lockedWriter serializes writes to one writer from Select's two
// goroutines: the reader printing the prompt (the header and list, then one
// line per rejected answer) and Select printing "cancelled" when a signal
// wins the race. It keeps each write whole rather than ordering the two, which
// is all a terminal needs and is what makes a shared bytes.Buffer race-free.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

// chooseLabeled runs the one-line prompt and returns the chosen indices in
// list order (#139). Enter applies what was typed, with no confirming step:
//
//   - nothing (or "q", "esc", "skip", or an exhausted reader) stops nothing —
//     the default, because stopping a model costs a reload and must be an
//     explicit choice;
//   - "a" or "all" chooses every model;
//   - space-separated numbers choose those models, each once.
//
// Anything else rejects the whole line — "1 x" does not stop model 1 on a
// typo — says which part was not understood, and asks again. It is not more
// elaborate than that on purpose: whatever this prompt leaves running, `wt
// stop` stops.
func chooseLabeled(sc *bufio.Scanner, w io.Writer, header string, labels []string) []int {
	fmt.Fprintln(w, header)
	for i, l := range labels {
		fmt.Fprintf(w, "  %d  %s\n", i+1, l)
	}
	fmt.Fprintln(w, "  numbers (e.g. 1 2) · a = all · q = skip · Enter = none")
	for {
		fmt.Fprint(w, "stop> ")
		if !sc.Scan() {
			return nil
		}
		line := strings.ToLower(strings.TrimSpace(sc.Text()))
		switch line {
		case "", "q", "esc", "skip":
			return nil
		case "a", "all":
			all := make([]int, len(labels))
			for i := range all {
				all[i] = i
			}
			return all
		}
		chosen := make([]bool, len(labels))
		var bad []string
		for _, f := range strings.Fields(line) {
			if n, err := strconv.Atoi(f); err == nil && n >= 1 && n <= len(labels) {
				chosen[n-1] = true
				continue
			}
			bad = append(bad, strconv.Quote(f))
		}
		if len(bad) > 0 {
			fmt.Fprintf(w, "not a number from the list: %s — type numbers (e.g. 1 2), a for all, or Enter to stop nothing\n", strings.Join(bad, " "))
			continue
		}
		var out []int
		for i, on := range chosen {
			if on {
				out = append(out, i)
			}
		}
		return out
	}
}
