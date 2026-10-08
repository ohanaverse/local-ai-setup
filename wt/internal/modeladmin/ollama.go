package modeladmin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// ollamaShowTimeout bounds the one `ollama show` an add runs.
const ollamaShowTimeout = 10 * time.Second

// runOllamaShow runs `ollama show <name>` against the daemon at host and
// returns its text. A seam: tests never run the developer's ollama.
var runOllamaShow = func(ctx context.Context, host, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "ollama", "show", name)
	// Pinned: without it the CLI asks whatever OLLAMA_HOST the shell has, or
	// the default port, which need not be the daemon the registry names.
	cmd.Env = append(os.Environ(), "OLLAMA_HOST="+host)
	out, err := cmd.Output()
	return string(out), err
}

// ollamaCapabilityKeys maps a capability `ollama show` lists to the
// model_info key LiteLLM reads (modelman's ollama_caps.py).
var ollamaCapabilityKeys = map[string]string{
	"tools":  "supports_function_calling",
	"vision": "supports_vision",
}

// UsesOllama reports whether a provider's models are ollama's, so an add of
// one can ask ollama what the model can do.
func UsesOllama(providerID string) bool { return localmodels.Family(providerID) == "ollama" }

// OllamaCapabilities asks ollama what model name can do and returns the
// model_info keys to record: supports_function_calling and supports_vision,
// each present (true) only when ollama lists the capability. wt copies
// model_info into the LiteLLM route, and an agent that sends tools to a
// route without supports_function_calling has them dropped. The daemon asked
// is the one the registry's ollama provider row names (cfg may be nil: the
// default address). An error means the lookup did not happen; the caller
// adds the model without the keys and says so.
func OllamaCapabilities(cfg *config.Config, name string) (map[string]any, error) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	host, _ := localmodels.FamilyOrigin(cfg, "ollama")
	ctx, cancel := context.WithTimeout(context.Background(), ollamaShowTimeout)
	defer cancel()
	out, err := runOllamaShow(ctx, host, name)
	if err != nil {
		return nil, fmt.Errorf("`ollama show %s` (at %s) failed: %w", name, host, err)
	}
	return parseOllamaShow(out), nil
}

// parseOllamaShow reads the Capabilities section of `ollama show`'s text: a
// line reading "Capabilities", then one capability per line until a blank
// line.
func parseOllamaShow(text string) map[string]any {
	info := map[string]any{}
	inCaps := false
	for _, line := range strings.Split(text, "\n") {
		word := strings.TrimSpace(line)
		switch {
		case word == "":
			inCaps = false
		case strings.EqualFold(word, "capabilities"):
			inCaps = true
		case inCaps:
			if key, ok := ollamaCapabilityKeys[word]; ok {
				info[key] = true
			}
		}
	}
	return info
}
