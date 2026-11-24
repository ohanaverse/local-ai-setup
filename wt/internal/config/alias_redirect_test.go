package config

import (
	"errors"
	"strings"
	"testing"
)

const redirectRegistry = aliasRegistry + `
[[aliases]]
id = "alias/new"
targets = [{ model = "openrouter/x", order = 1 }, { model = "ollama/local-a", order = 2 }]

[[aliases]]
id = "alias/old"
redirect_to = "alias/new"
`

// TestRedirectAliasReadsItsTargetsFromTheTargetAlias pins #687 decision 1 and
// 3: a redirect has no targets of its own, Config.AliasTargets resolves the
// target alias's at read time (so a later change to the target moves both
// ids), and AliasLocalTarget follows it. If a consumer read a.Targets, the old
// id would route nowhere.
func TestRedirectAliasReadsItsTargetsFromTheTargetAlias(t *testing.T) {
	scratchRegistry(t, redirectRegistry)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	old, ok := cfg.AliasByID("alias/old")
	if !ok || old.RedirectTo != "alias/new" || old.Malformed != "" || len(old.Targets) != 0 {
		t.Fatalf("alias/old = %+v, %v; want a redirect to alias/new with no targets of its own", old, ok)
	}
	nw, _ := cfg.AliasByID("alias/new")
	if got := cfg.AliasTargets(old); len(got) != 2 || got[0] != nw.Targets[0] || got[1] != nw.Targets[1] {
		t.Errorf("AliasTargets(old) = %+v, want alias/new's %+v", got, nw.Targets)
	}
	if m, ok := cfg.AliasLocalTarget(old); !ok || m.ID != "ollama/local-a" {
		t.Errorf("AliasLocalTarget(old) = %v, %v; want ollama/local-a", m.ID, ok)
	}
	if got := cfg.AliasRedirectsTo("alias/new"); len(got) != 1 || got[0] != "alias/old" {
		t.Errorf("AliasRedirectsTo(new) = %v", got)
	}
	if got := cfg.AliasesTargeting("ollama/local-a"); len(got) != 1 || got[0] != "alias/new" {
		t.Errorf("AliasesTargeting = %v, want only the real alias", got)
	}
	cfg.DefaultTag = "code"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want a sound registry", err)
	}
}

// TestRedirectAliasFaults pins the reader and the cross-alias rules: both
// keys, a self redirect, a missing target and a redirect chain are each a
// fault (Malformed or AliasFault), so the route path freezes the alias rather
// than serving a half-resolved one.
func TestRedirectAliasFaults(t *testing.T) {
	cases := []struct{ name, extra, id, want string }{
		{"both keys", "id = \"alias/o\"\nredirect_to = \"alias/new\"\ntargets = [{ model = \"openrouter/x\" }]\n", "alias/o", "not both"},
		{"self", "id = \"alias/o\"\nredirect_to = \"alias/o\"\n", "alias/o", "the alias itself"},
		{"not an alias id", "id = \"alias/o\"\nredirect_to = \"openrouter/x\"\n", "alias/o", "must be an alias id"},
		{"missing target", "id = \"alias/o\"\nredirect_to = \"alias/ghost\"\n", "alias/o", "not an alias"},
		{"chain", "id = \"alias/o\"\nredirect_to = \"alias/old\"\n", "alias/o", "itself a redirect"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scratchRegistry(t, redirectRegistry+"\n[[aliases]]\n"+c.extra)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			a, _ := cfg.AliasByID(c.id)
			err = AliasFault(cfg, a)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("AliasFault = %v, want it to name %q", err, c.want)
			}
			if got := cfg.AliasTargets(a); len(got) != 0 {
				t.Errorf("AliasTargets = %+v, want none for a faulty redirect", got)
			}
		})
	}
}

// TestRedirectWriterRules pins the writer side: RedirectAlias turns an alias
// into a redirect and the write is validated (a redirect to a missing alias,
// or a redirect that is itself redirected to, is refused), SetAliasTargets on
// a redirect names the alias to set instead, and RemoveAlias refuses an alias
// a redirect points at, naming the redirect, until the redirect is removed.
func TestRedirectWriterRules(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry+"\n[[aliases]]\nid = \"alias/new\"\ntargets = [{ model = \"openrouter/x\" }]\n\n[[aliases]]\nid = \"alias/old\"\ntargets = [{ model = \"openrouter/x\" }]\n")
	if _, err := UpdateRegistry(func(d *RegistryDoc) error { return d.RedirectAlias("alias/old", "alias/ghost") }); !errors.Is(err, ErrRegistryInvalid) || !strings.Contains(err.Error(), "not an alias") {
		t.Fatalf("redirect to a missing alias: %v", err)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error { return d.RedirectAlias("alias/old", "alias/new") }); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.HasSuffix(got, "[[aliases]]\nid = \"alias/old\"\nredirect_to = \"alias/new\"\n") {
		t.Errorf("redirect layout wrong:\n%s", got)
	}
	// A redirect target cannot itself become a redirect.
	if _, err := UpdateRegistry(func(d *RegistryDoc) error { return d.RedirectAlias("alias/new", "alias/old") }); !errors.Is(err, ErrRegistryInvalid) {
		t.Errorf("redirecting a redirect target: %v, want ErrRegistryInvalid", err)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.SetAliasTargets("alias/old", []map[string]any{{"model": "openrouter/x"}})
	}); err == nil || !strings.Contains(err.Error(), "set alias/new's targets instead") {
		t.Errorf("SetAliasTargets on a redirect: %v", err)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error { _, err := d.RemoveAlias("alias/new"); return err }); !errors.Is(err, ErrAliasRedirected) || !strings.Contains(err.Error(), "alias/old") {
		t.Errorf("RemoveAlias(target) = %v, want ErrAliasRedirected naming alias/old", err)
	}
	if _, err := UpdateRegistry(func(d *RegistryDoc) error {
		for _, id := range []string{"alias/old", "alias/new"} {
			if _, err := d.RemoveAlias(id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Errorf("removing the redirect then its target: %v", err)
	}
}
