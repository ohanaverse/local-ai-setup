package modeladmin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ollamaShowText is `ollama show` output in the shape ollama prints it:
// indented sections, each ended by a blank line.
const ollamaShowText = `  Model
    architecture        qwen3
    parameters          8.2B
    context length      40960

  Capabilities
    completion
    tools
    vision

  Parameters
    stop    "<|im_end|>"

  License
    Apache License
`

// stubOllamaShow answers `ollama show` with text (or err) and records the
// host and the model it was asked about.
func stubOllamaShow(t *testing.T, text string, err error) *[2]string {
	t.Helper()
	asked := &[2]string{}
	old := runOllamaShow
	runOllamaShow = func(_ context.Context, host, name string) (string, error) {
		asked[0], asked[1] = host, name
		return text, err
	}
	t.Cleanup(func() { runOllamaShow = old })
	return asked
}

// TestOllamaCapabilitiesReadsToolsAndVision verifies the two capabilities
// LiteLLM has keys for are recorded, nothing else is (completion, and "tools"
// under another heading), and the daemon asked is the registry row's, not the
// shell's OLLAMA_HOST. Without supports_function_calling in the route,
// LiteLLM drops an agent's tool definitions and the agent cannot edit files.
func TestOllamaCapabilitiesReadsToolsAndVision(t *testing.T) {
	asked := stubOllamaShow(t, ollamaShowText+"\n  Notes\n    tools\n", nil)
	cfg := &config.Config{Providers: []config.Provider{
		{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://gpu-box:11500"}},
	}}
	info, err := OllamaCapabilities(cfg, "qwen3:8b")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"supports_function_calling": true, "supports_vision": true}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("info = %v, want %v", info, want)
	}
	if asked[0] != "http://gpu-box:11500" || asked[1] != "qwen3:8b" {
		t.Errorf("asked %q about %q, want the registry row's address and the model", asked[0], asked[1])
	}
}

// TestOllamaCapabilitiesWithoutAnyAndWithoutARegistry verifies a model with
// neither capability gives an empty result (no model_info is written), and
// that with no config loaded the default ollama address is asked. The first
// add on a new machine runs before any registry exists.
func TestOllamaCapabilitiesWithoutAnyAndWithoutARegistry(t *testing.T) {
	asked := stubOllamaShow(t, "  Capabilities\n    completion\n", nil)
	info, err := OllamaCapabilities(nil, "tiny:1b")
	if err != nil || len(info) != 0 {
		t.Fatalf("OllamaCapabilities = (%v, %v), want an empty map", info, err)
	}
	if asked[0] != config.OllamaBaseURL {
		t.Errorf("asked %q, want the default %q", asked[0], config.OllamaBaseURL)
	}
}

// TestOllamaCapabilitiesReportsAFailedLookup verifies a failed `ollama show`
// is an error that names the command and the address, so the caller can add
// the model anyway and tell the user what was not recorded and why.
func TestOllamaCapabilitiesReportsAFailedLookup(t *testing.T) {
	stubOllamaShow(t, "", errors.New("exit status 1"))
	_, err := OllamaCapabilities(nil, "nope:1b")
	if err == nil || !strings.Contains(err.Error(), "`ollama show nope:1b` (at "+config.OllamaBaseURL+") failed: exit status 1") {
		t.Fatalf("err = %v, want the command, the address and the cause", err)
	}
}

// TestUsesOllama verifies which providers get the lookup: ollama, and no
// other local or cloud provider.
func TestUsesOllama(t *testing.T) {
	for id, want := range map[string]bool{"ollama": true, "omlx": false, "omlx-6bit": false, "openrouter": false, "": false} {
		if got := UsesOllama(id); got != want {
			t.Errorf("UsesOllama(%q) = %v, want %v", id, got, want)
		}
	}
}
