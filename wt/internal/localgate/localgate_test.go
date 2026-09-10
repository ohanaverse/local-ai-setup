package localgate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// modelsHandler serves an OpenAI-compatible /v1/models body with the given
// ids (empty slice → empty data array).
func modelsHandler(ids ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}
}

// TestNameMatches pins the probe's matching rule: lenient on the prefix (a
// server may spell the same model with a path prefix), strict on the
// variant tail — omlx's 4-bit and 6-bit variants share port 8000 and
// differ exactly there, so a mismatched tail must read as a different
// model, never as a spelling variant.
func TestNameMatches(t *testing.T) {
	cases := []struct {
		served, want string
		match        bool
	}{
		{"qwen3.8:27b-mlx", "qwen3.8:27b-mlx", true},
		{"models/qwen3.8:27b-mlx", "qwen3.8:27b-mlx", true},
		{"org/repo", "repo", true},
		{"repo", "org/repo", true},
		{"Ornith-1.5-35B-A3B-MLX-4bit", "Ornith-1.5-35B-A3B-MLX-6bit", false},
		{"other-model", "qwen3.8:27b-mlx", false},
	}
	for _, tc := range cases {
		if got := nameMatches(tc.served, tc.want); got != tc.match {
			t.Errorf("nameMatches(%q, %q) = %v, want %v", tc.served, tc.want, got, tc.match)
		}
	}
}

// TestAvailableOmlxNameChecked asserts the probe verifies the MARKED
// model, not just provider liveness: a 200 from /v1/models serving a
// different variant (4-bit vs 6-bit share port 8000) must read as "not
// running", or a stale/mismatched marker would route the launch to wrong
// weights with no error.
func TestAvailableOmlxNameChecked(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("Ornith-1.5-35B-A3B-MLX-4bit"))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	m := config.Model{ID: "omlx/Ornith-1.5-35B-A3B-MLX-4bit", ModelName: "Ornith-1.5-35B-A3B-MLX-4bit", ProviderID: "omlx"}
	if !Available(m) {
		t.Error("expected the served model to be available")
	}
	sixBit := config.Model{ID: "omlx/Ornith-1.5-35B-A3B-MLX-6bit", ModelName: "Ornith-1.5-35B-A3B-MLX-6bit", ProviderID: "omlx"}
	if Available(sixBit) {
		t.Error("expected a different served variant to be unavailable")
	}
}

// TestAvailableOmlxDownReportsUnavailable asserts an unreachable probe URL
// (nothing listening) reads as "not running", not as an error.
func TestAvailableOmlxDownReportsUnavailable(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here
	if Available(config.Model{ID: "omlx/some-model", ModelName: "some-model"}) {
		t.Error("expected omlx model to be unavailable when the probe URL is unreachable")
	}
}

// TestAvailableMlxLmServerRequiresServedModel asserts mlx_lm_server's
// probe demands at least one served model id, not merely a 2xx: the
// single-pairing server loads before serving, so an empty list means the
// model is not up even if the port answers.
func TestAvailableMlxLmServerServingModel(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("org/target-repo"))
	defer srv.Close()
	defer SetMlxLMServerProbeURLForTest(srv.URL)()

	if !Available(config.Model{ID: "mlx_lm_server/target-repo", ModelName: "target-repo", ProviderID: "mlx_lm_server"}) {
		t.Error("expected mlx_lm_server model to be available when a model is served")
	}
}

// TestAvailableMlxLmServerEmptyModelList asserts a 200 with an empty
// model list is NOT availability — a bare status probe would verify a
// server that serves nothing.
func TestAvailableMlxLmServerEmptyModelList(t *testing.T) {
	srv := httptest.NewServer(modelsHandler())
	defer srv.Close()
	defer SetMlxLMServerProbeURLForTest(srv.URL)()

	if Available(config.Model{ID: "mlx_lm_server/target-repo", ModelName: "target-repo", ProviderID: "mlx_lm_server"}) {
		t.Error("expected mlx_lm_server model to be unavailable when no model is served")
	}
}

// TestAvailableUnknownProviderIsUnavailable asserts that a marker naming a
// not-yet-wired-in provider (e.g. the future mtplx, issue #66) fails
// closed as "not running" rather than panicking or reporting healthy.
func TestAvailableUnknownProviderIsUnavailable(t *testing.T) {
	if Available(config.Model{ID: "mtplx/some-model", ModelName: "some-model"}) {
		t.Error("expected an unknown provider to report unavailable")
	}
}

