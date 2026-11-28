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

[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"

[providers.auth]
type = "none"

[[models]]
id = "mtplx/m1"
family = "f"
provider_id = "mtplx"
model_name = "m1"
tags = []

[[models]]
id = "mtplx/m2"
family = "f"
provider_id = "mtplx"
model_name = "m2"
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
// two local targets of one Exclusive family (#690; two of a Shared or Pool
// family are sound, pinned below). Each would be written as an alias every route
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
		"two local":    {AliasRequest{ID: "alias/main", Targets: targets("mtplx/m1", 1, "mtplx/m2", 2)}, "both served by the mtplx family"},
	} {
		if err := CheckAlias(cfg, tc.r); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: CheckAlias = %v, want %q", name, err, tc.want)
		}
	}
	if err := CheckAlias(cfg, AliasRequest{ID: "alias/main", Targets: targets("openrouter/big", 1, "ollama/qwen3:8b", 2)}); err != nil {
		t.Errorf("a sound alias: %v", err)
	}
	// #690: two local targets of a Shared family (ollama) are allowed.
	if err := CheckAlias(cfg, AliasRequest{ID: "alias/main", Targets: targets("ollama/gemma4:9b", 1, "ollama/qwen3:8b", 2)}); err != nil {
		t.Errorf("two ollama local targets: %v", err)
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
	got, err := ParseAliasTargets(cfg, []string{"openrouter/big:2", "ollama/qwen3:8b", "openrouter/nope"})
	want := []config.AliasTarget{{Model: "openrouter/big", Order: 2}, {Model: "ollama/qwen3:8b"}, {Model: "openrouter/nope"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("ParseAliasTargets = %v, %v; want %v", got, err, want)
	}
	// #691 decision 2: a :digits suffix splits only off a registered base; an
	// unregistered spec whose base is no model either is refused quoting the
	// whole value, not read as an order of a model that does not exist.
	for spec, wantErr := range map[string]string{
		"openrouter/big:0":   "whole number from 1",
		"":                   "empty",
		"ollama/qwen3:8":     "--target ollama/qwen3:8: not a registry model (an order is written ID:ORDER with a registered ID)",
		"openrouter/nope:12": "--target openrouter/nope:12: not a registry model",
	} {
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

// aliasIDs is the ids of the registry's aliases, in file order.
func aliasIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, a := range loadCfg(t).Aliases {
		ids = append(ids, a.ID)
	}
	return ids
}

// TestSetAliasesWritesAllInOneUpdate pins that SetAliases writes several new
// aliases in one registry update and reports each id it wrote in request
// order: `alias apply` promises one write, so a file edited mid-way can never
// hold half a set.
func TestSetAliasesWritesAllInOneUpdate(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	reqs := []AliasRequest{
		{ID: "alias/a", Targets: targets("openrouter/big", 1)},
		{ID: "alias/b", Targets: targets("openrouter/small", 1, "ollama/qwen3:8b", 2)},
		{ID: "alias/c", Targets: targets("openrouter/unknown", 0)},
	}
	changed, err := SetAliases(reqs)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alias/a", "alias/b", "alias/c"}; !slices.Equal(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	if got := aliasIDs(t); !slices.Equal(got, []string{"alias/a", "alias/b", "alias/c"}) {
		t.Fatalf("registry aliases = %v\n%s", got, read(t, path))
	}
	if b, _ := loadCfg(t).AliasByID("alias/b"); !slices.Equal(b.Targets, targets("openrouter/small", 1, "ollama/qwen3:8b", 2)) {
		t.Errorf("alias/b targets = %v", b.Targets)
	}
}

// TestSetAliasesRefusesAllWhenOneFails pins all-or-nothing: when the second of
// three requests names a model the registry lacks, the whole call fails with
// an error naming that alias and the registry bytes are exactly as before, so
// a bad line in a file never leaves its good neighbours half applied.
func TestSetAliasesRefusesAllWhenOneFails(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	before := read(t, path)
	changed, err := SetAliases([]AliasRequest{
		{ID: "alias/a", Targets: targets("openrouter/big", 1)},
		{ID: "alias/bad", Targets: targets("openrouter/nope", 1)},
		{ID: "alias/c", Targets: targets("openrouter/small", 1)},
	})
	if err == nil || !strings.Contains(err.Error(), "alias/bad") {
		t.Fatalf("SetAliases = %v, %v; want an error naming alias/bad", changed, err)
	}
	if changed != nil {
		t.Errorf("changed = %v, want none", changed)
	}
	if read(t, path) != before {
		t.Errorf("the registry changed:\n%s", read(t, path))
	}
}

// TestSetAliasesSkipsAnAliasWhoseEffectiveTargetsMatch pins the no-op rule: an
// alias written without orders equals a request that spells the positions out,
// so it is not in changed and the registry bytes stay (the file keeps its own
// spelling); a request with a different order for one target is written.
func TestSetAliasesSkipsAnAliasWhoseEffectiveTargetsMatch(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	if _, err := SetAlias(AliasRequest{ID: "alias/main", Targets: targets("openrouter/big", 0, "openrouter/small", 0)}); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)
	changed, err := SetAliases([]AliasRequest{{ID: "alias/main", Targets: targets("openrouter/big", 1, "openrouter/small", 2)}})
	if err != nil || len(changed) != 0 {
		t.Fatalf("SetAliases = %v, %v; want nothing changed", changed, err)
	}
	if read(t, path) != before {
		t.Errorf("an equal alias rewrote the file:\n%s", read(t, path))
	}
	changed, err = SetAliases([]AliasRequest{{ID: "alias/main", Targets: targets("openrouter/big", 1, "openrouter/small", 3)}})
	if err != nil || !slices.Equal(changed, []string{"alias/main"}) {
		t.Fatalf("SetAliases with another order = %v, %v", changed, err)
	}
	if a, _ := loadCfg(t).AliasByID("alias/main"); !slices.Equal(a.Targets, targets("openrouter/big", 1, "openrouter/small", 3)) {
		t.Errorf("targets = %v", a.Targets)
	}
}

