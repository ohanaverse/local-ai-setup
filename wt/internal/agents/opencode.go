package agents

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/session"
)

func init() { register("opencode", func() Driver { return opencodeDriver{} }) }

type opencodeDriver struct{}

func (opencodeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

func (opencodeDriver) OllamaURL() string { return config.OllamaBaseURL + "/v1" }

func (opencodeDriver) ResumeFlag() string { return "--session" }

func (opencodeDriver) LatestSession(path string) (*session.Session, error) {
	projectID, err := session.OpenCodeProjectID(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode",
		"storage", "session", projectID)
	return session.LatestByExt(dir, ".json", func(f os.FileInfo) string {
		return f.Name()
	})
}

// OpenCode is ollama-only after the native-provider alignment. The
// OPENCODE_CONFIG_CONTENT env var routes through the ollama gateway with
// the provider/model form ("ollama/<ModelName>"). The bare provider-side
// name comes from m.ModelName, not m.ID — m.ID already carries the
// "ollama/" registry prefix, so using it would produce
// "ollama/ollama/<model>" (a double prefix that the gateway rejects).
//
// In gateway mode OpenCode routes through a wt-declared custom provider
// (@ai-sdk/openai-compatible, chat completions wire) pointed at LiteLLM's
// /v1 endpoint, with the registry id declared in the provider's models map.
// The builtin "openai" provider cannot serve registry ids (opencode resolves
// model ids against its own catalog → "Model not found") and its models.dev
// path speaks the responses API, whose bridged stream opencode cannot map.
// OpenCode splits model refs on the first slash, so "agent-wt/<m.ID>"
// selects the wt provider while the registry id survives verbatim as the
// model name inside it. small_model is pinned to the same provider so
// background summarization does not query the proxy with model names it
// does not expose (default gpt-5-nano → 400).
func (opencodeDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	if gw.IsLitellm() {
		baseURL := gw.BaseURL() + "/v1"
		modelRef := opencodeGatewayProviderID + "/" + m.ID
		lc.Env = append(lc.Env, "OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
			`{"model":%q,"small_model":%q,"provider":{%q:{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":%q,"apiKey":%q},"models":{%q:{"name":%q}}}}}`,
			modelRef, modelRef, opencodeGatewayProviderID, baseURL, gw.APIKey, m.ID, m.ModelName,
		))
		return lc
	}
	// The builtin "ollama" provider resolves model ids against opencode's own
	// catalog (models.dev), so a registry model absent from that catalog —
	// every local/cloud model wt launches — is rejected with
	// ProviderModelNotFoundError. Declaring the bare name in the provider's
	// models map registers it explicitly, the same workaround the litellm
	// branch uses for the custom provider.
	lc.Env = append(lc.Env,
		"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
			`{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s","apiKey":""},"models":{%q:{"name":%q}}}}}`,
			m.ModelName, opencodeDriver{}.OllamaURL(), m.ModelName, m.ModelName,
		),
	)
	return lc
}

// opencodeGatewayProviderID names the custom provider wt declares in
// OPENCODE_CONFIG_CONTENT for LiteLLM-routed launches. It must not collide
// with a models.dev-known provider id (those get catalog-validated); a unique
// id + npm @ai-sdk/openai-compatible makes opencode treat it as fully custom.
const opencodeGatewayProviderID = "agent-wt"
