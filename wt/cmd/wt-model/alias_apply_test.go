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

// aliasIDsIn is the alias ids the registry at path holds, in file order.
func aliasIDsIn(t *testing.T, path string) []string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
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
	if got := aliasIDsIn(t, path); !slices.Equal(got, []string{"alias/a", "alias/b", "alias/c"}) {
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
	h, fake, path := aliasEnv(t, aliasRegistry)
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
	if *syncs != 1 || !slices.Equal(aliasIDsIn(t, path), []string{"alias/a", "alias/b", "alias/c"}) || len(fake.ids()) != 0 {
		t.Fatalf("syncs %d registry %v rows %v", *syncs, aliasIDsIn(t, path), fake.ids())
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
