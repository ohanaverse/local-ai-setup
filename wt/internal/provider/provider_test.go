package provider

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestTableInvariants checks the table agrees with itself: every provider's
// family exists, every provider in a family has a LiteLLM policy (a family
// member without one is never routed, the omlx-6bit bug), ids are unique, and
// the family facts are well formed. A new row that breaks one fails here
// before it reaches a user.
func TestTableInvariants(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range All() {
		if seen[s.ID] {
			t.Errorf("provider id %q appears twice", s.ID)
		}
		seen[s.ID] = true
		if s.Family != "" {
			if _, ok := FamilyByName(s.Family); !ok {
				t.Errorf("provider %q names family %q, which is not in the table", s.ID, s.Family)
			}
			if s.Policy == nil {
				t.Errorf("provider %q is in family %q but has no LiteLLM policy", s.ID, s.Family)
			}
		}
		if s.Seed != nil {
			if s.Seed.Location != "local" && s.Seed.Location != "cloud" {
				t.Errorf("provider %q seed location %q must be local or cloud", s.ID, s.Seed.Location)
			}
			if by := s.Seed.PathSuppressedBy; by != "" {
				if _, ok := Lookup(by); !ok {
					t.Errorf("provider %q is PATH-suppressed by %q, which is not in the table", s.ID, by)
				}
			}
		}
	}
	for _, f := range Families() {
		spec, _ := FamilyByName(f)
		if len(FamilyProviderIDs(f)) == 0 {
			t.Errorf("family %q has no providers", f)
		}
		if pr := spec.Process; pr != nil {
			if spec.Tenancy != Exclusive {
				t.Errorf("family %q has a Process but tenancy %v, want Exclusive", f, spec.Tenancy)
			}
			if pr.Binary == "" {
				t.Errorf("family %q process has no Binary", f)
			}
			// Without these the server wt spawns is never recognised (argv
			// match) or has no pidfile to be found by.
			if pr.PidfileDefault == "" || pr.Subcommand == "" || pr.PortFlag == "" {
				t.Errorf("family %q process needs PidfileDefault, Subcommand and PortFlag", f)
			}
			// {model} is optional (rec serves one fixed model); {port} is
			// required when wt hands the server a port.
			if spec.WtPort != 0 && !slices.Contains(pr.Spawn, "{port}") {
				t.Errorf("family %q has a WtPort but spawn %v has no {port}", f, pr.Spawn)
			}
			switch pr.Ready.Kind {
			case ReadyModelsList:
			case ReadyCommandJSON:
				if len(pr.Ready.Command) == 0 || pr.Ready.Path == "" {
					t.Errorf("family %q command-json ready needs Command and Path", f)
				}
			default:
				t.Errorf("family %q ready kind %q is not known", f, pr.Ready.Kind)
			}
		}
		for _, p := range scopeProblems(spec) {
			t.Errorf("family %q: %s", f, p)
		}
		if spec.DefaultOrigin == "" || strings.HasSuffix(spec.DefaultOrigin, "/") || strings.HasSuffix(spec.DefaultOrigin, "/v1") {
			t.Errorf("family %q default origin %q must be non-empty with no trailing / or /v1", f, spec.DefaultOrigin)
		}
	}
	if f := Families(); !slices.IsSorted(f) {
		t.Errorf("Families() = %v, want sorted", f)
	}
}

// scopeProblems is what is wrong with a family's Scope: a ScopeMachine
// family is one instance on the whole machine, so it holds one model at a
// time (Exclusive), and wt can find that instance on a port the registry
// does not name only through a readiness command (command-json).
func scopeProblems(f FamilySpec) []string {
	if f.Scope != ScopeMachine {
		return nil
	}
	var out []string
	if f.Tenancy != Exclusive {
		out = append(out, "scope is machine but tenancy is not Exclusive")
	}
	if f.Process == nil || f.Process.Ready.Kind != ReadyCommandJSON {
		out = append(out, "scope is machine but readiness is not a command-json check")
	}
	return out
}

