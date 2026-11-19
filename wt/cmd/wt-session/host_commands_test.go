package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// hostCommandsRegistry is the registry the host-command tests start from:
// one OpenRouter-priced model, two ollama cloud entries and a local ollama
// model. A test-only copy of cmd/wt-model's cloudSyncRegistry (test files
// cannot cross packages), kept since cloud-sync moved to wt-model.
const hostCommandsRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_OPENROUTER_KEY"

[[models]]
id = "openrouter/vendor--gpt"
family = "gpt"
provider_id = "openrouter"
model_name = "vendor/gpt"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 2.5
output_price_per_million = 10

[[models]]
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 9
subscription_price = 100
subscription_period = "month"

[[models]]
id = "ollama/retired:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired:cloud"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "local"
source = "curated"
tags = []
`

// hostHome is a scratch home whose registry (hostCommandsRegistry, at the
// default path) a host command reads, whose config.toml also names a fake
// model server, and whose config.yaml is a scratch file. It returns the fake
// (which no host command may call) and the registry's path.
func hostHome(t *testing.T, registry string) (*fakeModelServer, string) {
	t.Helper()
	// The agy provider: every config.toml with agents needs one
	// (wt/CLAUDE.md "Fixture gotcha"), or the registry fails validation.
	path := modelHome(t, registry+"\n[[providers]]\nid = \"agy\"\nname = \"agy\"\nlocation = \"cloud\"\nauth = { type = \"native\" }\n")
	f := newFakeModelServer(t)
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte(f.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("default_tag = \"code\"\n\n[model-server]\nurl = %q\ntoken_file = %q\n", f.URL, tok)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	litellmEnv(t, "model_list: []\n")
	return f, path
}

// execOn runs one command built over a with args, returning stdout+stderr
// and the error.
func execOn(c *cobra.Command, args ...string) (string, error) {
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

// TestHostCommandsStillLoadTheRegistry pins R3 and R17: after the flip the
// host commands of this binary (`stop <provider>`) still read registry.toml,
// through app.hostConfig() and never through the model server, even with a model
// server configured, and behave as they did before it: each one's output or
// error equals the pre-flip function's over config.Load()'s config, and
// newApp itself reads no registry (`model list --json`, `model add`,
// `served`, `warm`, `cloud-sync` and `litellm list` moved to wt-model, whose tests pin
// model-list.sample.json and the moved host commands). A host command
// moved onto the session config would see no models and silently plan,
// route or stop nothing.
func TestHostCommandsStillLoadTheRegistry(t *testing.T) {
	loaded := func(t *testing.T) *config.Config {
		t.Helper()
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	type hostCase struct {
		name  string
		build func(a *app) *cobra.Command
		args  []string
		// before returns what the pre-flip function says for the same input.
		before func(t *testing.T, cfg *config.Config) (string, error)
		check  func(t *testing.T, out string)
	}
	for _, tc := range []hostCase{
		{name: "stop provider", build: stopCmd, args: []string{"ollama", "--yes"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				var out bytes.Buffer
				err := runStop(&out, cfg, "ollama", true)
				return out.String(), err
			},
			check: func(t *testing.T, out string) {
				if strings.Contains(out, "unknown provider") {
					t.Errorf("the registry's ollama provider was not found:\n%s", out)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := hostHome(t, hostCommandsRegistry)
			var stopped []string
			oldStop := stopProvider
			stopProvider = func(_ context.Context, _ *config.Config, p string) error {
				stopped = append(stopped, p)
				return nil
			}
			t.Cleanup(func() { stopProvider = oldStop })

			a, err := newApp()
			if err != nil {
				t.Fatal(err)
			}
			if a.hostSet {
				t.Fatal("newApp loaded the registry")
			}
			var want string
			var wantErr error
			if tc.before != nil {
				want, wantErr = tc.before(t, loaded(t))
				stopped = nil
			}
			var out string
			captureFile(t, &os.Stdout, func() { out, err = execOn(tc.build(a), tc.args...) })
			if tc.before != nil {
				if fmt.Sprint(err) != fmt.Sprint(wantErr) {
					t.Errorf("err = %v, want the pre-flip %v", err, wantErr)
				}
				if want != "" && out != want {
					t.Errorf("output =\n%s\nwant the pre-flip\n%s", out, want)
				}
			} else if err != nil {
				t.Errorf("err = %v\n%s", err, out)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
			if !a.hostSet {
				t.Error("the command did not read the registry through hostConfig")
			}
			if reqs := f.reqs(); len(reqs) != 0 {
				t.Errorf("a host command called the model server: %q", reqs)
			}
		})
	}
}

// hostCfgOf is a's host config, read through hostConfig (loaded once).
func hostCfgOf(a *app) *config.Config {
	cfg, _ := a.hostConfig()
	return cfg
}

// hostLoadErrOf is a's host config load error.
func hostLoadErrOf(a *app) error {
	_, err := a.hostConfig()
	return err
}

// setHostLoadErr makes a's host config load error err, after the load (so a
// later hostConfig call does not replace it).
func setHostLoadErr(a *app, err error) {
	a.loadHost()
	a.hostLoadErr = err
}

// setHostCfgErr makes a's host config error (load or validation) err.
func setHostCfgErr(a *app, err error) {
	a.loadHost()
	a.hostCfgErr = err
}
