package litellm

import "testing"

// These tests pin the LiteLLM policy table so it can move into
// internal/provider without changing any route wt builds.

// TestPoliciesPinned pins the LiteLLM mapping of every routable provider and
// the absence of one for natives and unknown ids: a gap here means a started
// model gets no route (the omlx-6bit bug).
func TestPoliciesPinned(t *testing.T) {
	want := map[string]Policy{
		"ollama":        {Prefix: "ollama_chat/"},
		"omlx":          {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"omlx-6bit":     {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"mlx_lm_server": {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"mtplx":         {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"llamacpp":      {Prefix: "openai/local-model", FixedModel: true, APIKey: "dummy-key"},
		"openrouter":    {Prefix: "openrouter/", SecretRef: true, Cloud: true},
	}
	for id, w := range want {
		got, ok := PolicyFor(id)
		if !ok || got != w {
			t.Errorf("PolicyFor(%q) = %+v, %v; want %+v, true", id, got, ok, w)
		}
	}
	for _, id := range []string{"agy", "claude", ""} {
		if p, ok := PolicyFor(id); ok {
			t.Errorf("PolicyFor(%q) = %+v, true; want no policy (native or unknown)", id, p)
		}
	}
}

// TestProvidersPinned pins `wt litellm providers`: the provider ids wt can
// route and which of them are cloud.
func TestProvidersPinned(t *testing.T) {
	want := map[string]bool{
		"ollama": false, "omlx": false, "omlx-6bit": false, "mlx_lm_server": false,
		"mtplx": false, "rec": false, "llamacpp": false, "openrouter": true,
	}
	got := Providers()
	if len(got) != len(want) {
		t.Fatalf("Providers() = %v, want %v", got, want)
	}
	for id, cloud := range want {
		if c, ok := got[id]; !ok || c != cloud {
			t.Errorf("Providers()[%q] = %v, %v; want %v, true", id, c, ok, cloud)
		}
	}
}
