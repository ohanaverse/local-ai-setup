package localgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
// provider this probe doesn't know about fails closed as "not running"
// rather than panicking or reporting healthy.
func TestAvailableUnknownProviderIsUnavailable(t *testing.T) {
	if Available(config.Model{ID: "ghost/some-model", ModelName: "some-model", ProviderID: "ghost"}) {
		t.Error("expected an unknown provider to report unavailable")
	}
}

// TestAvailableMtplxNameChecked asserts the mtplx probe verifies the MARKED
// model on port 8003, not just provider liveness.
func TestAvailableMtplxNameChecked(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality"))
	defer srv.Close()
	defer SetMtplxProbeURLForTest(srv.URL)()

	m := config.Model{ID: "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", ModelName: "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality", ProviderID: "mtplx"}
	if !Available(m) {
		t.Error("expected the served mtplx model to be available")
	}
	other := config.Model{ID: "mtplx/Other/Model", ModelName: "Other/Model", ProviderID: "mtplx"}
	if Available(other) {
		t.Error("expected a different served model to be unavailable")
	}
}

// TestAvailableOllamaExemptFromLiveVerification asserts the deliberate
// ollama exemption: `modelman start` for an ollama model is flag-only (no
// warmup call — ollama lazy-loads on first request), so nothing ever loads
// the model at start time. If Available() verified ollama the same way as
// the other providers (via `ollama ps`, the
// currently-*loaded* set), the very first probe after `modelman start`
// would read the model as not running and silently self-clear the flag —
// permanently defeating "starting an ollama model makes it appear in the
// picker." There is no live "is this specific model loaded" signal that
// corresponds to what the running flag means for ollama, so Available()
// must trust the flag unconditionally for this provider. This test proves
// that by clearing PATH entirely (ollama unreachable, nothing loaded, not
// even installed) and confirming Available still reports true.
func TestAvailableOllamaExemptFromLiveVerification(t *testing.T) {
	t.Setenv("PATH", "")
	if !Available(config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}) {
		t.Error("expected an ollama model to be available even with ollama unreachable/nothing loaded — the flag is trusted unconditionally for this provider")
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

// TestResolveAllNoFlaggedModels asserts the no-flags case returns an empty
// slice, never an error — cloud-only filtering is FilterToRunningLocal's
// job, not a gate failure.
func TestResolveAllNoFlaggedModels(t *testing.T) {
	cfg := &config.Config{}
	got := ResolveAll(cfg)
	if len(got) != 0 {
		t.Errorf("ResolveAll() = %v, want empty", got)
	}
}

// TestResolveAllReturnsEveryVerifiedRunningModel checks that ResolveAll
// probes every flagged model independently and returns only the ones
// that actually answer - the foundation of multi-model concurrency: one
// drifted flag must not hide a model that's genuinely still serving.
func TestResolveAllReturnsEveryVerifiedRunningModel(t *testing.T) {
	omlxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"model-a"}]}`)
	}))
	defer omlxSrv.Close()
	defer SetOmlxProbeURLForTest(omlxSrv.URL)()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/model-a", ProviderID: "omlx", ModelName: "model-a", Location: config.LocationLocal},
			{ID: "omlx/model-b", ProviderID: "omlx", ModelName: "model-b", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/model-a", "omlx/model-b")

	got := ResolveAll(cfg)
	if len(got) != 1 || got[0] != "omlx/model-a" {
		t.Errorf("ResolveAll() = %v, want [omlx/model-a] (model-b flagged but not verified)", got)
	}
}

// TestResolveAllNotInCatalogExcluded asserts a flagged id missing from the
// registry catalog (a data gap) is silently excluded from the verified
// set, not an error — an unverifiable id must never pass as healthy.
func TestResolveAllNotInCatalogExcluded(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetLocalRunningForTest("omlx/ghost-model")
	got := ResolveAll(cfg)
	if len(got) != 0 {
		t.Errorf("ResolveAll() = %v, want empty (model absent from catalog)", got)
	}
}

// TestResolveAllStaleFlagExcluded asserts a flagged id whose probe fails
// (stopped or crashed outside modelman) is excluded from the verified set,
// not an error — ResolveAll never errors; a caller learns about a drifted
// flag only by the id's absence from the returned slice.
func TestResolveAllStaleFlagExcluded(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/qwen3.8", ModelName: "qwen3.8", ProviderID: "omlx"},
		},
	}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	got := ResolveAll(cfg)
	if len(got) != 0 {
		t.Errorf("ResolveAll() = %v, want empty (stale flag)", got)
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
	res := Apply(cfg, models, "")
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
	res := Apply(cfg, cfg.Models, "")
	if len(res.VerifiedRunning) != 1 || res.VerifiedRunning[0] != "omlx/Ornith-1.5-35B-A3B-MLX-4bit" {
		t.Errorf("VerifiedRunning = %v", res.VerifiedRunning)
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
	res := Apply(cfg, cfg.Models, "omlx/other")
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
	res := Apply(cfg, cfg.Models, "omlx/Ornith-1.5-35B-A3B-MLX-4bit")
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
	res := Apply(cfg, cfg.Models, "ghost/x")
	nre, ok := res.PinnedRejected.(*NotRunningError)
	if !ok {
		t.Fatalf("PinnedRejected = %v (%T), want *NotRunningError", res.PinnedRejected, res.PinnedRejected)
	}
	if nre.ModelID != "ghost/x" {
		t.Errorf("PinnedRejected.ModelID = %q, want ghost/x", nre.ModelID)
	}
}

// TestApplyNeverErrorsOnDriftedFlag checks the deliberate relaxation from
// issue #65: a flagged-but-unverified local model is silently excluded
// from the eligible list, never a fatal error for the whole launch. This
// replaces the old single-marker "stale marker is fatal" behavior — with
// multiple models, one drifting out must not block a launch that doesn't
// need it.
func TestApplyNeverErrorsOnDriftedFlag(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")() // nothing listens here
	// cfg.Models must be populated with the flagged model so ResolveAll's
	// idx lookup succeeds and Available() is genuinely invoked (and fails
	// against the unreachable probe URL above) — an empty cfg.Models would
	// exclude the id at the earlier catalog-gap check instead, never
	// reaching the probe this test is meant to exercise.
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	models := []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Location: config.LocationLocal}}
	result := Apply(cfg, models, "")
	if result.PinnedRejected != nil {
		t.Errorf("PinnedRejected = %v, want nil (no pin was set)", result.PinnedRejected)
	}
	if len(result.Eligible) != 0 {
		t.Errorf("Eligible = %v, want empty (the only local model failed its probe)", result.Eligible)
	}
}

// TestApplyPinnedRejectedStillFatalForThatLaunch checks the one surviving
// failure mode: a -M pin naming a specific local model that fails its
// probe is still rejected, even though a non-pinned launch would just
// silently drop it - an explicit request can't be silently substituted.
func TestApplyPinnedRejectedStillFatalForThatLaunch(t *testing.T) {
	defer SetOmlxProbeURLForTest("http://127.0.0.1:1")()
	// cfg.Models must be populated (same reasoning as
	// TestApplyNeverErrorsOnDriftedFlag above) so the pin is rejected
	// because Available() genuinely fails against the unreachable probe,
	// not because of the catalog-gap shortcut.
	cfg := &config.Config{
		Models: []config.Model{
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/qwen3.8")
	models := []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Location: config.LocationLocal}}
	result := Apply(cfg, models, "omlx/qwen3.8")
	var notRunning *NotRunningError
	if !errors.As(result.PinnedRejected, &notRunning) {
		t.Fatalf("PinnedRejected = %v, want *NotRunningError", result.PinnedRejected)
	}
}

// TestApplyKeepsEveryVerifiedLocalDropsOnlyTheDrifted checks the headline
// multi-model behavior at the Apply level (not just ResolveAll): two
// flagged local models that both pass their live probe survive together
// in Eligible alongside a cloud model, while a third flagged local model
// whose probe genuinely fails (the omlx server here simply never lists
// its id) is excluded. This is the exact scenario the whole plan exists to
// introduce — a regression back to single-model semantics, or a shortcut
// that never really reaches Available(), would sail through without this.
func TestApplyKeepsEveryVerifiedLocalDropsOnlyTheDrifted(t *testing.T) {
	srv := httptest.NewServer(modelsHandler("model-a", "model-b")) // model-c deliberately absent
	defer srv.Close()
	defer SetOmlxProbeURLForTest(srv.URL)()

	cfg := &config.Config{
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", Location: config.LocationCloud},
			{ID: "omlx/model-a", ModelName: "model-a", ProviderID: "omlx", Location: config.LocationLocal},
			{ID: "omlx/model-b", ModelName: "model-b", ProviderID: "omlx", Location: config.LocationLocal},
			{ID: "omlx/model-c", ModelName: "model-c", ProviderID: "omlx", Location: config.LocationLocal},
		},
	}
	cfg.SetLocalRunningForTest("omlx/model-a", "omlx/model-b", "omlx/model-c")

	result := Apply(cfg, cfg.Models, "")

	wantVerified := []string{"omlx/model-a", "omlx/model-b"}
	if len(result.VerifiedRunning) != len(wantVerified) {
		t.Fatalf("VerifiedRunning = %v, want %v", result.VerifiedRunning, wantVerified)
	}
	for i, id := range wantVerified {
		if result.VerifiedRunning[i] != id {
			t.Errorf("VerifiedRunning = %v, want %v", result.VerifiedRunning, wantVerified)
		}
	}

	got := idsOf(result.Eligible)
	want := []string{"claude/opus", "omlx/model-a", "omlx/model-b"}
	if len(got) != len(want) {
		t.Fatalf("Eligible = %v, want %v (cloud + both verified locals, drifted model-c dropped)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Eligible = %v, want %v", got, want)
		}
	}
}
