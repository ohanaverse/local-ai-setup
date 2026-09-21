package localmodels

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ollamaModelNames returns the names in an ollama {"models":[...]} response
// (/api/tags: pulled models; /api/ps: loaded models). Entries with a non-empty
// remote_host are ollama.com cloud models, not local ones, and are skipped.
func ollamaModelNames(client *http.Client, url string) ([]string, error) {
	resp, err := client.Get(url)
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
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" && m.RemoteHost == "" {
			names = append(names, m.Name)
		}
	}
	return names, nil
}

// OllamaLoaded returns the local models ollama has loaded right now (its
// /api/ps), with the same parsing inventory uses. An error means the daemon
// gave no usable answer, so an empty list must not be read as "none loaded".
func OllamaLoaded(client *http.Client, origin string) ([]string, error) {
	return ollamaModelNames(client, origin+"/api/ps")
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

// mtplxRepoID maps an MTPLX "<org>--<model>" directory name to "org/model"
// (every "--" becomes "/", matching modelman's _repo_id
// (modelman/src/modelman/providers/mtplx.py). Caveat documented there: a
// literal "--" inside a segment does not round-trip.
func mtplxRepoID(dirName string) string {
	return strings.ReplaceAll(dirName, "--", "/")
}
