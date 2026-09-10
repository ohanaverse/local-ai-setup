// Package localgate probes whether modelman's marked "currently running"
// local model (issue #65's one-local-model-at-a-time policy) is actually
// serving right now. The marker itself — [local].running_model in
// ~/.config/local-ai/modelman.toml — is read via internal/config
// (Config.LocalRunningModel); this package only does the runtime
// availability probe, mirroring internal/ollamacheck's role for the
// pre-existing ollama-only availability check.
package localgate

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
)

// probeTimeout bounds each HTTP availability probe.
const probeTimeout = 2 * time.Second

// omlxModelsURL and mlxLMServerModelsURL are the endpoints
// bin/llm-isolate-provider's own warmup logic treats as "provider is up"
// for these two providers (see that script's start_omlx/start_mlx_lm_server
// functions). Package-level vars so tests can point them at an
// httptest.Server instead of the real localhost ports.
var (
	omlxModelsURL        = "http://localhost:8000/v1/models"
	mlxLMServerModelsURL = "http://localhost:8001/v1/models"
)

// httpClient is a seam so tests can rely on probeTimeout without waiting
// out a longer default transport timeout against an unreachable port.
var httpClient = &http.Client{Timeout: probeTimeout}

// SetOmlxProbeURLForTest overrides the omlx availability-probe URL and
// returns a func that restores the original. Tests only.
func SetOmlxProbeURLForTest(url string) (restore func()) {
	old := omlxModelsURL
	omlxModelsURL = url
	return func() { omlxModelsURL = old }
}

// SetMlxLMServerProbeURLForTest is SetOmlxProbeURLForTest's mlx_lm_server
// counterpart. Tests only.
func SetMlxLMServerProbeURLForTest(url string) (restore func()) {
	old := mlxLMServerModelsURL
	mlxLMServerModelsURL = url
	return func() { mlxLMServerModelsURL = old }
}

// providerOf splits a registry model id "<provider>/<name>" on the FIRST
// "/" — the model name itself may contain one (e.g.
// openrouter/z-ai/glm-5.3-flash), so only the first separator counts. This
// mirrors the marker schema in docs/superpowers/specs/2026-09-10-one-local-
// model-at-a-time-design.md.
func providerOf(modelID string) (provider, name string, ok bool) {
	idx := strings.Index(modelID, "/")
	if idx < 0 {
		return "", "", false
	}
	return modelID[:idx], modelID[idx+1:], true
}

// probeURL reports whether a GET to url succeeds with a 2xx status inside
// probeTimeout.
func probeURL(url string) bool {
	resp, err := httpClient.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// Available reports whether the local model identified by modelID
// ("<provider>/<name>") is actually serving right now. Unknown/unsupported
// providers (including a future mtplx before it is wired in here, issue
// #66) return false — a marker naming a provider this probe doesn't know
// about fails closed as "not running" rather than reporting stale state as
// healthy.
func Available(modelID string) bool {
	provider, name, ok := providerOf(modelID)
	if !ok {
		return false
	}
	switch provider {
	case "ollama":
		available, err := ollamacheck.Available(name)
		return err == nil && available
	case "omlx", "omlx-6bit":
		return probeURL(omlxModelsURL)
	case "mlx_lm_server":
		return probeURL(mlxLMServerModelsURL)
	default:
		return false
	}
}

// NotRunningError names the model that failed its availability probe (or
// was pinned via -M while a different, or no, local model is marked
// running) and the fix: `modelman start <id>`.
type NotRunningError struct {
	ModelID string
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf(
		"local model %q is not running — start it with `modelman start %s`",
		e.ModelID, e.ModelID,
	)
}

// Resolve reads cfg's [local].running_model marker (via
// Config.LocalRunningModel) and verifies it with Available. It returns
// ("", nil) when no marker is set — every local model should be filtered
// out of the picker (Config.FilterToRunningLocal handles that); (id, nil)
// when the marker is set and verified available; or ("", *NotRunningError)
// when the marker is set but the probe fails — a stale marker (the model
// was stopped or crashed outside modelman) that callers should treat as
// fatal rather than launching.
func Resolve(cfg *config.Config) (string, error) {
	marker := cfg.LocalRunningModel()
	if marker == "" {
		return "", nil
	}
	if !Available(marker) {
		return "", &NotRunningError{ModelID: marker}
	}
	return marker, nil
}
