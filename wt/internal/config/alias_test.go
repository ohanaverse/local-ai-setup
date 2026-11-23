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
	return a.ID == b.ID && a.Malformed == b.Malformed && slices.Equal(a.Targets, b.Targets)
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
	const withAlias = aliasRegistry + `
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
		{"two local targets", aliasRegistry, func(t *testing.T, d *RegistryDoc) error {
			return d.AddAlias(aliasRow(t, "alias/a", tgt("ollama/local-a", 1), tgt("ollama/local-b", 2)))
		}, "at most one local target"},
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
			return d.SetAliasTargets("alias/other", []map[string]any{tgt("ollama/local-a", 1), tgt("ollama/local-b", 2)})
		}, "at most one local target"},
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

// TestAliasFaultIsARegistryEntryError pins that a cross-table fault a hand
// edit left (two local targets, a target no model has) is a validation error
// marked ErrRegistryEntry, so the repair hint names registry.toml, while Load
// still succeeds; and that a model id under alias/ or a provider id `alias`
// are refused the same way.
func TestAliasFaultIsARegistryEntryError(t *testing.T) {
	cases := map[string]string{
		"two local targets": aliasRegistry + "\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"ollama/local-a\" }, { model = \"ollama/local-b\" }]\n",
		"unknown target":    aliasRegistry + "\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"ghost/m\" }]\n",
		"model id alias/x":  aliasRegistry + "\n[[models]]\nid = \"alias/x\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"x\"\n",
		"provider alias":    aliasRegistry + "\n[[providers]]\nid = \"alias\"\nname = \"A\"\nlocation = \"cloud\"\n[providers.auth]\ntype = \"none\"\n",
		"duplicate alias":   aliasRegistry + "\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"openrouter/x\" }]\n\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"openrouter/x\" }]\n",
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
	reg := aliasRegistry + "\n[[aliases]]\nid = \"alias/g\"\ntargets = [{ model = \"ollama/cloud-c\" }, { model = \"ollama/local-a\" }]\n"
	edits := map[string]func(d *RegistryDoc) error{
		"own location": func(d *RegistryDoc) error {
			return d.PatchModel("ollama/cloud-c", map[string]any{"location": "local"}, nil)
		},
		"inherited from the provider": func(d *RegistryDoc) error {
			return d.PatchModel("ollama/cloud-c", nil, []string{"location"})
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
	if _, err := UpdateRegistry(setFamily("ollama/cloud-c", "g")); err != nil {
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