// TestSetAliasesIgnoresListOrderAmongEqualOrders pins that two targets that
// share an order are a set: the same pair listed the other way round is equal,
// so re-applying a file that only reshuffled such a pair writes nothing.
func TestSetAliasesIgnoresListOrderAmongEqualOrders(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	if _, err := SetAlias(AliasRequest{ID: "alias/main", Targets: targets("openrouter/big", 1, "openrouter/small", 1)}); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)
	changed, err := SetAliases([]AliasRequest{{ID: "alias/main", Targets: targets("openrouter/small", 1, "openrouter/big", 1)}})
	if err != nil || len(changed) != 0 || read(t, path) != before {
		t.Fatalf("SetAliases = %v, %v, file changed %v; want a no-op", changed, err, read(t, path) != before)
	}
}

// TestSetAliasesRefusesARedirect pins that an id that is currently a redirect
// (#687) refuses the whole call, naming it, with the file unchanged: a redirect
// has no targets of its own, and a good alias beside it must not be written.
func TestSetAliasesRefusesARedirect(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	if _, err := SetAlias(AliasRequest{ID: "alias/new", Targets: targets("openrouter/big", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := RenameAlias(RenameRequest{Old: "alias/new", New: "alias/newer"}); err != nil {
		t.Fatal(err)
	}
	before := read(t, path)
	_, err := SetAliases([]AliasRequest{
		{ID: "alias/fresh", Targets: targets("openrouter/small", 1)},
		{ID: "alias/new", Targets: targets("openrouter/small", 1)},
	})
	if err == nil || !strings.Contains(err.Error(), "alias/new") || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("SetAliases = %v; want a refusal naming the redirect alias/new", err)
	}
	if read(t, path) != before {
		t.Errorf("the registry changed:\n%s", read(t, path))
	}
}

// TestSetAliasesRefusesDuplicateIDsInTheRequests pins that one id twice in a
// call is a plain error naming it, with the file unchanged: the second would
// silently win over the first, which is never what a file of aliases means.
func TestSetAliasesRefusesDuplicateIDsInTheRequests(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	before := read(t, path)
	_, err := SetAliases([]AliasRequest{
		{ID: "alias/a", Targets: targets("openrouter/big", 1)},
		{ID: "alias/a", Targets: targets("openrouter/small", 1)},
	})
	if err == nil || !strings.Contains(err.Error(), "alias/a") || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("SetAliases = %v; want an error naming alias/a given more than once", err)
	}
	if read(t, path) != before {
		t.Errorf("the registry changed:\n%s", read(t, path))
	}
}

// TestSetAliasesGuardRunsInsideTheWrite pins that a request's Guard runs
// against the document being written and that its error refuses every request,
// the earlier good ones included, with the file unchanged (the shrink re-check
// of #689 must hold for a batch too).
func TestSetAliasesGuardRunsInsideTheWrite(t *testing.T) {
	path := scratchRegistry(t, aliasRegistry)
	before := read(t, path)
	var seen int
	second := AliasRequest{ID: "alias/g", Targets: targets("openrouter/big", 1)}
	second.Guard = func(cfg *config.Config) error {
		seen = AliasWindowChange(cfg, tiers.Index{}, second).New
		return errors.New("guard says no")
	}
	_, err := SetAliases([]AliasRequest{{ID: "alias/a", Targets: targets("openrouter/small", 1)}, second})
	if err == nil || err.Error() != "guard says no" || read(t, path) != before {
		t.Fatalf("SetAliases = %v; want the guard's refusal and the file unchanged", err)
	}
	if seen != 262144 {
		t.Errorf("the guard read window %d from the document, want 262144", seen)
	}
}

// TestAliasTargetsEqual pins the comparison the writer and the dry run share:
// an omitted order is the 1-based position, equal orders are a set, a missing
// alias never equals, and different models or orders differ.
func TestAliasTargetsEqual(t *testing.T) {
	scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/main"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small" }]

