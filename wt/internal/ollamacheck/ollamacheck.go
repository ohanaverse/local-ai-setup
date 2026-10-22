// Package ollamacheck verifies whether an Ollama model is locally available.
package ollamacheck

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// IsOllamaModel returns true if the model is from the ollama provider.
func IsOllamaModel(m config.Model) bool {
	return m.ProviderID == "ollama"
}

// Check reports whether m is locally available. Non-ollama models are always
// available (true, nil); ollama models are checked against `ollama list`,
// asked of the daemon the registry's ollama provider row names
// (localmodels.FamilyOrigin, the address `wt stop`, `wt model add` and
// `wt cloud-sync` pin the CLI to as well) and not of whichever daemon the
// shell's OLLAMA_HOST names.
//
// An error means the check could not tell. One way is Available's own
// `ollama list` failure — the command ran and exited non-zero. The others
// run no command at all: cfg has no ollama provider row, or the row's
// base_url names no daemon ("/v1", whitespace, no scheme). ollama reads an
// empty or unusable OLLAMA_HOST as its default daemon, so asking anyway would
// answer from a daemon the registry does not describe. (A row with no
// base_url at all has wt's documented default, which is an address.)
func Check(cfg *config.Config, m config.Model) (bool, error) {
	if !IsOllamaModel(m) {
		return true, nil
	}
	if cfg == nil || cfg.ProviderByID(m.ProviderID) == nil {
		return false, fmt.Errorf("no %s provider in the registry, so no daemon to ask", m.ProviderID)
	}
	origin := localmodels.FamilyOrigin(cfg, "ollama")
	if u, err := url.Parse(origin); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		// Short, with the value last: the picker shows this behind
		// "ollama check failed: " on one status line that it cuts at the
		// terminal's width, and what to set must survive 80 columns.
		return false, fmt.Errorf("ollama row's base_url must be http://host:port, not %q", origin)
	}
	return Available(origin, m.ModelName)
}

// Available checks whether modelName appears in the `ollama list` output of
// the daemon at origin.
// Returns false with a nil error when ollama is not installed.
// Returns an error when `ollama list` exits non-zero.
func Available(origin, modelName string) (bool, error) {
	names, installed, err := list(origin)
	if !installed {
		return false, nil // ollama not installed — nothing is available
	}
	if err != nil {
		return false, fmt.Errorf("ollama list: %w", err)
	}
	for _, name := range names {
		if name == modelName {
			return true, nil
		}
	}
	return false, nil
}

// list returns the model names `ollama list` prints for the daemon at
// origin, and whether there is an ollama command to run at all. A seam: a
// test of anything that reaches Check swaps it with StubListForTest, and the
// TestMain of each package whose tests can reach it (cmd/wt, internal/tui)
// makes it fail, so no test runs the developer's ollama.
var list = realList

// How long `ollama list` may take before wt stops it: the same limit cmd/wt's
// ollama flow gives the same command (ollamaListTimeout), because the
// address is the registry's and may be a remote daemon. Check runs on the
// TUI's update goroutine, so a daemon that accepts the connection and then
// never answers would otherwise freeze the launcher for good. A var so a test
// can lower it; listWaitDelay bounds what the limit leaves open, exactly as
// ollamaWaitDelay does for the ollama flow's commands.
var listTimeout = 30 * time.Second

const listWaitDelay = 5 * time.Second

// realList pins the CLI to origin with OLLAMA_HOST, as `wt cloud-sync`
// (cmd/wt/cloudsync_ollama.go) and `wt stop` (internal/lifecycle/ollama.go)
// do: an OLLAMA_HOST inherited from the shell that pointed elsewhere would
// list a daemon the registry does not describe.
func realList(origin string) (names []string, installed bool, err error) {
	bin, err := exec.LookPath("ollama")
	if err != nil {
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "list")
	cmd.WaitDelay = listWaitDelay
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+origin)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, true, fmt.Errorf("timed out after %s (wt's own limit)", listTimeout)
		}
		return nil, true, err
	}
	return parseOllamaNames(string(out)), true, nil
}

// parseOllamaNames extracts the NAME column from `ollama list`/`ollama ps`
// output. Mirrors the row shape the old registry parser accepted: header
// line skipped, rows with fewer than 3 fields skipped, cloud rows (SIZE
// "-") included since a cloud model is "available" to ollama.
func parseOllamaNames(output string) []string {
	var names []string
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if i == 0 {
			continue // header row: NAME  ID  SIZE  MODIFIED
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		names = append(names, fields[0])
	}
	return names
}
