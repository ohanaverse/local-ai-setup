package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// TestLoadFixHint pins the one place that chooses a repair for a config load
// or validation error, over every kind of error Load and Validate hand back.
// Commands used to choose for themselves, and `wt litellm` chose "run
// `wt config` to repair" for all of them — including a missing registry
// (whose error already says `wt model init`) and a registry link that leads
// nowhere, neither of which `wt config` can touch (#291). `wt config` edits
// config.toml, so it is the answer for a config.toml problem and nothing else.
func TestLoadFixHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	registry := filepath.Join(home, "local-ai", "registry.toml")
	cfgFile := filepath.Join(home, "agent-wt", "config.toml")
	for _, dir := range []string{filepath.Dir(registry), filepath.Dir(cfgFile)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// loadErr is Load's error with the registry and config.toml in the given
	// state; an empty body leaves that file out, and link makes the registry
	// a symlink to nothing.
	loadErr := func(t *testing.T, registryBody, cfgBody string, link bool) error {
		t.Helper()
		os.Remove(registry)
		os.Remove(cfgFile)
		switch {
		case link:
			if err := os.Symlink(filepath.Join(home, "nowhere.toml"), registry); err != nil {
				t.Fatal(err)
			}
		case registryBody != "":
			if err := os.WriteFile(registry, []byte(registryBody), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if cfgBody != "" {
			if err := os.WriteFile(cfgFile, []byte(cfgBody), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Load()
		if err == nil {
			t.Fatal("Load succeeded on a broken fixture")
		}
		return err
	}
	const okRegistry = "providers = []\nmodels = []\n"
	const wtConfig = "run `wt config` to repair"
	const byHand = "fix that file by hand"

	missing := loadErr(t, "", "", false)
	link := loadErr(t, "", "", true)
	registryParse := loadErr(t, "models = [unclosed\n", "", false)
	cfgParse := loadErr(t, okRegistry, "default_tag = [unclosed\n", false)
	location := (&Config{DefaultTag: "code", Providers: []Provider{{ID: "omlx", Location: "Local"}}}).Validate()
	root, err := tomlw.Decode([]byte("providers = []\nextra = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, topLevel := newRegistryDoc(root)

	cases := []struct {
		name string
		err  error
		is   error // the kind the fixture must be, nil for unmarked errors
		want string
	}{
		{"nil", nil, nil, ""},
		{"missing registry", missing, ErrRegistryMissing, ""},
		{"dangling registry link", link, ErrRegistryLink, "fix the link or move it aside"},
		{"registry parse error", registryParse, ErrRegistryFile, byHand},
		{"unknown top-level key", topLevel, ErrRegistryTopLevel, byHand},
		{"mistyped location", location, ErrLocation, "fix the entry in " + registry},
		{"config.toml parse error", cfgParse, nil, wtConfig},
		{"config.toml validation error", errors.New("default_tag must not be empty"), nil, wtConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.is != nil && !errors.Is(c.err, c.is) {
				t.Fatalf("fixture error = %v, want it to be %v", c.err, c.is)
			}
			for _, err := range []error{c.err, wrapNonNil(c.err)} {
				got := LoadFixHint(err)
				if got != c.want {
					t.Errorf("LoadFixHint(%v) = %q, want %q", err, got, c.want)
				}
				if c.is != nil && strings.Contains(got, "wt config") {
					t.Errorf("LoadFixHint(%v) = %q: `wt config` cannot repair a registry problem", err, got)
				}
			}
		})
	}

	// The marker changes what the error is, not what it says: the text is
	// the one users and docs already know, naming the file.
	if got := registryParse.Error(); !strings.HasPrefix(got, "parse "+registry+": toml: ") {
		t.Errorf("registry parse error = %q, want it to keep naming the file it parsed", got)
	}
	if errors.Is(cfgParse, ErrRegistryFile) {
		t.Errorf("a config.toml parse error (%v) is marked as a registry file error", cfgParse)
	}
}

func wrapNonNil(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("config error: %w", err)
}
