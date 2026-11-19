package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/spf13/cobra"
)

// hostHome is a scratch home whose registry (cloudSyncRegistry, at the
// default path) a host command reads, whose config.toml also names a fake
// model server, and whose config.yaml is a scratch file. It returns the fake
// (which no host command may call) and the registry's path.
func hostHome(t *testing.T, registry string) (*fakeModelServer, string) {
	t.Helper()
	// The agy provider: every config.toml with agents needs one
	// (wt/CLAUDE.md "Fixture gotcha"), or the registry fails validation.
	path, _ := cloudSyncHome(t, registry+"\n[[providers]]\nid = \"agy\"\nname = \"agy\"\nlocation = \"cloud\"\nauth = { type = \"native\" }\n")
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
// host commands of this binary (`cloud-sync --dry-run`, `litellm list`,
// `served`, `warm`, `stop <provider>`) still read registry.toml, through
// app.hostConfig() and never through the model server, even with a model
// server configured, and behave as they did before it: each one's output or
// error equals the pre-flip function's over config.Load()'s config, and
// newApp itself reads no registry (`model list --json` and `model add` moved
// to wt-model, whose tests pin model-list.sample.json). A host command
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
		// noRegistry: the command reads no registry at all (litellm list
		// reads config.yaml only), so hostConfig is not expected to run.
		noRegistry bool
	}
	for _, tc := range []hostCase{
		{name: "cloud-sync --dry-run", build: cloudSyncCmd, args: []string{"--only", "openrouter", "--dry-run"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				var out bytes.Buffer
				o := cloudSyncOpts{dryRun: true}
				if err := selectFlows("openrouter", true, &o); err != nil {
					t.Fatal(err)
				}
				err := runCloudSync(context.Background(), &out, &out, cfg, o)
				return out.String(), err
			},
			check: func(t *testing.T, out string) {
				if !strings.HasPrefix(out, "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry") {
					t.Errorf("output = %q, want the registry's one priced model", out)
				}
			}},
		{name: "litellm list", build: litellmCmd, args: []string{"list"}, noRegistry: true},
		{name: "served", build: servedCmd, args: []string{"nosuch"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				return "", runServed(&bytes.Buffer{}, cfg, &http.Client{}, "nosuch", false)
			}},
		{name: "warm", build: warmCmd, args: []string{"ollama", "m"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				return "", runWarm(context.Background(), &bytes.Buffer{}, cfg, "ollama", "m")
			}},
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
			f, _ := hostHome(t, cloudSyncRegistry)
			stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
			stubRouteSync(t, "")
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
			if !tc.noRegistry && !a.hostSet {
				t.Error("the command did not read the registry through hostConfig")
			}
			if reqs := f.reqs(); len(reqs) != 0 {
				t.Errorf("a host command called the model server: %q", reqs)
			}
		})
	}

	// A registry that does not parse is each registry reader's error, as it
	// was before the flip: the host commands read the file, not the server.
	for _, tc := range []struct {
		name  string
		build func(a *app) *cobra.Command
		args  []string
	}{
		{"cloud-sync", cloudSyncCmd, []string{"--dry-run"}},
		{"served", servedCmd, []string{"omlx"}},
		{"warm", warmCmd, []string{"omlx", "m"}},
	} {
		t.Run("broken registry/"+tc.name, func(t *testing.T) {
			f, path := hostHome(t, cloudSyncRegistry)
			if err := os.WriteFile(path, []byte("models = [\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, loadErr := config.Load()
			if loadErr == nil {
				t.Fatal("fixture: the registry loaded")
			}
			a, err := newApp()
			if err != nil {
				t.Fatal(err)
			}
			_, err = execOn(tc.build(a), tc.args...)
			if want := configError(loadErr); err == nil || err.Error() != want.Error() {
				t.Errorf("err = %v, want %v", err, want)
			}
			if len(f.reqs()) != 0 {
				t.Errorf("a host command called the model server: %q", f.reqs())
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
