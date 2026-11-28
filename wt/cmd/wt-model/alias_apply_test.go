package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
)

// aliasApplySample is the sample file `wt-model alias apply` reads, held by
// TestAliasApplySampleFileParses.
const aliasApplySample = "../../../docs/contracts/alias-apply.sample.toml"

// threeAliasFile sets three aliases over aliasRegistry's models: one with
// explicit orders, one without, one single target.
const threeAliasFile = `[[aliases]]
id = "alias/a"
targets = [{ model = "openrouter/big", order = 1 }, { model = "openrouter/small", order = 2 }]

[[aliases]]
id = "alias/b"
targets = [{ model = "openrouter/small" }, { model = "openrouter/big" }]

[[aliases]]
id = "alias/c"
targets = [{ model = "openrouter/unknown" }]
`

// writeAliasFile writes body to a file in a fresh directory and returns its
// path.
func writeAliasFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aliases.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// countRouteSync wraps h's route sync (the real one aliasEnv set up) with a
// counter, so a test sees both the rows it writes and how often it ran.
func countRouteSync(h *host) *int {
	calls := 0
	inner := h.syncRoutesAfterWrite
	h.syncRoutesAfterWrite = func(out, errOut io.Writer) string {
		calls++
		return inner(out, errOut)
	}
	return &calls
}

// registryAliasIDs is the alias ids the scratch registry (WT_REGISTRY, set by
// aliasEnv) holds, in file order.
func registryAliasIDs(t *testing.T) []string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load the registry: %v", err)
	}
	var ids []string
	for _, a := range cfg.Aliases {
		ids = append(ids, a.ID)
	}
	return ids
}

// TestAliasApplyCreatesAllFromAFile pins the point of `alias apply`: every
// alias in the file is created in the registry, each says `set alias: X` in
// file order, and the route sync that follows gives each one its database
// rows, so a fresh machine gets its groups from one command.
func TestAliasApplyCreatesAllFromAFile(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistry)
	syncs := countRouteSync(h)
	out, errOut, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, threeAliasFile))
	if err != nil || !strings.HasPrefix(out, "set alias: alias/a\nset alias: alias/b\nset alias: alias/c\n") {
		t.Fatalf("err %v stdout %q stderr %q", err, out, errOut)
	}
	if got := registryAliasIDs(t); !slices.Equal(got, []string{"alias/a", "alias/b", "alias/c"}) {
		t.Fatalf("registry aliases %v", got)
	}
	if *syncs != 1 || len(aliasRows(fake, "alias/a")) != 2 || len(aliasRows(fake, "alias/b")) != 2 || len(aliasRows(fake, "alias/c")) != 1 {
		t.Fatalf("syncs %d rows %v", *syncs, fake.ids())
	}
	// An omitted order stays omitted in the registry, as `alias set --target ID`
	// writes it.
	if reg := mustRead(t, path); !strings.Contains(reg, `{ model = "openrouter/small" }`) {
		t.Errorf("alias/b's omitted orders were spelled out:\n%s", reg)
	}
}

// TestAliasApplyIsIdempotent pins that applying the same file again changes
// nothing: each alias says `no change: X`, the registry is byte-identical and
// no route sync runs, so a replayed file is safe and cheap.
func TestAliasApplyIsIdempotent(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	file := writeAliasFile(t, threeAliasFile)
	if _, _, err := execAlias(t, h, "alias", "apply", file); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, path)
	syncs := stubRouteSync(h, "")
	out, errOut, err := execAlias(t, h, "alias", "apply", file)
	want := "no change: alias/a\nno change: alias/b\nno change: alias/c\n"
	if err != nil || out != want || *syncs != 0 || mustRead(t, path) != before {
		t.Fatalf("err %v stdout %q (want %q) stderr %q syncs %d", err, out, want, errOut, *syncs)
	}
}