// TestAvailableOllamaRequiresLoadedModel mirrors ollamacheck's fake-binary
// fixture and asserts the ollama probe is name-checked against `ollama ps`
// — the models actually loaded — not `ollama list`'s downloaded-but-idle
// catalog, which would verify a stale marker forever.
func TestAvailableOllamaRequiresLoadedModel(t *testing.T) {
	tmpDir := t.TempDir()
	fakeOllama := filepath.Join(tmpDir, "ollama")
	script := "#!/bin/sh\necho \"NAME    ID    SIZE    PROCESSOR    UNTIL\"\necho \"qwen3.8:27b-mlx   abc   5.0 GB   100% GPU   4 minutes from now\"\n"
	if err := exec.Command("sh", "-c", "cat > "+fakeOllama+" <<'EOF'\n"+script+"EOF\nchmod +x "+fakeOllama).Run(); err != nil {
		t.Fatalf("creating fake ollama: %v", err)
	}
	t.Setenv("PATH", tmpDir)

	if !Available(config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}) {
		t.Error("expected the loaded model to be available")
	}
	if Available(config.Model{ID: "ollama/missing-model", ModelName: "missing-model", ProviderID: "ollama"}) {
		t.Error("expected a not-loaded model to be unavailable")
	}
}

// TestNotRunningErrorMessage pins the user-facing wording: the message is
// the recovery instruction wt's fatal path shows, so `modelman start <id>`
// must stay copy-exact.
func TestNotRunningErrorMessage(t *testing.T) {
	err := &NotRunningError{ModelID: "ollama/qwen3.8:27b-mlx"}
	want := "local model \"ollama/qwen3.8:27b-mlx\" is not running — start it with `modelman start ollama/qwen3.8:27b-mlx`"
	if err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}

// TestResolveNoMarker asserts the no-marker case is not an error —
// cloud-only filtering is FilterToRunningLocal's job, not a gate failure.
func TestResolveNoMarker(t *testing.T) {
	cfg := &config.Config{}
	id, err := Resolve(cfg)
	if err != nil || id != "" {
		t.Errorf("Resolve() = (%q, %v), want (\"\", nil)", id, err)
	}
}

// TestResolveMarkerVerified covers the healthy path: marker set, catalog
// contains the model, and its probe answers. Uses the omlx seam since a
// hand-built cfg can't shell a fake ollama into PATH for a catalog model.
func TestResolveMarkerVerified(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("Ornith-1.5-35B-A3B-MLX-4bit"))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/Ornith-1.5-35B-A3B-MLX-4bit", ModelName: "Ornith-1.5-35B-A3B-MLX-4bit", ProviderID: "omlx"},
		},
	}
	cfg.SetLocalRunningForTest("omlx/Ornith-1.5-35B-A3B-MLX-4bit")
	id, err := Resolve(cfg)
	if err != nil || id != "omlx/Ornith-1.5-35B-A3B-MLX-4bit" {
		t.Errorf("Resolve() = (%q, %v), want the marker id, nil", id, err)
	}
}

// TestResolveMarkerNotInCatalogFailsClosed asserts a marker naming a model
// missing from the registry catalog (a data gap) is treated as stale, not
// as verified-running — an unverifiable id must never pass as healthy.
func TestResolveMarkerNotInCatalogFailsClosed(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/ghost-model")
	id, err := Resolve(cfg)
	if id != "" {
		t.Errorf("Resolve() id = %q, want \"\"", id)
	}
	if _, ok := err.(*NotRunningError); !ok {
		t.Fatalf("err = %v (%T), want *NotRunningError", err, err)
	}
}

// TestResolveMarkerStale asserts a marker whose probe fails (stopped or
// crashed outside modelman) resolves to ("", *NotRunningError) so callers
// treat it as fatal rather than launching.
func TestResolveMarkerStale(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/qwen3.8", ModelName: "qwen3.8", ProviderID: "omlx"},
		},
	}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	id, err := Resolve(cfg)
	if id != "" {
		t.Errorf("Resolve() id = %q, want \"\"", id)
	}
	nre, ok := err.(*NotRunningError)
	if !ok {
		t.Fatalf("err = %v (%T), want *NotRunningError", err, err)
	}
	if nre.ModelID != "omlx/qwen3.8" {
		t.Errorf("NotRunningError.ModelID = %q, want omlx/qwen3.8", nre.ModelID)
	}
}

// idsOf maps Apply's result to the eligible model ids, in order, so the
// Apply tests can assert the filtered list without repeating the loop.
func idsOf(models []config.Model) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// TestApplyInactiveGateIsNoop pins the hand-built-Config contract: a bare
// Config{} literal (the shape pre-issue-#65 tests use — LocalGateActive()
// false) gets its models back untouched, so the gate can never surprise a
// caller that didn't opt into it.
func TestApplyInactiveGateIsNoop(t *testing.T) {
	cfg := &config.Config{}
	models := []config.Model{
		{ID: "omlx/qwen3.8", ProviderID: "omlx"},
	}
	res, err := Apply(cfg, models, "")
	if err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	if len(res.Eligible) != 1 || res.Eligible[0].ID != "omlx/qwen3.8" {
		t.Errorf("Eligible = %v, want the input unchanged", idsOf(res.Eligible))
	}
}

