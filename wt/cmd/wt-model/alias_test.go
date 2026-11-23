package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/servestate"
)

// aliasKeySentinel is what the openrouter provider's secret_ref resolves to
// in these tests: a cloud target's alias row carries it in litellm_params
// (decision 13), so it must never reach a printed line or a JSON document.
const aliasKeySentinel = "sk-or-SENTINEL-must-never-print-7f3a"

// aliasRegistry is the registry the alias command tests start from: three
// OpenRouter models, two with a context_window and one without.
const aliasRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_ALIAS_OPENROUTER_KEY"
base_url = "https://openrouter.ai/api/v1"

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
`

// storeYAML is a settled config.yaml with database models on.
const storeYAML = "model_list: []\n" +
	"litellm_settings:\n  drop_params: true\n  use_chat_completions_url_for_anthropic_messages: true\n" +
	"general_settings:\n  cancel_on_disconnect: true\n  store_model_in_db: true\n"

// aliasEnv is a throwaway machine for the alias commands: the registry
// (WT_REGISTRY) and config.yaml (WT_LITELLM_CONFIG) in a scratch directory,
// the proxy restart `true`, the OpenRouter key the sentinel, a fake LiteLLM
// model API behind h.adminFor and the real route sync over an empty
// inventory. It returns the host, the fake and the registry's path.
func aliasEnv(t *testing.T, registry string) (*host, *fakeModelAPI, string) {
	t.Helper()
	dir := t.TempDir()
	withCleanConfigEnv(t, dir)
	// The wt-model home is redirected with the registry, as a scratch run
	// must: beside the default home the alias half deletes no row
	// (litellm's scratchRegistryRealProxy).
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	path := filepath.Join(dir, "scratch", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_REGISTRY", path)
	litellmEnv(t, storeYAML)
	t.Setenv("WT_TEST_ALIAS_OPENROUTER_KEY", aliasKeySentinel)
	fake, srv := newFakeModelAPI(t)
	h := testHost()
	realRouteSync(t, h)
	h.adminFor = func(*config.Config) *litellm.AdminClient { return adminFromPair(srv.URL, "sk-test") }
	return h, fake, path
}

// execAlias is execWTModel as a new process: h's cached host config is
// dropped first, so each command line reads the registry as it is now.
func execAlias(t *testing.T, h *host, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	h.hostSet = false
	return execWTModel(t, h, args...)
}

// stubSessions makes h's client-session read answer counts (nil: no server
// record).
func stubSessions(h *host, counts map[string]int) {
	h.clientSessions = func(context.Context, io.Writer, servestate.Store) func() map[string]int {
		return func() map[string]int { return counts }
	}
}

// aliasRows is the fake's alias row ids for alias.
func aliasRows(f *fakeModelAPI, alias string) []string {
	var out []string
	for _, id := range f.ids() {
		if strings.HasPrefix(id, litellm.DBAliasIDPrefix+strings.TrimPrefix(alias, "alias/")+"/") {
			out = append(out, id)
		}
	}
	return out
}

// TestAliasSetWritesAndSyncs pins `wt-model alias set` (decision 9): the
// alias is written to the registry, `set alias: <id>` is printed, and the
// one route sync that follows adds one database row per live target, so the
// alias is routed with no restart and no further command.
func TestAliasSetWritesAndSyncs(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistry)
	out, errOut, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big", "--target", "openrouter/small:2")
	if err != nil || !strings.HasPrefix(out, "set alias: alias/x\n") {
		t.Fatalf("err %v stdout %q stderr %q", err, out, errOut)
	}
	if !strings.Contains(mustRead(t, path), "id = \"alias/x\"") {
		t.Fatalf("registry lacks the alias:\n%s", mustRead(t, path))
	}
	rows := aliasRows(fake, "alias/x")
	if len(rows) != 2 || !strings.HasSuffix(rows[0], "/openrouter/big") && !strings.HasSuffix(rows[1], "/openrouter/big") {
		t.Fatalf("alias rows %v, want one per target", rows)
	}
}

// TestAliasSetNoChange pins that the same set again writes nothing and says
// `no change: <id>`, with no route sync (as `model edit`).
func TestAliasSetNoChange(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	if _, _, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big"); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, path)
	syncs := stubRouteSync(h, "")
	out, _, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big")
	if err != nil || out != "no change: alias/x\n" || *syncs != 0 || mustRead(t, path) != before {
		t.Fatalf("err %v stdout %q syncs %d", err, out, *syncs)
	}
}

// TestAliasSetRefusesAShrinkWithoutTheFlag pins Review Focus 4 (decision 7):
// a set whose window is smaller than the old known one, or unknown where it
// was known, is refused with no terminal (exit 3, the registry unchanged,
// the message naming --accept-smaller-window), and the note says how many
// client sessions use the alias; with --accept-smaller-window it writes.
// Running sessions keep the window they launched with, so a silent shrink
// would make them compact too late.
func TestAliasSetRefusesAShrinkWithoutTheFlag(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistry)
	if _, _, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big"); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, path)
	h.confirmRemove = h.realConfirmRemove // testHost's openTTY fails: no terminal
	stubSessions(h, map[string]int{"alias/x": 2})
	for _, target := range []string{"openrouter/small", "openrouter/unknown"} {
		_, errOut, err := execAlias(t, h, "alias", "set", "alias/x", "--target", target)
		want := "approval missing: pass --accept-smaller-window (there is no terminal to ask); nothing was changed"
		if err == nil || err.Error() != want || exitcode.Of(err) != 3 {
			t.Fatalf("%s: err %v (exit %d), want %q", target, err, exitcode.Of(err), want)
		}
		if !strings.Contains(errOut, "2 client session(s)") || mustRead(t, path) != before {
			t.Fatalf("%s: stderr %q, registry changed %v", target, errOut, mustRead(t, path) != before)
		}
	}
	out, _, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/small", "--accept-smaller-window")
	if err != nil || !strings.HasPrefix(out, "set alias: alias/x\n") {
		t.Fatalf("with the flag: err %v stdout %q", err, out)
	}
}

// TestAliasSetDryRunPrintsNoKey pins decision 13's rule for every output of
// the alias commands: a cloud target's row carries its resolved key, and no
// line or JSON document may. The dry run prints the targets, the window and
// each planned row as id, target, order and h8, and writes nothing; the real
// set and `alias list --json` print the key nowhere either.
func TestAliasSetDryRunPrintsNoKey(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistry)
	before := mustRead(t, path)
	out, errOut, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big", "--target", "openrouter/small:2", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s%s", err, out, errOut)
	}
	for _, want := range []string{"would set alias: alias/x\n", "  target openrouter/big order 1\n", "  target openrouter/small order 2\n", "context: - -> 131072\n", "row wta:alias/x/"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if mustRead(t, path) != before || len(fake.ids()) != 0 {
		t.Fatal("a dry run wrote the registry or the database")
	}
	set, setErr, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big", "--target", "openrouter/small:2")
	if err != nil {
		t.Fatal(err)
	}
	list, listErr, err := execAlias(t, h, "alias", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"dry stdout": out, "dry stderr": errOut, "set stdout": set, "set stderr": setErr, "list stdout": list, "list stderr": listErr} {
		if strings.Contains(text, aliasKeySentinel) || strings.Contains(text, "litellm_params") {
			t.Errorf("%s carries the key or the params:\n%s", name, text)
		}
	}
}

// TestAliasSetWithDatabaseDownThenSyncHeals pins decision 4 for `alias
// set`: with LiteLLM's database half unavailable the registry write stands,
// the command exits 0 and one warning names the repair; a later `wt-model
// litellm sync` with the proxy back adds the alias's rows.
func TestAliasSetWithDatabaseDownThenSyncHeals(t *testing.T) {
	h, fake, path := aliasEnv(t, aliasRegistry)
	fake.mu.Lock()
	fake.down = true
	fake.mu.Unlock()
	_, errOut, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big")
	if err != nil || !strings.Contains(errOut, "alias routes unavailable") || !strings.Contains(errOut, "wt-model litellm sync") {
		t.Fatalf("err %v stderr %q", err, errOut)
	}
	if !strings.Contains(mustRead(t, path), "alias/x") || len(fake.ids()) != 0 {
		t.Fatal("want the registry written and no row added")
	}
	fake.mu.Lock()
	fake.down = false
	fake.mu.Unlock()
	if _, _, err := execAlias(t, h, "litellm", "sync"); err != nil {
		t.Fatal(err)
	}
	if len(aliasRows(fake, "alias/x")) != 1 {
		t.Fatalf("rows after sync %v", fake.ids())
	}
}

