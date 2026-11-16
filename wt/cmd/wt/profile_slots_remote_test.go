package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// noModelClient makes building a model-server client fail the test: a
// command that must work offline never builds one.
func noModelClient(t *testing.T) {
	t.Helper()
	old := newModelClient
	newModelClient = func(modelclient.Endpoint) *modelclient.Client {
		t.Error("a model-server client was built")
		return old(modelclient.Endpoint{})
	}
	t.Cleanup(func() { newModelClient = old })
}

// writeSlotFile writes slot-profiles.toml in the current config home.
func writeSlotFile(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profiles.SlotPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSlotsAddFailsHardWhenUnreachable proves `wt profile slots add` and
// `set` on a client session take their model ids from the model server and
// fail hard when it does not answer: exit 1, the server named, "nothing was
// changed", and slot-profiles.toml neither created nor touched. A fallback to
// the registry, or to no id check at all, would write a profile whose slots
// the server may not serve.
func TestSlotsAddFailsHardWhenUnreachable(t *testing.T) {
	f := newFakeModelServer(t)
	a := pointAtFake(t, f, "")
	f.Close()
	_, err := runProfileCmd(t, a, "slots", "add", "mine", "--slot", "sonnet=openrouter/a")
	if err == nil || !strings.HasSuffix(err.Error(), "nothing was changed") || exitcode.Of(err) != 1 {
		t.Fatalf("add: err = %v (exit %d), want exit 1 ending in the still clause", err, exitcode.Of(err))
	}
	if _, serr := os.Stat(profiles.SlotPath()); !os.IsNotExist(serr) {
		t.Errorf("slot-profiles.toml exists after a refused add: %v", serr)
	}
	writeSlotFile(t, slotsBase)
	_, err = runProfileCmd(t, a, "slots", "set", "base", "--slot", "haiku=openrouter/a")
	if err == nil || !strings.HasSuffix(err.Error(), "nothing was changed") || exitcode.Of(err) != 1 {
		t.Fatalf("set: err = %v (exit %d), want exit 1 ending in the still clause", err, exitcode.Of(err))
	}
	if got := readSlotFile(t); got != slotsBase {
		t.Errorf("file changed by a refused set:\n%s", got)
	}
}

// TestSlotsAddTakesModelIDsFromTheCatalog proves the ids `add` accepts are
// the model server's catalog rows, not the registry's: an id the server lists
// is written even when the session has no registry, and one it does not list
// is refused with nothing written.
func TestSlotsAddTakesModelIDsFromTheCatalog(t *testing.T) {
	f := newFakeModelServer(t, wireRow("openrouter/served", modelapi.ActionLaunch, 1))
	a := pointAtFake(t, f, "")
	a.cfg = nil
	out, err := runProfileCmd(t, a, "slots", "add", "mine", "--slot", "sonnet=openrouter/served")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(readSlotFile(t), `sonnet = "openrouter/served"`) {
		t.Errorf("file lacks the slot:\n%s", readSlotFile(t))
	}
	if f.count("GET /control/v1/catalog") != 1 {
		t.Errorf("catalog reads = %d, want 1", f.count("GET /control/v1/catalog"))
	}
	before := readSlotFile(t)
	if _, err := runProfileCmd(t, a, "slots", "add", "other", "--slot", "sonnet=openrouter/a"); err == nil {
		t.Error("an id the server does not list was accepted")
	}
	if readSlotFile(t) != before {
		t.Error("a refused add changed the file")
	}
}

// TestSlotsRmAndUsageWorkOffline proves `profile slots rm` and `usage` need
// neither a model server nor a registry: with no endpoint configured and no
// server anywhere, both succeed and no client is built. They only read or
// shrink slot-profiles.toml, and a laptop away from its model server must
// still be able to tidy it.
func TestSlotsRmAndUsageWorkOffline(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	noModelClient(t)
	writeSlotFile(t, slotsBase)
	a := &app{}
	if out, err := runProfileCmd(t, a, "slots", "usage", "--json"); err != nil || !strings.Contains(out, `"name": "base"`) {
		t.Fatalf("usage: %v\n%s", err, out)
	}
	if out, err := runProfileCmd(t, a, "slots", "rm", "base"); err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	if strings.Contains(readSlotFile(t), "base") {
		t.Errorf("profile still in the file:\n%s", readSlotFile(t))
	}
}

// jsonKeys is the sorted key set of a JSON object.
func jsonKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSlotsCandidatesJSONShapeUnchanged proves `profile slots candidates
// --json` over the model server prints the document the
// wt-manage-slot-profiles skill reads, key for key: the same top-level keys
// and candidate keys as the registry path (run here too), which are the
// golden lists below. The values come from the server's candidate list.
func TestSlotsCandidatesJSONShapeUnchanged(t *testing.T) {
	wantTop := []string{"candidates", "stale_sources", "tiers_generated_at"}
	wantCand := []string{"confidence", "context", "flags", "id", "location", "open_weights", "output_per_million",
		"provider", "rated", "score", "subscription", "subscription_period", "tier"}
	shape := func(out string) ([]string, []string, map[string]json.RawMessage) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		var cands []json.RawMessage
		_ = json.Unmarshal(doc["candidates"], &cands)
		if len(cands) == 0 {
			t.Fatalf("no candidates in\n%s", out)
		}
		return jsonKeys(t, []byte(out)), jsonKeys(t, cands[0]), doc
	}

	// Today's shape: the registry path, no endpoint.
	legacy := slotsFixture(t, "")
	out, err := runProfileCmd(t, legacy, "slots", "candidates", "--json")
	if err != nil {
		t.Fatal(err)
	}
	top, cand, _ := shape(out)
	if !slices.Equal(top, wantTop) || !slices.Equal(cand, wantCand) {
		t.Fatalf("registry path keys = %v / %v, want the golden %v / %v", top, cand, wantTop, wantCand)
	}

	score, price := 61.5, 2.0
	f := newFakeModelServer(t)
	f.Candidates = modelapi.CandidateList{
		TiersGenerated: "2026-11-01T00:00:00Z", StaleSources: []string{"arena"},
		Candidates: []modelapi.Candidate{{ID: "openrouter/a", Provider: "openrouter", Location: "cloud", Tier: "B",
			Score: &score, Rated: true, OutputPerMillion: &price, Flags: []string{}}},
	}
	a := pointAtFake(t, f, "")
	a.cfg = nil
	out, err = runProfileCmd(t, a, "slots", "candidates", "--json", "--provider", "openrouter", "--near", "openrouter/a")
	if err != nil {
		t.Fatalf("candidates: %v\n%s", err, out)
	}
	top, cand, doc := shape(out)
	if !slices.Equal(top, wantTop) || !slices.Equal(cand, wantCand) {
		t.Errorf("server path keys = %v / %v, want the golden %v / %v", top, cand, wantTop, wantCand)
	}
	var stale []string
	_ = json.Unmarshal(doc["stale_sources"], &stale)
	if string(doc["tiers_generated_at"]) != `"2026-11-01T00:00:00Z"` || !slices.Equal(stale, []string{"arena"}) {
		t.Errorf("freshness = %s / %s", doc["tiers_generated_at"], doc["stale_sources"])
	}
	if i := slices.IndexFunc(f.reqs(), func(s string) bool { return strings.HasPrefix(s, "GET /control/v1/candidates?") }); i < 0 ||
		!strings.Contains(f.reqs()[i], "near=openrouter%2Fa") || !strings.Contains(f.reqs()[i], "provider=openrouter") {
		t.Errorf("candidates request = %v, want the query carrying --near and --provider", f.reqs())
	}
}

