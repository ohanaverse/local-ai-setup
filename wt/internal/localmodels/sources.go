package localmodels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ollamaModel is one entry of an ollama {"models":[...]} response.
type ollamaModel struct {
	Name string
	Size int64 // bytes on disk, as /api/tags reports it; 0 when absent
}

// ollamaModels returns the local models in an ollama {"models":[...]}
// response (/api/tags: pulled models; /api/ps: loaded models). Entries with a
// non-empty remote_host are ollama.com cloud models, not local ones, and are
// skipped.
func ollamaModels(ctx context.Context, client *http.Client, url string) ([]ollamaModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name       string `json:"name"`
			RemoteHost string `json:"remote_host"`
			Size       int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	models := make([]ollamaModel, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" && m.RemoteHost == "" {
			models = append(models, ollamaModel{Name: m.Name, Size: m.Size})
		}
	}
	return models, nil
}

// ollamaModelNames is ollamaModels' names alone.
func ollamaModelNames(ctx context.Context, client *http.Client, url string) ([]string, error) {
	models, err := ollamaModels(ctx, client, url)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = m.Name
	}
	return names, nil
}

// OllamaLoaded returns the local models ollama has loaded right now (its
// /api/ps), with the same parsing inventory uses. An error means the daemon
// gave no usable answer, so an empty list must not be read as "none loaded".
// The request honors ctx, so a cancelled caller aborts the probe instead of
// waiting out the client timeout.
func OllamaLoaded(ctx context.Context, client *http.Client, origin string) ([]string, error) {
	return ollamaModelNames(ctx, client, origin+"/api/ps")
}

// scanModelDirs lists the subdirectories of dir (symlinks to directories
// included), skipping dot-prefixed ones (.cache, .locks, .git — tool
// bookkeeping, not models). A missing dir is "no models", not an error.
func scanModelDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// scanOmlxModels lists the models in omlx's model directory under the ids
// omlx serves them by, following omlx's own discovery (omlx 0.7.0,
// model_discovery.py discover_models). omlx looks two levels deep. For each
// subdirectory of dir (as scanModelDirs lists them: symlinks to directories
// followed, dot-prefixed ones skipped):
//
//   - it holds adapter_config.json: a LoRA adapter, not a model;
//   - else it holds config.json: a model, named after the directory;
//   - else it is an organization folder: each of its subdirectories that holds
//     config.json and no adapter_config.json is a model, named after that
//     CHILD directory. The folder itself is never a model, and one with no
//     such child contributes nothing.
//
// A name found twice is listed once. When nothing was found, dir itself is
// the one model, named after itself, if it is a model by the same test (it
// holds config.json and no adapter_config.json) and holds no Hugging Face
// cache entry. The cache condition is a deliberate conservative answer: omlx
// registers a model from a usable cache entry, and then its own fallback does
// not fire, so the fallback is skipped whenever an entry is present. When the
// entry is one omlx cannot use, omlx itself would take its fallback and serve
// dir, and wt does not list it. A missing dir is "no models", not an error.
//
// Not covered: omlx also resolves Hugging Face hub cache entries
// (models--Org--Name/snapshots/<hash>/) under id rules of its own. Such an
// entry is not listed here.
//
// The names are sorted. They must equal the ids of omlx's /v1/models/status,
// since Pool.Find, matchArtifact and the load and unload requests match on
// them.
func scanOmlxModels(dir string) ([]string, error) {
	names, _, err := scanOmlxModelPaths(dir)
	return names, err
}

// scanOmlxModelPaths is scanOmlxModels plus where each model is: paths maps
// a listed name to its directory. A name found in more than one directory is
// listed once and has no path: the leaf name does not say which directory is
// this model's, and a guess could name another organization's weights.
func scanOmlxModelPaths(dir string) (names []string, paths map[string]string, err error) {
	tops, err := scanModelDirs(dir)
	if err != nil {
		return nil, nil, err
	}
	paths = map[string]string{}
	seen := map[string]bool{}
	add := func(name, path string) {
		if seen[name] {
			delete(paths, name)
			return
		}
		seen[name] = true
		paths[name] = path
		names = append(names, name)
	}
	// isModel reports whether p is a model directory; adapter is true for a
	// LoRA adapter, which is not one whatever else it holds.
	isModel := func(p string) (model, adapter bool) {
		if fileExists(filepath.Join(p, "adapter_config.json")) {
			return false, true
		}
		return fileExists(filepath.Join(p, "config.json")), false
	}
	sawHFCache := false
	for _, top := range tops {
		p := filepath.Join(dir, top)
		model, adapter := isModel(p)
		switch {
		case adapter:
		case model:
			add(top, p)
		case isHFCacheEntry(p):
			sawHFCache = true
		default:
			// An organization folder. One that cannot be read holds no
			// model wt can name, as for omlx.
			children, err := scanModelDirs(p)
			if err != nil {
				continue
			}
			for _, child := range children {
				if model, _ := isModel(filepath.Join(p, child)); model {
					add(child, filepath.Join(p, child))
				}
			}
		}
	}
	// omlx's fallback fires only when it registered nothing, and a cache entry
	// may have registered a model there. Neither tool lists cache models, so
	// listing nothing is the safe answer.
	if len(names) == 0 && !sawHFCache {
		if model, _ := isModel(dir); model {
			return []string{filepath.Base(dir)}, map[string]string{filepath.Base(dir): dir}, nil
		}
	}
	sort.Strings(names)
	return names, paths, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// isHFCacheEntry reports whether p is a Hugging Face hub cache entry
// (models--Org--Name with a snapshots directory).
func isHFCacheEntry(p string) bool {
	if !strings.HasPrefix(filepath.Base(p), "models--") {
		return false
	}
	st, err := os.Stat(filepath.Join(p, "snapshots"))
	return err == nil && st.IsDir()
}

// mtplxRepoID maps an MTPLX "<org>--<model>" directory name to "org/model"
// (every "--" becomes "/", matching modelman's _repo_id
// (modelman/src/modelman/providers/mtplx.py). Caveat documented there: a
// literal "--" inside a segment does not round-trip.
func mtplxRepoID(dirName string) string {
	return strings.ReplaceAll(dirName, "--", "/")
}
