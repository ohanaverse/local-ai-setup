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

// TestRenameAliasMovesTheTargetsAndKeepsTheOldIdAsARedirect pins #687
// decision 2: one registry write creates alias/new with alias/old's targets
// and orders and turns alias/old into `redirect_to = "alias/new"`; the
// listing view of the old id shows the effective targets and window and names
// the redirect; a later `alias set` on the new id moves both; the old id
// cannot be set or removed-from-under, and the sweep helper finds it only
// when no session uses it. If the write left the old id with targets of its
// own, the two ids could drift.
func TestRenameAliasMovesTheTargetsAndKeepsTheOldIdAsARedirect(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/old"
targets = [{ model = "openrouter/big", order = 2 }, { model = "openrouter/small" }]
`)
	if err := RenameAlias(RenameRequest{Old: "alias/old", New: "alias/new"}); err != nil {
		t.Fatal(err)
	}
	if want := "[[aliases]]\nid = \"alias/old\"\nredirect_to = \"alias/new\"\n"; !strings.Contains(read(t, path), want) {
		t.Fatalf("registry lacks the redirect:\n%s", read(t, path))
	}
	cfg := loadCfg(t)
	nw, _ := cfg.AliasByID("alias/new")
	if want := targets("openrouter/big", 2, "openrouter/small", 2); !slices.Equal(nw.Targets, want) {
		t.Fatalf("alias/new targets = %v, want %v", nw.Targets, want)
	}
	views := AliasViews(cfg, tiers.Index{})
	var ov AliasView
	for _, v := range views {
		if v.ID == "alias/old" {
			ov = v
		}
	}
	if ov.RedirectTo != "alias/new" || !slices.Equal(ov.Targets, nw.Targets) || ov.Context != 131072 {
		t.Errorf("old view = %+v, want a redirect showing alias/new's targets and window", ov)
	}
	// alias set on the new id moves both ids.
	if _, err := SetAlias(AliasRequest{ID: "alias/new", Targets: targets("openrouter/big", 1)}); err != nil {
		t.Fatal(err)
	}
	cfg = loadCfg(t)
	oldA, _ := cfg.AliasByID("alias/old")
	if got := cfg.AliasTargets(oldA); !slices.Equal(got, targets("openrouter/big", 1)) {
		t.Errorf("redirect targets after set = %v", got)
	}
	if err := CheckAlias(cfg, AliasRequest{ID: "alias/old", Targets: targets("openrouter/big", 1)}); err == nil || !strings.Contains(err.Error(), "set alias/new's targets instead") {
		t.Errorf("CheckAlias on a redirect = %v", err)
	}
	// The removal guard: the target stays while the redirect points at it;
	// one write may remove both, either way round.
	if _, err := RemoveAliases([]string{"alias/new"}); !errors.Is(err, config.ErrAliasRedirected) || !strings.Contains(err.Error(), "alias/old") {
		t.Errorf("RemoveAliases(target) = %v, want ErrAliasRedirected naming alias/old", err)
	}
	if removed, err := RemoveAliases([]string{"alias/new", "alias/old"}); err != nil || !slices.Equal(removed, []string{"alias/new", "alias/old"}) {
		t.Errorf("RemoveAliases(both) = %v, %v", removed, err)
	}
}

// TestCheckRenameRefusals pins what `alias rename` refuses before a write:
// an unknown or already-redirected old id, a new id that exists, is the same,
// is not alias/<name> or is a model's, and an old id some redirect still
// points at. Each refusal names the id to fix, and none writes anything.
func TestCheckRenameRefusals(t *testing.T) {
	scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/a"
targets = [{ model = "openrouter/big" }]

[[aliases]]
id = "alias/b"
redirect_to = "alias/c"

[[aliases]]
id = "alias/c"
targets = [{ model = "openrouter/small" }]
`)
	cfg := loadCfg(t)
	for name, tc := range map[string]struct {
		r    RenameRequest
		want string
	}{
		"unknown old":      {RenameRequest{"alias/zzz", "alias/n"}, "alias not found"},
		"old is redirect":  {RenameRequest{"alias/b", "alias/n"}, "already a redirect to \"alias/c\""},
		"new exists":       {RenameRequest{"alias/a", "alias/c"}, "alias already exists"},
		"same":             {RenameRequest{"alias/a", "alias/a"}, "same as the old"},
		"bad new id":       {RenameRequest{"alias/a", "main"}, `is not "alias/<name>"`},
		"old has redirect": {RenameRequest{"alias/c", "alias/n"}, "redirect target of alias/b"},
	} {
		if _, _, err := CheckRename(cfg, tc.r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: CheckRename = %v, want %q", name, err, tc.want)
		}
	}
	if err := RenameAlias(RenameRequest{"alias/zzz", "alias/n"}); !errors.Is(err, config.ErrAliasNotFound) {
		t.Errorf("RenameAlias of an unknown alias = %v", err)
	}
	after, nw, err := CheckRename(cfg, RenameRequest{"alias/a", "alias/n"})
	if err != nil || nw.ID != "alias/n" || len(after.AliasTargets(nw)) != 1 {
		t.Fatalf("CheckRename ok case = %v, %+v", err, nw)
	}
	if a, _ := after.AliasByID("alias/a"); a.RedirectTo != "alias/n" {
		t.Errorf("the after-config's old alias = %+v, want a redirect", a)
	}
	if a, _ := cfg.AliasByID("alias/a"); a.RedirectTo != "" {
		t.Error("CheckRename changed the caller's config")
	}
}