[[aliases]]
id = "alias/tie"
targets = [{ model = "openrouter/big", order = 1 }, { model = "openrouter/small", order = 1 }]
`)
	cfg := loadCfg(t)
	for name, tc := range map[string]struct {
		id   string
		ts   []config.AliasTarget
		want bool
	}{
		"positions spelled out": {"alias/main", targets("openrouter/big", 1, "openrouter/small", 2), true},
		"omitted orders":        {"alias/main", targets("openrouter/big", 0, "openrouter/small", 0), true},
		"swapped orders":        {"alias/main", targets("openrouter/big", 2, "openrouter/small", 1), false},
		"another model":         {"alias/main", targets("openrouter/big", 1, "openrouter/unknown", 2), false},
		"fewer targets":         {"alias/main", targets("openrouter/big", 1), false},
		"tie either way":        {"alias/tie", targets("openrouter/small", 1, "openrouter/big", 1), true},
		"tie with another rank": {"alias/tie", targets("openrouter/big", 1, "openrouter/small", 2), false},
		"no such alias":         {"alias/none", targets("openrouter/big", 1), false},
	} {
		if got := AliasTargetsEqual(cfg, tc.id, tc.ts); got != tc.want {
			t.Errorf("%s: AliasTargetsEqual = %v, want %v", name, got, tc.want)
		}
	}
}

// TestAliasTargetsEqualIsFalseForAMalformedOrRedirectAlias pins that "equal"
// is never said of an alias that is not an ordinary, well-formed one: the
// reader keeps the targets it could read from a malformed alias (an order of
// 0 read as the position, a target with an unknown key) and a redirect reads
// as its target alias's targets, so a plain comparison says "equal" and
// `alias apply` would skip the rewrite that repairs a frozen alias.
func TestAliasTargetsEqualIsFalseForAMalformedOrRedirectAlias(t *testing.T) {
	scratchRegistry(t, aliasRegistry+`
[[aliases]]
id = "alias/main"
targets = [{ model = "openrouter/big" }]

[[aliases]]
id = "alias/zero"
targets = [{ model = "openrouter/big", order = 0 }]

[[aliases]]
id = "alias/weight"
targets = [{ model = "openrouter/big", weight = 2 }]

