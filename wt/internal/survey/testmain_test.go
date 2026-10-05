package survey

import (
	"os"
	"testing"
)

// shippedEnabled is Enabled as it stood before TestMain turned it on.
var shippedEnabled bool

// TestMain makes the flushTTY seam package-wide: no test in this package may
// drain the developer's real terminal input queue, even when run as a
// hand-built `go test -c` binary or from an IDE that attaches a tty. Tests
// that need to observe a flush still assign flushTTY themselves and restore it
// (the restored value is this no-op). Mirrors internal/tui's and cmd/wt's
// TestMain.
//
// The survey itself is switched off in production (Enabled, #136). Its code
// and tests are kept, so the package's tests run with it on; shippedEnabled
// holds the value the binary is built with, for the one test about that.
func TestMain(m *testing.M) {
	flushTTY = func() {}
	shippedEnabled = Enabled
	Enabled = true
	os.Exit(m.Run())
}
