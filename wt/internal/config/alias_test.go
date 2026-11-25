package config

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tomlw"
)

// aliasRegistry is a registry with one local and one cloud provider, a
// native one, two local models, a cloud model, an ollama cloud model (a cloud
// override on the local provider) and a native model: every kind of target an
// alias rule is about.
const aliasRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"

[[providers]]
id = "agy"
name = "Agy"
location = "cloud"

[providers.auth]
type = "native"

[[models]]
id = "ollama/local-a"
family = "f"
provider_id = "ollama"
model_name = "local-a"

[[models]]
id = "ollama/local-b"
family = "f"
provider_id = "ollama"
model_name = "local-b"

[[models]]
id = "ollama/cloud-c"
family = "f"
provider_id = "ollama"
model_name = "cloud-c"
location = "cloud"

[[models]]
id = "openrouter/x"
family = "f"
provider_id = "openrouter"
model_name = "org/x"

[[models]]
id = "agy/n"
family = "f"
provider_id = "agy"
model_name = "n"
`

// aliasExclusiveRegistry adds an mtplx provider (Exclusive tenancy: one model
// per process) with two models, and an omlx pool provider with two models.
const aliasExclusiveRegistry = aliasRegistry + `
[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"

[providers.auth]
type = "none"

[[providers]]
id = "omlx"
name = "oMLX"
location = "local"

[providers.auth]
type = "none"

[[models]]
id = "mtplx/m1"
family = "f"
provider_id = "mtplx"
model_name = "m1"

[[models]]
id = "mtplx/m2"
family = "f"
provider_id = "mtplx"
model_name = "m2"

[[models]]
id = "omlx/o1"
family = "f"
provider_id = "omlx"
model_name = "o1"

[[models]]
id = "omlx/o2"
family = "f"
provider_id = "omlx"
model_name = "o2"
`

// aliasRow builds an alias table as a writer would hand it to AddAlias.
func aliasRow(t *testing.T, id string, targets ...map[string]any) *tomlw.Table {
	t.Helper()
	row := tomlw.NewTable()
	row.Set("id", id)
	v, err := orderedValue(targets, aliasSchemas, "targets")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		v = []any{}
	}
	row.Set("targets", v)
	return row
}

func tgt(model string, order int) map[string]any {
	if order == 0 {
		return map[string]any{"model": model}
	}
	return map[string]any{"model": model, "order": order}
}

// TestAliasesRoundTripThroughTheWriter pins the [[aliases]] layout wt writes
// and reads back: AddAlias appends an alias table after the models, its
// targets an inline array of inline tables keyed `model, order` (tomli-w's
// form, so llmbench and a hand edit see the same file), SetAliasTargets
// replaces the array, and a SetAliasTargets to the same targets writes
// nothing. If the layout drifted, every alias write would rewrite lines it
// did not mean to.
func TestAliasesRoundTripThroughTheWriter(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.AddAlias(aliasRow(t, "alias/main", map[string]any{"order": 1, "model": "openrouter/x"}, tgt("ollama/local-a", 2)))
	})
	if err != nil || !changed {
		t.Fatalf("AddAlias: UpdateRegistry = (%v, %v)", changed, err)
	}
	want := "[[aliases]]\nid = \"alias/main\"\ntargets = [\n    { model = \"openrouter/x\", order = 1 },\n    { model = \"ollama/local-a\", order = 2 },\n]\n"
	if got := readFile(t, path); !strings.HasSuffix(got, "\n\n"+want) {
		t.Fatalf("file after AddAlias does not end with\n%s\ngot:\n%s", want, got)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.SetAliasTargets("alias/main", []map[string]any{tgt("ollama/cloud-c", 1)})
	}); err != nil {
		t.Fatalf("SetAliasTargets: %v", err)
	}
	reg, err := readRegistry()
	if err != nil {
		t.Fatal(err)
	}
	wantAliases := []Alias{{ID: "alias/main", Targets: []AliasTarget{{Model: "ollama/cloud-c", Order: 1}}}}
	if !slices.EqualFunc(reg.Aliases, wantAliases, aliasEqual) {
		t.Errorf("Aliases = %+v, want %+v", reg.Aliases, wantAliases)
	}
	changed, err = UpdateRegistry(func(d *RegistryDoc) error {
		return d.SetAliasTargets("alias/main", []map[string]any{tgt("ollama/cloud-c", 1)})
	})
	if err != nil || changed {
		t.Errorf("same targets again: UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.SetAliasTargets("alias/none", []map[string]any{tgt("ollama/cloud-c", 1)})
	}); !errors.Is(err, ErrAliasNotFound) {
		t.Errorf("SetAliasTargets on a missing alias: err = %v, want ErrAliasNotFound", err)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.AddAlias(aliasRow(t, "alias/main", tgt("openrouter/x", 1)))
	}); !errors.Is(err, ErrAliasExists) {
		t.Errorf("AddAlias of an existing id: err = %v, want ErrAliasExists", err)
	}
	// Removing the last alias removes the key, so the file is one an older
	// wt-model (which refuses an unknown top-level key) can write again.
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		_, err := d.RemoveAlias("alias/main")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); strings.Contains(got, "aliases") {
		t.Errorf("the last alias removed, the file still names aliases:\n%s", got)
	}
}

func aliasEqual(a, b Alias) bool {
	return a.ID == b.ID && a.RedirectTo == b.RedirectTo && a.Malformed == b.Malformed && slices.Equal(a.Targets, b.Targets)
}

// TestAliasOrderDefaultsToPosition pins decision 1: `order` is optional and a
// target without one takes its 1-based position, so a hand-written list of
// targets is a priority list in file order.
func TestAliasOrderDefaultsToPosition(t *testing.T) {
	scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/g"
targets = [
    { model = "openrouter/x" },
    { model = "ollama/cloud-c", order = 5 },
    { model = "ollama/local-a" },
]
`)
	reg, err := readRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []AliasTarget{{"openrouter/x", 1}, {"ollama/cloud-c", 5}, {"ollama/local-a", 3}}
	if len(reg.Aliases) != 1 || !slices.Equal(reg.Aliases[0].Targets, want) || reg.Aliases[0].Malformed != "" {
		t.Errorf("Aliases = %+v, want one alias with targets %+v", reg.Aliases, want)
	}
}

