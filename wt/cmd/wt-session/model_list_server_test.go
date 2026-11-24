package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
)

const modelListServerFixture = "../../../docs/contracts/model-list.server.sample.json"

func floatp(f float64) *float64 { return &f }

// serverListRows are the catalog rows the fixture documents: a running local
// model with a tier, a priced cloud model, a stopped one, a blocked one and an
// alias over two of them (#596).
func serverListRows() []modelapi.Row {
	return []modelapi.Row{
		{ID: "omlx/qwen3.8-27b", GatewayID: "omlx/qwen3.8-27b", Order: 1, Routed: boolp(true), Family: "qwen3.8", ProviderID: "omlx",
			ModelName: "qwen3.8-27b", Location: "local", Tags: []string{"code"}, Registered: true, State: modelapi.StateRunning,
			Action: modelapi.ActionLaunch, Tier: "strong", TierCode: "A", Confidence: "high", Aligned: floatp(71.5), TierSources: 4,
			Context: 131072, Protocols: []string{"anthropic", "openai"}, Sessions: 2},
		{ID: "openrouter/z-ai/glm-5.3", GatewayID: "openrouter/z-ai/glm-5.3", Order: 2, Routed: boolp(true), Family: "glm", ProviderID: "openrouter",
			ModelName: "z-ai/glm-5.3", Location: "cloud", Tags: []string{"code", "cheap"}, Registered: true, State: modelapi.StateRunning,
			Action: modelapi.ActionLaunch, Context: 202752, Protocols: []string{"openai"}, Price: &modelapi.Price{Input: floatp(0.6), Output: floatp(2.2)}},
		{ID: "omlx/gemma4-9b", GatewayID: "omlx/gemma4-9b", Order: 3, Routed: boolp(false), Family: "gemma4", ProviderID: "omlx",
			ModelName: "gemma4-9b", Location: "local", Tags: []string{}, Registered: true, State: modelapi.StateStopped,
			Action: modelapi.ActionStart, Protocols: []string{}},
		{ID: "ollama/old-model", GatewayID: "ollama/old-model", Order: 4, Family: "old", ProviderID: "ollama",
			ModelName: "old-model", Location: "local", Tags: []string{}, Discovered: true, State: modelapi.StateBlocked,
			Action: modelapi.ActionBlock, BlockReason: "ollama is not running; start it with 'ollama serve'", Protocols: []string{}},
		{ID: "alias/coder", GatewayID: "alias/coder", Order: 5, Routed: boolp(true), Family: "alias", ProviderID: "alias",
			ModelName: "coder", Location: "cloud", Tags: []string{}, Registered: true, State: modelapi.StateRunning,
			Action: modelapi.ActionLaunch, Context: 131072, Protocols: []string{"openai"}, Sessions: 1, Kind: modelapi.KindAlias,
			Targets: []modelapi.AliasTarget{
				{ID: "openrouter/z-ai/glm-5.3", Order: 1, ProviderID: "openrouter", Location: "cloud", State: modelapi.StateRunning, Context: 202752},
				{ID: "omlx/qwen3.8-27b", Order: 2, ProviderID: "omlx", Location: "local", State: modelapi.StateRunning, Context: 131072},
			}},
		// A redirect `wt-model alias rename` left (#687): the target alias's
		// targets, and the alias it redirects to.
		{ID: "alias/old-coder", GatewayID: "alias/old-coder", Order: 6, Routed: boolp(true), Family: "alias", ProviderID: "alias",
			ModelName: "old-coder", Location: "cloud", Tags: []string{}, Registered: true, State: modelapi.StateRunning,
			Action: modelapi.ActionLaunch, Context: 131072, Protocols: []string{"openai"}, Kind: modelapi.KindAlias, RedirectTo: "alias/coder",
			Targets: []modelapi.AliasTarget{
				{ID: "openrouter/z-ai/glm-5.3", Order: 1, ProviderID: "openrouter", Location: "cloud", State: modelapi.StateRunning, Context: 202752},
				{ID: "omlx/qwen3.8-27b", Order: 2, ProviderID: "omlx", Location: "local", State: modelapi.StateRunning, Context: 131072},
			}},
	}
}

// serverListRig points an app at a fake model server serving the fixture's
// rows, with the loopback-only client and polls that do not wait.
func serverListRig(t *testing.T) (*app, *fakeModelServer) {
	t.Helper()
	f := newFakeModelServer(t, serverListRows()...)
	f.Generated = "2026-11-16T10:00:00Z"
	f.Providers = map[string]string{"omlx": "ok", "openrouter": "ok", "ollama": "unreachable"}
	a := pointAtFake(t, f, "")
	a.hooks.newClient = fastModelClient
	return a, f
}

