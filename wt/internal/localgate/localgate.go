// Package localgate probes whether modelman's marked "currently running"
// local model (issue #65's one-local-model-at-a-time policy) is actually
// serving right now. The marker itself — [local].running_model in
// ~/.config/local-ai/modelman.toml — is read via internal/config
// (Config.LocalRunningModel); this package only does the runtime
// availability probe, mirroring internal/ollamacheck's role for the
// pre-existing ollama-only availability check.
package localgate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/ollamacheck"
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
// now. The probe is name-checked against the marker's model — not just
// provider liveness: an ollama marker naming a merely-downloaded (never
// loaded) model must read as "not running" (ollama ps, not ollama list),
// and oMLX's 4-bit and 6-bit variants share port 8000, so a bare 2xx
// would verify either as the other.
//
// Unknown/unsupported providers (including a future mtplx before it is
// wired in here, issue #66) return false — a marker naming a provider
// this probe doesn't know about fails closed as "not running" rather
// than reporting stale state as healthy.
func Available(m config.Model) bool {
	switch m.ProviderID {
	case "ollama":
		loaded, err := ollamacheck.Loaded(m.ModelName)
		return err == nil && loaded
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
//
// A marker naming a model absent from the catalog (a registry data gap)
// also fails closed: wt cannot name-checked-probe a model it cannot
// identify, and presenting an unverifiable id as "verified running" is
// the failure mode this probe exists to prevent.
func Resolve(cfg *config.Config) (string, error) {
	marker := cfg.LocalRunningModel()
	if marker == "" {
		return "", nil
	}
	idx := config.IndexModelByID(cfg.Models, marker)
	if idx < 0 {
		return "", &NotRunningError{ModelID: marker}
	}
	if !Available(cfg.Models[idx]) {
		return "", &NotRunningError{ModelID: marker}
	}
	return marker, nil
}

// Result is Apply's outcome: the verified running local model id, the
// eligible list after the gate's filter, and — when pinned (-M) named an
// otherwise-eligible local model that isn't the running one — the
// pinned-local rejection. Callers map PinnedRejected to their own UX (the
// non-TUI path treats it as fatal; the TUI routes back to the agent
// picker with the message as status).
type Result struct {
	RunningLocal   string
	Eligible       []config.Model
	PinnedRejected error
}

// Apply is the one-local-model-at-a-time gate policy (issue #65) in one
// place, shared by cmd/wt/resolve.go (non-TUI launch) and internal/tui's
// enterModelPhase so the two launch paths can never diverge: resolve the
// marker, reject a pinned local model that isn't the running one, then
// narrow models to cloud-plus-the-one-running-local. error is fatal (a
// stale marker blocks every launch through the caller, not just ones that
// would have picked a local model, until `modelman start`/`modelman stop`
// repairs it). pinned is the -M flag value ("" = not pinned).
func Apply(cfg *config.Config, models []config.Model, pinned string) (Result, error) {
	runningLocal, err := Resolve(cfg)
	if err != nil {
		return Result{}, err
	}
	res := Result{RunningLocal: runningLocal, Eligible: models}
	if !cfg.LocalGateActive() {
		return res, nil
	}
	if pinned != "" {
		if idx := config.IndexModelByID(models, pinned); idx >= 0 {
			pm := models[idx]
			// Fail closed on an unresolvable location (a registry data
			// gap): treat the model as local, so a pin it names is
			// rejected rather than silently allowed.
			if loc, lerr := cfg.ResolveLocation(pm); (lerr != nil || loc == config.LocationLocal) && pm.ID != runningLocal {
				res.PinnedRejected = &NotRunningError{ModelID: pm.ID}
			}
		}
	}
	res.Eligible = cfg.FilterToRunningLocal(models, runningLocal)
	return res, nil
}