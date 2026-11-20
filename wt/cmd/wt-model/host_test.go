package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/spf13/cobra"
)

// execOn runs one command built over a host with args, returning stdout+stderr
// and the error.
func execOn(c *cobra.Command, args ...string) (string, error) {
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

// TestHostCommandsStillLoadTheRegistry pins R3 and R17 for the host commands
// that moved to wt-model (`served`, `warm`, `cloud-sync --dry-run`, `stop
// <provider>`): each reads registry.toml through the host's hostConfig and behaves
// as it did under wt, its error equal to the run function's over
// config.Load()'s config. A registry that does not parse is the config
// error of the load, as it was under wt. The stop row runs with no server
// record (testRuntime's store is empty), so it has no client session to ask
// about and asks nothing.
func TestHostCommandsStillLoadTheRegistry(t *testing.T) {
	type hostCase struct {
		name  string
		build func(h *host) *cobra.Command
		args  []string
		// before returns what the run function says for the same input.
		before func(t *testing.T, cfg *config.Config) (string, error)
		check  func(t *testing.T, out string)
		// registry is the registry the case starts from (writeRegistry when
		// empty).
		registry string
	}
	for _, tc := range []hostCase{
		{name: "served", build: servedCmd, args: []string{"nosuch"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				return "", runServed(&bytes.Buffer{}, cfg, &http.Client{}, "nosuch", false)
			}},
		{name: "warm", build: warmCmd, args: []string{"ollama", "m"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				return "", runWarm(context.Background(), &bytes.Buffer{}, cfg, "ollama", "m")
			}},
		{name: "cloud-sync --dry-run", build: cloudSyncCmd, args: []string{"--only", "openrouter", "--dry-run"}, registry: cloudSyncRegistry,
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				var out bytes.Buffer
				o := cloudSyncOpts{dryRun: true}
				if err := selectFlows("openrouter", true, &o); err != nil {
					t.Fatal(err)
				}
				bh := testHost()
				stubCloudFetch(t, bh, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
				err := bh.runCloudSync(context.Background(), &out, &out, cfg, o)
				return out.String(), err
			},
			check: func(t *testing.T, out string) {
				if !strings.HasPrefix(out, "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry") {
					t.Errorf("output = %q, want the registry's one priced model", out)
				}
			}},
		{name: "stop provider", build: func(h *host) *cobra.Command { return stopCmd(h, testRuntime(t).Record) }, args: []string{"ollama", "--yes"},
			before: func(t *testing.T, cfg *config.Config) (string, error) {
				var out bytes.Buffer
				err := testHost().runStop(&out, cfg, "ollama", true)
				return out.String(), err
			},
			check: func(t *testing.T, out string) {
				if strings.Contains(out, "unknown provider") {
					t.Errorf("the registry's ollama provider was not found:\n%s", out)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tc.registry
			if registry == "" {
				registry = writeRegistry
			}
			modelHome(t, registry)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			want, wantErr := tc.before(t, cfg)
			h := testHost()
			stubCloudFetch(t, h, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
			stubRouteSync(h, "")
			// No row may stop anything or ask: the stop row's record store
			// is empty, so no client session is known.
			h.stopProvider = func(_ context.Context, _ *config.Config, id string) error {
				t.Errorf("halted %s", id)
				return nil
			}
			h.stopEntries = func(_ io.Writer, _ *config.Config, es []localmodels.Entry) error {
				t.Errorf("stopped %v", es)
				return nil
			}
			h.confirmStop = func(q string) (bool, error) {
				t.Errorf("asked %q", q)
				return false, nil
			}
			out, err := execOn(tc.build(h), tc.args...)
			if fmt.Sprint(err) != fmt.Sprint(wantErr) {
				t.Errorf("err = %v, want %v", err, wantErr)
			}
			if want != "" && out != want {
				t.Errorf("output =\n%s\nwant\n%s", out, want)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
			if !h.hostSet {
				t.Error("the command did not read the registry through hostConfig")
			}
		})
	}

	for _, tc := range []struct {
		name  string
		build func(h *host) *cobra.Command
		args  []string
	}{
		{"served", servedCmd, []string{"omlx"}},
		{"warm", warmCmd, []string{"omlx", "m"}},
		{"cloud-sync", cloudSyncCmd, []string{"--dry-run"}},
	} {
		t.Run("broken registry/"+tc.name, func(t *testing.T) {
			path := modelHome(t, writeRegistry)
			if err := os.WriteFile(path, []byte("models = [\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, loadErr := config.Load()
			if loadErr == nil {
				t.Fatal("fixture: the registry loaded")
			}
			_, err := execOn(tc.build(testHost()), tc.args...)
			if want := config.ConfigError(loadErr); err == nil || err.Error() != want.Error() {
				t.Errorf("err = %v, want %v", err, want)
			}
		})
	}
}
