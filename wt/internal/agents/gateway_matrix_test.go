package agents

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// gateway_matrix_test.go — systematic driver × gateway-mode × provider
// coverage for Driver.Build. Per-driver test files pin one mode at a time;
// this file pins the whole reachable matrix so a mode- or provider-dependent
// regression cannot hide in an untested cell.
//
// The reachable matrix (and why the unreachable cells are absent):
//
//   - native rows: no gateway is consulted. Tested under a litellm gateway on
//     purpose — proving gateway config cannot leak into a native launch is
//     the interesting direction (a native launch that inherits gateway env
//     would silently route a subscription model through the proxy).
//   - direct mode: wt funnels every non-native model through the local Ollama
//     OpenAI-compatible endpoint (config.OllamaBaseURL); no driver reads
//     m.ProviderID on this path, so there is exactly one direct cell per
//     driver (ollama). Direct × llamacpp/omlx/openrouter is unreachable in
//     production — ollamacheck rejects a model absent from `ollama list`
//     before any driver runs — so those cells are deliberately not pinned
//     here; adding per-provider direct routing would be a feature, not a fix.
//   - litellm mode: wt itself is provider-agnostic (it emits the gateway URL
//     plus the registry id; the provider fan-out happens inside LiteLLM), so
//     every provider fixture must produce the same env/arg shape with only
//     the model identity differing. A provider-specific branch sneaking into
//     a driver shows up here as a shape change on one fixture.
//
// The provider fixtures mirror real registry.toml shapes, including the
// awkward ones on purpose: llamacpp and openrouter ModelNames contain slashes,
// and openrouter's bare ModelName ("qwen/qwen3.8-27b") is materially
// different from its registry id — exactly the fixture class that catches a
// ModelName/ID mix-up in the litellm branch, where the registry id is the
// only string LiteLLM can resolve.

// matrixGateway is the fixed LiteLLM gateway every litellm matrix case uses.
var matrixGateway = Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}

// matrixModels mirrors the four provider shapes a real registry.toml carries.
// IDs are registry keys; ModelNames are provider-side names. The llamacpp and
// openrouter ModelNames deliberately contain slashes (real registry data
// does), so a driver that reconstructs the model reference from ModelName in
// litellm mode produces a visibly wrong string, not a silently working one.
var matrixModels = []config.Model{
	{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama", Location: config.LocationLocal},
	{ID: "llamacpp/ornith-1.5-35b", ModelName: "ornith-ai/Ornith-1.5-35B-A3B-GGUF", ProviderID: "llamacpp", Location: config.LocationLocal},
	{ID: "omlx/Ornith-1.5-35B-A3B-MLX-4bit", ModelName: "Ornith-1.5-35B-A3B-MLX-4bit", ProviderID: "omlx", Location: config.LocationLocal},
	{ID: "openrouter/qwen/qwen3.8-27b", ModelName: "qwen/qwen3.8-27b", ProviderID: "openrouter", Location: config.LocationCloud},
}

// TestGatewayMatrixClaude pins claude's Build across the matrix: direct mode
// routes to the bare Ollama anthropic-compatible endpoint (no /v1) with the
// bare model name; litellm mode routes to the gateway's anthropic endpoint
// (also no /v1 — unlike codex/copilot/opencode, whose OpenAI-compatible wires
// need /v1) with the registry id, identically for every provider; a native
// model ignores the gateway entirely. A regression in any cell means the
// launched claude talks to the wrong endpoint or asks for a model the
// endpoint cannot resolve.
func TestGatewayMatrixClaude(t *testing.T) {
	d := ByName("claude")
	if d == nil {
		t.Fatal("claude driver not registered")
	}

	t.Run("direct-ollama", func(t *testing.T) {
		m := matrixModels[0]
		lc := d.Build(m, false, directGateway())
		assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", "http://localhost:11434")
		assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "ollama")
		got, ok := argsFlagValue(lc.Args, "--model")
		if !ok || got != m.ModelName {
			t.Errorf("--model = %q, want bare %q", got, m.ModelName)
		}
	})

	for _, m := range matrixModels {
		t.Run("litellm-"+m.ProviderID, func(t *testing.T) {
			lc := d.Build(m, false, matrixGateway)
			assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", "http://localhost:4000")
			assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "sk-litellm")
			assertEnv(t, lc.Env, "ANTHROPIC_API_KEY", "")
			got, ok := argsFlagValue(lc.Args, "--model")
			if !ok || got != m.ID {
				t.Errorf("--model = %q, want registry id %q (LiteLLM keys model_list on it)", got, m.ID)
			}
		})
	}

	t.Run("native-under-litellm", func(t *testing.T) {
		lc := d.Build(nativeModel("claude"), false, matrixGateway)
		if len(lc.Env) != 0 {
			t.Errorf("native env = %v, want none (subscription must win over gateway)", lc.Env)
		}
		for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"} {
			if !slices.Contains(lc.ClearEnv, k) {
				t.Errorf("native ClearEnv = %v, want it to include %q", lc.ClearEnv, k)
			}
		}
	})
}

