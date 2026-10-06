package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// NoRoomError means omlx refused to load a model for want of memory: the model
// is larger than the ceiling, or nothing evictable frees enough. Detail is
// omlx's own message, which names what holds the memory.
type NoRoomError struct{ Model, Detail string }

func (e *NoRoomError) Error() string {
	return fmt.Sprintf("omlx has no room for %s: %s", e.Model, e.Detail)
}

// omlxPoolID is the on-disk id omlx knows modelName by: the pool model it
// matches, else the name's last path segment (registry names are HF repo ids,
// omlx serves directory basenames).
func omlxPoolID(cfg *config.Config, e *env, modelName string) string {
	if pool, err := localmodels.OmlxPool(cfg, e.probeClient); err == nil {
		if m, ok := pool.Find(modelName); ok {
			return m.ID
		}
	}
	return path.Base(modelName)
}

// omlxPost sends one bodiless management POST, with the registry's key when
// there is one, and returns the status and omlx's `detail` (else the body).
func omlxPost(ctx context.Context, e *env, target, key string) (code int, detail string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return 0, "", err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := e.chatClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var doc struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &doc) == nil && doc.Detail != "" {
		return resp.StatusCode, doc.Detail, nil
	}
	return resp.StatusCode, truncateForError(body), nil
}

// omlxLoad loads modelName into the running omlx through its load endpoint,
// which blocks until the model is loaded. omlx evicts by its own rule when the
// model does not fit; the caller reconciles from the pool afterwards. A model
// already loading (409), a server still initialising (503) and a request that
// timed out or was reset are waited for; a refused key and "no room" end at
// once, since waiting changes neither. So does a refused connection: the
// caller has already seen the server answer, so nothing listening now means
// omlx went away during the load — most likely it ran out of memory — and
// retrying for the whole warmup budget would only hide that.
func omlxLoad(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	key := localmodels.FamilyAPIKey(cfg, "omlx")
	id := omlxPoolID(cfg, e, modelName)
	target := origin + "/v1/models/" + url.PathEscape(id) + "/load"
	bounded, cancel := context.WithTimeout(ctx, e.warmupTimeout)
	defer cancel()
	last := "no attempts completed"
	for {
		code, detail, err := omlxPost(bounded, e, target, key)
		switch {
		case errors.Is(err, syscall.ECONNREFUSED):
			return fmt.Errorf("omlx stopped answering while loading %s (it may have run out of memory): %w", id, err)
		case err != nil:
			last = err.Error()
		case code >= 200 && code < 300:
			return nil
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return &KeyRefusedError{URL: target, KeySent: key != "", Detail: detail}
		case code == http.StatusInsufficientStorage:
			return &NoRoomError{Model: id, Detail: detail}
		case code == http.StatusNotFound:
			return fmt.Errorf("omlx has no model %q in its pool: %s", id, detail)
		case code == http.StatusConflict || code == http.StatusServiceUnavailable:
			last = fmt.Sprintf("HTTP %d: %s", code, detail)
		default:
			return fmt.Errorf("omlx could not load %s: HTTP %d: %s", id, code, detail)
		}
		if e.sleep(bounded, e.pollInterval) != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("timed out loading %s into omlx: %s", id, last)
		}
	}
}

// omlxUnload unloads one model and leaves the service and every other loaded
// model up. The goal is "not loaded", so omlx's "not loaded" (400) and "no
// such model" (404) answers are success, and the pool is read afterwards: a
// model it still shows loaded is a failed stop whatever the POST said.
func omlxUnload(ctx context.Context, e *env, cfg *config.Config, modelName string) error {
	origin, _ := localmodels.FamilyOrigin(cfg, "omlx")
	key := localmodels.FamilyAPIKey(cfg, "omlx")
	id := omlxPoolID(cfg, e, modelName)
	target := origin + "/v1/models/" + url.PathEscape(id) + "/unload"
	bounded, cancel := context.WithTimeout(ctx, e.loadTimeout)
	defer cancel()
	code, detail, err := omlxPost(bounded, e, target, key)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	switch {
	case err != nil:
		return fmt.Errorf("unloading %s from omlx: %w", id, err)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &KeyRefusedError{URL: target, KeySent: key != "", Detail: detail}
	case (code >= 200 && code < 300) || code == http.StatusBadRequest || code == http.StatusNotFound:
	default:
		return fmt.Errorf("omlx could not unload %s: HTTP %d: %s", id, code, detail)
	}
	if pool, perr := localmodels.OmlxPool(cfg, e.probeClient); perr == nil {
		if m, ok := pool.Find(modelName); ok && (m.Loaded || m.Loading) {
			return fmt.Errorf("omlx still has %s loaded", id)
		}
	}
	return nil
}
