package cloudsync

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// aliased marks an entry as the target of the given aliases, as EntriesOf
// reads it from the registry.
func aliased(users ...string) func(*Entry) { return func(e *Entry) { e.AliasUsers = users } }

// TestPlanPricesKeepsAnAliasTarget pins decision 11 in the openrouter flow:
// a model an alias targets is never planned as a removal, however absent
// from a healthy list, and the plan says why in one warning. The removal
// digest covers only what is removed, so the dry run's digest is the one the
// approved run computes. Without it, an approved run would abort the whole
// write on RemoveModel's ErrModelInUse.
func TestPlanPricesKeepsAnAliasTarget(t *testing.T) {
	kept := orEntry("kept", nil)
	aliased("alias/main")(&kept)
	plan := PlanPrices([]Entry{kept, orEntry("gone", nil), orEntry("here", nil)}, healthyList("vendor/here"))
	wantIDs(t, "removals", plan.Removals, "openrouter/gone")
	if !slices.Contains(plan.Warnings, "kept openrouter/kept: alias/main targets it") {
		t.Errorf("warnings = %q, want the kept line", plan.Warnings)
	}
	if want := (&PricePlan{Removals: []string{"openrouter/gone"}}).RemovalDigest(); plan.RemovalDigest() != want {
		t.Errorf("digest %s, want %s (the kept model is not part of it)", plan.RemovalDigest(), want)
	}
}

// TestPlanCatalogKeepsAnAliasTarget pins decision 11 in the ollama flow: an
// ollama cloud entry the page no longer lists is kept when an alias targets
// it (a warning names the aliases), its pulled tag is neither removed nor
// counted a stray, and an entry the page lists under another tag is kept as
// it is rather than replaced under a new id.
func TestPlanCatalogKeepsAnAliasTarget(t *testing.T) {
	entries := []Entry{
		cloudEntry("old:cloud", aliased("alias/a", "alias/b")),
		cloudEntry("gone:cloud"),
		cloudEntry("deepseek:latest", withName("deepseek"), aliased("alias/c")),
	}
	resolved := map[string]string{"deepseek": "deepseek:cloud"}
	plan := PlanCatalog(entries, catalogOf(cm("deepseek")), []string{"old:cloud", "gone:cloud", "deepseek:latest"}, resolved)
	wantIDs(t, "removals", plan.Removals, "ollama/gone:cloud")
	wantIDs(t, "additions", additionIDs(plan))
	if len(plan.StrayTags) != 0 {
		t.Errorf("stray tags %q, want none: the kept entry's tag is registered", plan.StrayTags)
	}
	for _, want := range []string{"kept ollama/old:cloud: alias/a, alias/b target it", "kept ollama/deepseek:latest: alias/c targets it"} {
		if !slices.ContainsFunc(plan.Warnings, func(w string) bool { return strings.HasPrefix(w, want) }) {
			t.Errorf("warnings = %q, want one starting %q", plan.Warnings, want)
		}
	}
}

// TestEntriesOfReadsAliasTargetsFromTheRawFile pins the carry-over from
// Task 2's review: cloud-sync reads alias targets as RemoveModel does, from
// the raw [[aliases]] tables, so an alias that is malformed elsewhere (here
// an unknown key) still protects its target.
func TestEntriesOfReadsAliasTargetsFromTheRawFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.toml")
	t.Setenv("WT_REGISTRY", path)
	body := `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "X"

[[models]]
id = "openrouter/a"
family = "f"
provider_id = "openrouter"
model_name = "a"
tags = []

[[models]]
id = "openrouter/b"
family = "f"
provider_id = "openrouter"
model_name = "b"
tags = []

[[aliases]]
id = "alias/odd"
odd_key = "an unknown key: the alias is malformed"
targets = [{ model = "openrouter/a" }]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := config.ReadRegistryDoc()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, e := range EntriesOf(doc) {
		got[e.ID] = e.AliasUsers
	}
	if !slices.Equal(got["openrouter/a"], []string{"alias/odd"}) || len(got["openrouter/b"]) != 0 {
		t.Errorf("alias users = %v, want openrouter/a used by alias/odd and openrouter/b by none", got)
	}
}

// TestMassRemovalGuardIgnoresAliasTargets pins that a kept alias target never
// dilutes the mass-removal guard: with six entries, three of them alias
// targets, and all six gone from a healthy list or page, the three that go
// are every removable entry, so both flows flag a mass removal. Counted in
// the denominator they read as 3 of 6 and the guard stays silent exactly
// when aliases target cloud models. A registry with no alias plans the same
// removals and digest as before.
func TestMassRemovalGuardIgnoresAliasTargets(t *testing.T) {
	var or, ol []Entry
	for i, n := range []string{"a", "b", "c", "d", "e", "f"} {
		o, c := orEntry(n, nil), cloudEntry(n+":cloud")
		if i < 3 {
			aliased("alias/x")(&o)
			aliased("alias/x")(&c)
		}
		or, ol = append(or, o), append(ol, c)
	}
	pp := PlanPrices(or, healthyList("vendor/other"))
	if pp.EntriesGone() != 3 || !pp.MassRemoval() {
		t.Errorf("openrouter: gone %d of %d, mass = %v; want 3 of 3 flagged", pp.EntriesGone(), pp.RemovableCandidates, pp.MassRemoval())
	}
	cp := PlanCatalog(ol, catalogOf(cm("other")), nil, nil)
	if cp.EntriesGone() != 3 || !cp.MassRemoval() {
		t.Errorf("ollama: gone %d of %d, mass = %v; want 3 of 3 flagged", cp.EntriesGone(), cp.CloudEntries, cp.MassRemoval())
	}

	// No alias: the same entries plan the counts and digests they always did.
	for i := range or {
		or[i].AliasUsers, ol[i].AliasUsers = nil, nil
	}
	pp = PlanPrices(or[:4], healthyList("vendor/a", "vendor/b"))
	if pp.RemovableCandidates != 4 || pp.MassRemoval() || pp.RemovalDigest() != digestOf([]string{"openrouter/c", "openrouter/d"}) {
		t.Errorf("openrouter, no alias: %d candidates, mass %v, digest %s", pp.RemovableCandidates, pp.MassRemoval(), pp.RemovalDigest())
	}
	cp = PlanCatalog(ol[:4], catalogOf(cm("a"), cm("b")), nil, nil)
	if cp.CloudEntries != 4 || cp.MassRemoval() || len(cp.Removals) != 2 {
		t.Errorf("ollama, no alias: %d entries, mass %v, removals %v", cp.CloudEntries, cp.MassRemoval(), cp.Removals)
	}
}
