package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
)

// TestCloudSyncFlowsAreNamedForTheirProviders pins the flows' names: each is
// named for the provider it syncs, openrouter and ollama, in --only, in the
// prefix of every line and in --help. The names they had for the command's
// first two days, prices and catalog, are not aliases: they are unknown
// flows, an error that lists the valid names, so a script still using one
// stops instead of running a flow it did not ask for.
func TestCloudSyncFlowsAreNamedForTheirProviders(t *testing.T) {
	// noOllamaRegistry is one openrouter model and no ollama provider row: the
	// openrouter flow has a plan to print and the ollama flow its skip line,
	// so both prefixes are seen with one stubbed page.
	_, cfg := cloudSyncHome(t, noOllamaRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	run := func(args ...string) (string, error) {
		cmd := cloudSyncCmd(&app{cfg: cfg})
		var out bytes.Buffer
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := run("--dry-run")
	want := "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (1):\n" +
		"openrouter:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15\n" +
		"openrouter: Unchanged prices: 0\n" +
		"ollama: no ollama provider in the registry; nothing to mirror\n"
	if err != nil || out != want {
		t.Errorf("wt cloud-sync --dry-run: err = %v, output:\n%s\nwant:\n%s", err, out, want)
	}
	if out, err := run("--only", "openrouter", "--dry-run"); err != nil || !strings.HasPrefix(out, "openrouter: ") || strings.Contains(out, "ollama: ") {
		t.Errorf("--only openrouter: err = %v, output:\n%s\nwant the openrouter lines alone", err, out)
	}
	if out, err := run("--only", "ollama", "--dry-run"); err != nil || out != "ollama: no ollama provider in the registry; nothing to mirror\n" {
		t.Errorf("--only ollama: err = %v, output %q, want the ollama flow's one line", err, out)
	}
	for _, old := range []string{"prices", "catalog", "prices,catalog"} {
		first, _, _ := strings.Cut(old, ",")
		if _, err := run("--only", old, "--dry-run"); err == nil || err.Error() != `--only: unknown flow "`+first+`" (valid: openrouter, ollama)` || exitCodeOf(err) != 1 {
			t.Errorf("--only %s: err = %v (exit %d), want it refused as an unknown flow", old, err, exitCodeOf(err))
		}
	}
	help, err := run("--help")
	if err != nil || !strings.Contains(help, "\n  openrouter  OpenRouter's per-token prices") || !strings.Contains(help, "\n  ollama      https://ollama.com/pricing, mirrored") ||
		!strings.Contains(help, "a comma list of openrouter, ollama") || strings.Contains(help, "catalog flow") || strings.Contains(help, "prices flow") {
		t.Errorf("--help does not name the flows openrouter and ollama (err %v):\n%s", err, help)
	}
}