// TestGatewayMatrixCodex pins codex's Build across the matrix: direct mode
// declares the local Ollama /v1/ provider inline with the bare model name;
// litellm mode points the same provider block at the gateway /v1/ endpoint,
// exports the gateway key via AGENT_WT_GATEWAY_API_KEY, and passes the
// registry id — with byte-identical args for every provider except the model
// value. Native launches carry no provider block regardless of gateway. A
// regression means codex falls back to the "openai" provider and prompts to
// sign in instead of using the routed model.
func TestGatewayMatrixCodex(t *testing.T) {
	d := ByName("codex")
	if d == nil {
		t.Fatal("codex driver not registered")
	}

	t.Run("direct-ollama", func(t *testing.T) {
		m := matrixModels[0]
		lc := d.Build(m, false, directGateway())
		if !slices.Equal(lc.Args, codexProviderArgs(m.ModelName)) {
			t.Errorf("args = %v, want direct template %v", lc.Args, codexProviderArgs(m.ModelName))
		}
		if len(lc.Env) != 0 {
			t.Errorf("direct env = %v, want none (local endpoint needs no key)", lc.Env)
		}
	})

	for _, m := range matrixModels {
		t.Run("litellm-"+m.ProviderID, func(t *testing.T) {
			lc := d.Build(m, false, matrixGateway)
			want := codexLitellmProviderArgs("http://localhost:4000/v1/", m.ID)
			if !slices.Equal(lc.Args, want) {
				t.Errorf("args = %v, want identical shape across providers %v", lc.Args, want)
			}
			if !slices.Equal(lc.Env, []string{codexGatewayEnvKey + "=sk-litellm"}) {
				t.Errorf("env = %v, want only the gateway key for env_key lookup", lc.Env)
			}
		})
	}

	t.Run("native-under-litellm", func(t *testing.T) {
		lc := d.Build(nativeModel("codex"), false, matrixGateway)
		if len(lc.Args) != 0 || len(lc.Env) != 0 {
			t.Errorf("native build = %+v, want bare codex (subscription, no provider block)", lc)
		}
	})
}

// TestGatewayMatrixCopilot pins copilot's Build across the matrix: direct mode
// exports the Ollama /v1 endpoint with the bare model name; litellm mode
// exports the gateway /v1 endpoint with the registry id; both modes pin
// WIRE_API=completions (the chat-completions wire — copilot's responses wire
// drops leading characters through the OpenAI-compatible bridge, breaking
// one-shot prompts). Native launches clear all COPILOT_* vars so the
// subscription wins. A wire regression here reintroduces the truncation bug;
// a model-identity regression sends LiteLLM a name it cannot resolve.
func TestGatewayMatrixCopilot(t *testing.T) {
	d := ByName("copilot")
	if d == nil {
		t.Fatal("copilot driver not registered")
	}

	t.Run("direct-ollama", func(t *testing.T) {
		m := matrixModels[0]
		lc := d.Build(m, false, directGateway())
		assertEnv(t, lc.Env, "COPILOT_PROVIDER_BASE_URL", "http://localhost:11434/v1")
		assertEnv(t, lc.Env, "COPILOT_PROVIDER_WIRE_API", "completions")
		assertEnv(t, lc.Env, "COPILOT_MODEL", m.ModelName)
	})

	for _, m := range matrixModels {
		t.Run("litellm-"+m.ProviderID, func(t *testing.T) {
			lc := d.Build(m, false, matrixGateway)
			assertEnv(t, lc.Env, "COPILOT_PROVIDER_BASE_URL", "http://localhost:4000/v1")
			assertEnv(t, lc.Env, "COPILOT_PROVIDER_API_KEY", "sk-litellm")
			assertEnv(t, lc.Env, "COPILOT_PROVIDER_WIRE_API", "completions")
			assertEnv(t, lc.Env, "COPILOT_MODEL", m.ID)
		})
	}

	t.Run("native-under-litellm", func(t *testing.T) {
		lc := d.Build(nativeModel("copilot"), false, matrixGateway)
		if len(lc.Env) != 0 {
			t.Errorf("native env = %v, want none (subscription must win over gateway)", lc.Env)
		}
		for _, k := range []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL"} {
			if !slices.Contains(lc.ClearEnv, k) {
				t.Errorf("native ClearEnv = %v, want it to include %q", lc.ClearEnv, k)
			}
		}
	})
}