// TestFinishedRedirects pins the sweep's selection: only redirects, and only
// those no client session uses; the ones kept come back with their counts.
func TestFinishedRedirects(t *testing.T) {
	cfg := &config.Config{Aliases: []config.Alias{
		{ID: "alias/n", Targets: targets("openrouter/big", 1)},
		{ID: "alias/r1", RedirectTo: "alias/n"},
		{ID: "alias/r2", RedirectTo: "alias/n"},
	}}
	fin, busy := FinishedRedirects(cfg, map[string]int{"alias/r2": 2, "alias/n": 5})
	if !slices.Equal(fin, []string{"alias/r1"}) || busy["alias/r2"] != 2 || len(busy) != 1 {
		t.Errorf("FinishedRedirects = %v, %v", fin, busy)
	}
}

// TestParseAliasTargets pins the shared target parser: ID, ID:ORDER, a model
// id that holds a colon whole, and the refusals the CLI's flag and the Aliases
// tab's field both give.
func TestParseAliasTargets(t *testing.T) {
	scratchRegistry(t, aliasRegistry)
	cfg := loadCfg(t)
	got, err := ParseAliasTargets(cfg, []string{"openrouter/big:2", "ollama/qwen3:8b", "openrouter/nope:12"})
	want := []config.AliasTarget{{Model: "openrouter/big", Order: 2}, {Model: "ollama/qwen3:8b"}, {Model: "openrouter/nope", Order: 12}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("ParseAliasTargets = %v, %v; want %v", got, err, want)
	}
	for spec, wantErr := range map[string]string{"openrouter/big:0": "whole number from 1", "": "empty"} {
		if _, err := ParseAliasTargets(cfg, []string{spec}); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%q: %v, want %q", spec, err, wantErr)
		}
	}
	if _, err := ParseAliasTargets(cfg, []string{"openrouter/big", "openrouter/big:2"}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("duplicate: %v", err)
	}
}

