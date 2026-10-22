package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// vanishedRegistry is cloudSyncRegistry plus OpenRouter models: each id in
// extra is a registry row for vendor/<id>.
func vanishedRegistry(extra ...string) string {
	out := cloudSyncRegistry
	for _, id := range extra {
		out += fmt.Sprintf(`
[[models]]
id = "openrouter/vendor--%s"
family = "x"
provider_id = "openrouter"
model_name = "vendor/%s"
location = "cloud"
source = "curated"
tags = []
`, id, id)
	}
	return out
}

// healthyOpenRouterBody is an OpenRouter list long enough for its absences
// to count as removals, listing vendor/gpt at the price cloudSyncRegistry
// has, plus the given ids.
func healthyOpenRouterBody(ids ...string) string {
	items := []string{`{"id": "vendor/gpt", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}`}
	for i := 0; i < cloudsync.MinListedForRemovals; i++ {
		items = append(items, fmt.Sprintf(`{"id": "pad/model-%d", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}`, i))
	}
	for _, id := range ids {
		items = append(items, fmt.Sprintf(`{"id": "vendor/%s", "pricing": {"prompt": "0.000001", "completion": "0.000002"}}`, id))
	}
	return `{"data": [` + strings.Join(items, ",") + `]}`
}

func orDigestIn(t *testing.T, stdout string) string {
	t.Helper()
	m := regexp.MustCompile(`openrouter: Removal digest: ([0-9a-f]{12}) `).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no openrouter removal digest in:\n%s", stdout)
	}
	return m[1]
}

// TestApprovedDigests pins how --approve-removals is read: a comma list,
// spaces and empty elements tolerated. One --yes run applies both flows by
// naming both digests, so a stray space must not make a digest unmatchable.
func TestApprovedDigests(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "a, b,,c ": "a|b|c", " ,": ""} {
		if got := strings.Join(approvedDigests(in), "|"); got != want {
			t.Errorf("approvedDigests(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOpenRouterRemovalNeedsApprovalUnderYes pins the gate on the new
// removal: --yes alone, or with another plan's digest, exits 5 and changes
// nothing; with the digest a dry run printed the vanished model is removed
// and the routes are synced even though no price moved.
func TestOpenRouterRemovalNeedsApprovalUnderYes(t *testing.T) {
	path, cfg := cloudSyncHome(t, vanishedRegistry("old"))
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	synced := stubRouteSync(t, "")
	dry, _, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if code != 0 || !strings.Contains(dry, "openrouter: Registry removals — not on openrouter.ai/api/v1/models (1):\nopenrouter:   openrouter/vendor--old\n") {
		t.Fatalf("dry run exit %d:\n%s", code, dry)
	}
	digest := orDigestIn(t, dry)
	before := mustRead(t, path)

	for _, approve := range []string{"", "000000000000"} {
		_, stderr, final, code := runCSFinal(t, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: approve})
		if code != 5 || !strings.Contains(stderr, "openrouter: error: ") || !strings.Contains(stderr, "Nothing was changed by the openrouter flow.") ||
			final != "cloud-sync: the openrouter flow changed nothing: removals not approved" {
			t.Errorf("approve %q: exit %d, stderr %q, final %q", approve, code, stderr, final)
		}
		if mustRead(t, path) != before {
			t.Fatalf("approve %q: the refused run changed the registry", approve)
		}
	}

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "openrouter: refreshed 1 model(s); 0 price(s) changed; removed 1\n") {
		t.Fatalf("approved run: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	if strings.Contains(mustRead(t, path), "vendor--old") || *synced != 1 {
		t.Errorf("want the model removed and one route sync, got syncs %d", *synced)
	}
}

// TestOpenRouterMassRemovalRefusedWithoutForce pins the guard: more than
// half the removable models going is exit 4 until --force, then the digest
// still applies.
func TestOpenRouterMassRemovalRefusedWithoutForce(t *testing.T) {
	path, cfg := cloudSyncHome(t, vanishedRegistry("old1", "old2"))
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(t, "")
	dry, _, _ := runCS(t, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	digest := orDigestIn(t, dry)
	_, stderr, final, code := runCSFinal(t, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest})
	if code != 4 || !strings.Contains(stderr, "openrouter: error: 2 of 3 OpenRouter-priced models would be removed") ||
		final != "cloud-sync: the openrouter flow changed nothing: mass removal refused" {
		t.Errorf("exit %d, stderr %q, final %q", code, stderr, final)
	}
	if strings.Contains(mustRead(t, path), "vendor--old") == false {
		t.Fatal("a refused mass removal removed models")
	}
	if _, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest, force: true}); code != 0 || stderr != "" {
		t.Errorf("--force with the digest: exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(mustRead(t, path), "vendor--old") {
		t.Error("--force with the digest did not remove the models")
	}
}

// TestInteractiveDeclineRemovesNothing pins that the one prompt covers
// removals too: answering no leaves the vanished model and says so.
func TestInteractiveDeclineRemovesNothing(t *testing.T) {
	path, cfg := cloudSyncHome(t, vanishedRegistry("old"))
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(t, "")
	asked := stubConfirm(t, false, nil, nil)
	stdout, _, code := runCS(t, cfg, cloudSyncOpts{openrouter: true})
	if code != 0 || *asked != 1 || !strings.HasSuffix(stdout, "openrouter: not applied (declined)\n") {
		t.Errorf("exit %d, asked %d, stdout:\n%s", code, *asked, stdout)
	}
	if mustRead(t, path) != vanishedRegistry("old") {
		t.Error("a declined run changed the registry")
	}
}

// TestYesWithOneFlowsDigestAppliesOnlyThatFlow pins the independence of the
// two gates: with only the ollama digest, the ollama flow applies and the
// OpenRouter one is refused (exit 5, models kept); naming both digests,
// comma separated with a space, applies both in one run.
func TestYesWithOneFlowsDigestAppliesOnlyThatFlow(t *testing.T) {
	pages := ollamaPages()
	pages[cloudsync.OpenRouterModelsURL] = healthyOpenRouterBody()
	both := cloudSyncOpts{openrouter: true, ollama: true}
	// start gives the subtest a registry with one vanished OpenRouter model
	// and one off-page ollama entry, and the digests a dry run prints.
	start := func(t *testing.T) (path string, cfg *config.Config, ollamaDigest, orDigest string) {
		path, cfg = cloudSyncHome(t, vanishedRegistry("old"))
		stubCloudFetch(t, pages)
		stubOllama(t, ollamaPulled...)
		stubRouteSync(t, "")
		dry := both
		dry.dryRun = true
		stdout, _, _ := runCS(t, cfg, dry)
		return path, cfg, digestIn(t, stdout), orDigestIn(t, stdout)
	}

	t.Run("only the ollama digest", func(t *testing.T) {
		path, cfg, ollamaDigest, _ := start(t)
		o := both
		o.yes, o.approve = true, ollamaDigest
		_, stderr, code := runCS(t, cfg, o)
		text := mustRead(t, path)
		if code != 5 || !strings.Contains(stderr, "openrouter: error: ") || strings.Contains(text, "retired:cloud") || !strings.Contains(text, "vendor--old") {
			t.Errorf("exit %d, stderr %q\nwant the ollama removal applied and the OpenRouter model kept:\n%s", code, stderr, text)
		}
	})

	t.Run("both digests", func(t *testing.T) {
		path, cfg, ollamaDigest, orDigest := start(t)
		o := both
		o.yes, o.approve = true, orDigest+", "+ollamaDigest
		stdout, stderr, code := runCS(t, cfg, o)
		text := mustRead(t, path)
		if code != 0 || stderr != "" || strings.Contains(text, "retired:cloud") || strings.Contains(text, "vendor--old") {
			t.Errorf("exit %d, stderr %q, stdout:\n%s\nregistry:\n%s", code, stderr, stdout, text)
		}
	})
}

// TestDryRunPrintsCombinedApproveLine pins the line that spares the user
// pasting two digests by hand: it appears only when both flows plan
// removals.
func TestDryRunPrintsCombinedApproveLine(t *testing.T) {
	pages := ollamaPages()
	pages[cloudsync.OpenRouterModelsURL] = healthyOpenRouterBody()
	_, cfg := cloudSyncHome(t, vanishedRegistry("old"))
	stubCloudFetch(t, pages)
	stubOllama(t, ollamaPulled...)
	stdout, _, _ := runCS(t, cfg, cloudSyncOpts{openrouter: true, ollama: true, dryRun: true})
	want := "removals: approve both with --yes --approve-removals " + orDigestIn(t, stdout) + "," + digestIn(t, stdout) + "\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout)
	}

	_, cfg = cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, pages)
	stubOllama(t, ollamaPulled...)
	if stdout, _, _ := runCS(t, cfg, cloudSyncOpts{openrouter: true, ollama: true, dryRun: true}); strings.Contains(stdout, "approve both") {
		t.Errorf("one flow's removals printed the combined line:\n%s", stdout)
	}
}

