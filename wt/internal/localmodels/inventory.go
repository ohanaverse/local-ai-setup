package localmodels

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Status is one provider family's probe outcome.
type Status string

const (
	// StatusOK means discovery succeeded. It does NOT imply the server is up:
	// a stopped omlx/mtplx server with model dirs on disk reports ok with
	// nothing running, and an ollama /api/ps failure leaves ollama ok with
	// nothing running.
	StatusOK Status = "ok"
	// StatusUnreachable means discovery itself failed: for ollama, /api/tags
	// failed (daemon down); for omlx/mtplx, the model-directory scan (or
	// config.ExpandHome) failed. It is NOT a server-liveness signal.
	StatusUnreachable Status = "unreachable"
	// StatusUnsupported marks a provider whose models cannot be discovered
	// (mlx_lm_server's target+draft pairing); only running-state is probed.
	StatusUnsupported Status = "unsupported"
)

// probeTimeout bounds each HTTP probe.
const probeTimeout = 2 * time.Second

const (
	defaultOmlxOrigin  = "http://localhost:8000"
	defaultMlxLMOrigin = "http://localhost:8001"
	defaultMtplxOrigin = "http://localhost:8003"
	defaultOmlxDir     = "~/.omlx/models"
	defaultMtplxDir    = "~/.mtplx/models"
)

// Entry is one local model: registered (Registered) or discovered.
type Entry struct {
	ProviderID string // registry provider id (omlx-6bit rows keep theirs); the family id for discovered entries
	Artifact   string // pulled/on-disk name in the provider's spelling; "" for a registered model not found on disk
	ModelID    string // registry id when registered, else config.DiscoveredModelID(ProviderID, Artifact)
	Registered bool
	Running    bool // serving right now (live probe only)
}

// Snapshot is one inventory round. Providers is keyed by provider family
// (ollama, omlx — which also covers omlx-6bit — mtplx, mlx_lm_server).
type Snapshot struct {
	Entries   []Entry
	Providers map[string]Status
}

// Inventory probes every local provider in the registry concurrently and
// merges discovered artifacts with the registry's local models.
func Inventory(cfg *config.Config) Snapshot {
	return inventory(cfg, &http.Client{Timeout: probeTimeout})
}

// familyOf maps a registry provider id to its probe family; "" when wt has no
// probe for it (e.g. retired llamacpp). omlx and omlx-6bit are ONE physical
// server, so they share the "omlx" family.
func familyOf(providerID string) string {
	switch providerID {
	case "ollama", "omlx", "mtplx", "mlx_lm_server":
		return providerID
	case "omlx-6bit":
		return "omlx"
	}
	return ""
}

// source is one family's probe result.
type source struct {
	family    string
	status    Status
	artifacts []string // discovered names, provider spelling (mtplx: repo id form)
	loaded    []string // names serving right now
}

func (s *source) matchArtifact(artifact, modelName string) bool {
	switch s.family {
	case "ollama":
		return OllamaNameMatches(artifact, modelName)
	case "omlx", "mtplx":
		return NameMatches(artifact, modelName)
	}
	return false
}

func (s *source) isRunning(name string) bool {
	switch s.family {
	case "ollama":
		for _, l := range s.loaded {
			if OllamaNameMatches(l, name) {
				return true
			}
		}
	case "omlx", "mtplx":
		for _, l := range s.loaded {
			if NameMatches(l, name) {
				return true
			}
		}
	case "mlx_lm_server":
		// One target+draft pairing per process, model loaded before serving:
		// a non-empty /v1/models is already model-accurate.
		return len(s.loaded) > 0
	}
	return false
}

// familyOrigin is the probe origin for a family: the first registry provider
// row of the family with an auth.base_url, else the default port.
func familyOrigin(cfg *config.Config, family string) string {
	ids, def := []string{family}, ""
	switch family {
	case "ollama":
		def = config.OllamaBaseURL
	case "omlx":
		ids, def = []string{"omlx", "omlx-6bit"}, defaultOmlxOrigin
	case "mtplx":
		def = defaultMtplxOrigin
	case "mlx_lm_server":
		def = defaultMlxLMOrigin
	}
	for _, id := range ids {
		if p := cfg.ProviderByID(id); p != nil && p.Auth.BaseURL != "" {
			return config.BaseOrigin(p.Auth.BaseURL)
		}
	}
	return def
}

func probeFamily(cfg *config.Config, client *http.Client, family string) *source {
	s := &source{family: family, status: StatusOK}
	origin := familyOrigin(cfg, family)
	switch family {
	case "ollama":
		names, err := ollamaModelNames(client, origin+"/api/tags")
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		s.artifacts = names
		if loaded, err := ollamaModelNames(client, origin+"/api/ps"); err == nil {
			s.loaded = loaded
		}
	case "omlx", "mtplx":
		s.loaded = FetchModelIDs(client, origin+"/v1/models")
		p, def := cfg.ProviderByID(family), defaultOmlxDir
		if family == "mtplx" {
			def = defaultMtplxDir
		}
		// Directories are attributed to the "omlx" provider only; a config
		// with just a hand-added omlx-6bit row does not scan.
		if p == nil {
			return s
		}
		dirSetting := p.ModelDir
		if dirSetting == "" {
			dirSetting = def
		}
		dir, err := config.ExpandHome(dirSetting)
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		names, err := scanModelDirs(dir)
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		if family == "mtplx" {
			for i, n := range names {
				names[i] = mtplxRepoID(n)
			}
		}
		s.artifacts = names
	case "mlx_lm_server":
		s.status = StatusUnsupported
		s.loaded = FetchModelIDs(client, origin+"/v1/models")
	}
	return s
}

func inventory(cfg *config.Config, client *http.Client) Snapshot {
	var families []string
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		f := familyOf(p.ID)
		if f == "" || p.Location != config.LocationLocal || seen[f] {
			continue
		}
		seen[f] = true
		families = append(families, f)
	}

	results := make([]*source, len(families))
	var wg sync.WaitGroup
	for i, f := range families {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			results[i] = probeFamily(cfg, client, f)
		}(i, f)
	}
	wg.Wait()

	snap := Snapshot{Providers: map[string]Status{}}
	sources := map[string]*source{}
	for i, f := range families {
		sources[f] = results[i]
		snap.Providers[f] = results[i].status
	}

	consumed := map[string]bool{}
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		e := Entry{ProviderID: m.ProviderID, ModelID: m.ID, Registered: true}
		if src := sources[familyOf(m.ProviderID)]; src != nil {
			for _, a := range src.artifacts {
				key := src.family + "\x00" + a
				if !consumed[key] && src.matchArtifact(a, m.ModelName) {
					consumed[key] = true
					e.Artifact = a
					break
				}
			}
			name := e.Artifact
			if name == "" {
				name = m.ModelName
			}
			e.Running = src.isRunning(name)
		}
		snap.Entries = append(snap.Entries, e)
	}
	for _, f := range families {
		src := sources[f]
		for _, a := range src.artifacts {
			if consumed[f+"\x00"+a] {
				continue
			}
			snap.Entries = append(snap.Entries, Entry{
				ProviderID: f,
				Artifact:   a,
				ModelID:    config.DiscoveredModelID(f, a),
				Running:    src.isRunning(a),
			})
		}
	}
	sort.SliceStable(snap.Entries, func(i, j int) bool {
		a, b := snap.Entries[i], snap.Entries[j]
		if a.ProviderID != b.ProviderID {
			return a.ProviderID < b.ProviderID
		}
		return a.ModelID < b.ModelID
	})
	return snap
}