[[aliases]]
id = "alias/old"
redirect_to = "alias/main"
`)
	cfg := loadCfg(t)
	for name, tc := range map[string]struct {
		id   string
		want bool
	}{
		"well-formed control":      {"alias/main", true},
		"order 0 (malformed)":      {"alias/zero", false},
		"unknown key in a target":  {"alias/weight", false},
		"redirect to a same alias": {"alias/old", false},
	} {
		if got := AliasTargetsEqual(cfg, tc.id, targets("openrouter/big", 1)); got != tc.want {
			t.Errorf("%s: AliasTargetsEqual = %v, want %v", name, got, tc.want)
		}
	}
}

// TestSetAliasesRewritesAMalformedAliasWhoseTargetsMatch pins the repair path
// of `alias apply`: a registry alias frozen by a faulty target row (order 0,
// or an unknown key) whose readable targets already match the request is
// rewritten, reported in changed, and reads well-formed afterwards, as
// `alias set` with the same targets repairs it. Skipping it as "no change"
// would leave its routes frozen while the command said all was well.
func TestSetAliasesRewritesAMalformedAliasWhoseTargetsMatch(t *testing.T) {
	for name, row := range map[string]string{
		"order 0":     `{ model = "openrouter/big", order = 0 }`,
		"unknown key": `{ model = "openrouter/big", weight = 2 }`,
	} {
		t.Run(name, func(t *testing.T) {
			scratchRegistry(t, aliasRegistry+"\n[[aliases]]\nid = \"alias/m\"\ntargets = ["+row+"]\n")
			if a, _ := loadCfg(t).AliasByID("alias/m"); a.Malformed == "" {
				t.Fatal("the fixture alias is not malformed")
			}
			changed, err := SetAliases([]AliasRequest{{ID: "alias/m", Targets: targets("openrouter/big", 1)}})
			if err != nil || !slices.Equal(changed, []string{"alias/m"}) {
				t.Fatalf("SetAliases = %v, %v; want [alias/m] changed", changed, err)
			}
			if a, _ := loadCfg(t).AliasByID("alias/m"); a.Malformed != "" || !slices.Equal(a.Targets, targets("openrouter/big", 1)) {
				t.Errorf("after the write: malformed %q targets %v", a.Malformed, a.Targets)
			}
		})
	}
}

// aliasRegistryForExport is aliasRegistry holding one good alias without
// explicit orders, one with explicit orders, a redirect, a malformed alias
// (an unknown key) and a faulty one (a target naming no model).
const aliasRegistryForExport = aliasRegistry + `
[[aliases]]
id = "alias/a"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small" }]

[[aliases]]
id = "alias/b"
targets = [{ model = "openrouter/small", order = 2 }, { model = "openrouter/big", order = 1 }]

[[aliases]]
id = "alias/old"
redirect_to = "alias/a"

[[aliases]]
id = "alias/bad"
targets = [{ model = "openrouter/big" }]
colour = "red"

[[aliases]]
id = "alias/gone"
targets = [{ model = "openrouter/missing" }]
`

// TestAliasesTOMLPrintsExplicitOrders pins that an alias the registry spells
// without orders is exported with each target's effective order spelled out
// (order = 1, order = 2), so a replay on another machine means the same
// priority whatever spelling the source registry used.
func TestAliasesTOMLPrintsExplicitOrders(t *testing.T) {
	scratchRegistry(t, aliasRegistryForExport)
	text, _, err := AliasesTOML(loadCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`id = "alias/a"`,
		`{ model = "openrouter/big", order = 1 }`,
		`{ model = "openrouter/small", order = 2 }`,
		"[[aliases]]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("export lacks %q:\n%s", want, text)
		}
	}
}

// TestAliasesTOMLSkipsRedirectsMalformedAndFaulty pins that the export holds
// only aliases `alias apply` would accept: a redirect, a malformed alias and
// one with a target naming no model are returned in skipped, each with a
// reason, and none is printed, so the output always replays cleanly.
func TestAliasesTOMLSkipsRedirectsMalformedAndFaulty(t *testing.T) {
	scratchRegistry(t, aliasRegistryForExport)
	text, skipped, err := AliasesTOML(loadCfg(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 3 || skipped[0].ID != "alias/old" || skipped[1].ID != "alias/bad" || skipped[2].ID != "alias/gone" {
		t.Fatalf("skipped = %+v", skipped)
	}
	for _, s := range skipped {
		if s.Why == "" || strings.Contains(s.Why, "\n") {
			t.Errorf("skipped %s: reason %q", s.ID, s.Why)
		}
	}
	for _, id := range []string{"alias/old", "alias/bad", "alias/gone"} {
		if strings.Contains(text, id) {
			t.Errorf("export prints skipped %s:\n%s", id, text)
		}
	}
	if !strings.Contains(text, `"alias/a"`) || !strings.Contains(text, `"alias/b"`) {
		t.Errorf("export lacks the good aliases:\n%s", text)
	}
}
