package agents

import (
	"encoding/json"
	"os"
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
