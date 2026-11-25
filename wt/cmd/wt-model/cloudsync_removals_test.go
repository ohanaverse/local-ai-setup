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
	h := testHost()
	path, cfg := cloudSyncHome(t, h, vanishedRegistry("old"))
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	synced := stubRouteSync(h, "")
	dry, _, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if code != 0 || !strings.Contains(dry, "openrouter: Registry removals — not on openrouter.ai/api/v1/models (1):\nopenrouter:   openrouter/vendor--old\n") {
		t.Fatalf("dry run exit %d:\n%s", code, dry)
	}
	digest := orDigestIn(t, dry)
	before := mustRead(t, path)

	for _, approve := range []string{"", "000000000000"} {
		_, stderr, final, code := runCSFinal(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: approve})
		if code != 5 || !strings.Contains(stderr, "openrouter: error: ") || !strings.Contains(stderr, "Nothing was changed by the openrouter flow.") ||
			final != "cloud-sync: the openrouter flow changed nothing: removals not approved" {
			t.Errorf("approve %q: exit %d, stderr %q, final %q", approve, code, stderr, final)
		}
		if mustRead(t, path) != before {
			t.Fatalf("approve %q: the refused run changed the registry", approve)
		}
	}

	stdout, stderr, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest})
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
	h := testHost()
	path, cfg := cloudSyncHome(t, h, vanishedRegistry("old1", "old2"))
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(h, "")
	dry, _, _ := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	digest := orDigestIn(t, dry)
	_, stderr, final, code := runCSFinal(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest})
	if code != 4 || !strings.Contains(stderr, "openrouter: error: 2 of 3 OpenRouter-priced models would be removed") ||
		final != "cloud-sync: the openrouter flow changed nothing: mass removal refused" {
		t.Errorf("exit %d, stderr %q, final %q", code, stderr, final)
	}
	if strings.Contains(mustRead(t, path), "vendor--old") == false {
		t.Fatal("a refused mass removal removed models")
	}
	if _, stderr, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest, force: true}); code != 0 || stderr != "" {
		t.Errorf("--force with the digest: exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(mustRead(t, path), "vendor--old") {
		t.Error("--force with the digest did not remove the models")
	}
}

// TestInteractiveDeclineRemovesNothing pins that the one prompt covers
// removals too: answering no leaves the vanished model and says so.
func TestInteractiveDeclineRemovesNothing(t *testing.T) {
	h := testHost()
	path, cfg := cloudSyncHome(t, h, vanishedRegistry("old"))
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(h, "")
	asked := stubConfirm(t, h, false, nil, nil)
	stdout, _, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true})
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
	h := testHost()
	pages := ollamaPages()
	pages[cloudsync.OpenRouterModelsURL] = healthyOpenRouterBody()
	both := cloudSyncOpts{openrouter: true, ollama: true}
	// start gives the subtest a registry with one vanished OpenRouter model
	// and one off-page ollama entry, and the digests a dry run prints.
	start := func(t *testing.T) (path string, cfg *config.Config, ollamaDigest, orDigest string) {
		path, cfg = cloudSyncHome(t, h, vanishedRegistry("old"))
		stubCloudFetch(t, h, pages)
		stubOllama(t, h, ollamaPulled...)
		stubRouteSync(h, "")
		dry := both
		dry.dryRun = true
		stdout, _, _ := runCS(t, h, cfg, dry)
		return path, cfg, digestIn(t, stdout), orDigestIn(t, stdout)
	}

	t.Run("only the ollama digest", func(t *testing.T) {
		path, cfg, ollamaDigest, _ := start(t)
		o := both
		o.yes, o.approve = true, ollamaDigest
		_, stderr, code := runCS(t, h, cfg, o)
		text := mustRead(t, path)
		if code != 5 || !strings.Contains(stderr, "openrouter: error: ") || strings.Contains(text, "retired:cloud") || !strings.Contains(text, "vendor--old") {
			t.Errorf("exit %d, stderr %q\nwant the ollama removal applied and the OpenRouter model kept:\n%s", code, stderr, text)
		}
	})

	t.Run("both digests", func(t *testing.T) {
		path, cfg, ollamaDigest, orDigest := start(t)
		o := both
		o.yes, o.approve = true, orDigest+", "+ollamaDigest
		stdout, stderr, code := runCS(t, h, cfg, o)
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
	h := testHost()
	pages := ollamaPages()
	pages[cloudsync.OpenRouterModelsURL] = healthyOpenRouterBody()
	_, cfg := cloudSyncHome(t, h, vanishedRegistry("old"))
	stubCloudFetch(t, h, pages)
	stubOllama(t, h, ollamaPulled...)
	stdout, _, _ := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, ollama: true, dryRun: true})
	want := "removals: approve both with --yes --approve-removals " + orDigestIn(t, stdout) + "," + digestIn(t, stdout) + "\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout)
	}

	_, cfg = cloudSyncHome(t, h, cloudSyncRegistry)
	stubCloudFetch(t, h, pages)
	stubOllama(t, h, ollamaPulled...)
	if stdout, _, _ := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, ollama: true, dryRun: true}); strings.Contains(stdout, "approve both") {
		t.Errorf("one flow's removals printed the combined line:\n%s", stdout)
	}
}

