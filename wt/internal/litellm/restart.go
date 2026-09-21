package litellm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// fallbackRestartCmd bounces the launchd-managed proxy. Used when no restart
// env var is set, because non-interactive launches (agents, worktree
// launchers) do not inherit interactive-shell exports.
const fallbackRestartCmd = "launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"

// runShell runs a shell command; a package var so tests never execute the
// real restart command.
var runShell = realRunShell

func realRunShell(ctx context.Context, cmd string) error {
	return exec.CommandContext(ctx, "/bin/sh", "-c", cmd).Run()
}

// RestartCommand returns the proxy restart command: WT_LITELLM_RESTART_CMD,
// then legacy MODELMAN_LITELLM_RESTART_CMD, then the launchctl fallback.
func RestartCommand() string {
	for _, k := range []string{"WT_LITELLM_RESTART_CMD", "MODELMAN_LITELLM_RESTART_CMD"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return fallbackRestartCmd
}

// restartTimeout bounds one restart command run.
const restartTimeout = 30 * time.Second

// RestartContext bounces the proxy after a config write, on the caller's
// context: a cancelled caller (Ctrl+C during a start or stop) stops paying for
// the restart command immediately instead of waiting out its full run. It
// never fails the caller: the config write is the source of truth, and a
// failed restart only leaves the proxy stale, reported as a warning naming
// the manual fix.
func RestartContext(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()
	cmd := RestartCommand()
	if err := runShell(ctx, cmd); err != nil {
		return []string{fmt.Sprintf("failed to restart LiteLLM proxy (%v); restart it manually: %s", err, cmd)}
	}
	return nil
}

// Restart is RestartContext for callers with no context of their own (the
// Apply default when Options.Restart is nil).
func Restart() []string { return RestartContext(context.Background()) }

// Listening reports whether a proxy may be serving at baseURL: false only when
// the request positively fails because nothing is there (connection refused,
// unresolvable host). A timeout, a non-200 or any other answer counts as
// listening, so a busy or half-started proxy is still waited for after a
// restart instead of being launched into mid-bounce.
func Listening(ctx context.Context, baseURL string, timeout time.Duration) bool {
	resp, err := healthGet(ctx, baseURL, timeout)
	if err != nil {
		var dns *net.DNSError
		return !errors.Is(err, syscall.ECONNREFUSED) && !errors.As(err, &dns)
	}
	resp.Body.Close()
	return true
}

// healthGet makes one bounded request to the proxy's liveness endpoint.
func healthGet(ctx context.Context, baseURL string, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL(baseURL), nil)
	if err != nil {
		return nil, err
	}
	return (&http.Client{Timeout: timeout}).Do(req)
}

func healthURL(baseURL string) string { return strings.TrimRight(baseURL, "/") + "/health/liveliness" }

// WaitReady polls <baseURL>/health/liveliness until it answers 200 or the
// timeout elapses, so a caller does not launch an agent into a proxy that is
// still coming back up.
func WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := healthURL(baseURL)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("LiteLLM proxy not ready at %s: %w", url, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}