// TestApproveAndForceAreAcceptedWithOnlyOpenrouter pins the flag rules after
// OpenRouter gained removals: --approve-removals and --force work with
// --only openrouter, --html still belongs to the ollama flow.
func TestApproveAndForceAreAcceptedWithOnlyOpenrouter(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: sameOpenRouterBody})
	for _, flags := range [][]string{{"--approve-removals", "abc"}, {"--force"}} {
		cmd := cloudSyncCmd(&app{cfg: cfg})
		cmd.SetArgs(append([]string{"--only", "openrouter", "--dry-run"}, flags...))
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		if err := cmd.Execute(); err != nil {
			t.Errorf("wt cloud-sync --only openrouter %v: %v", flags, err)
		}
	}
}

// TestRemovalAloneIsNotAdviceAboutRefreshing pins that when every OpenRouter
// model in the registry is removed, the output does not also say "no model
// could be refreshed": the removal is the news, not a failed refresh.
func TestRemovalAloneIsNotAdviceAboutRefreshing(t *testing.T) {
	registry := strings.Replace(vanishedRegistry("old"), `id = "openrouter/vendor--gpt"`, `id = "openrouter/vendor--gone2"`, 1)
	registry = strings.Replace(registry, `model_name = "vendor/gpt"`, `model_name = "vendor/gone2"`, 1)
	_, cfg := cloudSyncHome(t, registry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stdout, _, _ := runCS(t, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if !strings.Contains(stdout, "Registry removals") || strings.Contains(stdout, "no model could be refreshed") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

// TestWithheldRemovalsAreSaid pins that a list too short to trust prints the
// reason (the plan itself stays unchanged) and removes nothing.
func TestWithheldRemovalsAreSaid(t *testing.T) {
	path, cfg := cloudSyncHome(t, vanishedRegistry("old"))
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: sameOpenRouterBody})
	stubRouteSync(t, "")
	stdout, _, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 0 || !strings.Contains(stdout, "no removals are planned") {
		t.Errorf("exit %d, stdout:\n%s", code, stdout)
	}
	if !strings.Contains(mustRead(t, path), "vendor--old") {
		t.Error("a short list removed a model")
	}
}
