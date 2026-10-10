package localmodels

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Status is one provider family's probe outcome.
type Status string

const (
	// StatusOK means discovery succeeded AND the family's live probe answered
	// (mlx_lm_server has no discovery: its /v1/models answered).
	// A stopped omlx/mtplx server therefore does NOT report ok: its failed
	// /v1/models is partial, whether the cause is a dead server or a bad answer.
	StatusOK Status = "ok"
	// StatusPartial means discovery succeeded but live running-state could
	// not be determined (ollama: /api/tags ok, /api/ps failed; omlx/mtplx:
	// the model-dir scan succeeded, /v1/models failed; mlx_lm_server, which
	// has no discovery, only running state: /v1/models failed, or it
	// answered but no registered pairing of several matches — see
	// Snapshot.Ambiguous). Entries' Running flags are not trustworthy for
	// this family.
	StatusPartial Status = "partial"
	// StatusUnreachable means discovery itself failed: for ollama, /api/tags
	// failed (daemon down); for omlx/mtplx, the model-directory scan (or
	// config.ExpandHome) failed. It is NOT a server-liveness signal.
	StatusUnreachable Status = "unreachable"
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

// ModelDir is the expanded model directory the probe scans for a family: the
// first of the family's provider rows that names a model_dir, else the family
// default. An omlx-6bit-only registry still resolves (same server). Callers
// that need to agree with the probe use this rather than re-deriving it.
func ModelDir(cfg *config.Config, family string) (string, error) {
	setting := defaultOmlxDir
	if family == "mtplx" {
		setting = defaultMtplxDir
	}
	for _, id := range familyProviderIDs(family) {
		if p := cfg.ProviderByID(id); p != nil && p.ModelDir != "" {
			setting = p.ModelDir
			break
		}
	}
	return config.ExpandHome(setting)
}

// Entry is one local model: registered (Registered) or discovered.
type Entry struct {
	ProviderID string // registry provider id (omlx-6bit rows keep theirs); for discovered entries the family's registry provider id (familyProviderID), so the id resolves in the registry
	Artifact   string // pulled/on-disk name in the provider's spelling; "" for a registered model not found on disk
	// ModelID: the registry id when registered, else the FAMILY-prefixed
	// config.DiscoveredModelID(family, Artifact) — the id `wt litellm sync`,
	// the lifecycle route hook, -M, usage and the picker all key on, so it
	// must not depend on which provider row of the family the registry
	// happens to define (litellm.DiscoveredModel derives it the same way).
	ModelID    string
	ModelName  string // provider-side name: the registry model_name when registered, else the artifact name
	Registered bool
	Running    bool // serving right now (live probe only)
	// Loading: omlx reports the model mid-load, not loaded yet (#259). Running
	// is true as well — a loading model occupies the pool, keeps its route and
	// is still offered by `wt stop` — but it cannot answer a request yet, so a
	// start waits for it and the pickers do not offer it for launch. Only
	// omlx's status reading can say so: it is false for every other family, and
	// for an omlx that answered through the fallback reading (Pool.SizesKnown
	// false).
	Loading bool
	// ArtifactKnown reports whether the probe actually determined this entry's
	// artifact presence. False means unknown, NOT missing: the family's
	// discovery failed (StatusUnreachable, so artifacts was never populated) or
	// the family cannot enumerate artifacts at all (mlx_lm_server). A consumer
	// must not read a false ArtifactKnown plus an empty Artifact as "the model
	// isn't there" — a transient probe failure would then hide a model that is
	// pulled and launchable.
	ArtifactKnown bool
	// Path is where the artifact is on disk: the model's directory for omlx
	// and mtplx. For a registered entry it is that model's OWN directory or
	// "", for every reader: the match is by leaf name, and a directory
	// reached only through another organization's copy of the name is
	// withheld here (ownDirectory, #266), so a caller may print it as "this
	// model's weights" without a check of its own. A discovered entry keeps
	// the directory the scan found it in. "" too when the probe does not
	// learn a path: ollama keeps its models in a blob store with no per-model
	// path, an mlx_lm_server pairing is never enumerated, a model that is not
	// on disk has none, and neither has an omlx name found in more than one
	// directory. An empty Path says nothing about presence — Artifact does.
	Path string
	// Size is the artifact's size in bytes when the probe's own answer
	// carries it: ollama's /api/tags. 0 means not known, never "empty": no
	// directory is walked to fill it.
	Size int64
}

// Snapshot is one inventory round. Providers is keyed by provider family
// (ollama, omlx — which also covers omlx-6bit — mtplx, mlx_lm_server).
type Snapshot struct {
	Entries   []Entry
	Providers map[string]Status
	// Down marks families whose server actively refused the probe connection:
	// unlike a timeout or a bad answer, that positively means nothing is
	// serving, so those families' Running flags (all false) can be trusted
	// even though their Status is not StatusOK.
	Down map[string]bool
	// Ambiguous marks families whose server answered but whose running
	// model could not be identified: mlx_lm_server with two or more
	// registered pairings, none of which name-matches a served id. Their
	// Status is StatusPartial; this only lets callers say why.
	Ambiguous map[string]bool
	// ProbeFailures carries a probed family's probe error beside its
	// non-ok status, so a caller can show the reason rather than only the
	// status: the status says "partial", while the error text names the
	// repair — omlx's "set auth.secret_ref ..." hint lives in the error
	// alone.
	ProbeFailures map[string]error
	// OmlxPool is the omlx pool reading this round's Running flags came
	// from: sizes, pins and the ceiling the eviction plan needs (package
	// lifecycle, evictions.go). Nil when omlx was not probed or gave no reading.
	OmlxPool *Pool
}

// Inventory probes every local provider in the registry concurrently and
// merges discovered artifacts with the registry's local models.
func Inventory(cfg *config.Config) Snapshot {
	return inventory(cfg, &http.Client{Timeout: probeTimeout})
}

// familyIDs is the ONE table of probe families and the registry provider ids
// that belong to each; familyOf, Families and familyProviderIDs are all
// derived from it, so a new family (or a second provider id sharing one, as
// omlx-6bit shares omlx's single server) is added in exactly one place and can
// never be visible to one accessor and not another.
var familyIDs = map[string][]string{
	"mlx_lm_server": {"mlx_lm_server"},
	"mtplx":         {"mtplx"},
	"ollama":        {"ollama"},
	"omlx":          {"omlx", "omlx-6bit"},
}

// familyOf maps a registry provider id to its probe family; "" when wt has no
// probe for it (e.g. retired llamacpp). omlx and omlx-6bit are ONE physical
// server, so they share the "omlx" family.
func familyOf(providerID string) string {
	for family, ids := range familyIDs {
		for _, id := range ids {
			if id == providerID {
				return family
			}
		}
	}
	return ""
}

// Families lists every probe family wt knows, sorted.
func Families() []string {
	out := make([]string, 0, len(familyIDs))
	for f := range familyIDs {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Family maps a registry provider id to its probe family ("omlx-6bit" shares
// "omlx"); "" when wt has no probe for it.
func Family(providerID string) string { return familyOf(providerID) }

// RunningOnly reports whether a provider's family is probed for running state
// only: wt can never enumerate what it has on disk, so a stopped model of that
// family is indistinguishable from one that does not exist. True only for
// mlx_lm_server (one target+draft pairing per process, and the served name is
// not reconstructable). It is the one place that names such a family —
// knowsArtifacts and internal/catalog's row rules both ask it, so adding a
// second running-only family is an edit here alone.
func RunningOnly(providerID string) bool { return familyOf(providerID) == "mlx_lm_server" }

// RoutesFollowArtifact reports whether a family's LiteLLM routes follow
// artifact presence rather than running state. True only for ollama: it
// lazy-loads a model on request (and unloads idle ones), so a pulled model is
// servable whether or not it is loaded. family is a Family value.
func RoutesFollowArtifact(family string) bool { return family == "ollama" }

// source is one family's probe result.
type source struct {
	family     string
	status     Status
	artifacts  []string          // discovered names, provider spelling (mtplx: repo id form)
	loaded     []string          // names serving right now
	dir        string            // the model directory that was scanned (omlx, mtplx)
	paths      map[string]string // artifact -> its directory (omlx, mtplx)
	sizes      map[string]int64  // artifact -> bytes (ollama)
	registered int               // local registry models in this family
	down       bool              // the server refused the connection (nothing listening)
	probeErr   error             // the probe failure, for a caller that shows the reason
	pool       *Pool             // omlx only: the reading loaded came from
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

// ownDirectory reports whether the directory matched to a registered model
// is that model's own. matchArtifact goes by leaf name, so a model registered
// as org-a/Name can be matched to org-b/Name; a path wt prints is a path
// someone may delete, and that one would send the reader of `wt model rm` to
// another organization's weights (closed issue #266). The check sits here, at
// the match, so that Entry.Path is safe for every consumer rather than for
// the ones that remember to ask.
//
// The organization the row claims is its fetch.repo's, or its model_name's
// when that is written org/name; a row that names none (a repo or a name with
// no "/") has nothing to contradict the match and keeps the path. One that
// names an organization keeps it only when the directory does not belong to
// another:
//
//   - mtplx keeps every model directly in its model directory, named
//     <org>--<name>, so the directory's own name has to start with the
//     organization (one with no "--" in it names none and is kept);
//   - omlx: the directory sits directly in the scanned model directory, or in
//     a folder named after the organization.
func (s *source) ownDirectory(m config.Model, path string) bool {
	if path == "" {
		return false
	}
	org, _, ok := strings.Cut(m.Fetch.Repo, "/")
	if m.Fetch.Repo == "" {
		org, _, ok = strings.Cut(m.ModelName, "/")
	}
	if !ok || org == "" {
		return true
	}
	if s.family == "mtplx" {
		base := filepath.Base(path)
		return !strings.Contains(base, "--") || strings.HasPrefix(base, org+"--")
	}
	parent := filepath.Dir(path)
	return filepath.Base(parent) == org || filepath.Clean(s.dir) == filepath.Clean(parent)
}

// knowsArtifacts reports whether this family's probe could enumerate what is
// pulled or on disk. False for a running-only family (RunningOnly) and for a
// probe that failed outright (StatusUnreachable), in which case artifacts was
// never populated — so an empty Artifact carries no information either way.
func (s *source) knowsArtifacts() bool {
	return !RunningOnly(s.family) && s.status != StatusUnreachable
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

// isLoading reports whether the model name denotes is mid-load: the omlx pool
// lists it loading and not loaded. It asks Pool.Find, so the name resolves to
// the same pool model a load or unload of it would act on. The fallback pool
// reading marks every model it names loaded, and no other family has a pool,
// so both answer false.
func (s *source) isLoading(name string) bool {
	if s.pool == nil {
		return false
	}
	m, ok := s.pool.Find(name)
	return ok && m.Loading && !m.Loaded
}

// familyOrigin is the probe origin for a family: the first registry provider
// row of the family with an auth.base_url, else the default port.
func familyOrigin(cfg *config.Config, family string) string {
	o, _ := FamilyOrigin(cfg, family)
	return o
}

// familyProviderIDs lists the registry provider ids that belong to a family.
func familyProviderIDs(family string) []string { return familyIDs[family] }

// FamilyProviderIDs is familyProviderIDs for other packages, as a copy: every
// registry provider id that names the family's one server ("omlx" and
// "omlx-6bit" for omlx). A caller that must reach every registry row of a
// model the server holds asks under each of them.
func FamilyProviderIDs(family string) []string {
	return append([]string(nil), familyProviderIDs(family)...)
}

// familyProviderID is the provider id a family's DISCOVERED entries are named
// after: the first provider row of the family the registry actually defines
// ("omlx-6bit" on a registry with no plain "omlx" row), falling back to the
// family name itself. A discovered entry's provider id must resolve in the
// registry — config.ResolveRoute and litellm.prepareModel both reject an
// unknown provider — and the family name is only a provider id by convention.
func familyProviderID(cfg *config.Config, family string) string {
	for _, id := range familyProviderIDs(family) {
		if cfg.ProviderByID(id) != nil {
			return id
		}
	}
	return family
}

// FamilyOrigin is the probe origin for a family and whether it came from the
// registry (the first provider row of the family with an auth.base_url) rather
// than the default port. The inventory probe and internal/lifecycle's
// start/stop flows both resolve origins through it, so they always describe
// the same server. The registry's value is read through config.Provider.Origin,
// as a route's api_base and a direct route are: for an mtplx base_url with no
// port on this machine that is the port `wt start` serves it on, not port 80
// (#348).
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
			return p.Origin(), true
		}
	}
	return def, false
}

// FamilyOriginPort is FamilyOrigin with the URL's port resolved, for callers
// that must hand a numeric port to a command line as well as dial the origin
// (mtplx's start and stop). The origin it returns is FamilyOrigin's whenever
// that names a port, which it does for the default of every family and for
// mtplx on this machine.
//
// An origin with no port has one answer, and only for mtplx: an mtplx
// base_url that is https or names a host config.Provider.Origin does not
// take for this machine gets config.MtplxPort, applied to the origin itself
// and not only to the returned number, so the two never describe different
// servers. That origin is then not FamilyOrigin's, which keeps the url's
// implicit port: wt serves mtplx on 127.0.0.1 only, so a start against such
// a url works only where the host is this machine under a name Origin does
// not recognise (an /etc/hosts alias), and what it dials is left as it was
// rather than moved onto a port the user did not write. For every other
// family it is an error: wt hands those servers no port, so a port put on
// the origin here would be an address nothing else reads (#348).
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
	if family != "mtplx" {
		return "", 0, fmt.Errorf("origin %q has no port, and wt does not choose the port of family %q", origin, family)
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(config.MtplxPort))
	return u.String(), config.MtplxPort, nil
}

// refused reports whether err is a refused TCP connection: the port has no
// listener, so the server is down (as opposed to slow or answering badly).
func refused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

func probeFamily(cfg *config.Config, client *http.Client, family string) *source {
	s := &source{family: family, status: StatusOK}
	origin := familyOrigin(cfg, family)
	switch family {
	case "ollama":
		models, err := ollamaModels(context.Background(), client, origin+"/api/tags")
		if err != nil {
			s.status = StatusUnreachable
			s.down = refused(err)
			return s
		}
		s.sizes = map[string]int64{}
		for _, m := range models {
			s.artifacts = append(s.artifacts, m.Name)
			s.sizes[m.Name] = m.Size
		}
		// Same endpoint construction as the lifecycle re-probe's OllamaLoaded,
		// so the two always describe the same server.
		if loaded, err := OllamaLoaded(context.Background(), client, origin); err == nil {
			s.loaded = loaded
		} else {
			s.status = StatusPartial
			// /api/tags answered above, so a refused /api/ps means the daemon
			// died between the two probes on the same origin: nothing is
			// listening and its routes are stale. Without this, sync would
			// treat ollama as merely untrustworthy and keep them.
			s.down = refused(err)
		}
	case "omlx", "mtplx":
		// What the server is serving comes from ServedIDs, which for omlx is
		// the models it has loaded, not everything /v1/models lists (#201).
		// A failed probe must not read as "nothing is loaded": for a
		// single-model family that turns an unanswerable probe into permission to
		// replace a model that may well be serving. StatusPartial records the same
		// "Running flags are not trustworthy" state ollama already uses for a
		// failed /api/ps.
		var loaded []string
		var err error
		if family == "omlx" {
			var p Pool
			if p, err = OmlxPool(cfg, client); err == nil {
				s.pool = &p
				loaded = p.LoadedIDs()
			}
		} else {
			loaded, err = ServedIDs(cfg, client, family)
		}
		if err != nil {
			s.status = StatusPartial
			s.down = refused(err)
			s.probeErr = err
		}
		s.loaded = loaded
		dir, err := ModelDir(cfg, family)
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		// omlx discovers models two levels deep; mtplx's directory is flat.
		var names []string
		var paths map[string]string
		if family == "mtplx" {
			var dirs []string
			dirs, err = scanModelDirs(dir)
			paths = map[string]string{}
			for _, n := range dirs {
				names = append(names, mtplxRepoID(n))
				paths[mtplxRepoID(n)] = filepath.Join(dir, n)
			}
		} else {
			names, paths, err = scanOmlxModelPaths(dir)
		}
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		s.dir, s.artifacts, s.paths = dir, names, paths
	case "mlx_lm_server":
		// Running state only: a target+draft pairing cannot be enumerated
		// (knowsArtifacts stays false), but /v1/models still says whether the
		// server is serving, with the same trust rules as omlx/mtplx — an
		// answer is OK, a refused connection is Down (nothing listening), any
		// other failure is Partial (Running untrustworthy).
		loaded, err := FetchModelIDsErr(client, origin+"/v1/models")
		if err != nil {
			s.status = StatusPartial
			s.down = refused(err)
		}
		s.loaded = loaded
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

	snap := Snapshot{
		Providers:     map[string]Status{},
		Down:          map[string]bool{},
		Ambiguous:     map[string]bool{},
		ProbeFailures: map[string]error{},
	}
	sources := map[string]*source{}
	for i, f := range families {
		sources[f] = results[i]
		results[i].registered = registered[f]
		snap.Providers[f] = results[i].status
		if results[i].down {
			snap.Down[f] = true
		}
		if results[i].probeErr != nil {
			snap.ProbeFailures[f] = results[i].probeErr
		}
		if results[i].pool != nil {
			snap.OmlxPool = results[i].pool
		}
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
					e.Size = src.sizes[a]
					if p := src.paths[a]; src.ownDirectory(m, p) {
						e.Path = p
					}
					break
				}
			}
			name := e.Artifact
			if name == "" {
				name = m.ModelName
			}
			e.Running = src.isRunning(name)
			e.Loading = e.Running && src.isLoading(name)
		}
		snap.Entries = append(snap.Entries, e)
	}
	// mlx_lm_server's /v1/models lists HF repo ids and resolved paths, never a
	// "<target>+draft-<draft>" pairing name, so with several registered
	// pairings and no name match wt cannot tell which one is serving. OK would
	// let sync remove the serving pairing's route; Partial leaves the family's
	// routes alone. An EMPTY answer counts as unattributable too, not as
	// "nothing is running": a live server lists the pairing it just loaded, so
	// empty means a foreign listener on the port or one still loading, and OK
	// there made sync drop every registered pairing's route with no warning
	// (only a non-OK family reports "routes left unchanged").
	if src := sources["mlx_lm_server"]; src != nil && src.status == StatusOK &&
		src.registered >= 2 {
		matched := false
		for _, e := range snap.Entries {
			if e.Running && familyOf(e.ProviderID) == "mlx_lm_server" {
				matched = true
				break
			}
		}
		if !matched {
			src.status = StatusPartial
			snap.Providers["mlx_lm_server"] = StatusPartial
			snap.Ambiguous["mlx_lm_server"] = true
		}
	}
	for _, f := range families {
		src := sources[f]
		for _, a := range src.artifacts {
			if consumed[f+"\x00"+a] {
				continue
			}
			running := src.isRunning(a)
			snap.Entries = append(snap.Entries, Entry{
				ProviderID:    familyProviderID(cfg, f),
				Artifact:      a,
				ModelID:       config.DiscoveredModelID(f, a),
				ModelName:     a,
				Running:       running,
				Loading:       running && src.isLoading(a),
				ArtifactKnown: true,
				Path:          src.paths[a],
				Size:          src.sizes[a],
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