// TestAliasApplyDryRunChangesNothing pins --dry-run: each alias's dry-run
// block (the `alias set --dry-run` one) is printed, an alias already equal
// says `no change:`, and neither the registry nor the routes are touched.
func TestAliasApplyDryRunChangesNothing(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistryWithX)
	before := mustRead(t, path)
	syncs := countRouteSync(h)
	file := writeAliasFile(t, `[[aliases]]
id = "alias/x"
targets = [{ model = "openrouter/big" }]

[[aliases]]
id = "alias/y"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small", order = 2 }]
`)
	out, errOut, err := execAlias(t, h, "alias", "apply", file, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s%s", err, out, errOut)
	}
	for _, want := range []string{"no change: alias/x\n", "would set alias: alias/y\n", "  target openrouter/small order 2\n", "context: - -> 131072\n", "row wta:alias/y/", "dry run: nothing was changed\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if mustRead(t, path) != before || *syncs != 0 || len(fake.ids()) != 0 {
		t.Fatalf("a dry run changed something: registry changed %v, syncs %d, rows %v", mustRead(t, path) != before, *syncs, fake.ids())
	}
}

// TestAliasApplySyncsOnce pins the reason apply exists beside `alias set`:
// three changed aliases cost one registry write and exactly one route sync,
// not one per alias.
func TestAliasApplySyncsOnce(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistry)
	syncs := stubRouteSync(h, "")
	if _, _, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, threeAliasFile)); err != nil {
		t.Fatal(err)
	}
	if *syncs != 1 {
		t.Fatalf("route syncs %d, want 1", *syncs)
	}
}

// TestAliasApplyRefusesAnUnknownTopLevelKey pins the typo guard: a file
// holding anything but [[aliases]] tables (a misspelt [[alias]], a stray key)
// is refused whole with exit 1, the error naming the file and the key, and
// nothing is written.
func TestAliasApplyRefusesAnUnknownTopLevelKey(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	before := mustRead(t, path)
	for _, body := range []string{
		"[[alias]]\nid = \"alias/a\"\ntargets = [{ model = \"openrouter/big\" }]\n",
		"version = 1\n" + threeAliasFile,
	} {
		file := writeAliasFile(t, body)
		_, _, err := execAlias(t, h, "alias", "apply", file)
		if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "unknown top-level key") {
			t.Errorf("%q: err %v (exit %d)", body, err, exitcode.Of(err))
		}
	}
	if mustRead(t, path) != before {
		t.Fatal("a refused file changed the registry")
	}
}

// TestAliasApplyRefusesDuplicateIDsInTheFile pins that one id twice in the
// file is refused before anything is asked or written: which of the two the
// user meant cannot be guessed.
func TestAliasApplyRefusesDuplicateIDsInTheFile(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	before := mustRead(t, path)
	file := writeAliasFile(t, threeAliasFile+"\n[[aliases]]\nid = \"alias/a\"\ntargets = [{ model = \"openrouter/small\" }]\n")
	_, _, err := execAlias(t, h, "alias", "apply", file)
	if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), `"alias/a"`) || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("err %v (exit %d)", err, exitcode.Of(err))
	}
	if mustRead(t, path) != before {
		t.Fatal("a refused file changed the registry")
	}
}

// TestAliasApplyRefusesAnEmptyFile pins that a file with no [[aliases]]
// table (empty, or comments only) is an error naming the file, exit 1: an
// apply that silently did nothing would hide a wrong path or a truncated
// file.
func TestAliasApplyRefusesAnEmptyFile(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistry)
	for _, body := range []string{"", "# nothing here\n"} {
		file := writeAliasFile(t, body)
		_, _, err := execAlias(t, h, "alias", "apply", file)
		if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "no [[aliases]]") {
			t.Errorf("%q: err %v (exit %d)", body, err, exitcode.Of(err))
		}
	}
}

