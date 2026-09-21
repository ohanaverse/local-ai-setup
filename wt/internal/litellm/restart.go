package litellm

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// fallbackRestartCmd bounces the launchd-managed proxy. Used when no restart
// env var is set, because non-interactive launches (agents, worktree
// launchers) do not inherit interactive-shell exports.
const fallbackRestartCmd = "launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"

// runShell runs a shell command; a package var so tests never execute the
// real restart command.
var runShell = func(ctx context.Context, cmd string) error {
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

// Restart bounces the proxy after a config write. It never fails the caller:
// the config write is the source of truth, and a failed restart only leaves
// the proxy stale, reported as a warning naming the manual fix.
func Restart() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := RestartCommand()
	if err := runShell(ctx, cmd); err != nil {
		return []string{fmt.Sprintf("failed to restart LiteLLM proxy (%v); restart it manually: %s", err, cmd)}
	}
	return nil
}

// WaitReady polls <baseURL>/health/liveliness until it answers 200 or the
// timeout elapses, so a caller does not launch an agent into a proxy that is
// still coming back up.
func WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := strings.TrimRight(baseURL, "/") + "/health/liveliness"
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