// runModelListCmd runs `model list args…` under wt, returning stdout, stderr and the error.
func runModelListCmd(t *testing.T, a *app, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := modelListCmd(a)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// TestModelListServerMatchesTheFixture proves `wt model list --server --json`
// prints exactly the document docs/contracts/model-list.server.sample.json
// holds, as one two-space-indented document (J1), and that the fixture is
// read strictly: every member of the document and of each row is one the wire
// types name. It is the session's catalog view, not the host registry's
// `model list --json` (that fixture is model-list.sample.json), so a drift
// here must not touch what modeltiers reads.
func TestModelListServerMatchesTheFixture(t *testing.T) {
	a, _ := serverListRig(t)
	raw, err := os.ReadFile(modelListServerFixture)
	if err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runModelListCmd(t, a, "--server", "--json")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want none", errOut)
	}
	if !reflect.DeepEqual(asAny(t, []byte(out)), asAny(t, raw)) {
		t.Errorf("document differs from the fixture\ngot:  %s\nwant: %s", out, raw)
	}
	if !strings.HasPrefix(out, "{\n  \"version\": 1,") || !strings.HasSuffix(out, "}\n") {
		t.Errorf("not one two-space-indented document (J1):\n%s", out)
	}
	var doc struct {
		Version   int               `json:"version"`
		Schema    int               `json:"schema"`
		Generated string            `json:"generated"`
		Providers map[string]string `json:"providers"`
		Rows      []modelapi.Row    `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if doc.Version != 1 || doc.Schema != modelapi.SchemaVersion || doc.Generated == "" || len(doc.Providers) == 0 || len(doc.Rows) != 6 {
		t.Errorf("fixture says %+v", doc)
	}
	// An empty catalog still has "rows": [] and every row's slices are arrays.
	f2 := newFakeModelServer(t)
	a2 := pointAtFake(t, f2, "")
	out, _, err = runModelListCmd(t, a2, "--server", "--json")
	if err != nil || !strings.Contains(out, `"rows": []`) || strings.Contains(out, "null") {
		t.Errorf("empty catalog: err %v, output %s", err, out)
	}
}

// TestModelListServerFailureEnvelope proves a failure under --server --json
// (an unreachable server) prints the error envelope on stdout with exit 1 and
// a quiet error (J4), and nothing on stderr, wt's own or cobra's.
func TestModelListServerFailureEnvelope(t *testing.T) {
	a, f := serverListRig(t)
	f.Close()
	errBuf := captureStderr(t)
	out, cobraErr, err := runModelListCmd(t, a, "--server", "--json")
	if cobraErr != "" {
		t.Errorf("cobra's stderr = %q, want none (the error is quiet)", cobraErr)
	}
	if exitcode.Of(err) != 1 || !exitcode.IsQuiet(err) {
		t.Fatalf("err = %v (exit %d, quiet %v), want exit 1 quiet", err, exitcode.Of(err), exitcode.IsQuiet(err))
	}
	var env map[string]json.RawMessage
	if jerr := json.Unmarshal([]byte(out), &env); jerr != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", jerr, out)
	}
	var e struct{ Message, Code string }
	if _, ok := env["rows"]; ok || len(env) != 2 || json.Unmarshal(env["error"], &e) != nil ||
		!strings.Contains(e.Message, "is not reachable") || e.Code == "" || string(env["version"]) != "1" {
		t.Errorf("envelope = %s", out)
	}
	if errBuf.String() != "" {
		t.Errorf("stderr = %q, want none", errBuf.String())
	}
}

// TestModelListServerPlainColumns proves the text listing is a plain table
// with the columns MODEL STATE LOC TIER and CLIENT SESSIONS, the S4 state
// words as the server sends them, a blocked row's reason left to --json, and
// that `model list` without --server (now the pointer to wt-model) never asks
// the server (R3).
func TestModelListServerPlainColumns(t *testing.T) {
	a, f := serverListRig(t)
	out, _, err := runModelListCmd(t, a, "--server")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("want a header and six rows, got:\n%s", out)
	}
	if got := strings.Join(strings.Fields(lines[0]), " "); got != "MODEL STATE LOC TIER CLIENT SESSIONS" {
		t.Errorf("header = %q", got)
	}
	want := map[string]string{
		"omlx/qwen3.8-27b":        "omlx/qwen3.8-27b running local strong 2",
		"openrouter/z-ai/glm-5.3": "openrouter/z-ai/glm-5.3 running cloud - 0",
		"omlx/gemma4-9b":          "omlx/gemma4-9b stopped local - 0",
		"ollama/old-model":        "ollama/old-model blocked local - 0",
		"alias/coder":             "alias/coder running cloud - 1",
		"alias/old-coder":         "alias/old-coder running cloud - 0",
	}
	for _, l := range lines[1:] {
		id := strings.Fields(l)[0]
		if w, ok := want[id]; !ok || strings.Join(strings.Fields(l), " ") != w {
			t.Errorf("row %q, want %q", l, want[id])
		}
	}
	if strings.Contains(out, "ollama serve") {
		t.Errorf("a blocked row's reason is in --json only:\n%s", out)
	}
	if n := f.count("GET /control/v1/catalog"); n != 1 {
		t.Errorf("catalog fetched %d times, want 1", n)
	}
	before := len(f.reqs())
	// Without --server the command is the pointer to wt-model (Task 37):
	// an error, and still no call to the server.
	if _, _, err := runModelListCmd(t, a); err == nil {
		t.Fatal("model list without --server succeeded; want the wt-model pointer")
	}
	if len(f.reqs()) != before {
		t.Errorf("model list without --server asked the server: %v", f.reqs()[before:])
	}
}

// TestModelListWithoutServerIsAPointer pins the one rule of `model list`
// under wt after the registry listing moved to wt-model: without --server it does
// nothing but point there, exit 1, so a script still running the old command
// fails loudly instead of reading an empty inventory; --server stays the
// session's catalog view (the three tests above).
func TestModelListWithoutServerIsAPointer(t *testing.T) {
	out, errOut, err := runModelListCmd(t, &app{})
	const want = "model list moved to wt-model — run 'wt-model model list'; nothing was changed"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if code := exitcode.Of(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if out != "" || errOut != "" {
		t.Errorf("stdout %q, stderr %q; want nothing printed by the command itself", out, errOut)
	}
}