// TestAliasSetTargetParsing pins how --target is read: ID, ID:ORDER, a model
// id that itself holds a colon (an ollama tag) taken whole, a bad order and
// a target given twice refused, an order a plain error naming the flag.
func TestAliasSetTargetParsing(t *testing.T) {
	cfg := &config.Config{Models: []config.Model{{ID: "openrouter/big"}, {ID: "ollama/qwen3:8b"}, {ID: "ollama/odd:7"}}}
	ok := map[string][]config.AliasTarget{
		"openrouter/big":     {{Model: "openrouter/big"}},
		"openrouter/big:2":   {{Model: "openrouter/big", Order: 2}},
		"ollama/qwen3:8b":    {{Model: "ollama/qwen3:8b"}},
		"ollama/qwen3:8b:3":  {{Model: "ollama/qwen3:8b", Order: 3}},
		"ollama/odd:7":       {{Model: "ollama/odd:7"}},
		"openrouter/nope:12": {{Model: "openrouter/nope", Order: 12}},
	}
	for spec, want := range ok {
		got, err := parseAliasTargets(cfg, []string{spec})
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("parse %q = %v, %v; want %v", spec, got, err, want)
		}
	}
	for spec, wantErr := range map[string]string{
		"openrouter/big:0":  "order must be a whole number from 1",
		"openrouter/big:-1": "order must be a whole number from 1",
		"":                  "empty",
	} {
		if _, err := parseAliasTargets(cfg, []string{spec}); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("parse %q = %v, want %q", spec, err, wantErr)
		}
	}
	if _, err := parseAliasTargets(cfg, []string{"openrouter/big", "openrouter/big:2"}); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("duplicate: %v", err)
	}
}

