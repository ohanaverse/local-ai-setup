package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// catalogPage is a small ollama.com/pricing: five models, one with an
// off-peak row. Of cloudSyncRegistry's two ollama cloud entries it lists
// deepseek-v4-pro and not retired.
const catalogPage = `<html><table>
<thead><tr><th>Model</th><th>Input</th><th>Cached input</th><th>Output</th></tr></thead>
<tbody>
<tr><td><a href="/library/deepseek-v4-pro">deepseek-v4-pro</a></td><td>$1.32</td><td>$0.044</td><td>$3.96</td></tr>
<tr><td>deepseek-v4-pro (Off-Peak)</td><td>$0.66</td><td>$0.022</td><td>$1.98</td></tr>
<tr><td><a href="/library/glm-5.3">glm-5.3</a></td><td>$1.40</td><td>$0.26</td><td>$4.40</td></tr>
<tr><td><a href="/library/gemma4">gemma4</a></td><td>$0.14</td><td>$0.05</td><td>$0.40</td></tr>
<tr><td><a href="/library/kimi-k3">kimi-k3</a></td><td>$3.00</td><td>$0.30</td><td>$15.00</td></tr>
<tr><td><a href="/library/gpt-oss">gpt-oss:120b</a></td><td>$0.15</td><td>$0.014</td><td>$0.60</td></tr>
</tbody></table></html>`

// catalogPages is everything the catalog flow fetches for catalogPage: the
// page, and a library tags page for each bare name (gpt-oss:120b pins its
// size and needs none). kimi-k3 publishes only a sized cloud tag.
func catalogPages() map[string]string {
	lib := func(name string, tags ...string) string {
		var b strings.Builder
		for _, tag := range tags {
			fmt.Fprintf(&b, `<a href="/library/%s:%s">%s</a>`, name, tag, tag)
		}
		return b.String()
	}
	return map[string]string{
		cloudsync.PricingURL:                              catalogPage,
		"https://ollama.com/library/deepseek-v4-pro/tags": lib("deepseek-v4-pro", "cloud"),
		"https://ollama.com/library/glm-5.3/tags":         lib("glm-5.3", "cloud", "latest"),
		"https://ollama.com/library/gemma4/tags":          lib("gemma4", "cloud", "9b"),
		"https://ollama.com/library/kimi-k3/tags":         lib("kimi-k3", "1t-cloud"),
	}
}

// bothPages is catalogPages plus OpenRouter's model list, for a run of both
// flows.
func bothPages() map[string]string {
	pages := catalogPages()
	pages[cloudsync.OpenRouterModelsURL] = openRouterBody
	return pages
}

// catalogPulled is what `ollama list` shows in these tests: the two
// registered cloud stubs, a stray one, and a real local model.
var catalogPulled = []string{"deepseek-v4-pro:cloud", "retired:cloud", "stray:cloud", "qwen3:8b"}

func digestIn(t *testing.T, stdout string) string {
	t.Helper()
	m := regexp.MustCompile(`catalog: Removal digest: ([0-9a-f]{12}) `).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("no removal digest in:\n%s", stdout)
	}
	return m[1]
}

const catalogPlanText = `catalog: ollama.com/pricing: 5 models (prices are input/cached/output per million tokens)
catalog: Price updates (1):
catalog:   ollama/deepseek-v4-pro:cloud: 9/-/- -> 1.32/0.044/3.96 (off-peak 0.66/0.022/1.98)
catalog: Registry additions (4):
catalog:   ollama/glm-5.3:cloud [family glm-5.3]: 1.4/0.26/4.4
catalog:   ollama/gemma4:cloud [family gemma4]: 0.14/0.05/0.4
catalog:   ollama/kimi-k3:1t-cloud [family kimi-k3]: 3/0.3/15
catalog:   ollama/gpt-oss:120b-cloud [family gpt-oss]: 0.15/0.014/0.6
catalog: Unchanged prices: 0
catalog: ollama pull (4):
catalog:   ollama/glm-5.3:cloud
catalog:   ollama/gemma4:cloud
catalog:   ollama/kimi-k3:1t-cloud
catalog:   ollama/gpt-oss:120b-cloud
catalog: Registry removals — off ollama.com/pricing or under a tag ollama doesn't publish; ` + "`ollama rm`" + ` if pulled (1):
catalog:   ollama/retired:cloud
catalog: ollama rm — pulled, unregistered, off the page (1):
catalog:   stray:cloud
catalog: Removal digest: DIGEST (apply non-interactively with ` + "`--yes --approve-removals DIGEST`" + `)
catalog: warning: ollama cloud entries disagree on subscription pricing; new entries get none
`

// TestCloudSyncCatalogDryRun pins the catalog flow's dry run: the whole plan
// under the catalog: prefix, with the removal digest the real run will ask
// for, and nothing changed — the registry byte-identical, and ollama asked
// only for its list, pinned to the provider row's address.
func TestCloudSyncCatalogDryRun(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	want := strings.ReplaceAll(catalogPlanText, "DIGEST", digestIn(t, stdout))
	if stdout != want || stderr != "" || code != 0 {
		t.Errorf("stdout:\n%s\nstderr: %q, exit %d\nwant stdout:\n%s", stdout, stderr, code, want)
	}
	if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
		t.Error("a dry run changed the registry or synced the routes")
	}
	if want := []string{testOllamaOrigin + " list"}; !reflect.DeepEqual(ollama.calls, want) {
		t.Errorf("ollama commands = %q, want only %q", ollama.calls, want)
	}
}

