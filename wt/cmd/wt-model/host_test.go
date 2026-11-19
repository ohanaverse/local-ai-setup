package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
// that moved to wt-model (`served`, `warm`; later PRs add cloud-sync and
// stop): each reads registry.toml through the host's hostConfig and behaves
// as it did under wt, its error equal to the run function's over
// config.Load()'s config. A registry that does not parse is the config
// error of the load, as it was under wt.
func TestHostCommandsStillLoadTheRegistry(t *testing.T) {
	type hostCase struct {
		name  string
		build func(h *host) *cobra.Command
		args  []string
		// before returns what the run function says for the same input.
		before func(t *testing.T, cfg *config.Config) (string, error)
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelHome(t, writeRegistry)
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			want, wantErr := tc.before(t, cfg)
			h := testHost()
			out, err := execOn(tc.build(h), tc.args...)
			if fmt.Sprint(err) != fmt.Sprint(wantErr) {
				t.Errorf("err = %v, want %v", err, wantErr)
			}
			if want != "" && out != want {
				t.Errorf("output =\n%s\nwant\n%s", out, want)
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
