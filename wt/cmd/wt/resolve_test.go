package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
)

// TestResolveModel covers the non-TUI model resolution path used after
// -W/--cwd has resolved the worktree and -A has resolved the agent.
// This is the gate that catches config errors and -M mismatches before
// launching an agent.
func TestResolveModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "ollama", ModelName: "sonnet", Family: "sonnet", Tags: []string{"design"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"claude", "ollama"}},
		},
	}
	cfg.ExposeAllForTest()

	// resolveModel errors on any ambiguous eligible list rather than
	// falling back to defaultModel. launch.go calls resolveModel and
	// handles the error via rotation.

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		pinned  string
		wantID  string
		wantErr bool
	}{
		{"single match", "claude", "", "", "", "claude/opus", false},
		{"pinned in eligible", "pi", "", "", "claude/opus", "claude/opus", false},
		{"pinned not in eligible", "pi", "", "", "ollama/missing", "", true},
		{"pinned wrong provider for agent", "claude", "", "", "ollama/gemma4:9b", "", true},
		{"multiple no pin errors", "pi", "", "", "", "", true},
		{"tag filter narrows to one", "pi", "code", "gemma4", "", "ollama/gemma4:9b", false},
		{"empty eligible errors", "claude", "design", "", "", "", true},
		{"unknown agent", "nope", "", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, err := resolveModel(tc.agent, cfg, tc.tags, tc.family, tc.pinned)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if m.ID != tc.wantID {
				t.Errorf("got ID %q, want %q", m.ID, tc.wantID)
			}
		})
	}

	// A command agent returns the errCommandAgent sentinel specifically, so
	// launchFiltered's errors.Is(err, errCommandAgent) dispatch matches. This
	// locks the sentinel identity the dispatch depends on.
	if _, _, err := resolveModel("shell", cfg, "", "", ""); !errors.Is(err, errCommandAgent) {
		t.Errorf("resolveModel(shell) err = %v, want errCommandAgent", err)
	}
}

// TestResolveModelReturnsEligible verifies resolveModel returns the full
// eligible list even when it returns an error, so callers can reuse it
// instead of calling cfg.EligibleModels a second time.
func TestResolveModelReturnsEligible(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama"}},
		Models: []config.Model{
			{ID: "ollama/code", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/design", ProviderID: "ollama", Tags: []string{"design"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	cfg.ExposeAllForTest()

	m, eligible, err := resolveModel("claude", cfg, "", "", "")
	if err == nil {
		t.Fatal("expected multiple-models error")
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero", m.ID)
	}
	if len(eligible) != 2 {
		t.Fatalf("eligible = %d, want 2", len(eligible))
	}
}

// TestResolveModelNoMarkerHidesLocalModels asserts issue #65's picker-
// filter half for the non-TUI path: with the gate active and no marker,
// resolveModel treats every local model as ineligible — only cloud models
// can be auto-selected or listed.
func TestResolveModelNoMarkerHidesLocalModels(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", Family: "opus", Tags: []string{"code"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"claude", "ollama"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("") // gate active, nothing running

	m, _, err := resolveModel("pi", cfg, "", "", "")
	if err != nil {
		t.Fatalf("resolveModel() error = %v, want nil (single eligible cloud model)", err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}
}

// TestResolveModelVerifiedMarkerAllowsLocalModel asserts a verified marker
// makes the marked local model eligible again.
func TestResolveModelVerifiedMarkerAllowsLocalModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer localgate.SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")

	m, _, err := resolveModel("pi", cfg, "", "", "")
	if err != nil || m.ID != "omlx/qwen3.8" {
		t.Errorf("resolveModel() = (%v, %v), want (omlx/qwen3.8, nil)", m, err)
	}
}

// TestResolveModelStaleMarkerErrors asserts the "exit with a message" half:
// a marker set but not verified is a fatal error for every launch through
// this agent, not just ones that would have used a local model.
func TestResolveModelStaleMarkerErrors(t *testing.T) {
	defer localgate.SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here

	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}}},
		Models:    []config.Model{{ID: "claude/opus", ProviderID: "claude", Family: "opus", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("omlx/qwen3.8")

	_, _, err := resolveModel("claude", cfg, "", "", "")
	var notRunning *localgate.NotRunningError
	if !errors.As(err, &notRunning) {
		t.Fatalf("err = %v, want *localgate.NotRunningError", err)
	}
	if notRunning.ModelID != "omlx/qwen3.8" {
		t.Errorf("NotRunningError.ModelID = %q, want omlx/qwen3.8", notRunning.ModelID)
	}
}

// TestResolveModelPinnedStaleLocalModelRejected asserts the pinned-model
// guard: -M pinning a local model that is not the currently-running one
// gets the specific "start it in modelman" message, not the generic
// "not in the eligible list" one.
func TestResolveModelPinnedStaleLocalModelRejected(t *testing.T) {
	// The running marker (ollama/b) must verify as available so the
	// pinned-model guard (not the stale-marker path) fires — a fake
	// `ollama` binary on PATH lists b as running.
	fakeBin := t.TempDir()
	fakeOllama := filepath.Join(fakeBin, "ollama")
	if err := os.WriteFile(fakeOllama, []byte("#!/bin/sh\necho \"NAME    ID    SIZE    MODIFIED\"\necho \"b   abc   5.0 GB   2 days ago\"\n"), 0o755); err != nil {
		t.Fatalf("write fake ollama: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models: []config.Model{
			{ID: "ollama/a", ProviderID: "ollama", Family: "a", Tags: []string{"code"}},
			{ID: "ollama/b", ProviderID: "ollama", Family: "b", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "pi", SupportedProviders: []string{"ollama"}}},
	}
	cfg.ExposeAllForTest()
	cfg.SetLocalRunningForTest("ollama/b") // b is running, pin a

	_, _, err := resolveModel("pi", cfg, "", "", "ollama/a")
	var notRunning *localgate.NotRunningError
	if !errors.As(err, &notRunning) {
		t.Fatalf("err = %v, want *localgate.NotRunningError", err)
	}
	if notRunning.ModelID != "ollama/a" {
		t.Errorf("NotRunningError.ModelID = %q, want ollama/a", notRunning.ModelID)
	}
}