// TestScopeProblems verifies the ScopeMachine rule TestTableInvariants
// applies, on synthetic rows: no table row is machine-scoped yet, so without
// this the rule would pass vacuously and could be wrong unnoticed until rec's
// row lands.
func TestScopeProblems(t *testing.T) {
	cmd := &ProcessSpec{Ready: Ready{Kind: ReadyCommandJSON, Command: []string{"status"}, Path: "ready", Equals: "true"}}
	list := &ProcessSpec{Ready: Ready{Kind: ReadyModelsList}}
	for name, tc := range map[string]struct {
		f    FamilySpec
		want int
	}{
		"port scope, pool":              {FamilySpec{Scope: ScopePort, Tenancy: Pool}, 0},
		"machine, exclusive, command":   {FamilySpec{Scope: ScopeMachine, Tenancy: Exclusive, Process: cmd}, 0},
		"machine, pool":                 {FamilySpec{Scope: ScopeMachine, Tenancy: Pool, Process: cmd}, 1},
		"machine, models list":          {FamilySpec{Scope: ScopeMachine, Tenancy: Exclusive, Process: list}, 1},
		"machine, shared and unmanaged": {FamilySpec{Scope: ScopeMachine, Tenancy: Shared}, 2},
	} {
		if got := scopeProblems(tc.f); len(got) != tc.want {
			t.Errorf("%s: scopeProblems = %q, want %d problems", name, got, tc.want)
		}
	}
}

