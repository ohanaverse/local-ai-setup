package tui

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startOutputProbe hands a test the writer the flow gave the engine, so the
// test can print into it later, as the engine's proxy restart does.
type startOutputProbe struct {
	mu   sync.Mutex
	opts lifecycle.Options
}

func (p *startOutputProbe) set(opts lifecycle.Options) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opts = opts
}

func (p *startOutputProbe) print(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.opts.Out != nil {
		fmt.Fprint(p.opts.Out, text)
	}
}

// lockedBuffer is a bytes.Buffer two goroutines may use.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStartOutputKeepsALimitedAmountAndSaysSo verifies the buffer between the
// engine and the screen: it keeps what it is given up to its limit, cuts at a
// whole line and says how much is missing, takes writes from two goroutines,
// and after release passes everything straight through.
func TestStartOutputKeepsALimitedAmountAndSaysSo(t *testing.T) {
	var o startOutput
	fmt.Fprint(&o, "wt: one\n")
	fmt.Fprint(&o, "wt: two\n")
	if got := o.text(); got != "wt: one\nwt: two\n" {
		t.Errorf("text = %q", got)
	}

	var big startOutput
	line := "wt: " + strings.Repeat("x", 95) + "\n" // 100 bytes
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				fmt.Fprint(&big, line)
				_ = big.text()
			}
		}()
	}
	wg.Wait()
	got := big.text()
	kept := startOutputMax / 100 * 100
	wantTail := fmt.Sprintf("wt: … %d more bytes of start output not kept\n", 200*100-kept)
	if !strings.HasSuffix(got, wantTail) || len(got) != kept+len(wantTail) {
		t.Errorf("a %d-byte output kept %d bytes ending %q, want %d bytes of whole lines and %q", 200*100, len(got), got[max(len(got)-70, 0):], kept, wantTail)
	}
	if strings.Count(got, line) != kept/100 {
		t.Errorf("the kept part holds %d whole lines, want %d", strings.Count(got, line), kept/100)
	}

	var term bytes.Buffer
	o.release(&term)
	fmt.Fprint(&o, "wt: three\n")
	if term.String() != "wt: one\nwt: two\nwt: three\n" || o.text() != "" {
		t.Errorf("after release the terminal has %q and the buffer %q; want all three lines and nothing", term.String(), o.text())
	}
}
