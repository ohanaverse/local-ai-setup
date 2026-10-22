package tui

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// startOutputMax is how much of one start's output is kept. The engine
// prints a line or two per model omlx unloaded, so this is a bound on a
// start gone wrong, not a size anything is expected to reach.
const startOutputMax = 16 << 10

// startOutput collects what the engine prints during one start from the
// picker (lifecycle.Options.Out). The alt screen hides stderr, so the picker
// takes the lines and shows them itself: on its status line, and on the real
// terminal once the alt screen is gone (#275).
//
// It is written by the start's goroutine and by the proxy restart the start
// may leave running, and read by the goroutine that reports the start, so
// every method takes the lock.
type startOutput struct {
	// what names the output in the line that says some was not kept: "start"
	// when empty.
	what    string
	mu      sync.Mutex
	buf     []byte
	dropped int
	// to, once set by release, is where every later write goes directly.
	to io.Writer
}

// Write keeps p, up to startOutputMax in all; what does not fit is counted.
// After release it writes p straight through.
func (o *startOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.to != nil {
		return o.to.Write(p)
	}
	if room := startOutputMax - len(o.buf); len(p) > room {
		o.dropped += len(p) - room
		o.buf = append(o.buf, p[:room]...)
		return len(p), nil
	}
	o.buf = append(o.buf, p...)
	return len(p), nil
}

// text is what has been kept so far. When some was dropped, the kept part
// ends at its last whole line and a line says how much is missing.
func (o *startOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.textLocked()
}

func (o *startOutput) textLocked() string {
	if o.dropped == 0 {
		return string(o.buf)
	}
	kept := o.buf
	if i := bytes.LastIndexByte(kept, '\n'); i >= 0 {
		kept = kept[:i+1]
	}
	what := o.what
	if what == "" {
		what = "start"
	}
	return string(kept) + fmt.Sprintf("wt: … %d more bytes of %s output not kept\n", o.dropped+len(o.buf)-len(kept), what)
}

// RestartNotice takes the proxy restart's notices (lifecycle.RestartNoticer).
// The in-progress one is for the waiting screen while the alt screen is up,
// and is printed only once release has given the output a real terminal.
func (o *startOutput) RestartNotice(n litellm.Notice) {
	o.mu.Lock()
	released := o.to != nil
	o.mu.Unlock()
	if n.InProgress && !released {
		showDrain(n.Msg)
		return
	}
	fmt.Fprintf(o, "wt: %s\n", n.Msg)
}

// take returns what has been kept so far and forgets it, for a reader that
// prints it and comes back for whatever is written later.
func (o *startOutput) take() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := o.textLocked()
	o.buf, o.dropped = nil, 0
	return kept
}

// release prints what was kept to w and sends everything written from now on
// straight to w. It is for the one way out that never reads the start's
// result: wt quitting while the start is still running.
func (o *startOutput) release(w io.Writer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	fmt.Fprint(w, o.textLocked())
	o.buf, o.dropped, o.to = nil, 0, w
}