// TestCloudSyncCatalogApplyUnderTheApprovedDigest pins the path the skill
// takes: a dry run, then --yes with the digest it printed. The registry gets
// the page's prices, the new models and loses the retired one; then, and
// only then, ollama is asked to pull what is missing and to remove the
// retired stub and the stray one, every command pinned to the provider row's
// address; and the routes are synced once, at the end.
func TestCloudSyncCatalogApplyUnderTheApprovedDigest(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	fetches := stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	var order []string
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(out, _ io.Writer) string {
		order = append(order, fmt.Sprintf("sync after %d ollama commands", len(ollama.calls)))
		fmt.Fprintln(out, "ollama/glm-5.3:cloud: routed")
		return ""
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })

	dry, _, _ := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	ollama.calls = nil
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, yes: true, approve: digestIn(t, dry)})
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q\nstdout:\n%s", code, stderr, stdout)
	}
	wantTail := "catalog: updated 1, added 4 and removed 1 model(s)\n" +
		"catalog: pulled glm-5.3:cloud\ncatalog: pulled gemma4:cloud\ncatalog: pulled kimi-k3:1t-cloud\ncatalog: pulled gpt-oss:120b-cloud\n" +
		"catalog: removed retired:cloud\ncatalog: removed stray:cloud\n" +
		"routes: ollama/glm-5.3:cloud: routed\n"
	if !strings.HasSuffix(stdout, wantTail) {
		t.Errorf("stdout ends:\n%s\nwant it to end:\n%s", stdout, wantTail)
	}
	wantCalls := []string{
		testOllamaOrigin + " list",
		testOllamaOrigin + " pull glm-5.3:cloud", testOllamaOrigin + " pull gemma4:cloud",
		testOllamaOrigin + " pull kimi-k3:1t-cloud", testOllamaOrigin + " pull gpt-oss:120b-cloud",
		testOllamaOrigin + " rm retired:cloud", testOllamaOrigin + " rm stray:cloud",
	}
	if !reflect.DeepEqual(ollama.calls, wantCalls) {
		t.Errorf("ollama commands:\n%q\nwant\n%q", ollama.calls, wantCalls)
	}
	if want := []string{"sync after 7 ollama commands"}; !reflect.DeepEqual(order, want) {
		t.Errorf("route syncs = %q, want one, after every ollama command", order)
	}
	text := mustRead(t, path)
	for _, snippet := range []string{
		"id = \"ollama/kimi-k3:1t-cloud\"\nfamily = \"kimi-k3\"\nprovider_id = \"ollama\"\nmodel_name = \"kimi-k3:1t-cloud\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at = \"" + cloudSyncStamp + "\"\n",
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.96\nsubscription_price = 100\nsubscription_period = \"month\"\n",
		"label = \"off-peak\"\ntimezone = \"UTC\"\ninput_price_per_million = 0.66\n",
		"[[models]]\nid = \"ollama/qwen3:8b\"\nfamily = \"qwen3\"\nprovider_id = \"ollama\"\nmodel_name = \"qwen3:8b\"\nlocation = \"local\"\nsource = \"curated\"\ntags = []\n",
	} {
		if !strings.Contains(text, snippet) {
			t.Errorf("registry.toml lacks:\n%s\n\nfile:\n%s", snippet, text)
		}
	}
	if strings.Contains(text, "retired:cloud") {
		t.Error("ollama/retired:cloud is still in the registry")
	}

	// The mirror is now in step: the same page plans nothing and asks nothing.
	ollama.tags = []string{"deepseek-v4-pro:cloud", "qwen3:8b", "glm-5.3:cloud", "gemma4:cloud", "kimi-k3:1t-cloud", "gpt-oss:120b-cloud"}
	ollama.calls, order = nil, nil
	before := len(fetches.all())
	stdout, stderr, code = runCS(t, cfg, cloudSyncOpts{catalog: true, yes: true})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "catalog: Unchanged prices: 5\n") || len(ollama.changes()) != 0 || len(order) != 0 || mustRead(t, path) != text {
		t.Errorf("a second run was not a no-op: exit %d, stderr %q, ollama %q, syncs %q\n%s", code, stderr, ollama.changes(), order, stdout)
	}
	// Every tag is now pulled and recorded, so none is looked up again: the
	// second run fetched the pricing page and nothing else.
	if again := fetches.all()[before:]; !reflect.DeepEqual(again, []string{cloudsync.PricingURL}) {
		t.Errorf("the second run fetched %v, want only the pricing page", again)
	}
}