// TestApproveAndForceAreAcceptedWithOnlyOpenrouter pins the flag rules after
// OpenRouter gained removals: --approve-removals and --force work with
// --only openrouter, --html still belongs to the ollama flow.
func TestApproveAndForceAreAcceptedWithOnlyOpenrouter(t *testing.T) {
	h := testHost()
	_, cfg := cloudSyncHome(t, h, cloudSyncRegistry)
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: sameOpenRouterBody})
	for _, flags := range [][]string{{"--approve-removals", "abc"}, {"--force"}} {
		cmd := cloudSyncCmd(hostOn(h, cfg, nil))
		cmd.SetArgs(append([]string{"--only", "openrouter", "--dry-run"}, flags...))
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		if err := cmd.Execute(); err != nil {
			t.Errorf("wt-model cloud-sync --only openrouter %v: %v", flags, err)
		}
	}
}

// TestRemovalAloneIsNotAdviceAboutRefreshing pins that when every OpenRouter
// model in the registry is removed, the output does not also say "no model
// could be refreshed": the removal is the news, not a failed refresh.
func TestRemovalAloneIsNotAdviceAboutRefreshing(t *testing.T) {
	h := testHost()
	registry := strings.Replace(vanishedRegistry("old"), `id = "openrouter/vendor--gpt"`, `id = "openrouter/vendor--gone2"`, 1)
	registry = strings.Replace(registry, `model_name = "vendor/gpt"`, `model_name = "vendor/gone2"`, 1)
	_, cfg := cloudSyncHome(t, h, registry)
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stdout, _, _ := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if !strings.Contains(stdout, "Registry removals") || strings.Contains(stdout, "no model could be refreshed") {
		t.Errorf("stdout:\n%s", stdout)
	}
}

// TestWithheldRemovalsAreSaid pins that a list too short to trust prints the
// reason (the plan itself stays unchanged) and removes nothing.
func TestWithheldRemovalsAreSaid(t *testing.T) {
	h := testHost()
	path, cfg := cloudSyncHome(t, h, vanishedRegistry("old"))
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: sameOpenRouterBody})
	stubRouteSync(h, "")
	stdout, _, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 0 || !strings.Contains(stdout, "no removals are planned") {
		t.Errorf("exit %d, stdout:\n%s", code, stdout)
	}
	if !strings.Contains(mustRead(t, path), "vendor--old") {
		t.Error("a short list removed a model")
	}
}

// fakeRemovalPlan is the part of a price or catalog plan the shared removal
// gate reads, so the gate's own decisions can be pinned without a full plan.
type fakeRemovalPlan struct {
	gone   int
	mass   bool
	digest string
}