// TestAliasValidationRules pins every rule a registry write holds an alias
// to (decision 1 and the id namespace rule): each case is refused as
// ErrRegistryInvalid and the file is left as it was. A write that let one
// through would leave a registry whose alias the route path freezes, or a
// model id or provider that collides with an alias id at the gateway.
func TestAliasValidationRules(t *testing.T) {
	const withAlias = aliasExclusiveRegistry + `
[[aliases]]
id = "alias/other"
targets = [
    { model = "openrouter/x", order = 1 },
]
`
	cases := []struct {
		name, reg string
		apply     func(t *testing.T, d *RegistryDoc) error
		want      string
	}{
		{"empty targets", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a"))
		}, "at least one target"},
		{"duplicate target", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", tgt("openrouter/x", 1), tgt("openrouter/x", 2)))
		}, "more than once"},
		{"unknown target", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", tgt("openrouter/nope", 1)))
		}, "no model"},
		{"alias target", withAlias, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", tgt("alias/other", 1)))
		}, "an alias cannot target an alias"},
		{"native target", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", tgt("agy/n", 1)))
		}, "native"},
		{"two local targets of one exclusive family", aliasExclusiveRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", tgt("mtplx/m1", 1), tgt("mtplx/m2", 2)))
		}, "mtplx/m1 and mtplx/m2 are both served by the mtplx family"},
		{"id without prefix", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "main", tgt("openrouter/x", 1)))
		}, `"alias/<name>"`},
		{"bad name", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/Main", tgt("openrouter/x", 1)))
		}, `"alias/<name>"`},
		{"order below one", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", map[string]any{"model": "openrouter/x", "order": 0}))
		}, "order"},
		{"id equal to a model id", aliasRegistry + "\n[[models]]\nid = \"alias/a\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"a\"\n",
			func(t *testing.T, d *RegistryDoc) error {
				return d.AddAlias(aliasRow(t, "alias/a", tgt("openrouter/x", 1)))
			}, "is also a model id"},
		{"targets replaced by a bad list", withAlias, func(t *testing.T, d *RegistryDoc) error {
			return d.SetAliasTargets("alias/other", []map[string]any{tgt("mtplx/m1", 1), tgt("mtplx/m2", 2)})
		}, "mtplx/m1 and mtplx/m2"},
		{"provider id alias", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddProvider(map[string]any{"id": "alias", "name": "A", "auth": map[string]any{"type": "none"}})
		}, "reserved"},
		{"model id alias/x", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddModel(map[string]any{"id": "alias/x", "family": "f", "provider_id": "openrouter", "model_name": "x"})
		}, "reserved"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := scratchRegistry(t, c.reg)
			changed, err := UpdateRegistry(func(d *RegistryDoc) error { return c.apply(t, d) })
			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryInvalid naming %q", changed, err, c.want)
			}
			if got := readFile(t, path); got != c.reg {
				t.Errorf("a refused write changed the file:\n%s", got)
			}
		})
	}
}

