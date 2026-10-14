package tui

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// drainLine is the proxy restart's in-progress notice (the proxy is finishing
// a request in flight, litellm.Notice.InProgress), for the screens that wait
// on a restart to show while they wait. It is set from the restart's
// goroutine. Printed with the route notes it would be read only once the
// restart was over, when it is no longer true. Reset by Run().
var drainLine atomic.Pointer[string]

func showDrain(msg string) { drainLine.Store(&msg) }

// drainView is the in-progress notice while a restart is still in flight, ""
// otherwise: a notice left from a restart that has finished is not shown.
func drainView() string {
	if p := drainLine.Load(); p != nil && routesPending() {
		return *p
	}
	return ""
}

// withDrain puts the in-progress notice, when there is one, under a waiting
// screen's head line.
func withDrain(head string, width int) string {
	if d := drainView(); d != "" {
		return head + "\n\n" + tuilayout.WrapText(d, width)
	}
	return head
}

// noticeBuffer is the route writer of a check or a slot stage: what the route
// writes print is kept for the real terminal, and the restart's notices are
// taken as lifecycle.RestartNoticer, the in-progress one for the screen.
type noticeBuffer struct{ bytes.Buffer }

func (b *noticeBuffer) RestartNotice(n litellm.Notice) {
	if n.InProgress {
		showDrain(n.Msg)
		return
	}
	fmt.Fprintf(&b.Buffer, "wt: %s\n", n.Msg)
}
