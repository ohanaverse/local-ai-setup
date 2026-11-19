package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// litellmEnv points the route sync at a scratch config.yaml holding body and
// makes the proxy restart a no-op (copied from cmd/wt-session's
// litellm_test.go with the test below). It returns the file's path.
func litellmEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_LITELLM_CONFIG", p)
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	return p
}

// TestModelInitNamesTheRepairForARegistryItCannotWrite pins what
// `wt-model model init` says about a registry it will not write: one with a
// top-level key wt does not know, one that does not parse, one it cannot
// read at all (a directory of that name), and one whose path it cannot
// examine (a file where the directory should be). All are registry problems
// only a hand edit repairs, so the refusal says so instead of leaving the
// user to guess (or to try `wt config`, which edits another file) — and it
// says what every other command says about the same registry.
func TestModelInitNamesTheRepairForARegistryItCannotWrite(t *testing.T) {
	const isDir, parentIsFile = "\x00a directory", "\x00parent is a file"
	for name, body := range map[string]string{
		"unknown top-level key":   "providers = []\nmodels = []\nextra = 1\n",
		"unparseable":             "models = [unclosed\n",
		"unreadable":              isDir,
		"path cannot be examined": parentIsFile,
	} {
		t.Run(name, func(t *testing.T) {
			h := testHost()
			home := t.TempDir()
			withCleanConfigEnv(t, home)
			litellmEnv(t, "model_list: []\n")
			registry := filepath.Join(home, ".config", "local-ai", "registry.toml")
			if err := os.MkdirAll(filepath.Dir(filepath.Dir(registry)), 0o755); err != nil {
				t.Fatal(err)
			}
			if body == parentIsFile {
				if err := os.WriteFile(filepath.Dir(registry), []byte("not a directory\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Dir(registry), 0o755); err != nil {
				t.Fatal(err)
			}
			switch body {
			case parentIsFile:
			case isDir:
				if err := os.Mkdir(registry, 0o755); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(registry, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			stubSeedEnv(h, config.SeedEnv{OnPath: onPath("ollama")})
			synced := stubRouteSync(h, "")
			err := h.runModelInit(&bytes.Buffer{}, &bytes.Buffer{}, false)
			if err == nil {
				t.Fatal("wt-model model init wrote a registry it cannot read")
			}
			got := err.Error()
			if !strings.Contains(got, registry) || !strings.HasSuffix(got, "(fix that file by hand)") {
				t.Errorf("err = %q, want it to name %s and end with the hand-edit hint", got, registry)
			}
			if strings.Contains(got, "wt config") {
				t.Errorf("err = %q names `wt config`, which cannot edit the registry", got)
			}
			if *synced != 0 {
				t.Errorf("route syncs after the refusal: %d, want none", *synced)
			}
			switch body {
			case parentIsFile:
				if b, _ := os.ReadFile(filepath.Dir(registry)); string(b) != "not a directory\n" {
					t.Errorf("the file in the registry directory's place after the refusal: %q, want it untouched", b)
				}
			case isDir:
				if info, err := os.Lstat(registry); err != nil || !info.IsDir() {
					t.Errorf("registry path after the refusal: %v (err %v), want the directory left alone", info, err)
				}
			default:
				if b, _ := os.ReadFile(registry); string(b) != body {
					t.Errorf("registry.toml after the refusal:\n%s\nwant it untouched", b)
				}
			}
		})
	}
}
