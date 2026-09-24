package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// piModelsFile mirrors the on-disk shape of ~/.pi/agent/models.json: a map of
// provider id to provider config. wt manages exactly two keys — "ollama" (the
// direct local-Ollama provider) and "litellm" (a wt-created gateway provider);
// every other provider entry round-trips untouched.
type piModelsFile struct {
	Providers map[string]piProvider `json:"providers"`
}

// piProvider is one provider block in pi's models.json. Only these fields are
// relevant to wt; unknown siblings pi supports round-trip through the generic
// JSON decode only if they are absent — extra fields inside a provider block
// are dropped, so wt never writes provider blocks it does not own.
//
// WTOwned marks a non-ollama block as one wt itself created or has since
// confirmed ownership of (see syncDirectProviders). Recording this
// explicitly, instead of re-inferring it from whether the block's current
// models are all still in the registry's model set, survives a registry
// model rename/removal — the append-only model list would otherwise
// permanently retain a defunct id and make the inference misfire forever.
// omitempty because the whole file is rewritten on every mutation
// (syncModels); without it every foreign block would gain a spurious
// "_wtOwned": false the first time anything else in the file changes.
type piProvider struct {
	API     string    `json:"api"`
	APIKey  string    `json:"apiKey"`
	BaseURL string    `json:"baseUrl"`
	Models  []piModel `json:"models"`
	WTOwned bool      `json:"_wtOwned,omitempty"`
	// Compat carries pi's per-provider wire-compat overrides (its own
	// ProviderConfigSchema.compat). Only SupportsStore is populated today,
	// for the nyt-litellm gateway workaround below; nil omits the field
	// entirely so providers that don't need it round-trip unchanged.
	Compat *piCompat `json:"compat,omitempty"`
}

// piCompat mirrors the subset of pi's OpenAICompletionsCompatSchema wt
// needs to write. Pointer fields so "unset" (omit) is distinct from
// "false".
type piCompat struct {
	SupportsStore *bool `json:"supportsStore,omitempty"`
}

// piModel is a single entry in pi's model catalog.
type piModel struct {
	Launch        bool     `json:"_launch"`
	ContextWindow int      `json:"contextWindow"`
	ID            string   `json:"id"`
	Input         []string `json:"input"`
	Reasoning     bool     `json:"reasoning"`
}

// piModelsPath resolves the location of pi's model catalog.
func piModelsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent", "models.json"), nil
}

const defaultPiOllamaBaseURL = config.OllamaBaseURL + "/v1"

// defaultPiOllamaAPIKey is pi's placeholder apiKey for keyless local Ollama.
// pi's models.json schema requires a non-empty apiKey; an empty value makes
// pi reject the whole catalog.
const defaultPiOllamaAPIKey = "ollama"

const (
	// piOllamaProviderID is pi's direct local-Ollama provider. Direct-mode
	// entries are keyed by bare model name (config.Model.ModelName).
	piOllamaProviderID = "ollama"

	// piLitellmProviderID hosts gateway models keyed by full registry id
	// (config.Model.ID, e.g. "ollama/glm-5.3-flash:cloud"). They must not live
	// under the "ollama" provider: pi resolves a --model value by splitting on
	// the FIRST slash and matching the remainder against the named provider's
	// entries, then sends the matched entry's id verbatim as the API model
	// name. Under "ollama", a registry id would be swallowed as provider
	// segment + bare pattern, the bare entry would win, and LiteLLM would
	// receive the unprefixed name. A provider whose id cannot appear as the
	// first path segment of a registry model id keeps ids verbatim.
	piLitellmProviderID = "litellm"

	// nytLitellmProviderID is the NYT-internal LiteLLM gateway registry
	// provider. Its Bedrock-backed models 400 on the OpenAI `store` param
	// (UnsupportedParamsError) rather than dropping it, and pi's
	// openai-completions client defaults to sending store:false for any
	// provider not on its own hardcoded non-standard-provider list. See
	// the supportsStoreFalse call in syncDirectProviders.
	nytLitellmProviderID = "nyt-litellm"
)

// supportsStoreFalse is pi's compat override for providers whose backend
// rejects the OpenAI `store` param outright (see nytLitellmProviderID).
var supportsStoreFalse = &piCompat{SupportsStore: boolPtr(false)}

func boolPtr(b bool) *bool { return &b }

// hasSupportsStoreFalse reports whether c already carries the
// supportsStoreFalse override, so a resync doesn't mark the file mutated
// (and rewrite it) on every launch once the override is in place.
func hasSupportsStoreFalse(c *piCompat) bool {
	return c != nil && c.SupportsStore != nil && !*c.SupportsStore
}

