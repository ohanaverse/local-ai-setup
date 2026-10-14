package litellm

import (
	"context"
	"time"
)

// StubProxyReadinessForTest replaces the two readiness steps of the restart a
// changed write makes by default (bounce: the probe before the restart and the
// wait after it) and returns the function that puts the previous ones back
// (`t.Cleanup(litellm.StubProxyReadinessForTest(listening, wait))`). The
// restart itself is not replaced: WT_LITELLM_RESTART_CMD or Options.Restart
// stands in for it, and RestartContext refuses launchctl in a test binary.
// Production code never calls it; it is exported because the code that
// reaches the default restart lives in cmd/wt, whose tests must not probe or
// wait on whatever answers at the developer's proxy address.
func StubProxyReadinessForTest(
	listening func(ctx context.Context, baseURL string, timeout time.Duration) bool,
	wait func(ctx context.Context, baseURL string, timeout time.Duration) error,
) (restore func()) {
	old := bounce
	bounce.Listening = listening
	bounce.WaitReady = wait
	return func() { bounce = old }
}
