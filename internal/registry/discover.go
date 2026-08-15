package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Discoverer discovers models from a single source.
type Discoverer interface {
	Discover() ([]config.Model, error)
}

// Ollama lists models via `ollama list`. The CLI prints both local and cloud
// models; cloud entries have "-" in the SIZE column.
type Ollama struct{}

func (Ollama) Discover() ([]config.Model, error) {
	if _, err := exec.LookPath("ollama"); err != nil {
		return nil, nil // ollama not installed — no models to discover
	}
	out, err := exec.Command("ollama", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("ollama list: %w", err)
	}
	return parseOllamaList(string(out)), nil
}

// parseOllamaList turns the output of `ollama list` into Model entries.
// Exported for testing.
func parseOllamaList(output string) []config.Model {
	var models []config.Model
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if i == 0 {
			continue // header row: NAME  ID  SIZE  MODIFIED
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[0]
		if name == "" {
			continue
		}
		// Cloud models have "-" in the SIZE column; local models have a size.
		loc := config.LocationLocal
		if fields[2] == "-" {
			loc = config.LocationCloud
		}
		models = append(models, config.Model{
			ID:         "ollama/" + name,
			Family:     name,
			ProviderID: "ollama",
			ModelName:  name,
			Location:   loc,
			Source:     config.SourceDiscovered,
		})
	}
	return models
}

// OpenRouter lists cloud models via the OpenRouter REST API.
type OpenRouter struct {
	Client *http.Client
}

func (or OpenRouter) Discover() ([]config.Model, error) {
	if or.Client == nil {
		or.Client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := or.Client.Get("https://openrouter.ai/api/v1/models")
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("openrouter decode: %w", err)
	}
	models := make([]config.Model, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := m.ID
		family := id
		if idx := strings.LastIndex(id, "/"); idx >= 0 {
			family = id[idx+1:]
		}
		models = append(models, config.Model{
			ID:         "openrouter/" + id,
			Family:     family,
			ProviderID: "openrouter",
			ModelName:  id,
			Location:   config.LocationCloud,
			Source:     config.SourceDiscovered,
		})
	}
	return models, nil
}