// isLocalOllamaBaseURL reports whether baseURL points at the local Ollama
// OpenAI-compatible endpoint (localhost or 127.0.0.1 on port 11434), in any
// form pi or wt may have written. Used by the direct-mode reset to normalize
// local-ollama values without touching a user's custom remote provider.
func isLocalOllamaBaseURL(baseURL string) bool {
	return baseURL == defaultPiOllamaBaseURL || baseURL == "http://127.0.0.1:11434/v1"
}

// isDefaultOllamaAPIKey reports whether apiKey is the default local-ollama key
// (empty or "ollama"). A non-default key is treated as user config and
// preserved by the direct-mode reset. The revert writes "ollama" — pi's
// documented keyless-local placeholder — because an empty apiKey fails pi's
// models.json schema validation (min length 1) and would invalidate the
// entire catalog.
func isDefaultOllamaAPIKey(apiKey string) bool {
	return apiKey == "" || apiKey == "ollama"
}

// syncModels updates the wt-owned parts of pi's models.json. It is idempotent
// and leaves every non-wt provider and model entry untouched.
//
// Direct mode adds any non-native cfg models missing from the "ollama"
// provider (keyed by bare ModelName, marked _launch: true) and resets the
// ollama provider to the standard local endpoint when its values are either
// gateway values wt itself wrote (they match the configured gateway) or
// local-ollama values in a non-canonical form. A user's own custom provider
// config (e.g. a remote ollama server) is preserved verbatim. (Limitation: if
// the whole [gateway] section was deleted rather than flipped to
// mode="direct", the URL is gone and the revert cannot match; the user must
// edit models.json by hand in that case.)
//
// Direct mode also writes one provider block per non-ollama registry
// provider that has models (see syncDirectProviders): a pre-existing block's
// baseUrl/apiKey are resynced to the registry's current values only when wt
// owns the block (marked _wtOwned, or recognizes every model already in it),
// so a user's independently-configured provider sharing that provider id
// keeps its own baseUrl/apiKey. Registry models are still appended to the
// block's model list either way — only the connection values are protected.
//
// Litellm mode creates/updates the dedicated "litellm" provider with
// registry-id entries pointing at the LiteLLM gateway, restores a
// previously-gateway-redirected ollama provider to the local endpoint (its
// bare entries would 400 through LiteLLM), and prunes the "ollama/…" entries
// an older litellm-mode sync left under the ollama provider, which pi's
// --model grammar can never launch.
func syncModels(cfg *config.Config, path string, target config.Model) error {
	var f piModelsFile
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// No catalog yet. In gateway mode, create one so pi routes through
		// LiteLLM on first launch; in direct mode there is nothing to sync.
		if !cfg.IsLitellm() {
			return nil
		}
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &f); err != nil {
			return err
		}
	}
	if f.Providers == nil {
		f.Providers = make(map[string]piProvider, 2)
	}

	mutated := false
	if cfg.IsLitellm() {
		mutated = syncLitellmProvider(cfg, f) || mutated
		mutated = revertOllamaProvider(cfg, f) || mutated
	} else {
		directMutated, err := syncDirectProviders(cfg, f, target)
		if err != nil {
			return err
		}
		mutated = directMutated || mutated
	}
	if !mutated {
		return nil
	}

	out, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, out, 0o644)
}

// syncLitellmProvider ensures provider "litellm" exists with the configured
// gateway endpoint and one _launch entry per non-native cfg model, keyed by
// full registry id. Existing entries are never removed or reordered.
func syncLitellmProvider(cfg *config.Config, f piModelsFile) bool {
	p := f.Providers[piLitellmProviderID]
	mutated := false
	if p.BaseURL != cfg.LitellmBaseURL()+"/v1" || p.APIKey != cfg.LitellmAPIKey() {
		p.BaseURL = cfg.LitellmBaseURL() + "/v1"
		p.APIKey = cfg.LitellmAPIKey()
		if p.API == "" {
			p.API = "openai-completions"
		}
		mutated = true
	}
	existing := make(map[string]bool, len(p.Models))
	for _, m := range p.Models {
		existing[m.ID] = true
	}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		if existing[m.ID] {
			continue
		}
		p.Models = append(p.Models, piModel{
			Launch:        true,
			ContextWindow: 262144,
			ID:            m.ID,
			Input:         []string{"text", "image"},
			Reasoning:     true,
		})
		existing[m.ID] = true
		mutated = true
	}
	if mutated {
		f.Providers[piLitellmProviderID] = p
	}
	return mutated
}