// TestAliasApplyRefusesTheWholeFileOnOneBadAlias pins all-or-nothing: one
// alias the registry would refuse (a target that names no model, a malformed
// table) refuses the file, the error names that alias, and the registry is
// byte-identical; the good aliases before it are not written either.
func TestAliasApplyRefusesTheWholeFileOnOneBadAlias(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	before := mustRead(t, path)
	syncs := stubRouteSync(h, "")
	for bad, want := range map[string]string{
		"[[aliases]]\nid = \"alias/bad\"\ntargets = [{ model = \"openrouter/nope\" }]\n":            `alias "alias/bad": target "openrouter/nope" names no model`,
		"[[aliases]]\nid = \"alias/bad\"\ntargets = [{ model = \"openrouter/big\", weight = 2 }]\n": `"alias/bad"`,
		"[[aliases]]\nid = \"alias/bad\"\ntargets = []\n":                                           "at least one target",
		"aliases = [1]\n": "aliases[0] is not a table",
	} {
		body := threeAliasFile + "\n" + bad
		if strings.HasPrefix(bad, "aliases = ") {
			body = bad
		}
		_, _, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, body))
		if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v (exit %d), want %q", bad, err, exitcode.Of(err), want)
		}
	}
	if mustRead(t, path) != before || *syncs != 0 {
		t.Fatalf("a refused file changed the registry (%v) or synced (%d)", mustRead(t, path) != before, *syncs)
	}
}

// aliasRegistryWithRedirect is aliasRegistryWithX plus alias/old, a redirect
// to alias/x (#687).
const aliasRegistryWithRedirect = aliasRegistryWithX + `
[[aliases]]
id = "alias/old"
redirect_to = "alias/x"
`

// TestAliasApplyRefusesARedirectID pins that apply refuses what `alias set`
// refuses for a redirect: giving a redirect id targets would silently end the
// redirect, so the file is refused, naming the alias, and nothing changes. A
// redirect_to written in the file is refused too: apply sets targets only.
func TestAliasApplyRefusesARedirectID(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryWithRedirect)
	before := mustRead(t, path)
	file := writeAliasFile(t, "[[aliases]]\nid = \"alias/old\"\ntargets = [{ model = \"openrouter/small\" }]\n")
	_, _, err := execAlias(t, h, "alias", "apply", file)
	if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), `alias "alias/old" is a redirect`) {
		t.Fatalf("err %v (exit %d)", err, exitcode.Of(err))
	}
	file = writeAliasFile(t, "[[aliases]]\nid = \"alias/new\"\nredirect_to = \"alias/x\"\n")
	_, _, err = execAlias(t, h, "alias", "apply", file)
	if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), "redirect_to") {
		t.Fatalf("redirect_to in the file: err %v (exit %d)", err, exitcode.Of(err))
	}
	if mustRead(t, path) != before {
		t.Fatal("a refused file changed the registry")
	}
}

// aliasRegistryStale is aliasRegistry holding alias/s, whose one target names
// a model the registry no longer has.
const aliasRegistryStale = aliasRegistry + `
[[aliases]]
id = "alias/s"
targets = [{ model = "openrouter/gone" }]
`

// TestAliasApplyDryRunRefusesWhatApplyRefuses pins that a dry run is not more
// lenient than the real run: a stale alias (its target is gone) or a redirect
// id whose file entry matches what the registry reads is refused with the
// real run's error, never reported as `no change:`, which would tell the user
// the file is fine.
func TestAliasApplyDryRunRefusesWhatApplyRefuses(t *testing.T) {
	cases := []struct {
		registry, file, want string
	}{
		{aliasRegistryStale, "[[aliases]]\nid = \"alias/s\"\ntargets = [{ model = \"openrouter/gone\" }]\n", `target "openrouter/gone" names no model`},
		{aliasRegistryWithRedirect, "[[aliases]]\nid = \"alias/old\"\ntargets = [{ model = \"openrouter/big\" }]\n", `alias "alias/old" is a redirect`},
	}
	for _, c := range cases {
		h, _, path := aliasEnv(t, c.registry)
		before := mustRead(t, path)
		file := writeAliasFile(t, c.file)
		dry, _, dryErr := execAlias(t, h, "alias", "apply", file, "--dry-run")
		_, _, realErr := execAlias(t, h, "alias", "apply", file)
		if dryErr == nil || realErr == nil || dryErr.Error() != realErr.Error() || !strings.Contains(dryErr.Error(), c.want) {
			t.Errorf("dry err %v, real err %v, want both %q", dryErr, realErr, c.want)
		}
		if strings.Contains(dry, "no change:") || mustRead(t, path) != before {
			t.Errorf("dry run said %q or the registry changed", dry)
		}
	}
}