// TestAccessorsMatchLegacyTables pins every accessor to the answer the tables
// it replaces gave (the characterization tests in config, localmodels and
// litellm hold the same values), so moving a caller onto the provider package
// cannot change what wt does.
func TestAccessorsMatchLegacyTables(t *testing.T) {
	families := map[string]string{
		"ollama": "ollama", "omlx": "omlx", "omlx-6bit": "omlx", "mtplx": "mtplx", "rec": "rec",
		"mlx_lm_server": "mlx_lm_server", "llamacpp": "", "openrouter": "", "agy": "", "": "",
	}
	for id, want := range families {
		if got := FamilyOf(id); got != want {
			t.Errorf("FamilyOf(%q) = %q, want %q", id, got, want)
		}
	}
	if got := FamilyOr("llamacpp"); got != "llamacpp" {
		t.Errorf("FamilyOr(llamacpp) = %q, want llamacpp", got)
	}
	if got := FamilyOr("omlx-6bit"); got != "omlx" {
		t.Errorf("FamilyOr(omlx-6bit) = %q, want omlx", got)
	}
	if got, want := Families(), []string{"mlx_lm_server", "mtplx", "ollama", "omlx", "rec"}; !slices.Equal(got, want) {
		t.Errorf("Families() = %v, want %v", got, want)
	}
	if got, want := FamilyProviderIDs("omlx"), []string{"omlx", "omlx-6bit"}; !slices.Equal(got, want) {
		t.Errorf("FamilyProviderIDs(omlx) = %v, want %v", got, want)
	}
	wantPolicies := map[string]Policy{
		"ollama":        {Prefix: "ollama_chat/"},
		"omlx":          {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"omlx-6bit":     {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"mlx_lm_server": {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"mtplx":         {Prefix: "openai/", APIKey: "not-needed", V1Base: true},
		"rec":           {Prefix: "openai/", SecretRef: true, V1Base: true},
		"llamacpp":      {Prefix: "openai/local-model", FixedModel: true, APIKey: "dummy-key"},
		"openrouter":    {Prefix: "openrouter/", SecretRef: true, Cloud: true},
	}
	if got := Policies(); !reflect.DeepEqual(got, wantPolicies) {
		t.Errorf("Policies() = %+v, want %+v", got, wantPolicies)
	}
	for id, want := range wantPolicies {
		if got, ok := PolicyFor(id); !ok || got != want {
			t.Errorf("PolicyFor(%q) = %+v, %v; want %+v, true", id, got, ok, want)
		}
	}
	for _, id := range []string{"agy", "claude", ""} {
		if _, ok := PolicyFor(id); ok {
			t.Errorf("PolicyFor(%q) found a policy, want none", id)
		}
	}
	if got, want := SeedableLocalIDs(), []string{"ollama", "omlx", "mlx_lm_server", "mtplx", "rec"}; !slices.Equal(got, want) {
		t.Errorf("SeedableLocalIDs() = %v, want %v", got, want)
	}
	if got, want := SeedableCloudIDs(), []string{"openrouter"}; !slices.Equal(got, want) {
		t.Errorf("SeedableCloudIDs() = %v, want %v", got, want)
	}
	if got, want := InstalledCommands(), map[string]string{"ollama": "ollama", "omlx": "omlx", "mtplx": "mtplx", "rec": "rec"}; !reflect.DeepEqual(got, want) {
		t.Errorf("InstalledCommands() = %v, want %v", got, want)
	}
	if got := PathSuppressedBy("omlx"); got != "omlx-6bit" {
		t.Errorf("PathSuppressedBy(omlx) = %q, want omlx-6bit", got)
	}
	if got := PathSuppressedBy("ollama"); got != "" {
		t.Errorf("PathSuppressedBy(ollama) = %q, want empty", got)
	}
	if got, want := ServedFamilies(), []string{"mlx_lm_server", "mtplx", "omlx", "rec"}; !slices.Equal(got, want) {
		t.Errorf("ServedFamilies() = %v, want %v", got, want)
	}
	for id, want := range map[string]int{"mtplx": 8003, "rec": 11500, "omlx": 0, "omlx-6bit": 0, "ollama": 0, "custom": 0, "": 0} {
		if got := WtPortFor(id); got != want {
			t.Errorf("WtPortFor(%q) = %d, want %d", id, got, want)
		}
	}
	flags := map[string][3]bool{ // running-only, routes-follow-artifact, served
		"ollama": {false, true, false}, "omlx": {false, false, true},
		"mtplx": {false, false, true}, "mlx_lm_server": {true, false, true},
		"rec": {false, false, true},
	}
	for f, w := range flags {
		spec, ok := FamilyByName(f)
		if !ok || spec.RunningOnly != w[0] || spec.RoutesFollowArtifact != w[1] || spec.Served != w[2] {
			t.Errorf("FamilyByName(%q) = %+v, %v; want running-only/routes-follow/served %v", f, spec, ok, w)
		}
	}
	tenancy := map[string]Tenancy{"ollama": Shared, "omlx": Pool, "mtplx": Exclusive, "rec": Exclusive, "mlx_lm_server": NoTenancy}
	for f, want := range tenancy {
		if spec, _ := FamilyByName(f); spec.Tenancy != want {
			t.Errorf("family %q tenancy = %d, want %d", f, spec.Tenancy, want)
		}
	}
	defaults := map[string][2]string{ // origin, dir
		"ollama": {"http://localhost:11434", ""}, "omlx": {"http://localhost:8000", "~/.omlx/models"},
		"mtplx": {"http://localhost:8003", "~/.mtplx/models"}, "mlx_lm_server": {"http://localhost:8001", ""},
		"rec": {"http://127.0.0.1:11500", ""},
	}
	for f, w := range defaults {
		if spec, _ := FamilyByName(f); spec.DefaultOrigin != w[0] || spec.DefaultModelDir != w[1] {
			t.Errorf("family %q origin/dir = %q/%q, want %q/%q", f, spec.DefaultOrigin, spec.DefaultModelDir, w[0], w[1])
		}
	}
}

// TestSeedRowsMatchLegacyLiterals pins the default registry rows to the
// literals registry_seed.go used to hold: they are written into users'
// registry.toml, so a changed key or value is a visible change.
func TestSeedRowsMatchLegacyLiterals(t *testing.T) {
	want := map[string]map[string]any{
		"ollama": {
			"id": "ollama", "name": "Ollama", "location": "local",
			"protocols": []string{"anthropic", "openai-chat"},
			"auth":      map[string]any{"type": "none", "base_url": "http://localhost:11434"},
		},
		"omlx": {
			"id": "omlx", "name": "oMLX", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8000"},
		},
		"mlx_lm_server": {
			"id": "mlx_lm_server", "name": "mlx-lm server (target+draft)", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8001/v1"},
		},
		"mtplx": {
			"id": "mtplx", "name": "MTPLX", "location": "local", "model_dir": "~/.mtplx/models",
			"auth": map[string]any{"type": "none", "base_url": "http://localhost:8003/v1"},
		},
		"rec": {
			"id": "rec", "name": "rec", "location": "local",
			"auth": map[string]any{"type": "none", "base_url": "http://127.0.0.1:11500/v1", "secret_ref": "exec:rec auth token"},
		},
		"openrouter": {
			"id": "openrouter", "name": "OpenRouter", "location": "cloud",
			"auth": map[string]any{"type": "api_key", "secret_ref": "OPENROUTER_API_KEY", "base_url": "https://openrouter.ai/api/v1"},
		},
	}
	for id, w := range want {
		if got := SeedRow(id); !reflect.DeepEqual(got, w) {
			t.Errorf("SeedRow(%q) = %#v, want %#v", id, got, w)
		}
	}
	for _, id := range []string{"omlx-6bit", "llamacpp", "bogus", ""} {
		if got := SeedRow(id); got != nil {
			t.Errorf("SeedRow(%q) = %#v, want nil", id, got)
		}
	}
}

// TestResultsAreCopies makes sure a caller that edits what an accessor returns
// cannot corrupt the table for the next caller; the table is process-wide state.
func TestResultsAreCopies(t *testing.T) {
	f := Families()
	f[0] = "mutated"
	if Families()[0] == "mutated" {
		t.Error("Families() returned the table's own slice")
	}
	ids := FamilyProviderIDs("omlx")
	ids[0] = "mutated"
	if FamilyProviderIDs("omlx")[0] == "mutated" {
		t.Error("FamilyProviderIDs returned the table's own slice")
	}
	all := All()
	all[0].ID = "mutated"
	if All()[0].ID == "mutated" {
		t.Error("All() returned the table's own slice")
	}
	pol := Policies()
	pol["omlx"] = Policy{Prefix: "mutated"}
	if p, _ := PolicyFor("omlx"); p.Prefix == "mutated" {
		t.Error("Policies() returned the table's own map")
	}
	cmds := InstalledCommands()
	cmds["ollama"] = "mutated"
	if InstalledCommands()["ollama"] == "mutated" {
		t.Error("InstalledCommands() returned the table's own map")
	}
	row := SeedRow("mtplx")
	row["name"] = "mutated"
	row["auth"].(map[string]any)["base_url"] = "mutated"
	if got := SeedRow("mtplx"); got["name"] == "mutated" || got["auth"].(map[string]any)["base_url"] == "mutated" {
		t.Error("SeedRow returned a shared map")
	}
	pf, _ := ProcessFor("mtplx")
	pf.Spawn[0] = "mutated"
	if again, _ := ProcessFor("mtplx"); again.Spawn[0] == "mutated" {
		t.Error("ProcessFor shares Spawn with the table")
	}
	fam, _ := FamilyByName("mtplx")
	fam.Process.Stop[0] = "mutated"
	if again, _ := FamilyByName("mtplx"); again.Process.Stop[0] == "mutated" {
		t.Error("FamilyByName shares Process.Stop with the table")
	}
	rf, _ := ProcessFor("rec")
	rf.Ready.Command[0] = "mutated"
	if again, _ := ProcessFor("rec"); again.Ready.Command[0] == "mutated" {
		t.Error("ProcessFor shares Ready.Command with the table")
	}
	spec, _ := Lookup("ollama")
	spec.Seed.Protocols[0] = "mutated"
	if again, _ := Lookup("ollama"); again.Seed.Protocols[0] == "mutated" {
		t.Error("Lookup shares a Seed's Protocols slice with the table")
	}
}

// TestProcessSpecMtplx pins every field of mtplx's process facts: the
// lifecycle engine spawns, stops and recognises the server from them, so a
// drifted flag would start or kill the wrong thing.
func TestProcessSpecMtplx(t *testing.T) {
	got, ok := ProcessFor("mtplx")
	if !ok {
		t.Fatal("ProcessFor(mtplx) absent")
	}
	want := ProcessSpec{
		Binary:         "mtplx",
		Spawn:          []string{"serve", "--model", "{model}", "--port", "{port}", "--host", "127.0.0.1", "--model-id", "{model}"},
		Stop:           []string{"stop", "--port", "{port}", "--grace-seconds", "10"},
		PidfileDefault: "/tmp/local-ai-setup-mtplx.pid",
		LogDefault:     "/tmp/local-ai-setup-mtplx.log",
		PidfileEnv:     "WT_MTPLX_PIDFILE",
		Subcommand:     "serve",
		DaemonModule:   "mtplx.server.openai",
		PortFlag:       "--port",
		ModelFlag:      "--model-id",
		Ready:          Ready{Kind: ReadyModelsList},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProcessFor(mtplx) = %+v, want %+v", got, want)
	}
	if f, _ := FamilyByName("mtplx"); f.Scope != ScopePort {
		t.Errorf("mtplx scope = %v, want ScopePort", f.Scope)
	}
}

// TestProcessFamilies checks only mtplx and rec have process facts: the other families
// are served by something wt does not spawn, and an engine that found a
// process for them would try to.
func TestProcessFamilies(t *testing.T) {
	if got := ProcessFamilies(); !reflect.DeepEqual(got, []string{"mtplx", "rec"}) {
		t.Errorf("ProcessFamilies() = %v, want [mtplx rec]", got)
	}
	for _, f := range []string{"omlx", "ollama", "mlx_lm_server", ""} {
		if _, ok := ProcessFor(f); ok {
			t.Errorf("ProcessFor(%q) present, want absent", f)
		}
	}
}

// TestRecRows pins every value of rec's table rows: the provider order, the
// process spec wt spawns and recognises it by, the family facts, the LiteLLM
// policy and the seed written into users' registries. A drifted value would
// start the wrong command, dial the wrong port or write a different row.
func TestRecRows(t *testing.T) {
	got, ok := ProcessFor("rec")
	if !ok {
		t.Fatal("ProcessFor(rec) absent")
	}
	want := ProcessSpec{
		Binary:         "rec",
		Spawn:          []string{"serve", "--port", "{port}"},
		Stop:           []string{"stop"},
		PidfileDefault: "/tmp/local-ai-setup-rec.pid",
		LogDefault:     "/tmp/local-ai-setup-rec.log",
		PidfileEnv:     "WT_REC_PIDFILE",
		Subcommand:     "serve",
		PortFlag:       "--port",
		Ready: Ready{
			Kind: ReadyCommandJSON, Command: []string{"status"}, Path: "ready", Equals: "true",
			ModelPath: "model", EndpointPath: "endpoint",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProcessFor(rec) = %+v, want %+v", got, want)
	}
	f, ok := FamilyByName("rec")
	if !ok {
		t.Fatal("family rec absent")
	}
	if f.Name != "rec" || f.Tenancy != Exclusive || f.Scope != ScopeMachine || f.WtPort != 11500 ||
		f.DefaultOrigin != "http://127.0.0.1:11500" || !f.Served || !f.ServedOnly || f.RunningOnly ||
		f.RoutesFollowArtifact || f.DefaultModelDir != "" {
		t.Errorf("family rec = %+v", f)
	}
	if p, ok := PolicyFor("rec"); !ok || p != (Policy{Prefix: "openai/", SecretRef: true, V1Base: true}) {
		t.Errorf("PolicyFor(rec) = %+v, %v", p, ok)
	}
	spec, ok := Lookup("rec")
	if !ok || spec.Family != "rec" || spec.Seed == nil {
		t.Fatalf("Lookup(rec) = %+v, %v", spec, ok)
	}
	wantSeed := Seed{
		Name: "rec", Location: "local", AuthType: "none", BaseURL: "http://127.0.0.1:11500/v1",
		SecretRef: "exec:rec auth token", Command: "rec",
	}
	if !reflect.DeepEqual(*spec.Seed, wantSeed) {
		t.Errorf("rec seed = %+v, want %+v", *spec.Seed, wantSeed)
	}
	var order []string
	for _, s := range All() {
		order = append(order, s.ID)
	}
	if w := []string{"ollama", "omlx", "omlx-6bit", "mlx_lm_server", "mtplx", "rec", "llamacpp", "openrouter"}; !slices.Equal(order, w) {
		t.Errorf("provider order = %v, want %v", order, w)
	}
}

// TestSeedRowRec pins the exact registry row wt writes for rec: it lands in
// users' registry.toml, so a changed key or value is a visible change (no
// protocols and no model_dir keys).
func TestSeedRowRec(t *testing.T) {
	want := map[string]any{
		"id": "rec", "name": "rec", "location": "local",
		"auth": map[string]any{"type": "none", "base_url": "http://127.0.0.1:11500/v1", "secret_ref": "exec:rec auth token"},
	}
	if got := SeedRow("rec"); !reflect.DeepEqual(got, want) {
		t.Errorf("SeedRow(rec) = %#v, want %#v", got, want)
	}
}

// TestServedOnlyFlags checks which families are served-only (probe is
// GET /v1/models alone, no artifacts listed) and which are running-only:
// mlx_lm_server is both, rec is served-only but wt can start it, and the
// flags must not leak onto the other families.
func TestServedOnlyFlags(t *testing.T) {
	for _, f := range Families() {
		spec, _ := FamilyByName(f)
		if want := f == "mlx_lm_server" || f == "rec"; spec.ServedOnly != want {
			t.Errorf("family %q ServedOnly = %v, want %v", f, spec.ServedOnly, want)
		}
		if want := f == "mlx_lm_server"; spec.RunningOnly != want {
			t.Errorf("family %q RunningOnly = %v, want %v", f, spec.RunningOnly, want)
		}
	}
}
