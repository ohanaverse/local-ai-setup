package agents

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

func readPiFile(t *testing.T, path string) piModelsFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestPiGatewayFileIsOwnerOnly pins that pi's models.json, which now holds the
// gateway token, is mode 0600 after a sync, including when it pre-existed as
// 0644 and including a sync that changes nothing: a world-readable token file
// is a credential leak.
func TestPiGatewayFileIsOwnerOnly(t *testing.T) {
	path := piHomeModelsPath(t)
	m := gwCloudModel()
	if err := os.WriteFile(path, []byte(`{"providers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (piDriver{}).SyncGateway(testGW(), []config.Model{m}, m); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	// Idempotent sync over a file loosened again.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := (piDriver{}).SyncGateway(testGW(), []config.Model{m}, m); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode after no-op sync = %v, want 0600", fi.Mode().Perm())
	}
	f := readPiFile(t, path)
	p := f.Providers["litellm"]
	if p.BaseURL != testGWOrigin+"/v1" || p.APIKey != testGWToken {
		t.Errorf("litellm block = %+v", p)
	}
}

// TestPiGatewayRemovesWtOwnedDirectBlocks pins that a gateway sync drops
// every _wtOwned block for another provider (leftovers of the direct rewrite,
// which carry provider keys) so no provider key stays on disk beside the
// gateway block.
func TestPiGatewayRemovesWtOwnedDirectBlocks(t *testing.T) {
	path := piHomeModelsPath(t)
	m := gwCloudModel()
	writeFile(t, path, `{"providers":{"openrouter":{"api":"openai-completions","apiKey":"`+providerKey+`","baseUrl":"`+providerOrigin+`","_wtOwned":true,"models":[{"_launch":true,"id":"qwen/qwen3.8-27b"}]}}}`)
	if err := (piDriver{}).SyncGateway(testGW(), []config.Model{m}, m); err != nil {
		t.Fatal(err)
	}
	f := readPiFile(t, path)
	if _, ok := f.Providers["openrouter"]; ok {
		t.Error("_wtOwned openrouter block survived")
	}
	if _, ok := f.Providers["litellm"]; !ok {
		t.Error("litellm block missing")
	}
}

// TestPiGatewayKeepsAUsersOwnBlock pins that a provider block without
// _wtOwned is the user's and is never touched by a gateway sync.
func TestPiGatewayKeepsAUsersOwnBlock(t *testing.T) {
	path := piHomeModelsPath(t)
	m := gwCloudModel()
	writeFile(t, path, `{"providers":{"mine":{"api":"openai-completions","apiKey":"user-key","baseUrl":"http://mine.invalid/v1","models":[{"_launch":true,"id":"x"}]}}}`)
	if err := (piDriver{}).SyncGateway(testGW(), []config.Model{m}, m); err != nil {
		t.Fatal(err)
	}
	p := readPiFile(t, path).Providers["mine"]
	if p.APIKey != "user-key" || p.BaseURL != "http://mine.invalid/v1" || len(p.Models) != 1 {
		t.Errorf("user block changed: %+v", p)
	}
}

// TestSyncLitellmBlockKeepsWhatIsThere pins syncLitellmBlock's guarantees:
// entries already in the litellm block are never removed or reordered (a
// user's `_launch: false` and a model the catalog no longer lists stay where
// they are), new ids are appended in order, and a user's `api` is kept (only
// an empty one is defaulted). Without it a sync could silently re-enable an
// entry the user switched off or drop one they still launch by hand.
func TestSyncLitellmBlockKeepsWhatIsThere(t *testing.T) {
	f := piModelsFile{Providers: map[string]piProvider{
		piLitellmProviderID: {API: "openai-responses", BaseURL: "http://old/v1", APIKey: "old", Models: []piModel{
			{ID: "user/own", Launch: false},
			{ID: "ollama/a", Launch: true},
		}},
	}}
	if !syncLitellmBlock(f, "http://gw", "tok", []piTarget{{ID: "ollama/b"}, {ID: "ollama/a"}, {ID: "ollama/c"}}) {
		t.Fatal("sync reported no change for a new origin and two new ids")
	}
	p := f.Providers[piLitellmProviderID]
	var ids []string
	for _, m := range p.Models {
		ids = append(ids, m.ID)
	}
	if want := []string{"user/own", "ollama/a", "ollama/b", "ollama/c"}; !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v (existing kept in place, new appended)", ids, want)
	}
	if p.Models[0].Launch {
		t.Error("the user's _launch: false entry was switched on")
	}
	if p.API != "openai-responses" || p.BaseURL != "http://gw/v1" || p.APIKey != "tok" {
		t.Errorf("block = api %q base %q key %q, want the user's api kept and the gateway's origin and token", p.API, p.BaseURL, p.APIKey)
	}
	if syncLitellmBlock(f, "http://gw", "tok", []piTarget{{ID: "ollama/a"}}) {
		t.Error("a second sync with nothing new reported a change")
	}
}

// TestPiGatewayKeepsAnExternalGatewayBlockByteForByte pins the documented way to
// reach an external gateway with pi (#618): a user-written provider whose apiKey
// is a `!command` pi runs itself, with a compat entry and fields wt has no
// struct for (headers, per-model name/maxTokens/cost). A gateway sync that
// writes the file for another reason must not rewrite or drop any of it.
func TestPiGatewayKeepsAnExternalGatewayBlockByteForByte(t *testing.T) {
	path := piHomeModelsPath(t)
	m := gwCloudModel()
	block := `{"api":"openai-completions","apiKey":"!/usr/local/bin/cred-helper.sh","baseUrl":"https://gw.invalid/v1","compat":{"supportsStore":false,"supportsDeveloperRole":false},"headers":{"x-tag":"a"},"models":[{"id":"claude-x","name":"Claude X","maxTokens":8192,"cost":{"input":1,"output":2},"contextWindow":200000,"input":["text"],"reasoning":true}]}`
	writeFile(t, path, `{"$schema":"keep-me","providers":{"ext":`+block+`}}`)
	if err := (piDriver{}).SyncGateway(testGW(), []config.Model{m}, m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Schema    string                     `json:"$schema"`
		Providers map[string]json.RawMessage `json:"providers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Schema != "keep-me" {
		t.Errorf("top-level $schema = %q, want it kept", raw.Schema)
	}
	var got, want any
	if err := json.Unmarshal(raw.Providers["ext"], &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(block), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("user block changed:\n got  %s\n want %s", raw.Providers["ext"], block)
	}
}

// TestSyncLitellmBlockWindows pins #689: a new entry takes the catalog window
// (piDefaultWindow when unknown), an existing _launch entry follows a changed
// catalog window, an unknown window and a _launch:false entry keep theirs, and
// a repeat sync is a no-op.
func TestSyncLitellmBlockWindows(t *testing.T) {
	f := piModelsFile{Providers: map[string]piProvider{
		piLitellmProviderID: {API: "openai-completions", BaseURL: "http://gw/v1", APIKey: "tok", Models: []piModel{
			{ID: "m/changed", Launch: true, ContextWindow: 262144, Reasoning: true},
			{ID: "m/unknown", Launch: true, ContextWindow: 12345},
			{ID: "m/off", Launch: false, ContextWindow: 1000},
		}},
	}}
	targets := []piTarget{
		{ID: "m/changed", Window: 1000000},
		{ID: "m/unknown"},
		{ID: "m/off", Window: 500000},
		{ID: "m/new", Window: 128000},
		{ID: "m/new-unknown"},
	}
	if !syncLitellmBlock(f, "http://gw", "tok", targets) {
		t.Fatal("no change reported")
	}
	got := map[string]int{}
	for _, m := range f.Providers[piLitellmProviderID].Models {
		got[m.ID] = m.ContextWindow
	}
	want := map[string]int{"m/changed": 1000000, "m/unknown": 12345, "m/off": 1000, "m/new": 128000, "m/new-unknown": piDefaultWindow}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s window = %d, want %d", id, got[id], w)
		}
	}
	if !f.Providers[piLitellmProviderID].Models[0].Reasoning {
		t.Error("a window update changed another field")
	}
	if syncLitellmBlock(f, "http://gw", "tok", targets) {
		t.Error("a repeat sync reported a change")
	}
}