// revertOllamaProvider undoes the gateway redirect an older litellm-mode sync
// wrote into pi's ollama provider (restoring the standard local endpoint so
// its bare-named entries work directly again) and prunes launcher-generated
// registry-id entries that pi's --model grammar can never resolve properly.
// Values that do not match the configured gateway or the local endpoint are
// user config and preserved. Returns whether anything changed.
func revertOllamaProvider(cfg *config.Config, f piModelsFile) bool {
	p, ok := f.Providers[piOllamaProviderID]
	if !ok {
		return false
	}
	mutated := false
	if isLocalOllamaBaseURL(p.BaseURL) || p.BaseURL == cfg.LitellmBaseURL()+"/v1" {
		p.BaseURL = defaultPiOllamaBaseURL
		// The apiKey was local-ollama or gateway-set; restore pi's placeholder.
		if isDefaultOllamaAPIKey(p.APIKey) || (cfg.LitellmAPIKey() != "" && p.APIKey == cfg.LitellmAPIKey()) {
			if p.APIKey != defaultPiOllamaAPIKey {
				p.APIKey = defaultPiOllamaAPIKey
				mutated = true
			}
		}
		mutated = true
	}
	// Prune wt-generated litellm artifacts: entries whose id is the full
	// registry id of a non-native cfg model. Only in gateway mode, where the
	// replacement entries exist; a bare ModelName that coincides with any
	// such id is never equal to its own registry key, so direct-mode-managed
	// entries are never pruned.
	kept := p.Models[:0]
	for _, m := range p.Models {
		prune := false
		for _, cm := range cfg.Models {
			if !cm.Native && cm.ModelName != "" && m.ID == cm.ID && m.ID != cm.ModelName {
				prune = true
				break
			}
		}
		if prune {
			mutated = true
			continue
		}
		kept = append(kept, m)
	}
	if mutated {
		p.Models = kept
		f.Providers[piOllamaProviderID] = p
	}
	return mutated
}