// aliasRegistryWithX is aliasRegistry holding alias/x over openrouter/big.
const aliasRegistryWithX = aliasRegistry + `
[[aliases]]
id = "alias/x"
targets = [{ model = "openrouter/big" }]
`

// TestAliasRmNeedsForceInUse pins C7 for `alias rm` (decision 9): an alias
// client sessions use is not removed by --yes alone (exit 3, naming
// --force-in-use and the count, the registry unchanged); --force-in-use
// with --yes removes it and prints `removed alias: <id>`. Each flag grants
// one thing.
func TestAliasRmNeedsForceInUse(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryWithX)
	stubSessions(h, map[string]int{"alias/x": 2})
	_, _, err := execAlias(t, h, "alias", "rm", "alias/x", "--yes")
	if err == nil || exitcode.Of(err) != 3 || !strings.Contains(err.Error(), "--force-in-use") || !strings.Contains(err.Error(), "2 client session(s)") {
		t.Fatalf("err %v (exit %d)", err, exitcode.Of(err))
	}
	if mustRead(t, path) != aliasRegistryWithX {
		t.Fatal("a refused rm changed the registry")
	}
	out, _, err := execAlias(t, h, "alias", "rm", "alias/x", "--yes", "--force-in-use")
	if err != nil || !strings.HasPrefix(out, "removed alias: alias/x\n") || strings.Contains(mustRead(t, path), "alias/x") {
		t.Fatalf("err %v stdout %q", err, out)
	}
}

// TestAliasRmNoTerminalExit3 pins P4 for `alias rm`: with no terminal and
// no --yes it refuses with exit 3 and the model rm wording, removing
// nothing.
func TestAliasRmNoTerminalExit3(t *testing.T) {
	h, _, path := aliasEnv(t, aliasRegistryWithX)
	h.confirmRemove = h.realConfirmRemove
	_, _, err := execAlias(t, h, "alias", "rm", "alias/x")
	want := "approval missing: pass --yes (there is no terminal to ask); nothing was removed"
	if err == nil || err.Error() != want || exitcode.Of(err) != 3 || mustRead(t, path) != aliasRegistryWithX {
		t.Fatalf("err %v (exit %d), want %q and nothing removed", err, exitcode.Of(err), want)
	}
	if _, _, err := execAlias(t, h, "alias", "rm", "alias/nope", "--yes"); !errors.Is(err, config.ErrAliasNotFound) {
		t.Errorf("unknown alias: %v", err)
	}
}