// TestAliasApplyIgnoresNotes pins #691's rule for the file: a `notes` string
// on an alias is accepted and not written to the registry (the registry
// reader ignores it too), while a non-string notes is refused as the reader
// refuses it.
func TestAliasApplyIgnoresNotes(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	file := writeAliasFile(t, "[[aliases]]\nid = \"alias/n\"\nnotes = \"the main coding pool\"\ntargets = [{ model = \"openrouter/big\" }]\n")
	out, _, err := execAlias(t, h, "alias", "apply", file)
	if err != nil || !strings.HasPrefix(out, "set alias: alias/n\n") || strings.Contains(mustRead(t, path), "the main coding pool") {
		t.Fatalf("err %v stdout %q registry:\n%s", err, out, mustRead(t, path))
	}
	file = writeAliasFile(t, "[[aliases]]\nid = \"alias/m\"\nnotes = 3\ntargets = [{ model = \"openrouter/big\" }]\n")
	if _, _, err := execAlias(t, h, "alias", "apply", file); err == nil || !strings.Contains(err.Error(), "notes must be a string") {
		t.Fatalf("non-string notes: err %v", err)
	}
}

// aliasRegistryTwoBig is aliasRegistry holding alias/a and alias/b, both
// over openrouter/big (window 262144).
const aliasRegistryTwoBig = aliasRegistry + `
[[aliases]]
id = "alias/a"
targets = [{ model = "openrouter/big" }]

[[aliases]]
id = "alias/b"
targets = [{ model = "openrouter/big" }]
`

// shrinkBothFile makes alias/a smaller (131072) and alias/b unknown.
const shrinkBothFile = `[[aliases]]
id = "alias/a"
targets = [{ model = "openrouter/small" }]

[[aliases]]
id = "alias/b"
targets = [{ model = "openrouter/unknown" }]
`

// TestAliasApplyAsksOnceForSeveralShrinks pins decision 7 for a file: when
// several aliases would get a smaller or unknown window, one question names
// each of them (ShrinkSentence), not one question per alias; with no
// terminal the file is refused with exit 3 naming --accept-smaller-window
// and nothing is changed, and an answered no is exit 3 too.
func TestAliasApplyAsksOnceForSeveralShrinks(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryTwoBig)
	before := mustRead(t, path)
	file := writeAliasFile(t, shrinkBothFile)
	var questions []string
	h.confirmRemove = func(q string) (bool, error) { questions = append(questions, q); return false, nil }
	_, _, err := execAlias(t, h, "alias", "apply", file)
	if err == nil || exitcode.Of(err) != 3 || err.Error() != "approval missing: declined; nothing was changed" {
		t.Fatalf("declined: err %v (exit %d)", err, exitcode.Of(err))
	}
	if len(questions) != 1 || !strings.Contains(questions[0], "alias/a's context window would go from 262144 to 131072 tokens") ||
		!strings.Contains(questions[0], "alias/b's context window would go from 262144 to - tokens") {
		t.Fatalf("questions %q, want one naming both aliases", questions)
	}
	h.confirmRemove = h.realConfirmRemove // testHost's openTTY fails: no terminal
	_, errOut, err := execAlias(t, h, "alias", "apply", file)
	want := "approval missing: pass --accept-smaller-window (there is no terminal to ask); nothing was changed"
	if err == nil || err.Error() != want || exitcode.Of(err) != 3 {
		t.Fatalf("no terminal: err %v (exit %d), want %q", err, exitcode.Of(err), want)
	}
	if !strings.Contains(errOut, "alias/a's context window") || !strings.Contains(errOut, "alias/b's context window") {
		t.Errorf("stderr does not name both shrinks:\n%s", errOut)
	}
	if mustRead(t, path) != before {
		t.Fatal("a refused shrink changed the registry")
	}
}