// syncDirectProviders writes one pi provider entry per registry provider
// id (not just ollama), using each provider's own base_url and secret_ref,
// so a direct-mode launch of, e.g., an openrouter model resolves against
// openrouter's own endpoint instead of being folded into the "ollama"
// provider under a bare, possibly-slashed model name. pi splits --model on
// the first slash, so an unqualified model name containing a slash (e.g.
// openrouter's "z-ai/glm-4.6") would otherwise resolve against provider
// "z-ai" silently. Providers without a base_url are skipped — the same
// condition surfaces as an error from ResolveRoute at launch time.
//
// A pre-existing non-ollama block is only resynced (baseUrl/apiKey reset to
// the registry's current values) when every model already in it is one wt
// itself would write for that provider (see the "wt-owned" check below); a
// block holding a model wt doesn't know about is a signal the user
// configured that provider id in pi independently of this registry, and its
// baseUrl/apiKey are left untouched — same "wt never writes blocks it
// doesn't own" contract ollama gets via its fixed-pattern check, just
// detected differently since non-ollama base_urls are arbitrary registry
// values with no fixed "known stale" form to match against.
//
// A secret_ref resolution failure (e.g. a failing exec: credential helper)
// only aborts the sync when it belongs to target's own provider — the
// model actually being launched. Any other provider's broken helper is
// logged to stderr and that provider's block is left as-is (skipped this
// run, not written or resynced); the sync still succeeds for target's own
// provider and every other healthy one. target may be the zero Model (no
// specific launch in progress), in which case no provider is ever treated
// as the target and every failure is logged rather than fatal. Map
// iteration order over byProvider means, when multiple non-target
// providers fail in the same run, which one's warning prints first is
// unspecified — harmless now that no such failure can abort the sync.
//
// Returns whether anything changed.
func syncDirectProviders(cfg *config.Config, f piModelsFile, target config.Model) (bool, error) {
	byProvider := map[string][]config.Model{}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}

	mutated := false
	for providerID, models := range byProvider {
		provider := cfg.ProviderByID(providerID)
		if provider == nil || provider.Auth.BaseURL == "" {
			continue
		}

		wantBaseURL := config.BaseOrigin(provider.Auth.BaseURL) + "/v1"
		wantAPIKey := defaultPiOllamaAPIKey
		if provider.Auth.SecretRef != "" {
			var err error
			wantAPIKey, err = config.ResolveSecret(provider.Auth.SecretRef)
			if err != nil {
				if providerID == target.ProviderID {
					return false, fmt.Errorf("pi models.json sync: provider %q: %w", providerID, err)
				}
				fmt.Fprintf(os.Stderr, "wt: pi models.json sync: provider %q: %v (skipping — not the model being launched)\n", providerID, err)
				continue
			}
		}
		// pi's models.json schema requires a non-empty apiKey (see
		// defaultPiOllamaAPIKey); a provider whose secret is simply unset
		// (an os.environ/NAME or bare-name ref that resolved to "" with no
		// error) cannot produce a schema-valid block, so skip it entirely
		// rather than write one and poison the whole catalog. An exec: form
		// that fails outright is handled above and never reaches here.
		if wantAPIKey == "" {
			continue
		}

		p, existed := f.Providers[providerID]
		if !existed {
			// Wholly-new provider block: set the identity fields
			// unconditionally. The guarded "reset WT-written values" logic
			// below only fires on pre-existing (gateway-redirected) blocks —
			// without this branch a fresh block would be written with empty
			// baseUrl/apiKey, which invalidates the entire models.json for pi.
			p.API = "openai-completions"
			p.BaseURL = wantBaseURL
			p.APIKey = wantAPIKey
			if providerID != piOllamaProviderID {
				p.WTOwned = true
			}
			if providerID == nytLitellmProviderID {
				p.Compat = supportsStoreFalse
			}
			mutated = true
		}

		existing := make(map[string]bool, len(p.Models))
		for _, m := range p.Models {
			existing[m.ID] = true
		}

		// Ownership decides whether a pre-existing non-ollama block's
		// baseUrl/apiKey get resynced below (see the "else" branch). Must be
		// evaluated against the block's models from before this call's
		// additions, so it's computed here, ahead of the append loop.
		var owned bool
		if providerID != piOllamaProviderID {
			owned = p.WTOwned
			if !owned {
				// Legacy (pre-marker) block: fall back to requiring every
				// model already in it to still be one wt would write for
				// this provider today. See the piProvider.WTOwned doc
				// comment for why this is fragile and the marker exists.
				wantedModelNames := make(map[string]bool, len(models))
				for _, m := range models {
					wantedModelNames[m.ModelName] = true
				}
				// A block with no existing models can't confirm ownership
				// this way — an empty set is vacuously "all models match",
				// which would misclassify a foreign block someone is still
				// populating (e.g. their own "openrouter" entry with a
				// custom baseUrl/apiKey but no models yet) as wt-owned.
				owned = len(existing) > 0
				for id := range existing {
					if !wantedModelNames[id] {
						owned = false
						break
					}
				}
			}
		}

		for _, m := range models {
			if existing[m.ModelName] {
				continue
			}
			p.Models = append(p.Models, piModel{
				Launch:        true,
				ContextWindow: 262144,
				ID:            m.ModelName,
				Input:         []string{"text", "image"},
				Reasoning:     true,
			})
			existing[m.ModelName] = true
			mutated = true
		}

		if existed {
			if p.API == "" {
				p.API = "openai-completions"
				mutated = true
			}
			if providerID == piOllamaProviderID {
				// ollama uniquely supports a legitimate user-customized remote
				// endpoint (see TestSyncModelsDirectPreservesCustomProvider), so
				// only reset values wt is known to have written itself: gateway
				// redirects or non-canonical local-ollama forms.
				if isLocalOllamaBaseURL(p.BaseURL) || p.BaseURL == cfg.LitellmBaseURL()+"/v1" {
					if p.BaseURL != wantBaseURL {
						p.BaseURL = wantBaseURL
						mutated = true
					}
					if isDefaultOllamaAPIKey(p.APIKey) || (cfg.LitellmAPIKey() != "" && p.APIKey == cfg.LitellmAPIKey()) {
						if p.APIKey != wantAPIKey {
							p.APIKey = wantAPIKey
							mutated = true
						}
					}
				}
			} else {
				// Resync baseUrl/apiKey to the registry's current values, but
				// only when the block is owned (see above). Without resyncing
				// at all, a value wt itself wrote at some earlier point (e.g.
				// a registry base_url that has since changed) gets
				// permanently stuck — isLaunchable only checks that the model
				// id is present and _launch:true, never the provider's
				// baseUrl, so a stale port silently breaks every launch
				// against that provider with no error surfaced. But
				// resyncing unconditionally would clobber a user's
				// independently-configured pi provider that happens to share
				// this provider id (e.g. a personal "openrouter" block with
				// its own apiKey).
				if owned {
					if p.BaseURL != wantBaseURL {
						p.BaseURL = wantBaseURL
						mutated = true
					}
					if p.APIKey != wantAPIKey {
						p.APIKey = wantAPIKey
						mutated = true
					}
					if !p.WTOwned {
						// Confirmed via the legacy inference, not the
						// marker: stamp it now so this block stays
						// resyncable even after a future registry model
						// rename/removal would otherwise defeat that
						// inference.
						p.WTOwned = true
						mutated = true
					}
					if providerID == nytLitellmProviderID && !hasSupportsStoreFalse(p.Compat) {
						p.Compat = supportsStoreFalse
						mutated = true
					}
				}
			}
		}

		if mutated {
			f.Providers[providerID] = p
		}
	}
	return mutated, nil
}

// isLaunchable reports whether id is present under providerID in pi's
// models.json and marked _launch: true. A missing or unparseable file is
// treated as "not launchable" (the caller falls back to pi's default model).
func isLaunchable(providerID, id, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return false
	}
	for _, m := range f.Providers[providerID].Models {
		if m.ID == id && m.Launch {
			return true
		}
	}
	return false
}