func (f fakeRemovalPlan) EntriesGone() int      { return f.gone }
func (f fakeRemovalPlan) MassRemoval() bool     { return f.mass }
func (f fakeRemovalPlan) RemovalDigest() string { return f.digest }

// TestRemovalGateRefusesAMassRemovalWithoutForce pins the shared gate's
// mass-removal refusal: a plan that would remove more than half its removable
// candidates without --force is refused (exit 4) and prints the flow's wording.
func TestRemovalGateRefusesAMassRemovalWithoutForce(t *testing.T) {
	var buf bytes.Buffer
	var code int
	ok := removalGate(&buf, fakeRemovalPlan{gone: 2, mass: true}, "openrouter", cloudSyncOpts{}, func(c int) { code = c }, removalGateWording{
		massNoun:  "OpenRouter-priced models",
		massTotal: 3,
		massNote:  "",
		massCheck: "OpenRouter's list is complete",
		reason:    "the plan removes models",
	})
	if ok {
		t.Error("removalGate = true, want false for a mass removal without --force")
	}
	if code != 4 {
		t.Errorf("code = %d, want 4", code)
	}
	if want := "openrouter: error: 2 of 3 OpenRouter-priced models would be removed — check OpenRouter's list is complete, then re-run with --force. Nothing was changed by the openrouter flow.\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("output = %q, want it to contain %q", buf.String(), want)
	}
}

// TestRemovalGateRefusesAnUnapprovedRemovalUnderYes pins the shared gate's
// second refusal: under --yes, a removal plan whose digest was not approved is
// refused (exit 5) and prints the flow's wording.
func TestRemovalGateRefusesAnUnapprovedRemovalUnderYes(t *testing.T) {
	var buf bytes.Buffer
	var code int
	ok := removalGate(&buf, fakeRemovalPlan{gone: 1, digest: "deadbeefcafe"}, "ollama", cloudSyncOpts{yes: true}, func(c int) { code = c }, removalGateWording{
		massNoun:  "ollama cloud entries",
		massTotal: 3,
		massNote:  "",
		massCheck: "the page parsed correctly",
		reason:    "the plan deletes models",
	})
	if ok {
		t.Error("removalGate = true, want false for an unapproved removal")
	}
	if code != 5 {
		t.Errorf("code = %d, want 5", code)
	}
	if want := "ollama: error: the plan deletes models — review a --dry-run, then re-run with `--yes --approve-removals deadbeefcafe`. Nothing was changed by the ollama flow.\n"; !strings.Contains(buf.String(), want) {
		t.Errorf("output = %q, want it to contain %q", buf.String(), want)
	}
}

