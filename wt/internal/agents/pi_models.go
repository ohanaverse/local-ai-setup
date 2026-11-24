package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

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
// relevant to wt; fields it has no place for are dropped when a block is
// re-encoded from this struct. Only the block wt owns (`litellm`) is encoded
// from it: syncGateway writes every other block back as the raw JSON it read.
//
// WTOwned marks a block wt itself created: the direct-route sync before the
// model server set it on the non-ollama provider blocks it wrote (never on
// the local `ollama` block, which predates the marker). The gateway sync
// (syncGateway) removes every marked block except `litellm`; an unmarked
// block, a user's own or that old `ollama` one, is left as it is.
// omitempty, so an unmarked block round-trips with no spurious
// "_wtOwned": false.
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

const (
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
)

// piDefaultWindow is the contextWindow wt writes for a launch entry whose
// window the catalog does not know (the value wt always wrote before #689).
const piDefaultWindow = 262144

// piTarget is one gateway id wt syncs into pi's litellm block with the window
// the catalog reports for it (0 when unknown).
type piTarget struct {
	ID     string
	Window int
}

// launchEntry is the models.json entry wt writes for a model it launches
// under id. window is the catalog's context window for it; a window that is
// not positive (unknown) falls back to piDefaultWindow.
func launchEntry(id string, window int) piModel {
	if window <= 0 {
		window = piDefaultWindow
	}
	return piModel{
		Launch:        true,
		ContextWindow: window,
		ID:            id,
		Input:         []string{"text", "image"},
		Reasoning:     true,
	}
}

// syncLitellmBlock is the gateway sync's block builder: provider "litellm" gets baseURL+"/v1" and apiKey (and the
// openai-completions api when it has none) plus a _launch entry for every id
// it lacks. Existing entries are never removed or reordered. The catalog is
// the source of truth for a window: an existing entry wt launches
// (_launch: true) has its contextWindow updated when the catalog's is known
// (> 0) and differs; no other field changes, and an entry with an unknown
// catalog window keeps the value it has (#689). Returns whether the block
// changed.
func syncLitellmBlock(f piModelsFile, baseURL, apiKey string, targets []piTarget) bool {
	p := f.Providers[piLitellmProviderID]
	mutated := false
	if p.BaseURL != baseURL+"/v1" || p.APIKey != apiKey {
		p.BaseURL = baseURL + "/v1"
		p.APIKey = apiKey
		if p.API == "" {
			p.API = "openai-completions"
		}
		mutated = true
	}
	// p.Models is shared with the caller's copy of the block; clone it before
	// an in-place window update so a no-op sync never writes through it.
	p.Models = slices.Clone(p.Models)
	index := make(map[string]int, len(p.Models))
	for i, m := range p.Models {
		index[m.ID] = i
	}
	for _, t := range targets {
		if i, ok := index[t.ID]; ok {
			if m := &p.Models[i]; m.Launch && t.Window > 0 && m.ContextWindow != t.Window {
				m.ContextWindow = t.Window
				mutated = true
			}
			continue
		}
		p.Models = append(p.Models, launchEntry(t.ID, t.Window))
		index[t.ID] = len(p.Models) - 1
		mutated = true
	}
	if mutated {
		f.Providers[piLitellmProviderID] = p
	}
	return mutated
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

// syncGateway is SyncGateway over an explicit path. The catalog models plus
// the launch target (when the catalog lacks it) each get an entry keyed by
// gatewayID.
func syncGateway(gw Gateway, path string, models []config.Model, target config.Model) error {
	var f piModelsFile
	var rawTop map[string]json.RawMessage
	rawProviders := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &f); err != nil {
			return err
		}
		// The typed decode above drops fields wt has no struct for. Keep the raw
		// text of everything wt does not own so the write below can put it back.
		if err := json.Unmarshal(data, &rawTop); err != nil {
			return err
		}
		if raw, ok := rawTop["providers"]; ok {
			if err := json.Unmarshal(raw, &rawProviders); err != nil {
				return err
			}
		}
	}
	if f.Providers == nil {
		f.Providers = make(map[string]piProvider, 1)
	}
	var targets []piTarget
	for _, m := range append(slices.Clone(models), target) {
		if m.Native || gatewayID(m) == "" {
			continue
		}
		targets = append(targets, piTarget{ID: gatewayID(m), Window: m.HostContext})
	}
	mutated := syncLitellmBlock(f, gw.origin(), gw.Token, targets)
	for id, p := range f.Providers {
		if id != piLitellmProviderID && p.WTOwned {
			delete(f.Providers, id)
			mutated = true
		}
	}
	if !mutated {
		// Nothing to change, but the file holds the token: keep it owner-only.
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() != 0o600 {
			return os.Chmod(path, 0o600)
		}
		return nil
	}
	providers := make(map[string]json.RawMessage, len(f.Providers))
	for id, p := range f.Providers {
		raw, kept := rawProviders[id]
		if id == piLitellmProviderID || !kept {
			b, err := json.Marshal(p)
			if err != nil {
				return err
			}
			raw = b
		}
		// A block wt does not own (a user's external-gateway provider with a
		// `!command` apiKey, #618) goes back exactly as read, with the fields
		// piProvider has no place for.
		providers[id] = raw
	}
	if rawTop == nil {
		rawTop = make(map[string]json.RawMessage, 1)
	}
	b, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	rawTop["providers"] = b
	out, err := json.MarshalIndent(rawTop, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, out, 0o600)
}