// TestAliasListWithoutServerOrProxy pins that `alias list` works on a host
// with neither a running model server nor a proxy pair: SESSIONS is `-` and
// ROUTED `?`, never an error, and CONTEXT is the smallest window or `-`.
func TestAliasListWithoutServerOrProxy(t *testing.T) {
	h := testHost()
	modelHome(t, aliasRegistry+`
[[aliases]]
id = "alias/x"
targets = [{ model = "openrouter/big" }, { model = "openrouter/small", order = 2 }]

[[aliases]]
id = "alias/y"
targets = [{ model = "openrouter/unknown" }]
`)
	out, errOut, err := execAlias(t, h, "alias", "list")
	want := "ALIAS    TARGETS                              CONTEXT  SESSIONS  ROUTED\n" +
		"alias/x  1:openrouter/big,2:openrouter/small  131072   -         ?\n" +
		"alias/y  1:openrouter/unknown                 -        -         ?\n"
	if err != nil || out != want || errOut != "" {
		t.Fatalf("err %v\nstdout\n%s\nwant\n%s\nstderr %q", err, out, want, errOut)
	}
}

// TestAliasRmWithDatabaseDownThenSyncHeals pins decision 4 for `alias rm`:
// removing the last alias while LiteLLM's database cannot be listed still
// removes it from the registry (exit 0), leaves its rows routing, and says
// so exactly once with the repair; a later `wt-model litellm sync` with the
// proxy back deletes the orphan rows. A sync with no alias and no removal
// stays silent about alias routes, and an rm with a healthy proxy prints no
// such line. Without the line the rows would keep routing a removed alias
// with nothing said.
func TestAliasRmWithDatabaseDownThenSyncHeals(t *testing.T) {
	const line = "alias routes unavailable"
	h, fake, path := aliasEnv(t, aliasRegistryWithX)
	if _, _, err := execAlias(t, h, "litellm", "sync"); err != nil || len(aliasRows(fake, "alias/x")) != 1 {
		t.Fatalf("setup sync: err %v rows %v", err, fake.ids())
	}
	setDown := func(down bool) {
		fake.mu.Lock()
		fake.down = down
		fake.mu.Unlock()
	}
	setDown(true)
	out, errOut, err := execAlias(t, h, "alias", "rm", "alias/x", "--yes")
	if err != nil || !strings.HasPrefix(out, "removed alias: alias/x\n") || strings.Contains(mustRead(t, path), "alias/x") {
		t.Fatalf("rm with the proxy down: err %v stdout %q", err, out)
	}
	if n := strings.Count(errOut, line); n != 1 || !strings.Contains(errOut, "run 'wt-model litellm sync' when the proxy answers") {
		t.Fatalf("stderr has %d decision-4 lines, want 1:\n%s", n, errOut)
	}
	if len(aliasRows(fake, "alias/x")) != 1 {
		t.Fatalf("rows %v, want the orphan still there", fake.ids())
	}
	_, errOut, _ = execAlias(t, h, "litellm", "sync")
	if strings.Contains(errOut, line) {
		t.Errorf("a sync with no alias and no removal warned about alias routes:\n%s", errOut)
	}
	setDown(false)
	if _, _, err := execAlias(t, h, "litellm", "sync"); err != nil || len(aliasRows(fake, "alias/x")) != 0 {
		t.Fatalf("healing sync: err %v rows %v", err, fake.ids())
	}
	if _, _, err := execAlias(t, h, "alias", "set", "alias/y", "--target", "openrouter/big"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err = execAlias(t, h, "alias", "rm", "alias/y", "--yes")
	if err != nil || strings.Contains(errOut, line) || len(aliasRows(fake, "alias/y")) != 0 {
		t.Fatalf("healthy rm: err %v rows %v stderr:\n%s", err, fake.ids(), errOut)
	}
}

// TestAliasSetDryRunWithoutAProxyPair pins that a dry run with no proxy
// pair lists no rows: the real run would write none (decision 4), and an h8
// computed under no key would match no row the proxy ever holds. The targets
// and the window line stay.
func TestAliasSetDryRunWithoutAProxyPair(t *testing.T) {
	h, _, _ := aliasEnv(t, aliasRegistry)
	h.adminFor = func(*config.Config) *litellm.AdminClient { return nil }
	out, _, err := execAlias(t, h, "alias", "set", "alias/x", "--target", "openrouter/big", "--dry-run")
	if err != nil || !strings.Contains(out, "  target openrouter/big order 1\n") || !strings.Contains(out, "context: - -> 262144\n") ||
		!strings.Contains(out, "no proxy pair: no rows would be written\n") || strings.Contains(out, " row ") || strings.Contains(out, "h8") {
		t.Fatalf("err %v stdout:\n%s", err, out)
	}
}