// TestRemovalGateAllowsAnApprovedRemovalAndANoRemovalPlan pins the two
// ways the shared gate says yes: a removal plan under --yes whose digest was
// approved, and a plan with nothing to remove at all.
func TestRemovalGateAllowsAnApprovedRemovalAndANoRemovalPlan(t *testing.T) {
	var buf bytes.Buffer
	var code int
	ok := removalGate(&buf, fakeRemovalPlan{gone: 1, digest: "deadbeefcafe"}, "openrouter", cloudSyncOpts{yes: true, approve: "deadbeefcafe"}, func(c int) { code = c }, removalGateWording{
		massNoun:  "OpenRouter-priced models",
		massTotal: 3,
		massCheck: "OpenRouter's list is complete",
		reason:    "the plan removes models",
	})
	if !ok {
		t.Error("removalGate = false, want true for an approved removal under --yes")
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
	if ok := removalGate(&buf, fakeRemovalPlan{}, "ollama", cloudSyncOpts{}, func(c int) { code = c }, removalGateWording{
		massNoun:  "ollama cloud entries",
		massTotal: 3,
		massCheck: "the page parsed correctly",
		reason:    "the plan deletes models",
	}); !ok {
		t.Error("removalGate = false, want true for a plan with nothing to remove")
	}
}

// TestCloudSyncKeptVariantNeedsNoApprovalAndIsExplained pins the variant row
// end to end (#449 item 4): the dry run says why the row stays and what price
// it has, and `--yes` with no digest applies it, because a kept variant
// removes nothing and so has nothing to approve. The row is still in the
// registry afterwards.
func TestCloudSyncKeptVariantNeedsNoApprovalAndIsExplained(t *testing.T) {
	h := testHost()
	path, cfg := cloudSyncHome(t, h, vanishedRegistry("gpt:nitro"))
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(h, "")

	dry, stderr, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if code != 0 || stderr != "" ||
		!strings.Contains(dry, "openrouter: warning: No OpenRouter match for openrouter/vendor--gpt:nitro (vendor/gpt:nitro); its base model is listed, so it is left unpriced: :nitro bills at its own rate, not vendor/gpt's; set a price by hand\n") ||
		strings.Contains(dry, "Registry removals") {
		t.Fatalf("dry run exit %d, stderr %q:\n%s", code, stderr, dry)
	}

	_, stderr, final, code := runCSFinal(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 0 || stderr != "" || final != "" {
		t.Fatalf("--yes with a kept variant: exit %d, stderr %q, final %q; want no approval asked for", code, stderr, final)
	}
	if !strings.Contains(mustRead(t, path), `model_name = "vendor/gpt:nitro"`) {
		t.Error("the kept variant row is gone from the registry")
	}
}

// TestCloudSyncKeptVariantBesideARemovalStillNeedsTheDigest pins that a
// variant in the plan does not loosen the gate for a model that is gone: with
// both, `--yes` without the digest exits 5 and changes nothing, and with it
// only the vanished model is removed while the variant stays.
func TestCloudSyncKeptVariantBesideARemovalStillNeedsTheDigest(t *testing.T) {
	h := testHost()
	path, cfg := cloudSyncHome(t, h, vanishedRegistry("gpt:nitro", "old"))
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(h, "")

	dry, _, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if code != 0 || !strings.Contains(dry, "openrouter:   openrouter/vendor--old\n") || strings.Contains(dry, "openrouter:   openrouter/vendor--gpt:nitro\n") ||
		!strings.Contains(dry, "openrouter: Note: a model OpenRouter serves under another name") {
		t.Fatalf("dry run exit %d:\n%s", code, dry)
	}
	digest := orDigestIn(t, dry)
	before := mustRead(t, path)

	_, _, final, code := runCSFinal(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 5 || final != "cloud-sync: the openrouter flow changed nothing: removals not approved" || mustRead(t, path) != before {
		t.Fatalf("unapproved run: exit %d, final %q, registry changed %v", code, final, mustRead(t, path) != before)
	}

	_, stderr, _, code := runCSFinal(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest})
	after := mustRead(t, path)
	if code != 0 || stderr != "" || strings.Contains(after, "vendor--old") || !strings.Contains(after, `model_name = "vendor/gpt:nitro"`) {
		t.Errorf("approved run: exit %d, stderr %q; want old removed and the variant kept:\n%s", code, stderr, after)
	}
}

// TestCloudSyncKeepsAnAliasTargetAndWarns pins Review Focus 5 for cloud-sync
// (decision 11): in both flows a model an alias targets is kept out of the
// removal set while planning, with a `kept <id>: alias/<x> targets it`
// warning; the dry run's digests are the ones the `--yes --approve-removals`
// run accepts (a mismatch would be exit 5); that run removes the other
// vanished model, keeps both targets and never runs `ollama rm` on the kept
// entry's tag. Before, an approved removal of an alias target aborted the
// whole write on RemoveModel's ErrModelInUse.
func TestCloudSyncKeepsAnAliasTargetAndWarns(t *testing.T) {
	h := testHost()
	registry := vanishedRegistry("old", "gone") + `
[[aliases]]
id = "alias/x"
targets = [{ model = "openrouter/vendor--old", order = 1 }, { model = "ollama/retired:cloud", order = 2 }]
`
	path, cfg := cloudSyncHome(t, h, registry)
	pages := ollamaPages()
	pages[cloudsync.OpenRouterModelsURL] = healthyOpenRouterBody()
	stubCloudFetch(t, h, pages)
	ollama := stubOllama(t, h, ollamaPulled...)
	stubRouteSync(h, "")

	dry, _, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, ollama: true, dryRun: true})
	for _, want := range []string{
		"openrouter: warning: kept openrouter/vendor--old: alias/x targets it\n",
		"ollama: warning: kept ollama/retired:cloud: alias/x targets it\n",
		"openrouter:   openrouter/vendor--gone\n",
	} {
		if !strings.Contains(dry, want) {
			t.Errorf("dry run lacks %q:\n%s", want, dry)
		}
	}
	if code != 0 || strings.Contains(dry, "openrouter:   openrouter/vendor--old\n") || strings.Contains(dry, "ollama:   ollama/retired:cloud\n") {
		t.Fatalf("dry run exit %d plans an alias target's removal:\n%s", code, dry)
	}
	approve := orDigestIn(t, dry) + "," + digestIn(t, dry)
	ollama.calls = nil
	stdout, stderr, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, ollama: true, yes: true, approve: approve})
	if code != 0 {
		t.Fatalf("approved run: exit %d, stderr %q\n%s", code, stderr, stdout)
	}
	text := mustRead(t, path)
	if strings.Contains(text, "vendor--gone") || !strings.Contains(text, `id = "openrouter/vendor--old"`) || !strings.Contains(text, `id = "ollama/retired:cloud"`) {
		t.Errorf("want vendor--gone removed and both alias targets kept:\n%s", text)
	}
	for _, call := range ollama.changes() {
		if call == "rm retired:cloud" {
			t.Errorf("ollama rm ran on the kept entry's tag: %q", ollama.changes())
		}
	}
}