// TestProfileShowPreviewUsesTheGatewayAndAMaskedToken is R4 for `wt profile
// show -M profile/NAME` on a client session: the slots come from the model
// server's catalog, and the preview of Claude Code's command names the
// gateway origin and only a masked client token. The [litellm] url and key a
// config.toml (or a registry config) still holds are host state the session
// must never read, so neither appears in the output, nor does the real token.
func TestProfileShowPreviewUsesTheGatewayAndAMaskedToken(t *testing.T) {
	f := newFakeModelServer(t, wireRow("openrouter/a", modelapi.ActionLaunch, 1), wireRow("openrouter/b", modelapi.ActionLaunch, 2))
	f.Token = "gateway-token-9876"
	const litellmURL, litellmKey = "http://litellm-host.example:4000", "sk-host-secret-4242"
	a := pointAtFake(t, f, "\n[litellm]\nurl = \""+litellmURL+"\"\napi_key = \""+litellmKey+"\"\n")
	a.cfg = &config.Config{}
	a.cfg.SetLitellmForTest(config.LitellmState{URL: litellmURL, APIKey: litellmKey})
	a.slotChecks = []profiles.SlotCheck{slotFixtureCheck(0, "mix", "sonnet", map[string]string{"sonnet": "openrouter/a", "haiku": "openrouter/b"})}
	out, err := runProfileCmd(t, a, "show", "-A", "claude", "-M", "profile/mix")
	if err != nil {
		t.Fatalf("show: %v\n%s", err, out)
	}
	for _, want := range []string{"profile/mix (claude): launch", "slot sonnet*: openrouter/a", f.URL, "***9876"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{litellmURL, litellmKey, "4242", f.Token} {
		if strings.Contains(out, leak) {
			t.Errorf("output leaks %q:\n%s", leak, out)
		}
	}
	if f.count("GET /control/v1/catalog") != 1 {
		t.Errorf("catalog reads = %d, want 1", f.count("GET /control/v1/catalog"))
	}
}
