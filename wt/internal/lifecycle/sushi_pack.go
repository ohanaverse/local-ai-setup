package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// PackNotListedError means the server's /v1/models does not list the model
// name a start asked for.
type PackNotListedError struct {
	Provider, Model, URL string
	Listed               []string // every id the server lists, loaded or not
}

func (e *PackNotListedError) Error() string {
	return fmt.Sprintf("%s is not listed by %s at %s (it lists: %s); register the pack's exact id as model_name", e.Model, e.Provider, e.URL, strings.Join(e.Listed, ", "))
}

// listedPackIDs is every id modelsURL lists, whatever its "loaded" says: a
// pack sushi has not loaded yet is still one it can serve. A 401/403 is a
// *KeyRefusedError.
func listedPackIDs(client *http.Client, modelsURL, key string) ([]string, error) {
	ids, err := localmodels.FetchModelIDsKeyed(client, modelsURL, key)
	if errors.Is(err, localmodels.ErrKeyRefused) {
		return nil, &KeyRefusedError{URL: modelsURL, KeySent: key != "", Detail: err.Error()}
	}
	return ids, err
}

// requirePackListed fails a start whose model the server at modelsURL does
// not list, before any chat request: sushi answers an id it does not know
// with "503 No default model configured", which a warmup would only retry.
// The ids are compared exactly, since sushi loads only by its exact id (the
// lenient localmodels.NameMatches would pass a bare name against a listed
// org/name). A server that lists nothing at all is not judged here.
func (e *env) requirePackListed(ctx context.Context, provider, modelsURL, model, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := listedPackIDs(e.probeClient, modelsURL, key)
	if err != nil {
		return err
	}
	if len(ids) == 0 || slices.Contains(ids, model) {
		return nil
	}
	return &PackNotListedError{Provider: provider, Model: model, URL: modelsURL, Listed: ids}
}

// confirmLoaded fails a start whose warmup the server answered while its
// /v1/models still does not list model as loaded: a pack sushi could not
// load must not read as started. Only the loaded entries count, and the id
// must equal model exactly (sushi serves only its exact id; NameMatches would
// pass a bare name against a loaded org/name). A 401/403 is a
// *KeyRefusedError.
func (e *env) confirmLoaded(ctx context.Context, modelsURL, model, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := localmodels.FetchLoadedModelIDsKeyed(e.probeClient, modelsURL, key)
	if errors.Is(err, localmodels.ErrKeyRefused) {
		return &KeyRefusedError{URL: modelsURL, KeySent: key != "", Detail: err.Error()}
	}
	if err != nil {
		return err
	}
	if slices.Contains(ids, model) {
		return nil
	}
	return fmt.Errorf("%s answered the warmup but /v1/models does not list %s as loaded", modelsURL, model)
}