// TestCloudSyncCatalogChangesNothingAndSaysWhy walks every way the catalog
// flow stops before changing anything, and pins the exit code a caller (the
// cloud-sync skill) branches on: 2 for an input that could not be read, 3
// for a page that changed shape (with its HTML saved for the repair), 4 for a
// mass removal, 5 for removals nobody approved. In each the registry is
// byte-identical, ollama was asked for nothing but its list, and the
// command's own error (the last line main prints) says the catalog flow
// changed nothing and why.
func TestCloudSyncCatalogChangesNothingAndSaysWhy(t *testing.T) {
	massRegistry := cloudSyncRegistry + `
[[models]]
id = "ollama/retired2:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired2:cloud"
location = "cloud"
source = "curated"
tags = []
`
	// A page of bare names only, with ollama.com/library unreachable.
	allBare := map[string]string{cloudsync.PricingURL: strings.Replace(catalogPage, ">gpt-oss:120b<", ">gpt-oss<", 1)}
	cases := []struct {
		name     string
		registry string
		pages    map[string]string
		listFail string
		opts     cloudSyncOpts
		code     int
		stderr   string
	}{
		{name: "the page cannot be fetched", pages: map[string]string{}, opts: cloudSyncOpts{yes: true}, code: 2,
			stderr: "catalog: error: could not fetch ollama.com/pricing: HTTP 404; nothing was changed\n"},
		{name: "--html names a file that is not there", pages: catalogPages(), opts: cloudSyncOpts{yes: true, htmlFile: "/nonexistent/page.html"}, code: 2,
			stderr: "catalog: error: cannot read /nonexistent/page.html: open /nonexistent/page.html: no such file or directory; nothing was changed\n"},
		{name: "ollama list fails", pages: catalogPages(), listFail: "Error: could not connect to ollama server", opts: cloudSyncOpts{yes: true}, code: 2,
			stderr: "catalog: error: could not run `ollama list` against " + testOllamaOrigin + " (is the ollama daemon up?): Error: could not connect to ollama server; nothing was changed\n"},
		{name: "no cloud tag resolves", pages: allBare, opts: cloudSyncOpts{yes: true}, code: 2,
			stderr: "catalog:   deepseek-v4-pro: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   glm-5.3: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   gemma4: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   kimi-k3: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog:   gpt-oss: could not read its ollama.com/library tags (HTTP 404); skipped\n" +
				"catalog: error: could not resolve a cloud tag for any model on ollama.com/library; nothing was changed\n"},
		{name: "the page changed shape", pages: map[string]string{cloudsync.PricingURL: "<html><p>pricing moved</p></html>"}, opts: cloudSyncOpts{yes: true}, code: 3,
			stderr: "catalog: error: could not parse ollama.com/pricing: no <table> found on the page\n" +
				"catalog: raw HTML saved to TMPDIR/ollama-pricing-20261007-090000.html — the parser to update is wt/internal/cloudsync/pricingpage.go; nothing was changed\n"},
		{name: "more than half the cloud entries would go", registry: massRegistry, pages: catalogPages(), opts: cloudSyncOpts{yes: true, approve: "anything"}, code: 4,
			stderr: "catalog: error: 2 of 3 ollama cloud entries would be removed — check the page parsed correctly, then re-run with --force. Nothing was changed for the catalog.\n"},
		{name: "--yes with no digest", pages: catalogPages(), opts: cloudSyncOpts{yes: true}, code: 5,
			stderr: "catalog: error: the plan deletes models — review a --dry-run, then re-run with `--yes --approve-removals DIGEST`. Nothing was changed for the catalog.\n"},
		{name: "--yes with another plan's digest", pages: catalogPages(), opts: cloudSyncOpts{yes: true, approve: "000000000000"}, code: 5,
			stderr: "catalog: error: the removals are not the ones digest 000000000000 approved — review a --dry-run, then re-run with `--yes --approve-removals DIGEST`. Nothing was changed for the catalog.\n"},
		{name: "--force does not stand in for the digest", registry: massRegistry, pages: catalogPages(), opts: cloudSyncOpts{yes: true, force: true}, code: 5,
			stderr: "catalog: error: the plan deletes models — review a --dry-run, then re-run with `--yes --approve-removals DIGEST`. Nothing was changed for the catalog.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := tc.registry
			if registry == "" {
				registry = cloudSyncRegistry
			}
			path, cfg := cloudSyncHome(t, registry)
			stubCloudFetch(t, tc.pages)
			ollama := stubOllama(t, catalogPulled...)
			if tc.listFail != "" {
				ollama.fail["list"] = tc.listFail
			}
			synced := stubRouteSync(t, "")
			tc.opts.catalog = true

			stdout, stderr, final, code := runCSFinal(t, cfg, tc.opts)
			if want := "cloud-sync: the catalog flow changed nothing: " + map[int]string{
				2: "an input could not be read", 3: "the pricing page changed shape", 4: "mass removal refused", 5: "removals not approved",
			}[tc.code]; final != want {
				t.Errorf("the command's error = %q, want %q", final, want)
			}
			want := strings.ReplaceAll(tc.stderr, "TMPDIR", os.TempDir())
			if strings.Contains(want, "DIGEST") {
				want = strings.ReplaceAll(want, "DIGEST", digestIn(t, stdout))
			}
			if code != tc.code || stderr != want {
				t.Errorf("exit %d, stderr:\n%s\nwant exit %d, stderr:\n%s", code, stderr, tc.code, want)
			}
			if mustRead(t, path) != registry || len(ollama.changes()) != 0 || *synced != 0 {
				t.Errorf("the refused run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
			}
			if tc.code == 3 {
				saved := filepath.Join(os.TempDir(), "ollama-pricing-20261007-090000.html")
				if got := mustRead(t, saved); got != tc.pages[cloudsync.PricingURL] {
					t.Errorf("the saved page is not the page that was served: %q", got)
				}
			}
		})
	}
}

// TestCloudSyncCatalogWithTheLibraryDown pins the run where ollama.com's
// pricing page answers and its library pages do not. A name that pins its
// size still resolves, so the flow goes on, and every model whose tag could
// not be read is treated as still on the page: its entry keeps getting
// prices, nothing is added or pulled under a guessed tag, and nothing that
// may be it is removed. An outage must never be read as "these models are
// gone".
func TestCloudSyncCatalogWithTheLibraryDown(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.PricingURL: catalogPage})
	stubOllama(t, "deepseek-v4-pro:cloud", "glm-5.3:9b-cloud", "stray:cloud")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, line := range []string{
		"catalog: Price updates (1):\ncatalog:   ollama/deepseek-v4-pro:cloud: ",
		"catalog: Registry additions (1):\ncatalog:   ollama/gpt-oss:120b-cloud ",
		"catalog: ollama pull (1):\ncatalog:   ollama/gpt-oss:120b-cloud\n",
		// retired is on no page and still goes; glm-5.3's pulled stub, whose
		// name is on the page, is not a stray.
		"`ollama rm` if pulled (1):\ncatalog:   ollama/retired:cloud\n",
		"off the page (1):\ncatalog:   stray:cloud\n",
		"catalog: warning: glm-5.3: could not read its ollama.com/library tags (HTTP 404); skipped\n",
	} {
		if !strings.Contains(stdout, line) {
			t.Errorf("the plan lacks %q:\n%s", line, stdout)
		}
	}
	if mustRead(t, path) != cloudSyncRegistry {
		t.Error("a dry run changed the registry")
	}
}

// TestCloudSyncCatalogForceAppliesAMassRemoval pins the way through exit 4:
// with --force and the digest, the plan that removes most cloud entries is
// applied. --force is an answer to one question only (is this many removals
// right?) and the digest is still needed.
func TestCloudSyncCatalogForceAppliesAMassRemoval(t *testing.T) {
	registry := strings.Replace(cloudSyncRegistry, "id = \"ollama/deepseek-v4-pro:cloud\"", "id = \"ollama/also-retired:cloud\"", 1)
	registry = strings.Replace(registry, "model_name = \"deepseek-v4-pro:cloud\"", "model_name = \"also-retired:cloud\"", 1)
	path, cfg := cloudSyncHome(t, registry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t)
	stubRouteSync(t, "")

	dry, _, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true})
	if code != 0 {
		t.Fatalf("a dry run of a mass removal exits %d, want 0: it only prints", code)
	}
	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, yes: true, force: true, approve: digestIn(t, dry)})
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if text := mustRead(t, path); strings.Contains(text, "retired") || !strings.Contains(text, "ollama/deepseek-v4-pro:cloud") {
		t.Errorf("the forced plan was not applied:\n%s", text)
	}
	// Neither removed entry was pulled, so there is nothing to rm.
	if got := ollama.changes(); len(got) != 5 || strings.Contains(strings.Join(got, " "), "rm ") {
		t.Errorf("ollama commands = %q, want the five pulls and no rm", got)
	}
}

