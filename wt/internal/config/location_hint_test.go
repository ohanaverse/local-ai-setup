package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestRegistryFixHint pins the one source of the "where do I fix this" wording
// for a config error wt's own editor cannot repair (#209). A mistyped location
// is in registry.toml; the hint names that file, wrapped or not. Any other
// error has no registry hint, so callers keep their own wording for it.
func TestRegistryFixHint(t *testing.T) {
	t.Setenv("WT_REGISTRY", "/tmp/somewhere/registry.toml")
	cfg := &Config{DefaultTag: "code", Providers: []Provider{{ID: "omlx", Location: "Local"}}}
	err := cfg.Validate()
	if !errors.Is(err, ErrLocation) {
		t.Fatalf("fixture error = %v, want a location error", err)
	}
	const want = "fix the entry in /tmp/somewhere/registry.toml"
	if got := RegistryFixHint(err); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
	if got := RegistryFixHint(fmt.Errorf("config error: %w", err)); got != want {
		t.Errorf("wrapped: hint = %q, want %q", got, want)
	}
	if got := RegistryFixHint(errors.New("default_tag must not be empty")); got != "" {
		t.Errorf("another error: hint = %q, want none", got)
	}
	if got := RegistryFixHint(nil); got != "" {
		t.Errorf("nil: hint = %q, want none", got)
	}
}

// TestRegistryFixHint_ExpandHomeFailure tests the case where ExpandHome fails
// (e.g., HOME is unset). When this happens, RegistryPath writes to stderr and
// falls back to the literal path. The hint should still show the literal path
// so the user sees something actionable, even if it's not fully expanded.
func TestRegistryFixHint_ExpandHomeFailure(t *testing.T) {
	// Unset HOME and set WT_REGISTRY to a tilde path
	t.Setenv("HOME", "")
	t.Setenv("WT_REGISTRY", "~/custom/registry.toml")

	cfg := &Config{DefaultTag: "code", Providers: []Provider{{ID: "omlx", Location: "Local"}}}
	err := cfg.Validate()
	if !errors.Is(err, ErrLocation) {
		t.Fatalf("fixture error = %v, want a location error", err)
	}

	// The hint should contain the literal path since ExpandHome fails
	hint := RegistryFixHint(err)
	if !strings.Contains(hint, "~/custom/registry.toml") {
		t.Errorf("hint = %q should contain literal tilde path when HOME is unset", hint)
	}

	// Restore HOME
	t.Setenv("HOME", "/tmp/restored")
}
