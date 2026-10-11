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
// config.toml, so it is the answer for a config.toml problem and nothing else:
// a provider or model row that fails validation is registry.toml's, and so is
// a registry path wt cannot even stat.
func TestLoadFixHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
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

	// first is Validate's error for a config whose only problem is in the
	// given registry rows (or, for the agent case, in config.toml).
	first := func(t *testing.T, c Config) error {
		t.Helper()
		c.DefaultTag = "code"
		err := c.Validate()
		if err == nil {
			t.Fatal("Validate accepted a broken fixture")
		}
		return err
	}
	local := Provider{ID: "omlx", Location: LocationLocal}
	noModelName := first(t, Config{Providers: []Provider{local}, Models: []Model{{ID: "omlx/m", ProviderID: "omlx"}}})
	unknownProvider := first(t, Config{Models: []Model{{ID: "ghost/m", ProviderID: "ghost", ModelName: "m"}}})
	dupModel := first(t, Config{Providers: []Provider{local}, Models: []Model{
		{ID: "omlx/m", ProviderID: "omlx", ModelName: "m"}, {ID: "omlx/m", ProviderID: "omlx", ModelName: "m"}}})
	dupProvider := first(t, Config{Providers: []Provider{local, local}})
	emptyProviderID := first(t, Config{Providers: []Provider{{Location: LocationLocal}}})
	emptyModelID := first(t, Config{Providers: []Provider{local}, Models: []Model{{ProviderID: "omlx", ModelName: "m"}}})
	noLocation := first(t, Config{Providers: []Provider{{ID: "omlx"}}, Models: []Model{{ID: "omlx/m", ProviderID: "omlx", ModelName: "m"}}})
	agentProvider := first(t, Config{Agents: []Agent{{Name: "pi", SupportedProviders: []string{"ghost"}}}})

	// The registry's directory is a regular file: lstat fails, and not with
	// "no such file".
	os.Remove(registry)
	os.Remove(cfgFile)
	os.Remove(filepath.Dir(registry))
	if err := os.WriteFile(filepath.Dir(registry), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, cannotStat := Load()
	if cannotStat == nil {
		t.Fatal("Load succeeded with a file where the registry's directory should be")
	}
	const entry = "fix the entry in "

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
		{"registry path that cannot be examined", cannotStat, ErrRegistryFile, byHand},
		{"mistyped location", location, ErrLocation, entry + registry},
		{"model without model_name", noModelName, ErrRegistryEntry, entry + registry},
		{"model with an unknown provider", unknownProvider, ErrRegistryEntry, entry + registry},
		{"duplicate model id", dupModel, ErrRegistryEntry, entry + registry},
		{"duplicate provider id", dupProvider, ErrRegistryEntry, entry + registry},
		{"provider with an empty id", emptyProviderID, ErrRegistryEntry, entry + registry},
		{"model with an empty id", emptyModelID, ErrRegistryEntry, entry + registry},
		{"model with no location", noLocation, ErrRegistryEntry, entry + registry},
		{"agent with an unknown provider (config.toml)", agentProvider, nil, wtConfig},
		{"config.toml parse error", cfgParse, nil, wtConfig},
		{"config.toml validation error", errors.New("default_tag must not be empty"), nil, wtConfig},
		// Joined errors: registry error first, then config.toml error
		{"joined: registry then config", errors.Join(
			registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required")),
			errors.New("default_tag must not be empty")), nil, entry + registry},
		// Joined errors: config.toml error first, then registry error
		{"joined: config then registry", errors.Join(
			errors.New("default_tag must not be empty"),
			registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required"))), nil, entry + registry},
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
	if got, want := cannotStat.Error(), "lstat "+registry+": not a directory"; got != want {
		t.Errorf("stat error = %q, want %q", got, want)
	}
	// One error names one place: the hint says where the entry is, so the
	// text must not name the file as well (it used to, and then got
	// `wt config` appended after it).
	if got, want := noModelName.Error(), `model "omlx/m": model_name is required`; got != want {
		t.Errorf("model_name error = %q, want %q", got, want)
	}
	if errors.Is(agentProvider, ErrRegistryEntry) {
		t.Errorf("an agent error (%v) is marked as a registry entry error: agents are config.toml's", agentProvider)
	}
}

func wrapNonNil(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("config error: %w", err)
}

// TestRegistryFixHint_JoinedErrors pins that RegistryFixHint finds a registry
// problem anywhere in a joined error (as ValidateAll returns), wrapped or
// not. This ensures the correct repair location is shown even when a
// config.toml error appears first in the join (e.g., empty default_tag +
// missing model_name both present).
func TestRegistryFixHint_JoinedErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	registry := filepath.Join(home, "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a minimal registry so RegistryPath() works
	if err := os.WriteFile(registry, []byte("providers = []\nmodels = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"single registry error", registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required")), "fix the entry in " + registry},
		{"single config.toml error", errors.New("default_tag must not be empty"), ""},
		// Joined errors: registry error first
		{"joined: registry then config", errors.Join(
			registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required")),
			errors.New("default_tag must not be empty")), "fix the entry in " + registry},
		// Joined errors: config.toml error first
		{"joined: config then registry", errors.Join(
			errors.New("default_tag must not be empty"),
			registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required"))), "fix the entry in " + registry},
		// Multiple registry errors
		{"joined: multiple registry errors", errors.Join(
			registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required")),
			registryEntryError(fmt.Errorf("model \"omlx/n\": model_name is required"))), "fix the entry in " + registry},
		// Multiple config.toml errors
		{"joined: multiple config.toml errors", errors.Join(
			errors.New("default_tag must not be empty"),
			errors.New("agent \"claude\": must have at least one supported provider")), ""},
		// Wrapped joined error
		{"wrapped joined: config then registry", fmt.Errorf("context: %w", errors.Join(
			errors.New("default_tag must not be empty"),
			registryEntryError(fmt.Errorf("model \"omlx/m\": model_name is required")))), "fix the entry in " + registry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RegistryFixHint(c.err)
			if got != c.want {
				t.Errorf("RegistryFixHint(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}