// TestCloudSyncFlowsAreIndependent pins that neither flow's trouble stops
// the other, and that a catalog code wins the exit status: the catalog is
// refused for want of a digest (5) while the prices flow, in the same run,
// is applied and its routes synced; and when the prices fetch fails (1) in a
// run whose catalog is refused, the exit status is still the catalog's.
func TestCloudSyncFlowsAreIndependent(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	pages := catalogPages()
	pages[cloudsync.OpenRouterModelsURL] = openRouterBody
	stubCloudFetch(t, pages)
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")

	stdout, _, code := runCS(t, cfg, cloudSyncOpts{prices: true, catalog: true, yes: true})
	if code != 5 || !strings.HasSuffix(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") {
		t.Errorf("exit %d, want 5 with the prices applied; stdout ends %q", code, stdout[max(0, len(stdout)-80):])
	}
	text := mustRead(t, path)
	if !strings.Contains(text, "input_price_per_million = 3.0") || !strings.Contains(text, "ollama/retired:cloud") || strings.Contains(text, "glm-5.3") {
		t.Errorf("want the OpenRouter price applied and the catalog untouched:\n%s", text)
	}
	if *synced != 1 || len(ollama.changes()) != 0 {
		t.Errorf("syncs = %d, ollama = %q; want one sync for the price and no ollama command", *synced, ollama.changes())
	}
	if strings.Index(stdout, "prices: openrouter.ai:") > strings.Index(stdout, "catalog: ollama.com/pricing:") {
		t.Error("the plans are not printed prices first, then catalog")
	}

	delete(pages, cloudsync.OpenRouterModelsURL)
	_, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, catalog: true, yes: true})
	if code != 5 || !strings.HasPrefix(stderr, "prices: error: could not read OpenRouter's prices: HTTP 404") {
		t.Errorf("exit %d, stderr %q; want the catalog's 5 over the prices flow's 1", code, stderr)
	}
}

// TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock pins the last
// gate. The plan is made again under the registry lock, and if it is no
// longer the plan that was printed and approved (here another program adds a
// cloud entry, which the re-plan would remove, while the user reads the
// question) nothing is applied and the exit status is 5. Without this a
// model nobody saw in the plan could be removed from the registry and from
// ollama.
func TestCloudSyncCatalogRefusesAPlanThatChangedUnderTheLock(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")
	raced := cloudSyncRegistry + `
[[models]]
id = "ollama/late:cloud"
family = "late"
provider_id = "ollama"
model_name = "late:cloud"
location = "cloud"
`
	stubConfirm(t, true, nil, func() {
		if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	want := "catalog: error: the registry changed after the plan was printed, so this is no longer the plan that was approved; nothing was changed — run it again\n"
	if code != 5 || stderr != want {
		t.Errorf("exit %d, stderr %q\nwant exit 5, stderr %q", code, stderr, want)
	}
	if mustRead(t, path) != raced || len(ollama.changes()) != 0 || *synced != 0 {
		t.Errorf("the refused run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
	}

	// A change that leaves the plan as it was (a local model added) is not a
	// reason to refuse, and the other program's row survives the write.
	path, cfg = cloudSyncHome(t, cloudSyncRegistry)
	harmless := cloudSyncRegistry + "\n[[models]]\nid = \"ollama/late:7b\"\nfamily = \"late\"\nprovider_id = \"ollama\"\nmodel_name = \"late:7b\"\nlocation = \"local\"\n"
	stubConfirm(t, true, nil, func() {
		if err := os.WriteFile(path, []byte(harmless), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if _, stderr, code = runCS(t, cfg, cloudSyncOpts{catalog: true}); code != 0 || stderr != "" {
		t.Fatalf("with an unrelated row added: exit %d, stderr %q", code, stderr)
	}
	if text := mustRead(t, path); !strings.Contains(text, "ollama/late:7b") || !strings.Contains(text, "ollama/glm-5.3:cloud") || strings.Contains(text, "retired") {
		t.Errorf("want the plan applied and the other program's row kept:\n%s", text)
	}
}

// TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns pins what a
// failed pull or rm costs: an error line and exit 1, with every other
// command still run and the routes still synced. The registry is already
// written by then, so stopping at the first failure would leave more out of
// step, not less; the next run redoes only what is still missing. A tag that
// is already gone ("not found") is not a failure. A tag whose rm failed is
// named as left behind, with the way back in: a new dry run, because the
// digest that approved this run may not be the next run's.
func TestCloudSyncCatalogOllamaFailuresExit1AndTheRestStillRuns(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	ollama.fail["pull gemma4:cloud"] = "Error: pull model manifest: 401 unauthorized"
	ollama.fail["rm retired:cloud"] = "Error: model 'retired:cloud' not found"
	ollama.fail["rm stray:cloud"] = "Error: permission denied"
	synced := stubRouteSync(t, "")
	stubConfirm(t, true, nil, nil)

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	wantErr := "catalog: error: `ollama pull gemma4:cloud` failed: Error: pull model manifest: 401 unauthorized\n" +
		"catalog: error: `ollama rm stray:cloud` failed: Error: permission denied\n" +
		"catalog:   stray:cloud is left pulled; the next run lists it as a stray tag — start again from --dry-run, since the removal digest may have changed\n"
	if code != 1 || stderr != wantErr {
		t.Errorf("exit %d, stderr:\n%s\nwant exit 1, stderr:\n%s", code, stderr, wantErr)
	}
	for _, line := range []string{"catalog: pulled glm-5.3:cloud\n", "catalog: pulled gpt-oss:120b-cloud\n", "catalog: removed retired:cloud\n"} {
		if !strings.Contains(stdout, line) {
			t.Errorf("stdout lacks %q:\n%s", line, stdout)
		}
	}
	if got := ollama.changes(); len(got) != 6 || *synced != 1 {
		t.Errorf("ollama commands = %q, syncs = %d; want all six commands attempted and one sync", got, *synced)
	}
}

// noOllamaRegistry is a registry that does not use ollama: the openrouter
// provider and its one model.
var noOllamaRegistry = cloudSyncRegistry[strings.Index(cloudSyncRegistry, "[[providers]]\nid = \"openrouter\""):strings.Index(cloudSyncRegistry, "[[models]]\nid = \"ollama/deepseek-v4-pro:cloud\"")]

// TestCloudSyncSkipsTheCatalogOnARegistryWithoutOllama pins the plain
// `wt cloud-sync` on a machine that has no ollama provider row, which is
// every machine that uses only OpenRouter, and the command the stale-price
// notice tells its user to run. There is no catalog to mirror, so the
// catalog flow says so and is skipped: the prices are refreshed, nothing is
// fetched from ollama.com, no ollama command runs, and the exit status is 0.
// A run that "failed" every time for want of a provider nobody uses would
// teach people to ignore the exit status.
func TestCloudSyncSkipsTheCatalogOnARegistryWithoutOllama(t *testing.T) {
	path, cfg := cloudSyncHome(t, noOllamaRegistry)
	got := stubCloudFetch(t, bothPages())
	ollama := stubOllama(t)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{prices: true, catalog: true, yes: true})
	wantTail := "catalog: no ollama provider in the registry; nothing to mirror\n" +
		"prices: refreshed 1 model(s); 1 price(s) changed\n"
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, wantTail) {
		t.Errorf("exit %d, stderr %q, stdout:\n%s\nwant exit 0 and stdout ending:\n%s", code, stderr, stdout, wantTail)
	}
	if !strings.Contains(mustRead(t, path), "input_price_per_million = 3.0") || *synced != 1 {
		t.Errorf("the prices flow was not applied (syncs = %d)", *synced)
	}
	if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) || len(ollama.calls) != 0 {
		t.Errorf("fetched %v and ran %q; want OpenRouter's list only and no ollama command", urls, ollama.calls)
	}
}

// TestCloudSyncSkipsTheCatalogHoweverItIsAskedFor pins the same registry
// when the catalog flow was asked for by name: --only naming it, or one of
// its own flags (--html, --approve-removals, --force). A registry with no
// ollama provider row has no catalog, whoever asks: the flow prints the one
// nothing-to-mirror line on stdout and contributes exit 0. Nothing is fetched
// from ollama.com, the --html file is not even opened, no ollama command
// runs, and the registry is byte-identical. `wt cloud-sync` is two syncs, one
// per provider, and a sync whose provider the registry does not use is never
// an error: a script that runs `wt cloud-sync --only catalog --yes` on every
// machine must not fail on the ones that have no ollama.
func TestCloudSyncSkipsTheCatalogHoweverItIsAskedFor(t *testing.T) {
	const skip = "catalog: no ollama provider in the registry; nothing to mirror\n"
	for _, tc := range []struct {
		args []string
		// alone: the catalog is the only flow, so the skip line is all of
		// stdout and nothing at all is fetched.
		alone bool
	}{
		{[]string{"--only", "catalog"}, true},
		{[]string{"--only", "catalog", "--yes"}, true},
		{[]string{"--only", "catalog", "--dry-run"}, true},
		{[]string{"--only", "catalog", "--html", "/nonexistent/page.html", "--yes"}, true},
		{[]string{"--only", "catalog", "--yes", "--approve-removals", "abc"}, true},
		{[]string{"--only", "catalog", "--yes", "--force"}, true},
		{[]string{"--only", "prices,catalog", "--dry-run"}, false},
		{[]string{"--dry-run"}, false},
		{[]string{"--dry-run", "--force"}, false},
		{[]string{"--dry-run", "--html", "/nonexistent/page.html"}, false},
		{[]string{"--dry-run", "--approve-removals", "abc"}, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			path, cfg := cloudSyncHome(t, noOllamaRegistry)
			got := stubCloudFetch(t, bothPages())
			ollama := stubOllama(t)
			asked := stubConfirm(t, true, nil, nil)
			synced := stubRouteSync(t, "")

			cmd := cloudSyncCmd(&app{cfg: cfg})
			var out, errOut bytes.Buffer
			cmd.SetArgs(tc.args)
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			if err := cmd.Execute(); err != nil {
				t.Errorf("err = %v (exit %d), want exit 0", err, exitCodeOf(err))
			}
			if errOut.String() != "" || !strings.HasSuffix(out.String(), skip) {
				t.Errorf("stdout = %q\nstderr = %q\nwant stdout ending with the nothing-to-mirror line and no stderr", out.String(), errOut.String())
			}
			if tc.alone && out.String() != skip {
				t.Errorf("stdout = %q, want only %q", out.String(), skip)
			}
			for _, url := range got.all() {
				if tc.alone || url != cloudsync.OpenRouterModelsURL {
					t.Errorf("the skipped catalog flow fetched %s", url)
				}
			}
			if len(ollama.calls) != 0 || *asked != 0 || *synced != 0 || mustRead(t, path) != noOllamaRegistry {
				t.Errorf("ran %q, asked %d time(s), synced %d time(s), or changed the registry; want none of them", ollama.calls, *asked, *synced)
			}
		})
	}
}

// neitherRegistry is a registry that uses neither service: no ollama provider
// row and no model OpenRouter prices (its one model is on a native provider).
const neitherRegistry = `[[providers]]
id = "claude"
name = "Claude"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "claude/opus"
family = "opus"
provider_id = "claude"
model_name = "opus"
location = "cloud"
source = "curated"
tags = []
`

// TestCloudSyncWithNeitherProviderDoesNothing pins `wt cloud-sync` on a
// registry that uses neither ollama nor OpenRouter, however it is run (plain,
// --yes, --dry-run): the two skip lines, exit 0, and nothing else at all — no
// page fetched, no ollama command, no question, no registry write and no
// lock file beside the registry, no route sync. Both flows are skipped, not
// failed: such a machine must be able to run the command from a script
// without it stopping for want of a terminal, restarting the LiteLLM proxy,
// or reporting an error for services it does not use.
func TestCloudSyncWithNeitherProviderDoesNothing(t *testing.T) {
	for name, o := range map[string]cloudSyncOpts{
		"plain":     {prices: true, catalog: true},
		"--yes":     {prices: true, catalog: true, yes: true},
		"--dry-run": {prices: true, catalog: true, dryRun: true},
	} {
		t.Run(name, func(t *testing.T) {
			path, cfg := cloudSyncHome(t, neitherRegistry)
			got := stubCloudFetch(t, bothPages())
			ollama := stubOllama(t, catalogPulled...)
			asked := stubConfirm(t, true, nil, nil)
			synced := stubRouteSync(t, "")

			stdout, stderr, final, code := runCSFinal(t, cfg, o)
			want := "prices: no OpenRouter-priced model in the registry; nothing to refresh\n" +
				"catalog: no ollama provider in the registry; nothing to mirror\n"
			if stdout != want || stderr != "" || final != "" || code != 0 {
				t.Errorf("stdout = %q, stderr = %q, error %q, exit %d\nwant stdout %q, nothing else and exit 0", stdout, stderr, final, code, want)
			}
			if urls := got.all(); len(urls) != 0 || len(ollama.calls) != 0 {
				t.Errorf("fetched %v and ran %q; want no request and no ollama command", urls, ollama.calls)
			}
			if *asked != 0 || *synced != 0 {
				t.Errorf("asked %d time(s), synced %d time(s); want neither", *asked, *synced)
			}
			if mustRead(t, path) != neitherRegistry {
				t.Error("the run changed the registry")
			}
			if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
				t.Errorf("the run left %d files beside the registry, want only registry.toml (no lock file)", len(entries))
			}
		})
	}
}

// TestCloudSyncCatalogRefusesADuplicatedModelIDBeforeThePlan pins a registry
// in which an ollama cloud entry the catalog plan would change or remove is
// there twice. The write addresses a row by its id and refuses such an id, so
// the plan could never be applied: the run must say so (exit 1, a failed
// step, not one of the catalog's own codes) instead of printing a plan, a
// removal digest and a question that the apply then refuses. A dry run says
// the same. An entry that is there twice and that the plan leaves alone does
// not stop the flow.
func TestCloudSyncCatalogRefusesADuplicatedModelIDBeforeThePlan(t *testing.T) {
	block := func(id string) string {
		start := strings.Index(cloudSyncRegistry, "[[models]]\nid = \""+id+"\"")
		end := start + strings.Index(cloudSyncRegistry[start+1:], "[[models]]") + 1
		return cloudSyncRegistry[start:end]
	}
	for _, id := range []string{"ollama/deepseek-v4-pro:cloud", "ollama/retired:cloud"} {
		registry := cloudSyncRegistry + "\n" + strings.TrimRight(block(id), "\n") + "\n"
		for name, o := range map[string]cloudSyncOpts{
			"--dry-run": {catalog: true, dryRun: true},
			"plain":     {catalog: true},
			"--yes":     {catalog: true, yes: true, approve: "000000000000"},
		} {
			t.Run(id+" "+name, func(t *testing.T) {
				path, cfg := cloudSyncHome(t, registry)
				stubCloudFetch(t, catalogPages())
				ollama := stubOllama(t, catalogPulled...)
				asked := stubConfirm(t, true, nil, nil)
				synced := stubRouteSync(t, "")
				stdout, stderr, final, code := runCSFinal(t, cfg, o)
				want := "catalog: error: nothing was changed: model \"" + id + "\" is in the registry twice (providers ollama, ollama); " +
					"wt cannot tell which one you mean — fix the entry in " + path + "\n"
				if stderr != want || stdout != "" || code != 1 || final != "cloud-sync: a step failed; see the error lines above" {
					t.Errorf("stdout = %q\nstderr = %q, error %q, exit %d\nwant no plan, stderr %q and exit 1", stdout, stderr, final, code, want)
				}
				if *asked != 0 || *synced != 0 || len(ollama.changes()) != 0 || mustRead(t, path) != registry {
					t.Errorf("asked %d time(s), synced %d time(s), ran %q, or the registry changed; want none of them", *asked, *synced, ollama.changes())
				}
			})
		}
	}

	// The local model twice: no catalog plan touches it, so the catalog flow
	// is planned as ever.
	registry := cloudSyncRegistry + "\n" + block("ollama/qwen3:8b")
	_, cfg := cloudSyncHome(t, registry)
	stubCloudFetch(t, catalogPages())
	stubOllama(t, catalogPulled...)
	if stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true}); code != 0 || stderr != "" || !strings.HasPrefix(stdout, "catalog: ollama.com/pricing: 5 models") {
		t.Errorf("with an untouched id duplicated: exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
}

// TestCloudSyncRefusedPlansDoNotCreateARegistry pins the refusal of a changed
// plan when the registry is gone by the time of the write (removed while the
// user reads the question), for the catalog flow and for both flows at once.
// Every pending plan is refused (the catalog's with exit 5, as "no longer the
// plan that was approved"), no ollama command runs, and no registry.toml is
// left behind: an empty one would make every later command load an empty
// registry instead of saying that there is none and naming `wt model init`.
// The catalog-only half starts from a registry with no ollama model, whose
// plan is all additions and so reads the same when made again from nothing:
// it is the ollama provider row, gone with the file, that makes it another
// plan.
func TestCloudSyncRefusedPlansDoNotCreateARegistry(t *testing.T) {
	const catalogStale = "catalog: error: the registry changed after the plan was printed, so this is no longer the plan that was approved; nothing was changed — run it again\n"
	const pricesStale = "prices: error: the registry changed after the plan was printed; no price was changed — run it again\n"
	onlyProvider := cloudSyncRegistry[:strings.Index(cloudSyncRegistry, "[[providers]]\nid = \"openrouter\"")]
	for _, tc := range []struct {
		name, registry string
		opts           cloudSyncOpts
		stderr         string
	}{
		{"the catalog alone, a plan of additions only", onlyProvider, cloudSyncOpts{catalog: true}, catalogStale},
		{"the catalog alone", cloudSyncRegistry, cloudSyncOpts{catalog: true}, catalogStale},
		{"both flows", cloudSyncRegistry, cloudSyncOpts{prices: true, catalog: true}, pricesStale + catalogStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, cfg := cloudSyncHome(t, tc.registry)
			stubCloudFetch(t, bothPages())
			ollama := stubOllama(t)
			synced := stubRouteSync(t, "")
			stubConfirm(t, true, nil, func() {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			})

			_, stderr, code := runCS(t, cfg, tc.opts)
			if stderr != tc.stderr || code != 5 {
				t.Errorf("stderr = %q, exit %d\nwant %q and exit 5", stderr, code, tc.stderr)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("registry.toml after the refused run: stat err = %v, want it still absent", err)
			}
			if len(ollama.changes()) != 0 || *synced != 0 {
				t.Errorf("the refused run ran %q and synced %d time(s); want neither", ollama.changes(), *synced)
			}
		})
	}
}

