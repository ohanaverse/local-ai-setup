package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/provider"
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

// TestCloudFlowsMatchTheirProviders holds the flow table (cloudFlows) to what
// the command line and the registry say of a flow (#453): each flow is named
// for a provider of the table in internal/provider, a name is used once,
// `--only <name>` selects that flow and no other, selecting every flow by
// default reaches each one's option, and --help describes each. A flow added
// to the table without a provider, a way to select it or a line in --help
// fails here, instead of being reachable some ways and not others.
// (TestCloudSyncFlowsAreNamedForTheirProviders spells out the two flows'
// help text, so it would not notice a third.)
func TestCloudFlowsMatchTheirProviders(t *testing.T) {
	if len(cloudFlows) == 0 || len(cloudFlows) != len(cloudSyncFlows) {
		t.Fatalf("cloudFlows has %d flows and cloudSyncFlows %v", len(cloudFlows), cloudSyncFlows)
	}
	seen := map[string]bool{}
	for i, f := range cloudFlows {
		if f.name == "" || seen[f.name] || cloudSyncFlows[i] != f.name {
			t.Errorf("flow %d (%q): empty, repeated or out of order against cloudSyncFlows %v", i, f.name, cloudSyncFlows)
		}
		seen[f.name] = true
		if _, ok := provider.Lookup(f.provider); !ok || f.provider != f.name {
			t.Errorf("flow %q syncs provider %q: want a provider of the table in internal/provider that the flow is named for", f.name, f.provider)
		}

		var only cloudSyncOpts
		if err := selectFlows(f.name, true, &only); err != nil {
			t.Fatalf("--only %s: %v", f.name, err)
		}
		if got := selectedFlows(only); len(got) != 1 || got[0] != f.name {
			t.Errorf("--only %s selected %v, want only that flow", f.name, got)
		}
	}
	var all cloudSyncOpts
	if err := selectFlows("", false, &all); err != nil {
		t.Fatal(err)
	}
	if got := selectedFlows(all); strings.Join(got, ",") != strings.Join(cloudSyncFlows, ",") {
		t.Errorf("no --only selected %v, want every flow %v", got, cloudSyncFlows)
	}

	var help bytes.Buffer
	cmd := cloudSyncCmd(&app{cfg: &config.Config{}})
	cmd.SetArgs([]string{"--help"})
	cmd.SetOut(&help)
	cmd.SetErr(&help)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, f := range cloudFlows {
		if !strings.Contains(help.String(), "\n  "+f.name+" ") {
			t.Errorf("--help has no line describing the %s flow:\n%s", f.name, help.String())
		}
	}
}

// TestPendingFlowsFollowTheTableOrder pins #475's point: the lines that
// concern every flow about to be applied ("not applied (declined)", the
// error when the question cannot be asked) come out in cloudFlows' order, and
// only for the flows that have something to apply. They used to be appended
// by hand in an order that could disagree with the table's. The order is the
// table's even when the table is reordered, which the test does.
func TestPendingFlowsFollowTheTableOrder(t *testing.T) {
	both := map[string]bool{flowOpenRouter: true, flowOllama: true}
	if got := strings.Join(pendingFlows(both), ","); got != flowOpenRouter+","+flowOllama {
		t.Errorf("pendingFlows(both) = %s, want the table's order", got)
	}
	if got := pendingFlows(map[string]bool{flowOllama: true}); len(got) != 1 || got[0] != flowOllama {
		t.Errorf("pendingFlows(ollama only) = %v", got)
	}
	if got := pendingFlows(map[string]bool{flowOpenRouter: false, flowOllama: false}); got != nil {
		t.Errorf("pendingFlows(none) = %v, want nil", got)
	}

	saved := slices.Clone(cloudFlows)
	t.Cleanup(func() { cloudFlows = saved })
	slices.Reverse(cloudFlows)
	if got := strings.Join(pendingFlows(both), ","); got != flowOllama+","+flowOpenRouter {
		t.Errorf("with the table reversed, pendingFlows = %s, want it to follow", got)
	}
}
