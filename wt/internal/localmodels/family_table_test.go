package localmodels

import (
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// These tests pin the family table's observable behavior so it can move into
// internal/provider without changing what wt does.

// TestFamilyTablePinned pins which provider ids belong to which family and the
// per-family flags (running-only, routes-follow-artifact), so consolidating the
// tables cannot change how wt groups providers; an unknown id stays family "".
func TestFamilyTablePinned(t *testing.T) {
	cases := map[string]string{
		"ollama":        "ollama",
		"omlx":          "omlx",
		"omlx-6bit":     "omlx",
		"mtplx":         "mtplx",
		"mlx_lm_server": "mlx_lm_server",
		"llamacpp":      "",
		"openrouter":    "",
		"agy":           "",
		"":              "",
	}
	for id, want := range cases {
		if got := Family(id); got != want {
			t.Errorf("Family(%q) = %q, want %q", id, got, want)
		}
	}
	if got, want := Families(), []string{"mlx_lm_server", "mtplx", "ollama", "omlx", "rec"}; !slices.Equal(got, want) {
		t.Errorf("Families() = %v, want %v", got, want)
	}
	if got, want := FamilyProviderIDs("omlx"), []string{"omlx", "omlx-6bit"}; !slices.Equal(got, want) {
		t.Errorf("FamilyProviderIDs(omlx) = %v, want %v", got, want)
	}
	for id := range cases {
		if got, want := RunningOnly(id), id == "mlx_lm_server"; got != want {
			t.Errorf("RunningOnly(%q) = %v, want %v", id, got, want)
		}
	}
	for _, f := range append(Families(), "", "omlx-6bit") {
		if got, want := RoutesFollowArtifact(f), f == "ollama"; got != want {
			t.Errorf("RoutesFollowArtifact(%q) = %v, want %v", f, got, want)
		}
	}
}

// TestFamilyOriginDefaultsPinned pins each family's default origin when the
// registry names no base_url: probes, starts and routes all dial it.
func TestFamilyOriginDefaultsPinned(t *testing.T) {
	cfg := &config.Config{}
	cases := map[string]string{
		"ollama":        "http://localhost:11434",
		"omlx":          "http://localhost:8000",
		"mtplx":         "http://localhost:8003",
		"mlx_lm_server": "http://localhost:8001",
		"bogus":         "",
	}
	for family, want := range cases {
		if got := FamilyOrigin(cfg, family); got != want {
			t.Errorf("FamilyOrigin(%q) = %q, want %q", family, got, want)
		}
	}
}

// TestModelDirDefaultsPinned pins the default model directories, including the
// legacy fallback of other families to omlx's directory, which the inventory
// scan relies on.
func TestModelDirDefaultsPinned(t *testing.T) {
	cfg := &config.Config{}
	omlx, err := config.ExpandHome("~/.omlx/models")
	if err != nil {
		t.Fatal(err)
	}
	mtplx, err := config.ExpandHome("~/.mtplx/models")
	if err != nil {
		t.Fatal(err)
	}
	// ollama and mlx_lm_server fall back to the omlx directory (legacy).
	for family, want := range map[string]string{"omlx": omlx, "ollama": omlx, "mtplx": mtplx} {
		got, err := ModelDir(cfg, family)
		if err != nil {
			t.Fatalf("ModelDir(%q): %v", family, err)
		}
		if got != want {
			t.Errorf("ModelDir(%q) = %q, want %q", family, got, want)
		}
	}
}
