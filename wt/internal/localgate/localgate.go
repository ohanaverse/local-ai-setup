// Package localgate probes which of modelman's per-model "running"-flagged
// local models (the 2026-09-14 multi-model local lifecycle design,
// superseding issue #65's one-local-model-at-a-time marker) are actually
// serving right now. The flags themselves — modelman.toml's per-model
// `running` entries — are read via internal/config
// (Config.RunningLocalModelIDs, UNVERIFIED); this package does the runtime
// availability probe for each one, mirroring internal/ollamacheck's role
// for the pre-existing ollama-only availability check, and exposes the
// shared Apply policy so the multiple launch paths (non-TUI, TUI) agree on
// what "eligible" means.
package localgate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// probeTimeout bounds each HTTP availability probe.
const probeTimeout = 2 * time.Second

// omlxModelsURL and mlxLMServerModelsURL are the /v1/models endpoints
// bin/llm-isolate-provider's own warmup logic treats as "provider is up"
// for these two providers (see that script's start_omlx/start_mlx_lm_server
// functions). Package-level vars so tests can point them at an
// httptest.Server instead of the real localhost ports.
var (
	omlxModelsURL        = "http://localhost:8000/v1/models"
	mlxLMServerModelsURL = "http://localhost:8001/v1/models"
	// Port 8003 is mtplx's fixed serve port; modelman's single source is
	// modelman/providers/mtplx.py (MTPLX_PORT). Bash (bin/lib/mtplx.sh) and
	// Go each carry the number once — keep them in lockstep if it moves.
	mtplxModelsURL = "http://localhost:8003/v1/models"
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

// SetMtplxProbeURLForTest is SetOmlxProbeURLForTest's mtplx counterpart.
// Tests only.
func SetMtplxProbeURLForTest(url string) (restore func()) {
	old := mtplxModelsURL
	mtplxModelsURL = url
	return func() { mtplxModelsURL = old }
}

// nameMatches reports whether a server-reported model id names the same
// model as want. Lenient on prefix (a server may report a path-ish
// spelling of the same model), strict on the variant tail — omlx's 4-bit
// and 6-bit variants share port 8000 and differ exactly there, so a
// mismatched tail is a different model, never a spelling variant.
func nameMatches(served, want string) bool {
	return served == want || strings.HasSuffix(served, "/"+want) || strings.HasSuffix(want, "/"+served)
}

// fetchModelIDs GETs an OpenAI-compatible /v1/models endpoint and returns
// the ids of the models the server is actually serving; nil on any
// failure (connection refused, timeout, non-2xx, non-JSON body) — nil
// reads as "nothing serving", never as "unknown".
func fetchModelIDs(url string) []string {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// Available reports whether the local model m is actually serving right
// now. The probe is name-checked against the flagged model — not just
// provider liveness: oMLX's 4-bit and 6-bit variants share port 8000, so a
// bare 2xx would verify either as the other.
//
// Unknown/unsupported providers return false — a flagged id naming a
// provider this probe doesn't know about fails closed as "not running"
// rather than reporting stale state as healthy.
func Available(m config.Model) bool {
	switch m.ProviderID {
	case "ollama":
		// ollama is deliberately exempt from live verification. `modelman
		// start` for an ollama model is flag-only — no warmup call, since
		// ollama lazy-loads on first request — so nothing ever loads the
		// model at start time. The only signal available, `ollama ps`
		// (the currently-*loaded* set), would read the model as not
		// running on the very first probe after `modelman start`, silently
		// self-clearing the flag and permanently defeating "starting an
		// ollama model makes it appear in the picker." There is no live
		// "is this specific model loaded" signal that corresponds to what
		// the running flag means for ollama (the daemon serves whatever is
		// requested regardless of what's currently loaded), so the flag is
		// trusted as-is with no probe here — mirrors the same fix applied
		// to modelman's own _probe_running for this one provider.
		return true
	case "omlx", "omlx-6bit":
		for _, served := range fetchModelIDs(omlxModelsURL) {
			if nameMatches(served, m.ModelName) {
				return true
			}
		}
		return false
	case "mlx_lm_server":
		// One target+draft pairing per process — bin/llm-isolate-provider's
		// mlx_lm_server branch is the only thing that starts one, and
		// mlx_lm.server loads its model before serving, so a non-empty
		// /v1/models is already model-accurate. An exact-name check is not
		// possible here: the server reports the target string it was
		// started with (a local_path or repo id), which wt cannot
		// reconstruct — its registry parser deliberately ignores the
		// model's fetch/draft fields.
		return len(fetchModelIDs(mlxLMServerModelsURL)) > 0
	case "mtplx":
		for _, served := range fetchModelIDs(mtplxModelsURL) {
			if nameMatches(served, m.ModelName) {
				return true
			}
		}
		return false
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

// ResolveAll returns every local model id modelman's per-model running
// flag names (Config.RunningLocalModelIDs) that ALSO passes a live
// availability probe right now. A flagged-but-dead id is simply excluded
// — never an error — since one drifted model must not block a launch
// that doesn't need it. A flagged id absent from cfg.Models (a registry
// data gap) is excluded the same way: an unidentifiable model can't be
// name-checked-probed.
func ResolveAll(cfg *config.Config) []string {
	flagged := cfg.RunningLocalModelIDs()
	verified := make([]string, 0, len(flagged))
	for _, id := range flagged {
		idx := config.IndexModelByID(cfg.Models, id)
		if idx < 0 {
			continue
		}
		if Available(cfg.Models[idx]) {
			verified = append(verified, id)
		}
	}
	return verified
}

// Result is Apply's outcome: the verified-running local model ids, the
// eligible list after the gate's filter, and — when pinned (-M) named an
// otherwise-eligible local model that isn't in the verified set — the
// pinned-local rejection. Callers map PinnedRejected to their own UX (the
// non-TUI path treats it as fatal; the TUI routes back to the agent
// picker with the message as status).
type Result struct {
	VerifiedRunning []string
	Eligible        []config.Model
	PinnedRejected  error
}

// Apply is the multi-model local-running gate policy (2026-09-14 design)
// in one place, shared by cmd/wt/resolve.go (non-TUI launch) and
// internal/tui's enterModelPhase so the two launch paths can never
// diverge: verify every flagged local model, reject a pinned local model
// that isn't among the verified ones, then narrow models to
// cloud-plus-every-verified-running-local. Never returns a fatal error —
// a flagged-but-unverified model is silently excluded (see
// TestApplyNeverErrorsOnDriftedFlag); PinnedRejected is the only rejection
// a caller must still handle. pinned is the -M flag value ("" = not
// pinned).
func Apply(cfg *config.Config, models []config.Model, pinned string) Result {
	verified := ResolveAll(cfg)
	res := Result{VerifiedRunning: verified, Eligible: models}
	if !cfg.LocalGateActive() {
		return res
	}
	if pinned != "" {
		if idx := config.IndexModelByID(models, pinned); idx >= 0 {
			pm := models[idx]
			isVerified := false
			for _, id := range verified {
				if id == pm.ID {
					isVerified = true
					break
				}
			}
			// Fail closed on an unresolvable location (a registry data
			// gap): treat the model as local, so a pin it names is
			// rejected rather than silently allowed.
			if loc, lerr := cfg.ResolveLocation(pm); (lerr != nil || loc == config.LocationLocal) && !isVerified {
				res.PinnedRejected = &NotRunningError{ModelID: pm.ID}
			}
		}
	}
	res.Eligible = cfg.FilterToRunningLocal(models, verified)
	return res
}