// TestApplyFiltersToRunningLocal covers the healthy gate path: marker set
// and verified, gate active → cloud (claude/native) survives, the running
// local model survives, and every other local model is dropped.
func TestApplyFiltersToRunningLocal(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("Ornith-1.5-35B-A3B-MLX-4bit"))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", Location: config.LocationCloud},
			{ID: "omlx/Ornith-1.5-35B-A3B-MLX-4bit", ModelName: "Ornith-1.5-35B-A3B-MLX-4bit", ProviderID: "omlx", Location: config.LocationLocal},
			{ID: "omlx/other", ModelName: "other", ProviderID: "omlx", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/Ornith-1.5-35B-A3B-MLX-4bit")
	res, err := Apply(cfg, cfg.Models, "")
	if err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	if res.RunningLocal != "omlx/Ornith-1.5-35B-A3B-MLX-4bit" {
		t.Errorf("RunningLocal = %q", res.RunningLocal)
	}
	got := idsOf(res.Eligible)
	if len(got) != 2 || got[0] != "claude/opus" || got[1] != "omlx/Ornith-1.5-35B-A3B-MLX-4bit" {
		t.Errorf("Eligible = %v, want [claude/opus omlx/Ornith-...-4bit]", got)
	}
}

// TestApplyPinnedLocalRejected asserts the -M pin policy: pinning an
// otherwise-eligible LOCAL model that isn't the verified-running one is a
// PinnedRejected (the caller renders `modelman start <id>`), while the
// filter still drops the non-running locals from the eligible list.
func TestApplyPinnedLocalRejected(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("Ornith-1.5-35B-A3B-MLX-4bit"))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", Location: config.LocationCloud},
			{ID: "omlx/Ornith-1.5-35B-A3B-MLX-4bit", ModelName: "Ornith-1.5-35B-A3B-MLX-4bit", ProviderID: "omlx", Location: config.LocationLocal},
			{ID: "omlx/other", ModelName: "other", ProviderID: "omlx", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/Ornith-1.5-35B-A3B-MLX-4bit")
	res, err := Apply(cfg, cfg.Models, "omlx/other")
	if err != nil {
		t.Fatalf("Apply() err = %v, want nil (rejection is Result-level, not fatal)", err)
	}
	nre, ok := res.PinnedRejected.(*NotRunningError)
	if !ok {
		t.Fatalf("PinnedRejected = %v (%T), want *NotRunningError", res.PinnedRejected, res.PinnedRejected)
	}
	if nre.ModelID != "omlx/other" {
		t.Errorf("PinnedRejected.ModelID = %q, want omlx/other", nre.ModelID)
	}
	if len(res.Eligible) != 2 {
		t.Errorf("Eligible = %v, want cloud + running local only", idsOf(res.Eligible))
	}
}

// TestApplyPinnedRunningLocalAccepted asserts the inverse of
// TestApplyPinnedLocalRejected: pinning the verified-running local model is
// fine — no PinnedRejected — and that model stays in the eligible list.
func TestApplyPinnedRunningLocalAccepted(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("Ornith-1.5-35B-A3B-MLX-4bit"))
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", Location: config.LocationCloud},
			{ID: "omlx/Ornith-1.5-35B-A3B-MLX-4bit", ModelName: "Ornith-1.5-35B-A3B-MLX-4bit", ProviderID: "omlx", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/Ornith-1.5-35B-A3B-MLX-4bit")
	res, err := Apply(cfg, cfg.Models, "omlx/Ornith-1.5-35B-A3B-MLX-4bit")
	if err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	if res.PinnedRejected != nil {
		t.Errorf("PinnedRejected = %v, want nil for the running model", res.PinnedRejected)
	}
}

// TestApplyPinnedUnresolvableLocationRejected asserts the pin path fails
// closed too: a pinned model whose location cannot be resolved (provider
// absent from the config — a registry data gap) is treated as local, so
// pinning it while no local model is running is rejected rather than
// silently allowed.
func TestApplyPinnedUnresolvableLocationRejected(t *testing.T) {
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "ghost/x", ProviderID: "ghost"}, // provider absent from cfg
		},
	}
	cfg.SetLocalRunningForTest("")
	res, err := Apply(cfg, cfg.Models, "ghost/x")
	if err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	nre, ok := res.PinnedRejected.(*NotRunningError)
	if !ok {
		t.Fatalf("PinnedRejected = %v (%T), want *NotRunningError", res.PinnedRejected, res.PinnedRejected)
	}
	if nre.ModelID != "ghost/x" {
		t.Errorf("PinnedRejected.ModelID = %q, want ghost/x", nre.ModelID)
	}
}

// TestApplyStaleMarkerFatal asserts the fatal path survives Apply: a
// marker whose probe fails returns an error (not a Result), so callers
// block every launch until `modelman start`/`modelman stop` repairs it.
func TestApplyStaleMarkerFatal(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/qwen3.8", ModelName: "qwen3.8", ProviderID: "omlx", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	res, err := Apply(cfg, cfg.Models, "")
	if err == nil {
		t.Fatalf("Apply() err = nil, want *NotRunningError; Result = %+v", res)
	}
	if _, ok := err.(*NotRunningError); !ok {
		t.Fatalf("err = %v (%T), want *NotRunningError", err, err)
	}
}