// TestMalformedAliasReadsAsMalformed pins the reader tolerance (decision 3,
// the ServeArgs rule): a hand-edit slip in one alias never fails Load, the
// alias stays in Config.Aliases with Malformed naming the fault (so the route
// path can freeze its rows and the catalog can block it by name), and the
// well-formed alias beside it reads as usual.
func TestMalformedAliasReadsAsMalformed(t *testing.T) {
	scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/good"
targets = [{ model = "openrouter/x" }]

[[aliases]]
id = "alias/str"
targets = "openrouter/x"

[[aliases]]
id = "alias/order"
targets = [{ model = "openrouter/x", order = "two" }]

[[aliases]]
id = "noprefix"
targets = [{ model = "openrouter/x" }]

[[aliases]]
id = "alias/extra"
note = "x"
targets = [{ model = "openrouter/x" }]
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want a malformed alias to load", err)
	}
	want := map[string]string{
		"alias/good":  "",
		"alias/str":   "targets",
		"alias/order": "order",
		"noprefix":    "alias/<name>",
		"alias/extra": "note",
	}
	if len(cfg.Aliases) != len(want) {
		t.Fatalf("Aliases = %+v, want %d", cfg.Aliases, len(want))
	}
	for _, a := range cfg.Aliases {
		w := want[a.ID]
		if w == "" && a.Malformed != "" || w != "" && !strings.Contains(a.Malformed, w) {
			t.Errorf("alias %q: Malformed = %q, want it to name %q", a.ID, a.Malformed, w)
		}
	}
	if a, ok := cfg.AliasByID("alias/good"); !ok || len(a.Targets) != 1 || a.Targets[0] != (AliasTarget{"openrouter/x", 1}) {
		t.Errorf("AliasByID(alias/good) = %+v, %v", a, ok)
	}
	// A malformed alias is the reader's to tolerate, not validation's: it
	// does not stop a command that validates the registry.
	cfg.DefaultTag = "code"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil (malformed aliases are not validation errors)", err)
	}
}

// TestAliasNotesKeyIsAcceptedAndKept pins decision 1 of #691: a hand-written
// `notes` string on an alias is read as well-formed (and not stored), a
// non-string `notes` is Malformed, and the writer keeps the key when it
// patches the alias's targets. If notes read as an unknown key, a comment a
// user left on an alias would freeze its routes; if the writer dropped it,
// the first `wt-model alias set` would erase the user's note.
func TestAliasNotesKeyIsAcceptedAndKept(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/noted"
notes = "my daily driver"
targets = [{ model = "openrouter/x" }]

[[aliases]]
id = "alias/badnotes"
notes = 3
targets = [{ model = "openrouter/x" }]
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if a, ok := cfg.AliasByID("alias/noted"); !ok || a.Malformed != "" || len(a.Targets) != 1 {
		t.Errorf("alias/noted = %+v, %v, want a well-formed alias with one target", a, ok)
	}
	if a, ok := cfg.AliasByID("alias/badnotes"); !ok || !strings.Contains(a.Malformed, "notes must be a string") {
		t.Errorf("alias/badnotes: Malformed = %q, want %q", a.Malformed, "notes must be a string")
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.SetAliasTargets("alias/noted", []map[string]any{tgt("ollama/cloud-c", 1)})
	}); err != nil {
		t.Fatalf("SetAliasTargets on an alias with notes: %v", err)
	}
	if got := readFile(t, path); !strings.Contains(got, `notes = "my daily driver"`) {
		t.Errorf("the writer dropped the notes key:\n%s", got)
	}
}