// TestGatewayMatrixOpenCode pins opencode's Build across the matrix (no
// native cell — opencode is ollama-only after the native-provider alignment):
// direct mode builds inline JSON with the "ollama/<ModelName>" model ref and
// the Ollama /v1 baseURL; litellm mode builds the wt-declared
// @ai-sdk/openai-compatible provider at the gateway /v1 with the registry id
// as both the model ref suffix and the provider models-map key. The JSON is
// parsed rather than substring-matched so a malformed payload fails loudly.
func TestGatewayMatrixOpenCode(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}

	t.Run("direct-ollama", func(t *testing.T) {
		m := matrixModels[0]
		lc := d.Build(m, false, directGateway())
		content := envValue(t, lc.Env, "OPENCODE_CONFIG_CONTENT")
		var parsed struct {
			Model    string `json:"model"`
			Provider struct {
				Ollama struct {
					Options struct {
						BaseURL string `json:"baseURL"`
					} `json:"options"`
				} `json:"ollama"`
			} `json:"provider"`
		}
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			t.Fatalf("OPENCODE_CONFIG_CONTENT is not valid JSON: %v\n%s", err, content)
		}
		if parsed.Model != "ollama/"+m.ModelName {
			t.Errorf("model = %q, want ollama/%s (bare ModelName, not the registry id)", parsed.Model, m.ModelName)
		}
		if parsed.Provider.Ollama.Options.BaseURL != "http://localhost:11434/v1" {
			t.Errorf("baseURL = %q, want the Ollama /v1 endpoint", parsed.Provider.Ollama.Options.BaseURL)
		}
	})

	for _, m := range matrixModels {
		t.Run("litellm-"+m.ProviderID, func(t *testing.T) {
			lc := d.Build(m, false, matrixGateway)
			content := envValue(t, lc.Env, "OPENCODE_CONFIG_CONTENT")
			var parsed struct {
				Model      string `json:"model"`
				SmallModel string `json:"small_model"`
				Provider   map[string]struct {
					NPM     string `json:"npm"`
					Options struct {
						BaseURL string `json:"baseURL"`
						APIKey  string `json:"apiKey"`
					} `json:"options"`
					Models map[string]struct {
						Name string `json:"name"`
					} `json:"models"`
				} `json:"provider"`
			}
			if err := json.Unmarshal([]byte(content), &parsed); err != nil {
				t.Fatalf("OPENCODE_CONFIG_CONTENT is not valid JSON: %v\n%s", err, content)
			}
			p := parsed.Provider[opencodeGatewayProviderID]
			if p.NPM != "@ai-sdk/openai-compatible" {
				t.Errorf("npm = %q, want @ai-sdk/openai-compatible (chat wire)", p.NPM)
			}
			if p.Options.BaseURL != "http://localhost:4000/v1" {
				t.Errorf("baseURL = %q, want the gateway /v1 endpoint", p.Options.BaseURL)
			}
			if p.Options.APIKey != "sk-litellm" {
				t.Errorf("apiKey = %q, want the gateway key", p.Options.APIKey)
			}
			entry, ok := p.Models[m.ID]
			if !ok {
				t.Fatalf("provider models map lacks registry id %q (opencode rejects catalog-unknown ids): %v", m.ID, p.Models)
			}
			if entry.Name != m.ModelName {
				t.Errorf("models[%q].name = %q, want display name %q", m.ID, entry.Name, m.ModelName)
			}
			if parsed.Model != opencodeGatewayProviderID+"/"+m.ID {
				t.Errorf("model = %q, want %s/%s", parsed.Model, opencodeGatewayProviderID, m.ID)
			}
			if parsed.SmallModel != parsed.Model {
				t.Errorf("small_model = %q, want pinned to %q so background summarization queries the same gateway model", parsed.SmallModel, parsed.Model)
			}
		})
	}
}