// TestKeptAliasTargetNeitherRemovesNorDilutesTheMassGuard pins how the
// mass-removal guard counts an alias target the plan keeps (#691, rechecking
// #686): it is not a removal, and it does not count among the removable
// candidates either, so it can neither trip the guard nor dilute it.
// Registry: gpt (listed), old1 and old2 (vanished), kept (vanished, targeted
// by an alias). The guard must read 2 of 3 and refuse without --force. If the
// kept target were counted as a removal it would read 3 of 4 (still refused,
// and the digest would name it); if it sat in the denominator it would read
// 2 of 4, which is not more than half, and an approved run would delete two
// of the three models that left OpenRouter's list without --force. (The
// issue text and plan said "counted in the denominator"; the shipped rule,
// the doc comment on RemovableCandidates and this test say excluded, which is
// the conservative reading.)
func TestKeptAliasTargetNeitherRemovesNorDilutesTheMassGuard(t *testing.T) {
	h := testHost()
	registry := vanishedRegistry("old1", "old2", "kept") + `
[[aliases]]
id = "alias/x"
targets = [{ model = "openrouter/vendor--kept", order = 1 }]
`
	path, cfg := cloudSyncHome(t, h, registry)
	stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: healthyOpenRouterBody()})
	stubRouteSync(h, "")
	dry, _, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	if code != 0 || strings.Contains(dry, "openrouter:   openrouter/vendor--kept\n") {
		t.Fatalf("dry run exit %d plans the alias target's removal:\n%s", code, dry)
	}
	digest := orDigestIn(t, dry)
	_, stderr, _, code := runCSFinal(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest})
	if code != 4 || !strings.Contains(stderr, "openrouter: error: 2 of 3 OpenRouter-priced models would be removed") {
		t.Fatalf("approved run without --force: exit %d, stderr %q; want the guard to read 2 of 3", code, stderr)
	}
	if _, stderr, code := runCS(t, h, cfg, cloudSyncOpts{openrouter: true, yes: true, approve: digest, force: true}); code != 0 || stderr != "" {
		t.Fatalf("--force with the digest: exit %d, stderr %q", code, stderr)
	}
	text := mustRead(t, path)
	if strings.Contains(text, "vendor--old") || !strings.Contains(text, `id = "openrouter/vendor--kept"`) {
		t.Errorf("want old1 and old2 removed and the alias target kept:\n%s", text)
	}
}