// TestAliasFaultIsARegistryEntryError pins that a cross-table fault a hand
// edit left (two local targets, a target no model has) is a validation error
// marked ErrRegistryEntry, so the repair hint names registry.toml, while Load
// still succeeds; and that a model id under alias/ or a provider id `alias`
// are refused the same way.
func TestAliasFaultIsARegistryEntryError(t *testing.T) {
	cases := map[string]string{
		"two exclusive local targets": aliasExclusiveRegistry + "\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"mtplx/m1\" }, { model = \"mtplx/m2\" }]\n",
		"unknown target":              aliasRegistry + "\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"ghost/m\" }]\n",
		"model id alias/x":            aliasRegistry + "\n[[models]]\nid = \"alias/x\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"x\"\n",
		"provider alias":              aliasRegistry + "\n[[providers]]\nid = \"alias\"\nname = \"A\"\nlocation = \"cloud\"\n[providers.auth]\ntype = \"none\"\n",
		"duplicate alias":             aliasRegistry + "\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"openrouter/x\" }]\n\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"openrouter/x\" }]\n",
	}
	for name, reg := range cases {
		t.Run(name, func(t *testing.T) {
			scratchRegistry(t, reg)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() = %v, want a registry that loads", err)
			}
			cfg.DefaultTag = "code"
			if err := cfg.Validate(); !errors.Is(err, ErrRegistryEntry) {
				t.Errorf("Validate() = %v, want ErrRegistryEntry", err)
			}
		})
	}
}

// TestRemoveModelRefusedWhileAnAliasTargetsIt pins Review Focus 5 and
// decision 11: removing a model an alias targets is refused with
// ErrModelInUse naming the alias, and the file is unchanged. Touched-row
// validation alone would let `wt-model model rm` orphan the alias.
func TestRemoveModelRefusedWhileAnAliasTargetsIt(t *testing.T) {
	reg := aliasRegistry + "\n[[aliases]]\nid = \"alias/main\"\ntargets = [{ model = \"openrouter/x\" }]\n"
	path := scratchRegistry(t, reg)
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		_, err := d.RemoveModel("openrouter/x")
		return err
	})
	if !errors.Is(err, ErrModelInUse) || changed || !strings.Contains(err.Error(), "alias/main") {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrModelInUse naming alias/main", changed, err)
	}
	if got := readFile(t, path); got != reg {
		t.Errorf("a refused removal changed the file:\n%s", got)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.AliasesTargeting("openrouter/x"); !slices.Equal(got, []string{"alias/main"}) {
		t.Errorf("AliasesTargeting = %v, want [alias/main]", got)
	}
	// A model no alias targets is removed as before.
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		_, err := d.RemoveModel("ollama/local-b")
		return err
	}); err != nil {
		t.Errorf("removing an untargeted model: %v", err)
	}
}