// TestCloudSyncBothFlowsInOneRun pins the default invocation, the one the
// spec's sequence describes: both plans printed, one question for the two of
// them, both applied to the registry, the catalog's ollama work, and one
// route sync at the end. Run with a terminal and no --yes, so the question
// is the gate (the digest is for the run nobody watches).
func TestCloudSyncBothFlowsInOneRun(t *testing.T) {
	both := cloudSyncOpts{prices: true, catalog: true}
	start := func(t *testing.T, pages map[string]string) (string, *config.Config, *fakeOllama, *int) {
		path, cfg := cloudSyncHome(t, cloudSyncRegistry)
		stubCloudFetch(t, pages)
		return path, cfg, stubOllama(t, catalogPulled...), stubRouteSync(t, "")
	}

	t.Run("approved: one question, both applied, one sync", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		asked := stubConfirm(t, true, nil, nil)
		stdout, stderr, code := runCS(t, cfg, both)
		if code != 0 || stderr != "" || *asked != 1 || *synced != 1 {
			t.Fatalf("exit %d, stderr %q, asked %d, syncs %d; want exit 0, one question, one sync\n%s", code, stderr, *asked, *synced, stdout)
		}
		// Both plans come before either result.
		plans := strings.Index(stdout, "catalog: ollama.com/pricing:")
		results := strings.Index(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\ncatalog: updated 1, added 4 and removed 1 model(s)\n")
		if strings.Index(stdout, "prices: openrouter.ai:") != 0 || plans < 0 || results < plans {
			t.Errorf("want the prices plan, the catalog plan, then the two result lines:\n%s", stdout)
		}
		text := mustRead(t, path)
		if !strings.Contains(text, "input_price_per_million = 3.0") || !strings.Contains(text, "ollama/glm-5.3:cloud") || strings.Contains(text, "retired:cloud") {
			t.Errorf("want the OpenRouter price and the catalog's additions and removal in one registry:\n%s", text)
		}
		if got := ollama.changes(); len(got) != 6 {
			t.Errorf("ollama commands = %q, want the four pulls and the two removals", got)
		}
	})

	t.Run("declined: both flows say so, nothing changes, exit 0", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		stubConfirm(t, false, nil, nil)
		stdout, stderr, code := runCS(t, cfg, both)
		if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "prices: not applied (declined)\ncatalog: not applied (declined)\n") {
			t.Errorf("exit %d, stderr %q, stdout ends %q", code, stderr, stdout[max(0, len(stdout)-90):])
		}
		if mustRead(t, path) != cloudSyncRegistry || len(ollama.changes()) != 0 || *synced != 0 {
			t.Errorf("a declined run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
		}
	})

	t.Run("no terminal: both flows say so, nothing changes, exit 1", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		noTerminal(t)
		_, stderr, code := runCS(t, cfg, both)
		want := "prices: error: not applied: there is no terminal to confirm on — rerun with --yes to apply without asking\n" +
			"catalog: error: not applied: there is no terminal to confirm on — rerun with --yes to apply without asking\n"
		if code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		if mustRead(t, path) != cloudSyncRegistry || len(ollama.changes()) != 0 || *synced != 0 {
			t.Errorf("an unconfirmed run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
		}
	})

	t.Run("the prices plan went stale, the catalog is applied: exit 1", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		stubConfirm(t, true, nil, func() {
			raced := strings.Replace(cloudSyncRegistry, "input_price_per_million = 2.5", "input_price_per_million = 7", 1)
			if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		stdout, stderr, code := runCS(t, cfg, both)
		if want := "prices: error: the registry changed after the plan was printed; no price was changed — run it again\n"; code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "catalog: updated 1, added 4 and removed 1 model(s)\n") || !strings.Contains(text, "ollama/glm-5.3:cloud") || !strings.Contains(text, "input_price_per_million = 7\n") {
			t.Errorf("want the catalog applied and the other writer's price kept:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 6 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want the catalog's six commands and one sync", ollama.changes(), *synced)
		}
	})

	t.Run("the catalog plan went stale, the prices are applied: exit 5", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, bothPages())
		stubConfirm(t, true, nil, func() {
			raced := cloudSyncRegistry + "\n[[models]]\nid = \"ollama/late:cloud\"\nfamily = \"late\"\nprovider_id = \"ollama\"\nmodel_name = \"late:cloud\"\nlocation = \"cloud\"\n"
			if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		stdout, stderr, code := runCS(t, cfg, both)
		if code != 5 || !strings.HasPrefix(stderr, "catalog: error: the registry changed after the plan was printed") {
			t.Errorf("exit %d, stderr %q; want the catalog's 5", code, stderr)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") || !strings.Contains(text, "input_price_per_million = 3.0") ||
			!strings.Contains(text, "ollama/late:cloud") || !strings.Contains(text, "ollama/retired:cloud") || strings.Contains(text, "glm-5.3") {
			t.Errorf("want the price applied, the other writer's row kept and the catalog untouched:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 0 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want no ollama command and one sync for the price", ollama.changes(), *synced)
		}
	})

	t.Run("OpenRouter is down, the catalog is applied: exit 1", func(t *testing.T) {
		path, cfg, ollama, synced := start(t, catalogPages())
		asked := stubConfirm(t, true, nil, nil)
		stdout, stderr, code := runCS(t, cfg, both)
		if want := "prices: error: could not read OpenRouter's prices: HTTP 404; no price was changed\n"; code != 1 || stderr != want || *asked != 1 {
			t.Errorf("exit %d, asked %d, stderr %q\nwant exit 1, one question, stderr %q", code, *asked, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "catalog: updated 1, added 4 and removed 1 model(s)\n") || !strings.Contains(text, "ollama/glm-5.3:cloud") || !strings.Contains(text, "input_price_per_million = 2.5\n") {
			t.Errorf("want the catalog applied and the OpenRouter price as it was:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 6 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want the catalog's six commands and one sync", ollama.changes(), *synced)
		}
	})
}

// TestCloudSyncCatalogDeclinedChangesNothing pins a "no" to a catalog plan
// that deletes: the registry is byte-identical, ollama is asked for nothing
// but its list, the routes are not synced, and the exit status is 0 (the
// user's answer is not a failure).
func TestCloudSyncCatalogDeclinedChangesNothing(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	synced := stubRouteSync(t, "")
	asked := stubConfirm(t, false, nil, nil)

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	if *asked != 1 || code != 0 || stderr != "" || !strings.HasSuffix(stdout, "catalog: not applied (declined)\n") {
		t.Errorf("asked %d, exit %d, stderr %q, stdout ends %q", *asked, code, stderr, stdout[max(0, len(stdout)-60):])
	}
	if mustRead(t, path) != cloudSyncRegistry || len(ollama.changes()) != 0 || *synced != 0 {
		t.Errorf("a declined run changed something: ollama %q, syncs %d", ollama.changes(), *synced)
	}
}

// TestCloudSyncCatalogAsksWhenADigestComesWithoutYes pins
// --approve-removals without --yes: the digest is ignored (even a wrong
// one), the question is asked, and the answer decides. The digest exists
// for the run nobody is watching; with a person at the terminal, the person
// is the gate.
func TestCloudSyncCatalogAsksWhenADigestComesWithoutYes(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	stubOllama(t, catalogPulled...)
	stubRouteSync(t, "")
	asked := stubConfirm(t, true, nil, nil)

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, approve: "000000000000"})
	if *asked != 1 || code != 0 || stderr != "" {
		t.Fatalf("asked %d, exit %d, stderr %q; want one question and the plan applied", *asked, code, stderr)
	}
	if text := mustRead(t, path); !strings.Contains(text, "ollama/glm-5.3:cloud") || strings.Contains(text, "retired:cloud") {
		t.Errorf("the approved plan was not applied:\n%s", text)
	}
}

