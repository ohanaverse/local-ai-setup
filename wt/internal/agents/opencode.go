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

// OpenCode routes through a wt-declared custom provider
// (@ai-sdk/openai-compatible, chat completions wire) in both gateway and
// direct mode. The builtin "openai" provider cannot serve registry ids
// (opencode resolves model ids against its own catalog → "Model not found")
// and its models.dev path speaks the responses API, whose bridged stream
// opencode cannot map.
//
// The model ref is "agent-wt/<provider-side-name>". OpenCode splits model
// refs on the first slash, so "agent-wt/<ModelName>" selects the wt provider
// while the provider-side model name survives verbatim inside it. The
// provider's own models map registers the same name so catalog-unknown IDs
// do not raise ProviderModelNotFoundError. small_model is pinned to the
// same provider so background summarization does not query the endpoint with
// names it does not expose (default gpt-5-nano → 400).
func (opencodeDriver) Build(m config.Model, yolo bool, r Route) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	baseURL := r.BaseOrigin + "/v1"
	modelRef := opencodeGatewayProviderID + "/" + r.ModelRef
	lc.Env = append(lc.Env, "OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
		`{"model":%q,"small_model":%q,"provider":{%q:{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":%q,"apiKey":%q},"models":{%q:{"name":%q}}}}}`,
		modelRef, modelRef, opencodeGatewayProviderID, baseURL, r.APIKey, r.ModelRef, r.Display,
	))
	return lc
}

// opencodeGatewayProviderID names the custom provider wt declares in
// OPENCODE_CONFIG_CONTENT for both LiteLLM-routed and direct launches. It
// must not collide with a models.dev-known provider id (those get catalog-
// validated); a unique id + npm @ai-sdk/openai-compatible makes opencode treat
// it as fully custom.
const opencodeGatewayProviderID = "agent-wt"
