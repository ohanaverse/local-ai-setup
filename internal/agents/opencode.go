package agents

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/session"
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

// OpenCode is ollama-only after the native-provider alignment. The
// OPENCODE_CONFIG_CONTENT env var routes through the ollama gateway with
// the provider/model form ("ollama/<ModelName>"). The bare provider-side
// name comes from m.ModelName, not m.ID — m.ID already carries the
// "ollama/" registry prefix, so using it would produce
// "ollama/ollama/<model>" (a double prefix that the gateway rejects).
func (opencodeDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	lc.Env = append(lc.Env,
		"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
			`{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s/v1","apiKey":""}}}}`,
			m.ModelName, config.OllamaBaseURL,
		),
	)
	return lc
}
