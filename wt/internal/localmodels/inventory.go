package localmodels

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Status is one provider family's probe outcome.
type Status string

const (
	// StatusOK means discovery succeeded AND the family's live probe answered.
	// A stopped omlx/mtplx server therefore does NOT report ok: its failed
	// /v1/models is partial, whether the cause is a dead server or a bad answer.
	StatusOK Status = "ok"
	// StatusPartial means discovery succeeded but live running-state could
	// not be determined (ollama: /api/tags ok, /api/ps failed; omlx/mtplx:
	// the model-dir scan succeeded, /v1/models failed). Entries' Running
	// flags are not trustworthy for this family.
	StatusPartial Status = "partial"
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
	ModelName  string // provider-side name: the registry model_name when registered, else the artifact name
	Registered bool
	Running    bool // serving right now (live probe only)
	// ArtifactKnown reports whether the probe actually determined this entry's
	// artifact presence. False means unknown, NOT missing: the family's
	// discovery failed (StatusUnreachable, so artifacts was never populated) or
	// the family cannot enumerate artifacts at all (mlx_lm_server). A consumer
	// must not read a false ArtifactKnown plus an empty Artifact as "the model
	// isn't there" — a transient probe failure would then hide a model that is
	// pulled and launchable.
	ArtifactKnown bool
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

// Family maps a registry provider id to its probe family ("omlx-6bit" shares
// "omlx"); "" when wt has no probe for it.
func Family(providerID string) string { return familyOf(providerID) }

// source is one family's probe result.
type source struct {
	family     string
	status     Status
	artifacts  []string // discovered names, provider spelling (mtplx: repo id form)
	loaded     []string // names serving right now
	registered int      // local registry models in this family
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

// knowsArtifacts reports whether this family's probe could enumerate what is
// pulled or on disk. False for mlx_lm_server (one target+draft pairing per
// process, and the served name is not reconstructable) and for a probe that
// failed outright (StatusUnreachable), in which case artifacts was never
// populated — so an empty Artifact carries no information either way.
func (s *source) knowsArtifacts() bool {
	return s.family != "mlx_lm_server" && s.status != StatusUnreachable
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
		// One target+draft pairing per process. The served name is the
		// target string the server was started with, which wt often cannot
		// reconstruct, so try a name match first; failing that, a non-empty
		// /v1/models identifies the model only if it is the family's sole
		// registered one. With several registered pairings we cannot tell
		// which is serving, so none is reported Running.
		for _, l := range s.loaded {
			if NameMatches(l, name) {
				return true
			}
		}
		return len(s.loaded) > 0 && s.registered == 1
	}
	return false
}

// familyOrigin is the probe origin for a family: the first registry provider
// row of the family with an auth.base_url, else the default port.
func familyOrigin(cfg *config.Config, family string) string {
	o, _ := FamilyOrigin(cfg, family)
	return o
}

// familyProviderIDs lists the registry provider ids that belong to a family.
func familyProviderIDs(family string) []string {
	if family == "omlx" {
		return []string{"omlx", "omlx-6bit"}
	}
	return []string{family}
}

// FamilyOrigin is the probe origin for a family and whether it came from the
// registry (the first provider row of the family with an auth.base_url) rather
// than the default port. The inventory probe and internal/lifecycle's
// start/stop flows both resolve origins through it, so they always describe
// the same server.
func FamilyOrigin(cfg *config.Config, family string) (origin string, fromRegistry bool) {
	def := ""
	switch family {
	case "ollama":
		def = config.OllamaBaseURL
	case "omlx":
		def = defaultOmlxOrigin
	case "mtplx":
		def = defaultMtplxOrigin
	case "mlx_lm_server":
		def = defaultMlxLMOrigin
	}
	for _, id := range familyProviderIDs(family) {
		if p := cfg.ProviderByID(id); p != nil && p.Auth.BaseURL != "" {
			return config.BaseOrigin(p.Auth.BaseURL), true
		}
	}
	return def, false
}

// FamilyOriginPort is FamilyOrigin with the URL's port resolved, for callers
// that must hand a numeric port to a command line as well as dial the origin.
// When the origin carries no port the family default is applied to the origin
// itself, not only to the returned number, so the two can never describe
// different servers.
func FamilyOriginPort(cfg *config.Config, family string) (string, int, error) {
	origin, _ := FamilyOrigin(cfg, family)
	u, err := url.Parse(origin)
	if err != nil {
		return "", 0, fmt.Errorf("origin %q: %w", origin, err)
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fmt.Errorf("origin %q: bad port %q", origin, p)
		}
		return origin, n, nil
	}
	def, ok := defaultPortFor(family)
	if !ok {
		return "", 0, fmt.Errorf("origin %q has no port and family %q has no default", origin, family)
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(def))
	return u.String(), def, nil
}

// defaultPortFor is the port a family's default origin uses, so a registry base
// url that omits a port resolves to the same server the defaults describe.
func defaultPortFor(family string) (int, bool) {
	switch family {
	case "ollama":
		return 11434, true
	case "omlx":
		return 8000, true
	case "mtplx":
		return 8003, true
	case "mlx_lm_server":
		return 8001, true
	}
	return 0, false
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
		} else {
			s.status = StatusPartial
		}
	case "omlx", "mtplx":
		// A failed /v1/models must not read as "nothing is loaded": for a
		// single-model family that turns an unanswerable probe into permission to
		// replace a model that may well be serving. StatusPartial records the same
		// "Running flags are not trustworthy" state ollama already uses for a
		// failed /api/ps.
		loaded, err := FetchModelIDsErr(client, origin+"/v1/models")
		if err != nil {
			s.status = StatusPartial
		}
		s.loaded = loaded
		def := defaultOmlxDir
		if family == "mtplx" {
			def = defaultMtplxDir
		}
		// A family with only an omlx-6bit row still scans (same server).
		dirSetting := def
		for _, id := range familyProviderIDs(family) {
			if p := cfg.ProviderByID(id); p != nil && p.ModelDir != "" {
				dirSetting = p.ModelDir
				break
			}
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
	// A model can be local through its own location override while its
	// provider row is not; its family still needs a probe.
	registered := map[string]int{} // local registered models per family
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		f := familyOf(m.ProviderID)
		if f == "" {
			continue
		}
		registered[f]++
		if !seen[f] {
			seen[f] = true
			families = append(families, f)
		}
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
		results[i].registered = registered[f]
		snap.Providers[f] = results[i].status
	}

	consumed := map[string]bool{}
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		e := Entry{ProviderID: m.ProviderID, ModelID: m.ID, ModelName: m.ModelName, Registered: true}
		if src := sources[familyOf(m.ProviderID)]; src != nil {
			e.ArtifactKnown = src.knowsArtifacts()
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
				ProviderID:    f,
				Artifact:      a,
				ModelID:       config.DiscoveredModelID(f, a),
				ModelName:     a,
				Running:       src.isRunning(a),
				ArtifactKnown: true,
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