// TestCloudSyncFinishesAnInterruptedRun pins the run after one that died
// between its registry write and its route sync (Ctrl-C during a slow pull,
// a crash). The registry already holds the new entries, so this run plans
// no registry change at all, only the pulls and removals still owed. It
// must still sync the routes: nothing else will, and without it LiteLLM
// keeps the old prices and lacks the new models until some other command
// happens to sync.
func TestCloudSyncFinishesAnInterruptedRun(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, catalogPages())
	ollama := stubOllama(t, catalogPulled...)
	stubRouteSync(t, "")
	stubConfirm(t, true, nil, nil)
	if _, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true}); code != 0 {
		t.Fatalf("the first run: exit %d, stderr %q", code, stderr)
	}
	written := mustRead(t, path)

	// As if that run had died before any ollama command and before its
	// sync: ollama is as it was, the registry is as the run left it.
	ollama.calls = nil
	synced := stubRouteSync(t, "")
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true})
	if code != 0 || stderr != "" || !strings.Contains(stdout, "catalog: updated 0, added 0 and removed 0 model(s)\n") {
		t.Fatalf("exit %d, stderr %q\n%s", code, stderr, stdout)
	}
	want := []string{"pull glm-5.3:cloud", "pull gemma4:cloud", "pull kimi-k3:1t-cloud", "pull gpt-oss:120b-cloud", "rm retired:cloud", "rm stray:cloud"}
	if got := ollama.changes(); !reflect.DeepEqual(got, want) {
		t.Errorf("ollama commands = %q\nwant            %q", got, want)
	}
	if *synced != 1 {
		t.Errorf("the finishing run synced the routes %d time(s), want once", *synced)
	}
	if mustRead(t, path) != written {
		t.Error("the finishing run rewrote a registry that was already in step")
	}
}

// TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad pins what a broken
// row costs: the flow that must change it, and only that flow. The shared
// registry write is refused whole when a row it touches would not load
// (here a hand edit removed a family); the command then writes each flow on
// its own, so a broken ollama entry does not keep OpenRouter prices stale,
// nor the reverse. The broken flow's error names the row, and the exit
// status is 1.
func TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad(t *testing.T) {
	both := cloudSyncOpts{prices: true, catalog: true}

	t.Run("a broken ollama entry: prices applied, catalog not", func(t *testing.T) {
		broken := strings.Replace(cloudSyncRegistry, "family = \"deepseek\"\n", "", 1)
		path, cfg := cloudSyncHome(t, broken)
		stubCloudFetch(t, bothPages())
		ollama := stubOllama(t, catalogPulled...)
		synced := stubRouteSync(t, "")
		stubConfirm(t, true, nil, nil)

		stdout, stderr, code := runCS(t, cfg, both)
		want := "catalog: error: the catalog's changes were not written: invalid registry entry: model \"ollama/deepseek-v4-pro:cloud\": family is required\n"
		if code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.HasSuffix(stdout, "prices: refreshed 1 model(s); 1 price(s) changed\n") || !strings.Contains(text, "input_price_per_million = 3.0") ||
			strings.Contains(text, "glm-5.3") || !strings.Contains(text, "ollama/retired:cloud") {
			t.Errorf("want the price written and no catalog change:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 0 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want no ollama command and one sync for the price", ollama.changes(), *synced)
		}
	})

	t.Run("a broken OpenRouter model: catalog applied, prices not", func(t *testing.T) {
		broken := strings.Replace(cloudSyncRegistry, "family = \"gpt\"\n", "", 1)
		path, cfg := cloudSyncHome(t, broken)
		stubCloudFetch(t, bothPages())
		ollama := stubOllama(t, catalogPulled...)
		synced := stubRouteSync(t, "")
		stubConfirm(t, true, nil, nil)

		stdout, stderr, code := runCS(t, cfg, both)
		want := "prices: error: the price changes were not written: invalid registry entry: model \"openrouter/vendor--gpt\": family is required\n"
		if code != 1 || stderr != want {
			t.Errorf("exit %d, stderr %q\nwant exit 1, stderr %q", code, stderr, want)
		}
		text := mustRead(t, path)
		if !strings.Contains(stdout, "catalog: updated 1, added 4 and removed 1 model(s)\n") || !strings.Contains(text, "ollama/glm-5.3:cloud") ||
			!strings.Contains(text, "input_price_per_million = 2.5\n") {
			t.Errorf("want the catalog written and the OpenRouter price as it was:\n%s\n%s", stdout, text)
		}
		if len(ollama.changes()) != 6 || *synced != 1 {
			t.Errorf("ollama %q, syncs %d; want the catalog's six commands and one sync", ollama.changes(), *synced)
		}
	})
}

// TestCloudSyncCatalogReadsASavedPage pins --html: the page comes from the
// file and ollama.com/pricing is not fetched (the library tag lookups still
// are). It is how a repaired parser is tried against the page that broke it.
func TestCloudSyncCatalogReadsASavedPage(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	saved := filepath.Join(t.TempDir(), "saved.html")
	if err := os.WriteFile(saved, []byte(catalogPage), 0o600); err != nil {
		t.Fatal(err)
	}
	pages := catalogPages()
	delete(pages, cloudsync.PricingURL)
	got := stubCloudFetch(t, pages)
	stubOllama(t, catalogPulled...)

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{catalog: true, dryRun: true, htmlFile: saved})
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "catalog: ollama.com/pricing: 5 models") {
		t.Errorf("exit %d, stderr %q, stdout:\n%s", code, stderr, stdout)
	}
	for _, url := range got.all() {
		if url == cloudsync.PricingURL {
			t.Error("--html still fetched ollama.com/pricing")
		}
	}
}
