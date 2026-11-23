package modeladmin

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tiers"
)

// aliasRegistry is baseRegistry plus two cloud models with windows, one
// without, and a second local model.
const aliasRegistry = baseRegistry + `
[[models]]
id = "openrouter/big"
family = "f"
provider_id = "openrouter"
model_name = "org/big"
tags = []
context_window = 262144

[[models]]
id = "openrouter/small"
family = "f"
provider_id = "openrouter"
model_name = "org/small"
tags = []
context_window = 131072

[[models]]
id = "openrouter/unknown"
family = "f"
provider_id = "openrouter"
model_name = "org/unknown"
tags = []

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
tags = []
`

func targets(pairs ...any) []config.AliasTarget {
	var out []config.AliasTarget
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, config.AliasTarget{Model: pairs[i].(string), Order: pairs[i+1].(int)})
	}
	return out
}

func loadCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestCheckAliasRefusals pins what `wt-model alias set` refuses before any
// write: an id that is not alias/<name>, no target, a target given twice, an
// order below 1, a target that names no model or names an alias, and two
// local targets (decision 1). Each would be written as an alias every route
// path freezes.
func TestCheckAliasRefusals(t *testing.T) {
	scratchRegistry(t, aliasRegistry)
	cfg := loadCfg(t)
	for name, tc := range map[string]struct {
		r    AliasRequest
		want string
	}{
		"bad id":       {AliasRequest{ID: "main", Targets: targets("openrouter/big", 1)}, `is not "alias/<name>"`},
		"upper case":   {AliasRequest{ID: "alias/Main", Targets: targets("openrouter/big", 1)}, `is not "alias/<name>"`},
		"no target":    {AliasRequest{ID: "alias/main"}, "at least one target"},
		"duplicate":    {AliasRequest{ID: "alias/main", Targets: targets("openrouter/big", 1, "openrouter/big", 2)}, "more than once"},
		"bad order":    {AliasRequest{ID: "alias/main", Targets: []config.AliasTarget{{Model: "openrouter/big", Order: -1}}}, "whole number from 1"},
		"unknown":      {AliasRequest{ID: "alias/main", Targets: targets("openrouter/nope", 1)}, "names no model"},
		"alias target": {AliasRequest{ID: "alias/main", Targets: targets("alias/other", 1)}, "cannot target an alias"},
		"two local":    {AliasRequest{ID: "alias/main", Targets: targets("ollama/gemma4:9b", 1, "ollama/qwen3:8b", 2)}, "at most one local target"},
	} {
		if err := CheckAlias(cfg, tc.r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: CheckAlias = %v, want %q", name, err, tc.want)
		}
	}
	if err := CheckAlias(cfg, AliasRequest{ID: "alias/main", Targets: targets("openrouter/big", 1, "ollama/qwen3:8b", 2)}); err != nil {
		t.Errorf("a sound alias: %v", err)
	}
}

// TestSetAliasAddsReplacesAndRemoves pins the registry half of `alias set`
// and `alias rm`: a new alias is appended as an [[aliases]] table, the same
// set again writes nothing (changed false), a set replaces the whole target
// list, and RemoveAliases is all or nothing (an unknown id removes none).
func TestSetAliasAddsReplacesAndRemoves(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	r := AliasRequest{ID: "alias/main", Targets: targets("openrouter/big", 1, "ollama/qwen3:8b", 0)}
	if changed, err := SetAlias(r); err != nil || !changed {
		t.Fatalf("SetAlias = %v, %v", changed, err)
	}
	if want := "[[aliases]]\nid = \"alias/main\"\ntargets = [\n    { model = \"openrouter/big\", order = 1 },\n    { model = \"ollama/qwen3:8b\" },\n]\n"; !strings.Contains(read(t, path), want) {
		t.Fatalf("registry lacks\n%s\nfile:\n%s", want, read(t, path))
	}
	if changed, err := SetAlias(r); err != nil || changed {
		t.Fatalf("the same set again = %v, %v; want no change", changed, err)
	}
	if _, err := SetAlias(AliasRequest{ID: "alias/main", Targets: targets("openrouter/small", 1)}); err != nil {
		t.Fatal(err)
	}
	if a, _ := loadCfg(t).AliasByID("alias/main"); !slices.Equal(a.Targets, targets("openrouter/small", 1)) {
		t.Fatalf("targets after replace = %v", a.Targets)
	}
	before := read(t, path)
	if _, err := RemoveAliases([]string{"alias/main", "alias/nope"}); !errors.Is(err, config.ErrAliasNotFound) || read(t, path) != before {
		t.Fatalf("RemoveAliases with an unknown id = %v, file changed %v", err, read(t, path) != before)
	}
	if removed, err := RemoveAliases([]string{"alias/main"}); err != nil || !slices.Equal(removed, []string{"alias/main"}) {
		t.Fatalf("RemoveAliases = %v, %v", removed, err)
	}
	if strings.Contains(read(t, path), "aliases") {
		t.Errorf("the last alias removed should drop the key:\n%s", read(t, path))
	}
}

// TestSetAliasRechecksInsideTheWrite pins that SetAlias's apply checks the
// request against the document it writes, not the caller's config: a target
// another program removed between the caller's load and the write is refused
// and nothing is written.
func TestSetAliasRechecksInsideTheWrite(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	r := AliasRequest{ID: "alias/main", Targets: targets("openrouter/unknown", 1)}
	if err := CheckAlias(loadCfg(t), r); err != nil {
		t.Fatal(err)
	}
	gone := strings.Replace(aliasRegistry, `id = "openrouter/unknown"`, `id = "openrouter/renamed"`, 1)
	if err := os.WriteFile(path, []byte(gone), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetAlias(r); err == nil || !strings.Contains(err.Error(), "names no model") || read(t, path) != gone {
		t.Fatalf("SetAlias = %v; want the write's own refusal and the file unchanged", err)
	}
}

// TestAliasViews pins the listing's model of an alias: its targets in file
// order and decision 6's window (the smallest over all targets, withheld
// with the limiting target named when one is unknown).
func TestAliasViews(t *testing.T) {
	scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/known"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small" }]

[[aliases]]
id = "alias/withheld"
targets = [{ model = "openrouter/big" }, { model = "openrouter/unknown" }]
`)
	views := AliasViews(loadCfg(t), tiers.Index{})
	want := []AliasView{
		{ID: "alias/known", Targets: targets("openrouter/big", 1, "openrouter/small", 2), Context: 131072, Limiting: "openrouter/small"},
		{ID: "alias/withheld", Targets: targets("openrouter/big", 1, "openrouter/unknown", 2), Context: 0, Limiting: "openrouter/unknown"},
	}
	if len(views) != len(want) {
		t.Fatalf("views = %+v", views)
	}
	for i := range want {
		if views[i].ID != want[i].ID || !slices.Equal(views[i].Targets, want[i].Targets) || views[i].Context != want[i].Context || views[i].Limiting != want[i].Limiting {
			t.Errorf("view %d = %+v, want %+v", i, views[i], want[i])
		}
	}
}
