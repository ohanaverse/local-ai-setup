package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunCommandReturnsAtDeadlineDespiteAForkedChild verifies runCommand and
// runCommandEnv return soon after their context's deadline even when the
// command left a child holding its output pipe. Without a WaitDelay, Wait
// blocks until every writer to the pipe closes, so a readiness command that
// forks (a launcher) would hang a start, and the stop poll, indefinitely
// instead of reading as "not ready". The shell records its child's pid so
// the test kills the orphan it leaves; the two cases run in parallel to keep
// the test near the one-second WaitDelay.
func TestRunCommandReturnsAtDeadlineDespiteAForkedChild(t *testing.T) {
	for name, run := range map[string]func(ctx context.Context, args ...string) ([]byte, error){
		"runCommand": func(ctx context.Context, args ...string) ([]byte, error) {
			return runCommand(ctx, "/bin/sh", args...)
		},
		"runCommandEnv": func(ctx context.Context, args ...string) ([]byte, error) {
			return runCommandEnv(ctx, []string{"WT_TEST=1"}, "/bin/sh", args...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			pidfile := filepath.Join(t.TempDir(), "child.pid")
			t.Cleanup(func() {
				b, err := os.ReadFile(pidfile)
				if err != nil {
					return
				}
				if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := run(ctx, "-c", `sleep 30 & echo $! > "$0"; wait`, pidfile)
			if took := time.Since(start); took > 3*time.Second {
				t.Fatalf("returned after %v, want well under 3s past a 100ms deadline", took)
			}
			if err == nil {
				t.Error("err = nil, want the deadline's kill")
			}
		})
	}
}
