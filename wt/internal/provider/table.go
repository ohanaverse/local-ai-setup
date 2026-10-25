package provider

import "strconv"

// OllamaOrigin is where ollama listens by default.
const OllamaOrigin = "http://localhost:11434"

// MtplxPort is the port wt hands mtplx when the registry's base_url names
// none (wt serves it on 127.0.0.1 only).
const MtplxPort = 8003

// OpenRouterKeyEnv is the environment variable the seeded openrouter row names
// as its key: the name OpenRouter's own tools and this repo's LiteLLM setup use.
const OpenRouterKeyEnv = "OPENROUTER_API_KEY"

var mtplxOrigin = "http://localhost:" + strconv.Itoa(MtplxPort)

var openaiLocal = Policy{Prefix: "openai/", APIKey: "not-needed", V1Base: true}

// providers is every registry provider id wt knows, in the order seeded rows
// are added. A provider with a Family must have a Policy (TestTableInvariants).
var providers = []Spec{
	{
		ID: "ollama", Family: "ollama",
		Policy: &Policy{Prefix: "ollama_chat/"},
		Seed: &Seed{
			Name: "Ollama", Location: "local", Protocols: []string{"anthropic", "openai-chat"},
			AuthType: "none", BaseURL: OllamaOrigin, Command: "ollama",
		},
	},
	{
		ID: "omlx", Family: "omlx",
		Policy: &openaiLocal,
		Seed: &Seed{
			Name: "oMLX", Location: "local", AuthType: "none", BaseURL: "http://localhost:8000",
			Command: "omlx", PathSuppressedBy: "omlx-6bit",
		},
	},
	// omlx-6bit is the same physical oMLX server serving the 6-bit variants,
	// so it maps exactly like omlx (each provider's own registry base_url is
	// what the row dials). Without a policy a started 6-bit model got no
	// route at all and its stale rows survived the family sweep on stop.
	{ID: "omlx-6bit", Family: "omlx", Policy: &openaiLocal},
	{
		ID: "mlx_lm_server", Family: "mlx_lm_server",
		Policy: &openaiLocal,
		Seed: &Seed{
			Name: "mlx-lm server (target+draft)", Location: "local", AuthType: "none",
			BaseURL: "http://localhost:8001/v1",
		},
	},
	{
		ID: "mtplx", Family: "mtplx",
		Policy: &openaiLocal,
		Seed: &Seed{
			Name: "MTPLX", Location: "local", AuthType: "none", BaseURL: "http://localhost:8003/v1",
			ModelDir: "~/.mtplx/models", Command: "mtplx",
		},
	},
	{ID: "llamacpp", Policy: &Policy{Prefix: "openai/local-model", FixedModel: true, APIKey: "dummy-key"}},
	{
		ID:     "openrouter",
		Policy: &Policy{Prefix: "openrouter/", SecretRef: true, Cloud: true},
		Seed: &Seed{
			Name: "OpenRouter", Location: "cloud", AuthType: "api_key",
			SecretRef: OpenRouterKeyEnv, BaseURL: "https://openrouter.ai/api/v1",
		},
	},
}

// families is the facts shared by each server's provider ids.
var families = map[string]FamilySpec{
	"ollama": {
		Name: "ollama", DefaultOrigin: OllamaOrigin,
		RoutesFollowArtifact: true, Tenancy: Shared,
	},
	"omlx": {
		Name: "omlx", DefaultOrigin: "http://localhost:8000", DefaultModelDir: "~/.omlx/models",
		Served: true, Tenancy: Pool,
	},
	"mtplx": {
		Name: "mtplx", DefaultOrigin: mtplxOrigin, DefaultModelDir: "~/.mtplx/models",
		WtPort: MtplxPort, Served: true, Tenancy: Exclusive,
	},
	// mlx_lm_server is NoTenancy: wt cannot start it (llmbench does), so
	// lifecycle.TenancyOf has no backend for it.
	"mlx_lm_server": {
		Name: "mlx_lm_server", DefaultOrigin: "http://localhost:8001",
		RunningOnly: true, Served: true,
	},
}
