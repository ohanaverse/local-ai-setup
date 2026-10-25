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
		if spec.DefaultOrigin == "" || strings.HasSuffix(spec.DefaultOrigin, "/") || strings.HasSuffix(spec.DefaultOrigin, "/v1") {
			t.Errorf("family %q default origin %q must be non-empty with no trailing / or /v1", f, spec.DefaultOrigin)
		}
	}
	if f := Families(); !slices.IsSorted(f) {
		t.Errorf("Families() = %v, want sorted", f)
	}
}

// TestAccessorsMatchLegacyTables pins every accessor to the answer the tables
// it replaces gave (the characterization tests in config, localmodels and
// litellm hold the same values), so moving a caller onto the provider package
// cannot change what wt does.
func TestAccessorsMatchLegacyTables(t *testing.T) {
	families := map[string]string{
		"ollama": "ollama", "omlx": "omlx", "omlx-6bit": "omlx", "mtplx": "mtplx",
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
	if got, want := Families(), []string{"mlx_lm_server", "mtplx", "ollama", "omlx"}; !slices.Equal(got, want) {
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
	if got, want := SeedableLocalIDs(), []string{"ollama", "omlx", "mlx_lm_server", "mtplx"}; !slices.Equal(got, want) {
		t.Errorf("SeedableLocalIDs() = %v, want %v", got, want)
	}
	if got, want := SeedableCloudIDs(), []string{"openrouter"}; !slices.Equal(got, want) {
		t.Errorf("SeedableCloudIDs() = %v, want %v", got, want)
	}
	if got, want := InstalledCommands(), map[string]string{"ollama": "ollama", "omlx": "omlx", "mtplx": "mtplx"}; !reflect.DeepEqual(got, want) {
		t.Errorf("InstalledCommands() = %v, want %v", got, want)
	}
	if got := PathSuppressedBy("omlx"); got != "omlx-6bit" {
		t.Errorf("PathSuppressedBy(omlx) = %q, want omlx-6bit", got)
	}
	if got := PathSuppressedBy("ollama"); got != "" {
		t.Errorf("PathSuppressedBy(ollama) = %q, want empty", got)
	}
	if got, want := ServedFamilies(), []string{"mlx_lm_server", "mtplx", "omlx"}; !slices.Equal(got, want) {
		t.Errorf("ServedFamilies() = %v, want %v", got, want)
	}
	for id, want := range map[string]int{"mtplx": 8003, "omlx": 0, "omlx-6bit": 0, "ollama": 0, "custom": 0, "": 0} {
		if got := WtPortFor(id); got != want {
			t.Errorf("WtPortFor(%q) = %d, want %d", id, got, want)
		}
	}
	flags := map[string][3]bool{ // running-only, routes-follow-artifact, served
		"ollama": {false, true, false}, "omlx": {false, false, true},
		"mtplx": {false, false, true}, "mlx_lm_server": {true, false, true},
	}
	for f, w := range flags {
		spec, ok := FamilyByName(f)
		if !ok || spec.RunningOnly != w[0] || spec.RoutesFollowArtifact != w[1] || spec.Served != w[2] {
			t.Errorf("FamilyByName(%q) = %+v, %v; want running-only/routes-follow/served %v", f, spec, ok, w)
		}
	}
	tenancy := map[string]Tenancy{"ollama": Shared, "omlx": Pool, "mtplx": Exclusive, "mlx_lm_server": NoTenancy}
	for f, want := range tenancy {
		if spec, _ := FamilyByName(f); spec.Tenancy != want {
			t.Errorf("family %q tenancy = %d, want %d", f, spec.Tenancy, want)
		}
	}
	defaults := map[string][2]string{ // origin, dir
		"ollama": {"http://localhost:11434", ""}, "omlx": {"http://localhost:8000", "~/.omlx/models"},
		"mtplx": {"http://localhost:8003", "~/.mtplx/models"}, "mlx_lm_server": {"http://localhost:8001", ""},
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
	spec, _ := Lookup("ollama")
	spec.Seed.Protocols[0] = "mutated"
	if again, _ := Lookup("ollama"); again.Seed.Protocols[0] == "mutated" {
		t.Error("Lookup shares a Seed's Protocols slice with the table")
	}
}