// TestAliasTargetsTextRoundTrips pins the form's text: what AliasTargetsText
// writes, ParseAliasTargetsText reads back, an ollama tag included.
func TestAliasTargetsTextRoundTrips(t *testing.T) {
	scratchRegistry(t, aliasRegistry)
	cfg := loadCfg(t)
	a := config.Alias{ID: "alias/x", Targets: targets("openrouter/big", 0, "ollama/qwen3:8b", 5)}
	text := AliasTargetsText(cfg, a)
	if text != "openrouter/big:1, ollama/qwen3:8b:5" {
		t.Fatalf("text = %q", text)
	}
	got, err := ParseAliasTargetsText(cfg, text)
	if err != nil || !slices.Equal(got, targets("openrouter/big", 1, "ollama/qwen3:8b", 5)) {
		t.Errorf("round trip = %v, %v", got, err)
	}
	if _, err := ParseAliasTargetsText(cfg, " , "); err == nil {
		t.Error("no targets should be refused")
	}
}

// TestAliasWindowChange pins decision 7's shrink rule in its one home: a new
// alias never shrinks, a smaller or unknown window over a known one does, an
// equal or larger one does not.
func TestAliasWindowChange(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	_ = path
	if _, err := SetAlias(AliasRequest{ID: "alias/x", Targets: targets("openrouter/big", 1)}); err != nil {
		t.Fatal(err)
	}
	cfg := loadCfg(t)
	for name, tc := range map[string]struct {
		id       string
		t        []config.AliasTarget
		shrinks  bool
		had      bool
		old, new int
	}{
		"new alias":    {"alias/y", targets("openrouter/small", 1), false, false, 0, 131072},
		"smaller":      {"alias/x", targets("openrouter/small", 1), true, true, 262144, 131072},
		"unknown":      {"alias/x", targets("openrouter/unknown", 1), true, true, 262144, 0},
		"same":         {"alias/x", targets("openrouter/big", 1), false, true, 262144, 262144},
		"min of group": {"alias/x", targets("openrouter/big", 1, "openrouter/small", 2), true, true, 262144, 131072},
	} {
		wc := AliasWindowChange(cfg, tiers.Index{}, AliasRequest{ID: tc.id, Targets: tc.t})
		if wc.Shrinks != tc.shrinks || wc.Had != tc.had || wc.Old != tc.old || wc.New != tc.new {
			t.Errorf("%s: %+v, want shrinks %v had %v old %d new %d", name, wc, tc.shrinks, tc.had, tc.old, tc.new)
		}
	}
	wc := AliasWindowChange(cfg, tiers.Index{}, AliasRequest{ID: "alias/x", Targets: targets("openrouter/small", 1)})
	if got, want := wc.ShrinkSentence("alias/x", 2), "alias/x's context window would go from 262144 to 131072 tokens; 2 client session(s) use it and keep the window they launched with"; got != want {
		t.Errorf("sentence = %q", got)
	}
	if got := wc.ShrinkSentence("alias/x", -1); strings.Contains(got, "session") {
		t.Errorf("unread sessions should add nothing: %q", got)
	}
}

// TestSetAliasGuardRunsInsideTheWrite pins #689: Guard is handed the
// document's rows with each model's window (RefConfig), and its error refuses
// the write with the file unchanged.
func TestSetAliasGuardRunsInsideTheWrite(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	before := read(t, path)
	r := AliasRequest{ID: "alias/g", Targets: targets("openrouter/big", 1)}
	var seen int
	r.Guard = func(cfg *config.Config) error {
		wc := AliasWindowChange(cfg, tiers.Index{}, r)
		seen = wc.New
		return errors.New("guard says no")
	}
	if _, err := SetAlias(r); err == nil || err.Error() != "guard says no" || read(t, path) != before {
		t.Fatalf("SetAlias = %v; want the guard's refusal and the file unchanged", err)
	}
	if seen != 262144 {
		t.Errorf("the guard read window %d from the document, want 262144", seen)
	}
	r.Guard = func(*config.Config) error { return nil }
	if changed, err := SetAlias(r); err != nil || !changed {
		t.Fatalf("SetAlias with a passing guard = %v, %v", changed, err)
	}
}
