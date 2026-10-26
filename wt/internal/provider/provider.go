// Package provider is wt's one table of provider facts: which providers share
// a server (a family), how each routes through LiteLLM, which default registry
// row each is seeded with, and the per-family defaults and flags the inventory,
// lifecycle and commands read.
//
// It is a leaf: standard library only, so config, localmodels, litellm and
// lifecycle can all import it (config cannot import localmodels, which is why
// these tables used to be copied between packages and drifted). It holds facts,
// not behavior: probes, start/stop and name matching stay in the packages that
// own them and read their facts from here. The table is in table.go.
package provider

import (
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Tenancy is how a provider's server holds models, which decides what a start
// displaces, what a stop takes down, and which routes each removes.
type Tenancy int

const (
	// NoTenancy: wt has no lifecycle backend for the provider.
	NoTenancy Tenancy = iota
	// Exclusive: one model per process (mtplx). A start replaces the
	// occupant, a stop stops the process, and the family's routes go as one.
	Exclusive
	// Shared: models load beside each other and a pulled model is served on
	// request (ollama). Routes follow the artifact, not loaded state.
	Shared
	// Pool: several models loaded at once under a memory ceiling, evicted by
	// least recent use (omlx). A start loads beside and may evict; a stop
	// unloads one model; routes follow each model's loaded state.
	Pool
)

// Policy describes how one registry provider maps onto a LiteLLM row.
//   - Prefix     — LiteLLM `model` prefix (verbatim model string when FixedModel).
//   - APIKey     — literal api_key to write, "" to omit.
//   - SecretRef  — api_key comes from resolving the provider's
//     auth.secret_ref (config.ResolveSecret — env, exec:, or
//     literal) instead.
//   - Cloud      — the model lives remotely (`wt litellm providers` reports it).
//   - V1Base     — OpenAI-compatible server: api_base is the provider's
//     origin plus "/v1" whichever form the registry stores, because
//     LiteLLM's openai/ provider appends only /chat/completions (#168).
type Policy struct {
	Prefix     string
	FixedModel bool
	APIKey     string
	SecretRef  bool
	Cloud      bool
	V1Base     bool
}

// Seed is the default registry row wt adds for a provider (`wt model init`,
// `wt model add`), and what makes wt notice the provider is installed.
type Seed struct {
	Name      string
	Location  string   // "local" or "cloud"
	Protocols []string // omitted from the row when empty
	AuthType  string   // auth.type: "none" or "api_key"
	SecretRef string   // auth.secret_ref, omitted when empty
	BaseURL   string   // auth.base_url as the registry stores it (with /v1 where the provider has it)
	ModelDir  string   // omitted from the row when empty
	// Command is the binary whose presence on PATH makes wt seed the row;
	// empty means only a model reference or an agent listing seeds it.
	Command string
	// PathSuppressedBy names a provider whose existing row stops the PATH
	// trigger alone (a model reference or agent listing still seeds): omlx-6bit
	// is the same installed server as omlx.
	PathSuppressedBy string
}

// Spec is one registry provider id's row in the table.
type Spec struct {
	ID string
	// Family names the FamilySpec whose server this provider id talks to;
	// empty for providers outside every family (llamacpp, openrouter).
	Family string
	// Policy is the LiteLLM mapping; nil means the provider is never routed
	// through LiteLLM (native agents are not in the table at all).
	Policy *Policy
	// Seed is the default row; nil means wt adds none.
	Seed *Seed
}

// FamilySpec holds the facts shared by every provider id of one server.
type FamilySpec struct {
	Name string
	// DefaultOrigin is where the server listens when the registry names no
	// base_url, without a /v1 suffix.
	DefaultOrigin string
	// DefaultModelDir is the model directory when the registry names none.
	DefaultModelDir string
	// WtPort is the port wt hands the server when the registry's base_url
	// has none; 0 means the server owns its port.
	WtPort int
	// RunningOnly: wt cannot list what is on disk, only what is serving.
	RunningOnly bool
	// RoutesFollowArtifact: a pulled model is served on request, so its
	// route exists while the artifact does, not while it is loaded.
	RoutesFollowArtifact bool
	// Served: the server reports what it serves (`wt served`).
	Served bool
	// ServedOnly: the family's probe is GET /v1/models alone; wt lists no
	// artifacts for it.
	ServedOnly bool
	Tenancy    Tenancy
	// Scope is what one server of this family excludes: a port, or the machine.
	Scope Scope
	// Process says how wt spawns, stops and recognises the family's server;
	// nil when wt does not run it.
	Process *ProcessSpec
	// ServeArgs: a registry model of the family may carry `serve_args`, extra
	// argv appended to the spawn (ProcessSpec.SpawnArgs). Only mtplx does:
	// it has serve flags worth choosing per model (#432), while a family
	// that serves one fixed model has none wt knows of.
	ServeArgs bool
}

// Scope is the extent over which a family's servers exclude each other.
type Scope int

const (
	// ScopePort: one server per port.
	ScopePort Scope = iota
	// ScopeMachine: one server on the whole machine.
	ScopeMachine
)

// ReadyKind is how wt decides a spawned server is ready.
type ReadyKind string

const (
	// ReadyModelsList: the server's models list names the model.
	ReadyModelsList ReadyKind = "models-list"
	// ReadyCommandJSON: a command's JSON output holds the ready value.
	ReadyCommandJSON ReadyKind = "command-json"
)

// Ready is a family's readiness check.
type Ready struct {
	Kind ReadyKind
	// Command is the argv after the binary for command-json.
	Command []string
	// Path is a dot-separated JSON path whose scalar, rendered as text, must
	// equal Equals ("true" for a bool).
	Path   string
	Equals string
	// ModelPath is the JSON path of the model the instance serves.
	ModelPath string
	// EndpointPath is the JSON path of the instance's endpoint URL.
	EndpointPath string
}

// ProcessSpec is how wt runs a family's server as a process.
type ProcessSpec struct {
	Binary string
	// Spawn and Stop are argv templates after Binary with {model} and {port}
	// placeholders.
	Spawn, Stop                []string
	PidfileDefault, LogDefault string
	PidfileEnv                 string
	// Subcommand, DaemonModule and PortFlag say how the running server's argv
	// is recognised; ModelFlag is the argv flag whose value is the model name.
	Subcommand, DaemonModule, PortFlag, ModelFlag string
	Ready                                         Ready
}

// ExpandArgs is an argv template with {model} and {port} replaced in every
// element. The template itself is not changed.
func ExpandArgs(tmpl []string, model string, port int) []string {
	r := strings.NewReplacer("{model}", model, "{port}", strconv.Itoa(port))
	out := make([]string, len(tmpl))
	for i, a := range tmpl {
		out[i] = r.Replace(a)
	}
	return out
}

// SpawnArgs is the argv of the family's server after the binary: Spawn with
// {model} and {port} replaced, then extra (a model's serve_args) after wt's
// own arguments. It is the one place that argv is built, so a spawn and
// anything that prints or compares it cannot differ.
func (p ProcessSpec) SpawnArgs(model string, port int, extra []string) []string {
	return append(ExpandArgs(p.Spawn, model, port), extra...)
}

// OwnedFlags are the flags wt sets itself on a spawn, and so refuses in a
// model's serve_args: every `--flag` of Spawn (their values are not flags),
// and the port and model flags the spec names. wt's route, probes and stop
// path assume the port, host and model name it passed, and reads the model's
// name back from the model flag of a server still loading.
func (p ProcessSpec) OwnedFlags() []string {
	var out []string
	add := func(f string) {
		if strings.HasPrefix(f, "--") && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	for _, a := range p.Spawn {
		add(a)
	}
	add(p.PortFlag)
	add(p.ModelFlag)
	return out
}

func (p *ProcessSpec) clone() *ProcessSpec {
	if p == nil {
		return nil
	}
	c := *p
	c.Spawn = slices.Clone(p.Spawn)
	c.Stop = slices.Clone(p.Stop)
	c.Ready.Command = slices.Clone(p.Ready.Command)
	return &c
}

// ProcessFor returns a copy of a family's process facts; false when wt does
// not run the family.
func ProcessFor(family string) (ProcessSpec, bool) {
	if f, ok := families[family]; ok && f.Process != nil {
		return *f.Process.clone(), true
	}
	return ProcessSpec{}, false
}

// ProcessFamilies returns the families with process facts, sorted.
func ProcessFamilies() []string {
	var out []string
	for _, name := range Families() {
		if families[name].Process != nil {
			out = append(out, name)
		}
	}
	return out
}

func (s Spec) clone() Spec {
	if s.Policy != nil {
		p := *s.Policy
		s.Policy = &p
	}
	if s.Seed != nil {
		sd := *s.Seed
		sd.Protocols = slices.Clone(sd.Protocols)
		s.Seed = &sd
	}
	return s
}

// Lookup returns the table row for a provider id.
func Lookup(id string) (Spec, bool) {
	for _, s := range providers {
		if s.ID == id {
			return s.clone(), true
		}
	}
	return Spec{}, false
}

// All returns every provider row, in table order.
func All() []Spec {
	out := make([]Spec, len(providers))
	for i, s := range providers {
		out[i] = s.clone()
	}
	return out
}

// FamilyOf returns the family a provider id belongs to, "" for an id outside
// every family.
func FamilyOf(id string) string {
	for _, s := range providers {
		if s.ID == id {
			return s.Family
		}
	}
	return ""
}

// FamilyOr is FamilyOf, except an id outside every family is its own name.
func FamilyOr(id string) string {
	if f := FamilyOf(id); f != "" {
		return f
	}
	return id
}

// Families returns every family name, sorted.
func Families() []string {
	out := make([]string, 0, len(families))
	for name := range families {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// FamilyProviderIDs returns the provider ids of a family, in table order; nil
// for an unknown family.
func FamilyProviderIDs(family string) []string {
	var out []string
	for _, s := range providers {
		if s.Family == family {
			out = append(out, s.ID)
		}
	}
	return out
}

// FamilyByName returns a family's facts.
func FamilyByName(name string) (FamilySpec, bool) {
	f, ok := families[name]
	f.Process = f.Process.clone()
	return f, ok
}

// PolicyFor returns a provider's LiteLLM mapping; false for natives and
// unknown ids.
func PolicyFor(id string) (Policy, bool) {
	for _, s := range providers {
		if s.ID == id && s.Policy != nil {
			return *s.Policy, true
		}
	}
	return Policy{}, false
}

// Policies returns every provider's LiteLLM mapping, keyed by provider id.
func Policies() map[string]Policy {
	out := make(map[string]Policy, len(providers))
	for _, s := range providers {
		if s.Policy != nil {
			out[s.ID] = *s.Policy
		}
	}
	return out
}

func seedableIDs(location string) []string {
	var out []string
	for _, s := range providers {
		if s.Seed != nil && s.Seed.Location == location {
			out = append(out, s.ID)
		}
	}
	return out
}

// SeedableLocalIDs returns the local providers wt can seed a default row for,
// in table order.
func SeedableLocalIDs() []string { return seedableIDs("local") }

// SeedableCloudIDs returns the cloud providers wt can seed a default row for.
func SeedableCloudIDs() []string { return seedableIDs("cloud") }

// SeedRow returns a fresh default registry row for a provider id (the shape
// config.RegistryDoc takes), nil when wt seeds none.
func SeedRow(id string) map[string]any {
	for _, s := range providers {
		if s.ID != id || s.Seed == nil {
			continue
		}
		sd := s.Seed
		auth := map[string]any{"type": sd.AuthType, "base_url": sd.BaseURL}
		if sd.SecretRef != "" {
			auth["secret_ref"] = sd.SecretRef
		}
		row := map[string]any{"id": s.ID, "name": sd.Name, "location": sd.Location, "auth": auth}
		if len(sd.Protocols) > 0 {
			row["protocols"] = slices.Clone(sd.Protocols)
		}
		if sd.ModelDir != "" {
			row["model_dir"] = sd.ModelDir
		}
		return row
	}
	return nil
}

// InstalledCommands returns provider id -> the binary whose presence on PATH
// makes wt seed that provider's row.
func InstalledCommands() map[string]string {
	out := map[string]string{}
	for _, s := range providers {
		if s.Seed != nil && s.Seed.Command != "" {
			out[s.ID] = s.Seed.Command
		}
	}
	return out
}

// PathSuppressedBy returns the provider id whose existing row stops id's PATH
// trigger, "" if none.
func PathSuppressedBy(id string) string {
	for _, s := range providers {
		if s.ID == id && s.Seed != nil {
			return s.Seed.PathSuppressedBy
		}
	}
	return ""
}

// WtPortFor returns the port wt hands the server of a provider id when its
// registry base_url names none; 0 when the server owns its port or the id is
// unknown.
func WtPortFor(providerID string) int {
	if f, ok := families[FamilyOf(providerID)]; ok {
		return f.WtPort
	}
	return 0
}

// ServedFamilies returns the families whose server reports what it serves,
// sorted.
func ServedFamilies() []string {
	var out []string
	for _, name := range Families() {
		if families[name].Served {
			out = append(out, name)
		}
	}
	return out
}

// AcceptsServeArgs reports whether a registry model of the family may carry
// serve_args (FamilySpec.ServeArgs).
func AcceptsServeArgs(family string) bool {
	f, ok := families[family]
	return ok && f.ServeArgs
}