// TestAliasApplyAcceptSmallerWindowAnswersYes pins that
// --accept-smaller-window lets the shrinking file through with no question,
// and that a yes at the terminal does too.
func TestAliasApplyAcceptSmallerWindowAnswersYes(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryTwoBig)
	file := writeAliasFile(t, shrinkBothFile)
	asked := 0
	h.confirmRemove = func(string) (bool, error) { asked++; return false, nil }
	out, _, err := execAlias(t, h, "alias", "apply", file, "--accept-smaller-window")
	if err != nil || !strings.HasPrefix(out, "set alias: alias/a\nset alias: alias/b\n") || asked != 0 {
		t.Fatalf("err %v stdout %q asked %d", err, out, asked)
	}
	if !strings.Contains(mustRead(t, path), `{ model = "openrouter/unknown" }`) {
		t.Fatalf("registry not written:\n%s", mustRead(t, path))
	}
	h2, _, _ := aliasEnv(t, aliasRegistryTwoBig)
	h2.confirmRemove = func(string) (bool, error) { return true, nil }
	if out, _, err := execAlias(t, h2, "alias", "apply", file); err != nil || !strings.HasPrefix(out, "set alias: alias/a\n") {
		t.Fatalf("answered yes: err %v stdout %q", err, out)
	}
}

// TestAliasApplyRoutesUnavailableStillWritesTheRegistry pins decision 4 for
// apply: with LiteLLM's database unreachable the registry is still written,
// one warning names the repair and the command exits 0, as `alias set` does.
func TestAliasApplyRoutesUnavailableStillWritesTheRegistry(t *testing.T) {
	h, fake, _ := aliasEnv(t, aliasRegistry)
	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	syncs := countRouteSync(h)
	out, errOut, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, threeAliasFile))
	if err != nil || !strings.HasPrefix(out, "set alias: alias/a\n") {
		t.Fatalf("err %v stdout %q stderr %q", err, out, errOut)
	}
	if n := strings.Count(errOut, "alias routes unavailable"); n != 1 || !strings.Contains(errOut, "wt-model litellm sync") {
		t.Fatalf("stderr has %d unavailable warnings, want 1:\n%s", n, errOut)
	}
	if *syncs != 1 || !slices.Equal(registryAliasIDs(t), []string{"alias/a", "alias/b", "alias/c"}) || len(fake.ids()) != 0 {
		t.Fatalf("syncs %d registry %v rows %v", *syncs, registryAliasIDs(t), fake.ids())
	}
}

// sampleRegistry holds the placeholder models docs/contracts/
// alias-apply.sample.toml names.
const sampleRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_ALIAS_OPENROUTER_KEY"
base_url = "https://openrouter.ai/api/v1"

[[models]]
id = "openrouter/example-large"
family = "example"
provider_id = "openrouter"
model_name = "example/large"
tags = []

