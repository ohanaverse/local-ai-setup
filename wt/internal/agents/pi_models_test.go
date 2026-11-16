package agents

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// isLaunchable must return true only when the model is present under the
// named provider AND marked _launch: true. A model present but _launch: false
// must not be launched; an entry under the wrong provider must not match.
func TestIsLaunchable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"good"},{"_launch":false,"id":"orphan"}]},"litellm":{"models":[{"_launch":true,"id":"ollama/good"}]}}}`)
	if !isLaunchable("ollama", "good", path) {
		t.Error("isLaunchable(ollama, good) = false, want true")
	}
	if isLaunchable("ollama", "orphan", path) {
		t.Error("isLaunchable(ollama, orphan) = true, want false (_launch false)")
	}
	if isLaunchable("ollama", "missing", path) {
		t.Error("isLaunchable(ollama, missing) = true, want false")
	}
	if !isLaunchable("litellm", "ollama/good", path) {
		t.Error("isLaunchable(litellm, ollama/good) = false, want true")
	}
	if isLaunchable("ollama", "ollama/good", path) {
		t.Error("isLaunchable(ollama, ollama/good) = true, want false (wrong provider)")
	}
}

// isLaunchable must return false (not panic) when models.json is missing or
// unparseable, so the caller falls back to pi's default model.
func TestIsLaunchableMissingOrCorrupt(t *testing.T) {
	if isLaunchable("ollama", "x", filepath.Join(t.TempDir(), "nope.json")) {
		t.Error("isLaunchable on missing file = true, want false")
	}
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{not valid json`)
	if isLaunchable("ollama", "x", path) {
		t.Error("isLaunchable on corrupt file = true, want false")
	}
}

// piDriver.Build on the gateway path must pass the registry id routed through
// the wt-created provider as --model ("litellm/<registry-id>"). pi splits --model
// on the first slash, so "litellm" must carry the entry while the full id —
// which itself contains a slash — lands verbatim in the request body that
// LiteLLM dispatches on. Passing the bare name or a bare "<id>" resolves to
// the wrong entry and 400s at the gateway.
func TestPiBuildLitellm(t *testing.T) {
	writePiModels(t, `{"providers":{"litellm":{"models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]}}}`)

	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	lc := piDriver{}.Build(m, false, litellmRoute(m))
	if !slices.Equal(lc.Args, []string{"--model", "litellm/ollama/qwen3.8:27b-mlx"}) {
		t.Fatalf("expected --model litellm/<registry id>, got %v", lc.Args)
	}
}

// piDriver.Build on the gateway path must fall back to pi's default model with
// a warning when the gateway entry is not present/launchable in models.json —
// for example before the first sync or when the user disabled the entry.
func TestPiBuildLitellmNotConfigured(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]}}}`)

	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	lc := piDriver{}.Build(m, false, litellmRoute(m))
	if len(lc.Args) != 0 {
		t.Fatalf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when falling back")
	}
}

// TestPiDriverDeclaresWrapperAndEnvProfileMechanisms verifies pi's
// ProfileMechanisms is exactly {wrapper, env} — wrapper is the little-coder
// integration; env is allowed because profile env lands on the launched
// process (cmd/wt-session applies env before the wrapper), which with the
// little-coder wrapper is little-coder itself and its LITTLE_CODER_*
// whitelist gate reads it. Anything else (args/config_file) must still fail
// Validate rather than silently doing nothing.
func TestPiDriverDeclaresWrapperAndEnvProfileMechanisms(t *testing.T) {
	mechs := piDriver{}.ProfileMechanisms()
	want := map[profiles.Mechanism]bool{profiles.MechanismWrapper: true, profiles.MechanismEnv: true}
	if len(mechs) != len(want) {
		t.Fatalf("ProfileMechanisms() = %v, want exactly %v", mechs, want)
	}
	got := map[profiles.Mechanism]bool{}
	for _, m := range mechs {
		got[m] = true
		if !want[m] {
			t.Errorf("unexpected mechanism %q", m)
		}
	}
	for m := range want {
		if !got[m] {
			t.Errorf("ProfileMechanisms() = %v, missing expected mechanism %q", mechs, m)
		}
	}
}

// TestPiDriverRequiredMechanismCouplesEnvToWrapper verifies pi declares
// (via profiles.MechanismRequirer) that env requires wrapper on the same
// profile entry — env has no effect on bare pi, so profiles.Validate must
// reject a profile setting env without wrapper (code-review regression
// lock: ProfileMechanisms alone let such a profile pass validation
// cleanly, only to silently no-op at launch time).
func TestPiDriverRequiredMechanismCouplesEnvToWrapper(t *testing.T) {
	required, ok := piDriver{}.RequiredMechanism(profiles.MechanismEnv)
	if !ok || required != profiles.MechanismWrapper {
		t.Errorf("RequiredMechanism(env) = (%q, %v), want (wrapper, true)", required, ok)
	}
	if _, ok := (piDriver{}).RequiredMechanism(profiles.MechanismWrapper); ok {
		t.Error("RequiredMechanism(wrapper) reports a requirement, want none")
	}
}

// piHomeModelsPath points HOME at a temp dir and returns the models.json path
// piDriver.Build will read there, so a test can run SyncGateway and Build
// against the same file without touching the developer's real ~/.pi.
func piHomeModelsPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	piDir := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(piDir, "models.json")
}

// TestPiBuildFlagsModelFallback verifies both of Build's "using default
// model" paths set LaunchCmd.ModelFallback next to the warning, and a normal
// launch does not. wt smoke reads the flag to fail the row: a launch that
// quietly runs pi's default model answers the smoke prompt just as well as
// the model under test, so the warning text alone let a broken row PASS.
func TestPiBuildFlagsModelFallback(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}

	path := piHomeModelsPath(t)
	writeFile(t, path, `{"providers":{"litellm":{"models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]}}}`)
	if lc := (piDriver{}).Build(m, false, litellmRoute(m)); lc.ModelFallback || lc.Warn != "" {
		t.Errorf("launchable model: ModelFallback = %v, Warn = %q, want neither", lc.ModelFallback, lc.Warn)
	}

	writeFile(t, path, `{"providers":{}}`)
	if lc := (piDriver{}).Build(m, false, litellmRoute(m)); !lc.ModelFallback || lc.Warn == "" {
		t.Errorf("model missing from models.json: ModelFallback = %v, Warn = %q, want both set", lc.ModelFallback, lc.Warn)
	}

	t.Setenv("HOME", "") // os.UserHomeDir fails, so models.json cannot be located
	if lc := (piDriver{}).Build(m, false, litellmRoute(m)); !lc.ModelFallback || lc.Warn == "" {
		t.Errorf("models.json unlocatable: ModelFallback = %v, Warn = %q, want both set", lc.ModelFallback, lc.Warn)
	}
}
