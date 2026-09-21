package survey

import (
	"os"
	"testing"
)

// TestMain makes the flushTTY seam package-wide: no test in this package may
// drain the developer's real terminal input queue, even when run as a
// hand-built `go test -c` binary or from an IDE that attaches a tty. Tests
// that need to observe a flush still assign flushTTY themselves and restore it
// (the restored value is this no-op). Mirrors internal/tui's and cmd/wt's
// TestMain.
func TestMain(m *testing.M) {
	flushTTY = func() {}
	os.Exit(m.Run())
}