[[models]]
id = "openrouter/example-small"
family = "example"
provider_id = "openrouter"
model_name = "example/small"
tags = []
`

// TestAliasApplySampleFileParses holds docs/contracts/alias-apply.sample.toml,
// the file the docs point users at: it parses through parseAliasFile into
// alias/example-pro (explicit orders) and an alias with orders left out, and
// applies cleanly to a registry holding its placeholder models, then again as
// no change.
func TestAliasApplySampleFileParses(t *testing.T) {
	data, err := os.ReadFile(aliasApplySample)
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := parseAliasFile(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var explicit, omitted bool
	for _, r := range reqs {
		for _, tg := range r.Targets {
			explicit = explicit || tg.Order > 0
			omitted = omitted || tg.Order == 0
		}
	}
	if len(reqs) < 2 || reqs[0].ID != "alias/example-pro" || len(reqs[0].Targets) != 2 || !explicit || !omitted {
		t.Fatalf("sample reads as %+v; want alias/example-pro over two targets first, and both an explicit and an omitted order", reqs)
	}
	h, _, _ := aliasEnv(t, sampleRegistry)
	stubRouteSync(h, "")
	out, errOut, err := execAlias(t, h, "alias", "apply", aliasApplySample)
	if err != nil || strings.Count(out, "set alias: ") != len(reqs) {
		t.Fatalf("apply: err %v stdout %q stderr %q", err, out, errOut)
	}
	out, _, err = execAlias(t, h, "alias", "apply", aliasApplySample)
	if err != nil || strings.Count(out, "no change: ") != len(reqs) {
		t.Fatalf("second apply: err %v stdout %q", err, out)
	}
}

// aliasRegistryMalformed is aliasRegistry holding alias/m, frozen by a target
// row whose order is 0; the reader still reads its target as openrouter/big
// at position 1.
const aliasRegistryMalformed = aliasRegistry + `
[[aliases]]
id = "alias/m"
targets = [{ model = "openrouter/big", order = 0 }]
`

// repairFile is the corrected alias/m: the same effective target as the
// malformed registry alias, spelled validly.
const repairFile = "[[aliases]]\nid = \"alias/m\"\ntargets = [{ model = \"openrouter/big\", order = 1 }]\n"

// aliasMalformed is what the registry reader says of alias/m now ("" for a
// well-formed alias).
func aliasMalformed(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, ok := cfg.AliasByID("alias/m")
	if !ok {
		t.Fatal("alias/m is gone")
	}
	return a.Malformed
}

// TestAliasApplyRepairsAMalformedAlias pins that apply repairs an alias the
// registry holds malformed (frozen routes) even when its readable targets
// already mean what the file says: it prints `set alias:`, not `no change:`,
// exits 0, runs one route sync and the alias reads well-formed afterwards, as
// `alias set` with the same targets would leave it.
func TestAliasApplyRepairsAMalformedAlias(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistryMalformed)
	if aliasMalformed(t) == "" {
		t.Fatal("the fixture alias is not malformed")
	}
	syncs := countRouteSync(h)
	out, errOut, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, repairFile))
	if err != nil || !strings.HasPrefix(out, "set alias: alias/m\n") || strings.Contains(out, "no change:") {
		t.Fatalf("err %v (exit %d) stdout %q stderr %q", err, exitcode.Of(err), out, errOut)
	}
	if *syncs != 1 {
		t.Errorf("route syncs %d, want 1", *syncs)
	}
	if m := aliasMalformed(t); m != "" {
		t.Errorf("alias/m is still malformed: %s", m)
	}
}

// TestAliasApplyDryRunShowsTheRepairNotNoChange pins that the dry run agrees
// with the real run on a malformed alias: it prints the alias's would-set
// block, never `no change:`, which would tell the user the frozen alias is
// fine, and changes nothing.
func TestAliasApplyDryRunShowsTheRepairNotNoChange(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryMalformed)
	before := mustRead(t, path)
	out, errOut, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, repairFile), "--dry-run")
	if err != nil || !strings.Contains(out, "would set alias: alias/m\n") || strings.Contains(out, "no change:") {
		t.Fatalf("err %v stdout %q stderr %q", err, out, errOut)
	}
	if mustRead(t, path) != before {
		t.Fatal("a dry run changed the registry")
	}
}

// TestAliasListTOMLNoteThenApplyRepairsTheAlias pins the user's repair path:
// `alias list --toml` leaves a malformed alias out with a stderr note naming
// it, the user writes the corrected table to a file, `alias apply` repairs
// it, and the next `alias list --toml` prints it with no note.
func TestAliasListTOMLNoteThenApplyRepairsTheAlias(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistryMalformed)
	out, errOut, err := execAlias(t, h, "alias", "list", "--toml")
	if err != nil || out != "" || !strings.Contains(errOut, "wt-model: note: 1 alias skipped: alias/m (malformed: ") {
		t.Fatalf("list --toml: err %v stdout %q stderr %q", err, out, errOut)
	}
	stubRouteSync(h, "")
	if out, errOut, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, repairFile)); err != nil || !strings.HasPrefix(out, "set alias: alias/m\n") {
		t.Fatalf("apply: err %v stdout %q stderr %q", err, out, errOut)
	}
	out, errOut, err = execAlias(t, h, "alias", "list", "--toml")
	if err != nil || !strings.Contains(out, `id = "alias/m"`) || errOut != "" {
		t.Fatalf("list --toml after the repair: err %v stdout %q stderr %q", err, out, errOut)
	}
}

// TestAliasApplyShrinkIsRecheckedInsideTheWrite is
// TestAliasSetShrinkIsRecheckedInsideTheWrite for a file (#689): the shrinks
// approved at the one question are (old, new) pairs read before the lock, so
// when another program changes a window while the question is open, the
// write refuses with the re-check's error (its own "nothing was changed",
// not a second one), no alias of the file is written (the other alias's
// approved shrink included), no sync runs, and the registry holds only that
// other program's change.
func TestAliasApplyShrinkIsRecheckedInsideTheWrite(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryTwoBig)
	stubSessions(h, nil)
	syncs := stubRouteSync(h, "")
	var afterMutation string
	// Approved: alias/a 262144 -> 131072, alias/b 262144 -> unknown. During
	// the question openrouter/small shrinks to 50000.
	h.confirmRemove = func(string) (bool, error) {
		b := strings.Replace(mustRead(t, path), "context_window = 131072", "context_window = 50000", 1)
		if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		afterMutation = b
		return true, nil
	}
	_, _, err := execAlias(t, h, "alias", "apply", writeAliasFile(t, shrinkBothFile))
	if err == nil || exitcode.Of(err) != 1 || !strings.Contains(err.Error(), "alias/a's context window would now go from 262144 to 50000") ||
		!strings.Contains(err.Error(), "you approved 262144 to 131072") || !strings.Contains(err.Error(), "the window changed since the check; nothing was changed, rerun") ||
		strings.Count(err.Error(), "nothing was changed") != 1 {
		t.Fatalf("err = %v (exit %d), want the re-check refusal naming both windows, nothing was changed once", err, exitcode.Of(err))
	}
	if got := mustRead(t, path); afterMutation == "" || got != afterMutation || *syncs != 0 {
		t.Errorf("the registry changed after a refused re-check (syncs %d):\n%s", *syncs, got)
	}
}

// TestAliasApplyDryRunPrintsOneFooter pins that `apply --dry-run` ends with
// exactly one `dry run: nothing was changed` line, whether several aliases
// would change (one block each, not one footer each) or none would (every
// alias `no change:`, so the user still reads that nothing was written).
func TestAliasApplyDryRunPrintsOneFooter(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistry)
	stubRouteSync(h, "")
	file := writeAliasFile(t, threeAliasFile)
	out, _, err := execAlias(t, h, "alias", "apply", file, "--dry-run")
	if err != nil || strings.Count(out, "would set alias: ") != 3 || strings.Count(out, "dry run: nothing was changed\n") != 1 ||
		!strings.HasSuffix(out, "dry run: nothing was changed\n") {
		t.Fatalf("three changes: err %v stdout:\n%s", err, out)
	}
	if _, _, err := execAlias(t, h, "alias", "apply", file); err != nil {
		t.Fatal(err)
	}
	out, _, err = execAlias(t, h, "alias", "apply", file, "--dry-run")
	want := "no change: alias/a\nno change: alias/b\nno change: alias/c\ndry run: nothing was changed\n"
	if err != nil || out != want {
		t.Fatalf("all equal: err %v stdout %q, want %q", err, out, want)
	}
}

// TestAliasApplyErrorsEndInNothingWasChanged pins the one shape of every
// refusal of `apply`, `<file>: <why>; nothing was changed`, on the two paths
// outside the file's own parse and first check: a file that cannot be read,
// and a refusal from inside the registry write (a target model another
// program removed while the shrink question was open, caught by the write's
// own re-check). A user can then tell from the message alone that the
// registry was not touched.
func TestAliasApplyErrorsEndInNothingWasChanged(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryTwoBig)
	missing := filepath.Join(t.TempDir(), "absent.toml")
	_, _, err := execAlias(t, h, "alias", "apply", missing)
	if err == nil || exitcode.Of(err) != 1 || !strings.HasPrefix(err.Error(), missing+": ") || !strings.HasSuffix(err.Error(), "; nothing was changed") {
		t.Fatalf("unreadable file: err %v (exit %d)", err, exitcode.Of(err))
	}
	stubSessions(h, nil)
	syncs := stubRouteSync(h, "")
	var afterMutation string
	h.confirmRemove = func(string) (bool, error) {
		b := strings.Replace(mustRead(t, path), "[[models]]\nid = \"openrouter/unknown\"\nfamily = \"f\"\nprovider_id = \"openrouter\"\nmodel_name = \"org/unknown\"\ntags = []\n", "", 1)
		if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
		afterMutation = b
		return true, nil
	}
	file := writeAliasFile(t, shrinkBothFile)
	_, _, err = execAlias(t, h, "alias", "apply", file)
	if err == nil || exitcode.Of(err) != 1 || !strings.HasPrefix(err.Error(), file+": ") || !strings.Contains(err.Error(), `"openrouter/unknown" names no model`) ||
		!strings.HasSuffix(err.Error(), "; nothing was changed") || strings.Count(err.Error(), "nothing was changed") != 1 {
		t.Fatalf("in-write refusal: err %v (exit %d)", err, exitcode.Of(err))
	}
	if afterMutation == "" || mustRead(t, path) != afterMutation || *syncs != 0 {
		t.Errorf("the registry changed after an in-write refusal (syncs %d)", *syncs)
	}
}

// TestAliasApplyAcceptsBOMCRLFAndSubTables pins that the spellings an editor
// or a hand-written file may produce all read as the same aliases: a UTF-8
// byte-order mark, CRLF line endings, and targets written as
// [[aliases.targets]] sub-tables instead of inline tables. Each applies
// cleanly, reads well-formed, and a second apply is `no change`.
func TestAliasApplyAcceptsBOMCRLFAndSubTables(t *testing.T) {
	subTables := "[[aliases]]\nid = \"alias/a\"\n\n[[aliases.targets]]\nmodel = \"openrouter/big\"\norder = 1\n\n" +
		"[[aliases.targets]]\nmodel = \"openrouter/small\"\norder = 2\n"
	for name, body := range map[string]string{
		"utf-8 bom":  "\xef\xbb\xbf" + threeAliasFile,
		"crlf":       strings.ReplaceAll(threeAliasFile, "\n", "\r\n"),
		"sub-tables": subTables,
	} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := aliasEnv(t, aliasRegistry)
			stubRouteSync(h, "")
			file := writeAliasFile(t, body)
			out, errOut, err := execAlias(t, h, "alias", "apply", file)
			if err != nil || !strings.HasPrefix(out, "set alias: alias/a\n") {
				t.Fatalf("apply: err %v stdout %q stderr %q", err, out, errOut)
			}
			cfg, lerr := config.Load()
			if lerr != nil {
				t.Fatal(lerr)
			}
			want := []config.AliasTarget{{Model: "openrouter/big", Order: 1}, {Model: "openrouter/small", Order: 2}}
			if a, _ := cfg.AliasByID("alias/a"); a.Malformed != "" || !slices.Equal(a.Targets, want) {
				t.Errorf("alias/a reads as %+v, want targets %v", a, want)
			}
			if out, _, err := execAlias(t, h, "alias", "apply", file); err != nil || strings.Contains(out, "set alias:") {
				t.Errorf("second apply: err %v stdout %q", err, out)
			}
		})
	}
}
