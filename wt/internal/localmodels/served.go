package localmodels

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ServedIDs asks a family's server which models it is serving now — the ids
// the inventory marks Running and the lifecycle engine treats as occupying a
// single-model server. Both ask here, so they cannot describe the same server
// differently.
//
// A non-nil error means the server gave no usable answer, so an empty list
// must not be read as "nothing is serving"; it is a refused connection
// (errors.Is ECONNREFUSED) when nothing is listening. A nil error with no ids
// means the server answered and has nothing loaded.
//
// mtplx and mlx_lm_server serve one model per process, so their /v1/models is
// the answer. omlx's is not (#201) — see omlxLoaded.
func ServedIDs(cfg *config.Config, client *http.Client, family string) ([]string, error) {
	origin, _ := FamilyOrigin(cfg, family)
	if family == "omlx" {
		return omlxLoaded(client, origin, FamilyAPIKey(cfg, family))
	}
	return FetchModelIDsErr(client, origin+"/v1/models")
}

// FamilyAPIKey is the key the registry gives for a family's server: the first
// provider row of the family whose auth.secret_ref resolves to something. ""
// when there is none — the usual case, a local server that wants no key.
// Exported for internal/lifecycle, whose warmup sends it as the probe does.
func FamilyAPIKey(cfg *config.Config, family string) string {
	for _, id := range familyProviderIDs(family) {
		p := cfg.ProviderByID(id)
		if p == nil || p.Auth.SecretRef == "" {
			continue
		}
		if key, err := config.ResolveSecret(p.Auth.SecretRef); err == nil && key != "" {
			return key
		}
	}
	return ""
}

// omlxLoaded lists the models an omlx server has loaded (or is loading).
//
// omlx's /v1/models cannot say: it lists every model in the engine pool — the
// whole model directory, minus ones the user hid — loaded or not (omlx 0.7.0,
// server.py list_models). Read as "running" it made every omlx model on disk
// running for as long as the service was up (#201).
//
// What omlx does report, in order of what it costs to ask:
//
//   - /health, no key: the pool's model_count and loaded_count. Zero loaded is
//     "nothing", exactly, on a healthy (2xx) answer; a 503 answer carries the
//     same counts while pinned models preload, and loaded_count cannot see a
//     model mid-load, so zero built there is not settled until status has said
//     none is loading either. All loaded is "everything listed" — but only when
//     the list has as many models as the pool, since a hidden model is counted
//     and not listed.
//   - /v1/models/status: each model's loaded and is_loading flags, by its
//     on-disk id. It is behind omlx's management auth, which wants the
//     server's API key whenever one is set — as /v1/models itself does on
//     such a server — so key (the registry's secret_ref for the family, ""
//     if none) is sent when there is one. A model mid-load
//     counts: it occupies the server as much as a loaded one.
//
// When the counts are mixed and status does not answer, the result is an
// error, not a guess: "nothing" would let a start replace a serving model
// unasked, and "everything" is the bug. An omlx with no /health at all, or one
// whose answer has no pool counts, predates this and gets the list, as before.
func omlxLoaded(client *http.Client, origin, key string) ([]string, error) {
	var health struct {
		Pool *struct {
			Models int `json:"model_count"`
			Loaded int `json:"loaded_count"`
		} `json:"engine_pool"`
	}
	code, err := getJSON(client, origin+"/health", "", &health)
	if err != nil && code == 0 {
		return nil, err // no answer at all: refused, timed out
	}
	if code == http.StatusNotFound || (err == nil && health.Pool == nil) {
		return FetchModelIDsErr(client, origin+"/v1/models")
	}
	// /health answers 503 with the same body while pinned models preload, so
	// the counts are read whatever the status; without them it is no answer.
	if health.Pool == nil {
		return nil, err
	}
	// Zero loaded on a healthy answer settles it. On a 503 it does not:
	// loaded_count counts engines already built, so a pinned model mid-load
	// would read as "nothing" here and a sync in that preload window would
	// drop every omlx route for models omlx is about to serve — status sees
	// is_loading, so ask it before settling.
	if health.Pool.Loaded == 0 && code >= 200 && code < 300 {
		return nil, nil
	}
	// A list that does not answer is not the end: an omlx with an API key
	// refuses /v1/models without it (omlx 0.7.0), and status below is asked
	// with the key. A server that is really gone fails there too.
	if health.Pool.Loaded == health.Pool.Models {
		listed, err := FetchModelIDsErr(client, origin+"/v1/models")
		if err == nil && len(listed) == health.Pool.Models {
			return listed, nil
		}
	}
	var status struct {
		Models []struct {
			ID      string `json:"id"`
			Loaded  bool   `json:"loaded"`
			Loading bool   `json:"is_loading"`
		} `json:"models"`
	}
	if _, err := getJSON(client, origin+"/v1/models/status", key, &status); err != nil {
		return nil, fmt.Errorf("omlx has %d of %d models loaded and would not say which (set auth.secret_ref on the registry's omlx provider if the server has an API key): %w", health.Pool.Loaded, health.Pool.Models, err)
	}
	var ids []string
	for _, m := range status.Models {
		if m.ID != "" && (m.Loaded || m.Loading) {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// getJSON GETs url — with key as a Bearer token when there is one — and
// decodes the body into v. The status code is returned whenever the server
// answered (0 when it did not), and the body is decoded even on a non-2xx
// answer, since omlx's /health carries its counts on a 503 too; err is non-nil
// for a transport failure, a non-2xx status or an undecodable body.
func getJSON(client *http.Client, url, key string, v any) (int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: %w", url, err)
	}
	decodeErr := json.Unmarshal(body, v)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	if decodeErr != nil {
		return resp.StatusCode, fmt.Errorf("GET %s: %w", url, decodeErr)
	}
	return resp.StatusCode, nil
}