// TestGatewayMatrixPi pins pi's Build across the matrix: direct mode passes
// --model with the bare ModelName resolved against pi's local "ollama"
// provider; litellm mode passes --model "litellm/<registry-id>" resolved
// against the wt-created "litellm" provider (pi splits --model on the first
// slash, so the registry id must ride under a provider whose id cannot appear
// as the id's first path segment — under "ollama", "openrouter/qwen/…" would
// be mis-split and the bare tail sent upstream). The fixture catalog carries
// every matrix id so both branches resolve; native launches pi bare.
func TestGatewayMatrixPi(t *testing.T) {
	d := ByName("pi")
	if d == nil {
		t.Fatal("pi driver not registered")
	}

	// One catalog fixture with both provider blocks populated for every
	// matrix model, so isLaunchable resolves in both modes.
	writePiModels(t, `{"providers":{
		"ollama":{"apiKey":"ollama","baseUrl":"http://localhost:11434/v1","models":[{"_launch":true,"id":"qwen3.8:27b-mlx"}]},
		"litellm":{"api":"openai-completions","apiKey":"sk-litellm","baseUrl":"http://localhost:4000/v1","models":[
			{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"},
			{"_launch":true,"id":"llamacpp/ornith-1.5-35b"},
			{"_launch":true,"id":"omlx/Ornith-1.5-35B-A3B-MLX-4bit"},
			{"_launch":true,"id":"openrouter/qwen/qwen3.8-27b"}
		]}
	}}`)

	t.Run("direct-ollama", func(t *testing.T) {
		m := matrixModels[0]
		lc := d.Build(m, false, directGateway())
		got, ok := argsFlagValue(lc.Args, "--model")
		if !ok || got != m.ModelName {
			t.Errorf("--model = %q, want bare %q", got, m.ModelName)
		}
		if lc.Warn != "" {
			t.Errorf("warn = %q, want empty for a launchable model", lc.Warn)
		}
	})

	for _, m := range matrixModels {
		t.Run("litellm-"+m.ProviderID, func(t *testing.T) {
			lc := d.Build(m, false, matrixGateway)
			got, ok := argsFlagValue(lc.Args, "--model")
			if !ok || got != piLitellmProviderID+"/"+m.ID {
				t.Errorf("--model = %q, want %s/%s", got, piLitellmProviderID, m.ID)
			}
			if lc.Warn != "" {
				t.Errorf("warn = %q, want empty for a launchable model", lc.Warn)
			}
		})
	}

	t.Run("native", func(t *testing.T) {
		lc := d.Build(nativeModel("pi"), false, matrixGateway)
		if len(lc.Args) != 0 || lc.Warn != "" {
			t.Errorf("native build = %+v, want bare pi", lc)
		}
	})
}

// TestGatewayMatrixAgy pins that agy's Build is invariant across the entire
// matrix: agy picks its model inside its own TUI, so neither the model
// fixture nor the gateway mode may leak into the launch. A regression would
// pass a stray --model or env var that agy's CLI rejects or misreads.
func TestGatewayMatrixAgy(t *testing.T) {
	d := ByName("agy")
	if d == nil {
		t.Fatal("agy driver not registered")
	}
	for _, m := range matrixModels {
		for _, gw := range []Gateway{directGateway(), matrixGateway} {
			lc := d.Build(m, false, gw)
			if len(lc.Args) != 0 || len(lc.Env) != 0 {
				t.Errorf("agy build for %s under %s = %+v, want bare agy", m.ID, gw.Mode, lc)
			}
		}
	}
}

// TestGatewayMatrixModelIdentityIsProviderIndependent is the matrix's
// meta-invariant: in litellm mode the only string that may vary with the
// provider fixture is the model identity itself. Building all four provider
// fixtures and diffing env against the ollama baseline catches any
// provider-keyed branch a driver grows — the fan-out to llamacpp/omlx/
// openrouter belongs inside LiteLLM, never in a wt driver. Without this, a
// driver could special-case one provider and still pass every per-fixture
// test above (each only pins its own expectation).
func TestGatewayMatrixModelIdentityIsProviderIndependent(t *testing.T) {
	base := matrixModels[0] // ollama fixture is the baseline
	drivers := []string{"claude", "codex", "copilot", "opencode"}
	for _, name := range drivers {
		d := ByName(name)
		if d == nil {
			t.Fatalf("%s driver not registered", name)
		}
		baseline := d.Build(base, false, matrixGateway)
		for _, m := range matrixModels[1:] {
			lc := d.Build(m, false, matrixGateway)
			// Ordered ID-first: the baseline ID contains the baseline
			// ModelName as a substring ("ollama/<name>"), so a map's random
			// iteration order could apply the ModelName substitution inside
			// the ID and corrupt it. Replacing the ID first is stable.
			replaced := []struct{ old, newStr string }{
				{base.ID, m.ID},
				{base.ModelName, m.ModelName},
			}
			if len(lc.Env) != len(baseline.Env) {
				t.Errorf("%s env length for %s = %d, want %d (baseline)", name, m.ProviderID, len(lc.Env), len(baseline.Env))
			}
			for i, e := range lc.Env {
				if i >= len(baseline.Env) {
					break
				}
				want := baseline.Env[i]
				// The baseline env entry with the baseline's identity swapped
				// for this fixture's must equal the fixture's entry.
				for _, r := range replaced {
					want = strings.ReplaceAll(want, r.old, r.newStr)
				}
				if e != want {
					t.Errorf("%s env[%d] for %s = %q, want baseline %q with only the model identity substituted — env shape must not depend on the provider", name, i, m.ProviderID, e, want)
				}
			}
		}
	}
}