// TestModelEditRefusedWhenItGivesAnAliasTwoLocalTargets pins Review Focus 5:
// a write that touches only a model row re-checks every alias that targets
// it, so `wt-model model edit --location local`, or unsetting a cloud
// override so the model inherits its local provider's location, cannot give
// an alias a second local target. The file is left as it was.
func TestModelEditRefusedWhenItGivesAnAliasTwoLocalTargets(t *testing.T) {
	reg := aliasExclusiveRegistry + "\n[[models]]\nid = \"mtplx/c\"\nfamily = \"f\"\nprovider_id = \"mtplx\"\nmodel_name = \"c\"\nlocation = \"cloud\"\n" +
		"\n[[aliases]]\nid = \"alias/g\"\ntargets = [{ model = \"mtplx/c\" }, { model = \"mtplx/m1\" }]\n"
	edits := map[string]func(d *RegistryDoc) error{
		"own location": func(d *RegistryDoc) error {
			return d.PatchModel("mtplx/c", map[string]any{"location": "local"}, nil)
		},
		"inherited from the provider": func(d *RegistryDoc) error {
			return d.PatchModel("mtplx/c", nil, []string{"location"})
		},
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			path := scratchRegistry(t, reg)
			changed, err := UpdateRegistry(edit)
			if !errors.Is(err, ErrRegistryInvalid) || changed || !strings.Contains(err.Error(), "alias/g") {
				t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryInvalid naming alias/g", changed, err)
			}
			if got := readFile(t, path); got != reg {
				t.Errorf("a refused edit changed the file:\n%s", got)
			}
		})
	}
	// An edit that leaves the alias valid still goes through.
	scratchRegistry(t, reg)
	if _, err := UpdateRegistry(setFamily("mtplx/c", "g")); err != nil {
		t.Errorf("a harmless edit of a target was refused: %v", err)
	}
}

// TestAliasLocalTarget pins the helper the model server's start path uses:
// the one target that resolves local, if any.
func TestAliasLocalTarget(t *testing.T) {
	scratchRegistry(t, aliasRegistry+"\n[[aliases]]\nid = \"alias/g\"\ntargets = [{ model = \"openrouter/x\" }, { model = \"ollama/local-a\" }]\n\n[[aliases]]\nid = \"alias/c\"\ntargets = [{ model = \"openrouter/x\" }]\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	g, _ := cfg.AliasByID("alias/g")
	if m, ok := cfg.AliasLocalTarget(g); !ok || m.ID != "ollama/local-a" {
		t.Errorf("AliasLocalTarget(alias/g) = %q, %v", m.ID, ok)
	}
	c, _ := cfg.AliasByID("alias/c")
	if _, ok := cfg.AliasLocalTarget(c); ok {
		t.Error("a cloud-only alias has no local target")
	}
}

// TestAliasAllowsSeveralLocalTargetsOfAPoolOrSharedFamily pins #690: two local
// targets of ollama (Shared) or omlx (Pool) are sound, and AliasLocalTargets
// lists every one in (order, file position) order while AliasLocalTarget
// stays the first. A cloud target between them is skipped.
func TestAliasAllowsSeveralLocalTargetsOfAPoolOrSharedFamily(t *testing.T) {
	scratchRegistry(t, aliasExclusiveRegistry+`
[[aliases]]
id = "alias/shared"
targets = [{ model = "ollama/local-b", order = 2 }, { model = "openrouter/x", order = 1 }, { model = "ollama/local-a", order = 2 }]

[[aliases]]
id = "alias/pool"
targets = [{ model = "omlx/o1" }, { model = "omlx/o2" }]

[[aliases]]
id = "alias/mixed"
targets = [{ model = "mtplx/m1" }, { model = "omlx/o1" }, { model = "ollama/local-a" }]
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range cfg.Aliases {
		if err := AliasFault(cfg, a); err != nil {
			t.Errorf("AliasFault(%s) = %v, want nil", a.ID, err)
		}
	}
	ids := func(ms []Model) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	shared, _ := cfg.AliasByID("alias/shared")
	if got := ids(cfg.AliasLocalTargets(shared)); !slices.Equal(got, []string{"ollama/local-b", "ollama/local-a"}) {
		t.Errorf("AliasLocalTargets(shared) = %v, want [local-b local-a] (order 2 ties keep file position)", got)
	}
	if m, ok := cfg.AliasLocalTarget(shared); !ok || m.ID != "ollama/local-b" {
		t.Errorf("AliasLocalTarget(shared) = %q, %v, want the first of AliasLocalTargets", m.ID, ok)
	}
	pool, _ := cfg.AliasByID("alias/pool")
	if got := ids(cfg.AliasLocalTargets(pool)); !slices.Equal(got, []string{"omlx/o1", "omlx/o2"}) {
		t.Errorf("AliasLocalTargets(pool) = %v", got)
	}
}
