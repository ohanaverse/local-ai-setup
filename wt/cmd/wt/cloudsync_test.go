package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cloudsync"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// cloudSyncStamp is what a run at cloudSyncClock writes in pricing_updated_at.
const cloudSyncStamp = "2026-10-07T16:00:00+00:00"

var cloudSyncClock = time.Date(2026, 10, 7, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600))

// cloudSyncRegistry is the registry the command tests start from: one
// OpenRouter-priced model, two ollama cloud entries (one the test page lists,
// one it does not) and a local ollama model that nothing may touch.
const cloudSyncRegistry = `[[providers]]
id = "ollama"
name = "Ollama"
location = "local"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_OPENROUTER_KEY"

[[models]]
id = "openrouter/vendor--gpt"
family = "gpt"
provider_id = "openrouter"
model_name = "vendor/gpt"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 2.5
output_price_per_million = 10

[[models]]
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 9
subscription_price = 100
subscription_period = "month"

[[models]]
id = "ollama/retired:cloud"
family = "retired"
provider_id = "ollama"
model_name = "retired:cloud"
location = "cloud"
source = "curated"
tags = []

[[models]]
id = "ollama/qwen3:8b"
family = "qwen3"
provider_id = "ollama"
model_name = "qwen3:8b"
location = "local"
source = "curated"
tags = []
`

const openRouterBody = `{"data": [{"id": "vendor/gpt", "pricing": {"prompt": "0.000003", "completion": "0.000015"}}]}`

// sameOpenRouterBody prices vendor/gpt exactly as cloudSyncRegistry has it.
const sameOpenRouterBody = `{"data": [{"id": "vendor/gpt", "pricing": {"prompt": "0.0000025", "completion": "0.00001"}}]}`

// cloudSyncHome gives the test its own home with registry as its registry,
// and returns the registry path and the loaded config.
func cloudSyncHome(t *testing.T, registry string) (string, *config.Config) {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	// Where a refused pricing page is saved.
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	old := cloudSyncNow
	cloudSyncNow = func() time.Time { return cloudSyncClock }
	t.Cleanup(func() { cloudSyncNow = old })
	return path, cfg
}

// fetched records the URLs a test's cloudFetch stub was asked for.
type fetched struct {
	mu   sync.Mutex
	urls []string
}

func (f *fetched) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.urls)
}

// stubCloudFetch serves pages by URL. A URL with no page is an HTTP 404.
func stubCloudFetch(t *testing.T, pages map[string]string) *fetched {
	t.Helper()
	rec := &fetched{}
	old := cloudFetch
	cloudFetch = func(_ context.Context, url string) ([]byte, error) {
		rec.mu.Lock()
		rec.urls = append(rec.urls, url)
		rec.mu.Unlock()
		page, ok := pages[url]
		if !ok {
			return nil, errors.New("HTTP 404")
		}
		return []byte(page), nil
	}
	t.Cleanup(func() { cloudFetch = old })
	return rec
}

// stubConfirm answers the confirmation and counts how often it was asked.
// before, when not nil, runs first: it plays whatever happens to the machine
// while the user reads the plan.
func stubConfirm(t *testing.T, answer bool, err error, before func()) *int {
	t.Helper()
	asked := 0
	old := confirmCloudSync
	confirmCloudSync = func(string) (bool, error) {
		asked++
		if before != nil {
			before()
		}
		return answer, err
	}
	t.Cleanup(func() { confirmCloudSync = old })
	return &asked
}

func runCS(t *testing.T, cfg *config.Config, o cloudSyncOpts) (stdout, stderr string, code int) {
	t.Helper()
	stdout, stderr, _, code = runCSFinal(t, cfg, o)
	return stdout, stderr, code
}

// runCSFinal is runCS with the command's error text as well: the one line
// main prints, after "wt: ", when the run did not exit 0.
func runCSFinal(t *testing.T, cfg *config.Config, o cloudSyncOpts) (stdout, stderr, final string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := runCloudSync(context.Background(), &out, &errOut, cfg, o); err != nil {
		final, code = err.Error(), exitCodeOf(err)
	}
	return out.String(), errOut.String(), final, code
}

// noTerminal takes the controlling terminal away, as under an agent's shell
// or cron, and puts the real prompt behind confirmCloudSync (TestMain fails
// that seam by default).
func noTerminal(t *testing.T) {
	t.Helper()
	oldTTY, oldConfirm := openTTY, confirmCloudSync
	openTTY = func() (*os.File, error) { return nil, errors.New("open /dev/tty: device not configured") }
	confirmCloudSync = promptCloudSync
	t.Cleanup(func() { openTTY, confirmCloudSync = oldTTY, oldConfirm })
}

// TestCloudSyncSeamsFailClosed pins TestMain: with nothing stubbed, a
// cloud-sync test reaches neither the network nor a terminal. A test that
// forgot a stub fails with "not stubbed" instead of fetching a public page
// from whatever machine it runs on.
func TestCloudSyncSeamsFailClosed(t *testing.T) {
	if _, err := cloudFetch(context.Background(), cloudsync.OpenRouterModelsURL); err == nil || !strings.Contains(err.Error(), "not stubbed") {
		t.Errorf("cloudFetch err = %v, want a not-stubbed failure", err)
	}
	if ok, err := confirmCloudSync("?"); ok || err == nil {
		t.Errorf("confirmCloudSync = (%v, %v), want a refusal with an error", ok, err)
	}
}

// TestRealCloudFetch pins the one HTTP helper against a loopback server: a
// 2xx body is returned whole, and any other status is an error that names
// it. An error page read as a pricing page would be reported as "the page
// changed shape" and send someone to repair a parser that is not broken.
func TestRealCloudFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			fmt.Fprint(w, "<html>page</html>")
			return
		}
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	if body, err := realCloudFetch(context.Background(), srv.URL+"/ok"); err != nil || string(body) != "<html>page</html>" {
		t.Errorf("realCloudFetch(/ok) = (%q, %v)", body, err)
	}
	if _, err := realCloudFetch(context.Background(), srv.URL+"/limited"); err == nil || err.Error() != "HTTP 429" {
		t.Errorf("realCloudFetch(/limited) err = %v, want HTTP 429", err)
	}
}

// TestSelectFlows pins --only: the flag left out selects every flow, a comma
// list selects those named, and an unknown name is an error that lists the
// valid ones instead of silently running nothing. The flag given with an
// empty value is an error too: a script whose variable is unset must not run
// every flow because of it.
func TestSelectFlows(t *testing.T) {
	cases := []struct {
		only               string
		given              bool
		openrouter, ollama bool
		wantErr            string
	}{
		{"", false, true, true, ""},
		{"openrouter", true, true, false, ""},
		{" openrouter ", true, true, false, ""},
		{"openrouter,openrouter", true, true, false, ""},
		{"ollama", true, false, true, ""},
		{" ollama , openrouter ", true, true, true, ""},
		{"price", true, false, false, `--only: unknown flow "price" (valid: openrouter, ollama)`},
		{"openrouter,", true, true, false, `--only: unknown flow "" (valid: openrouter, ollama)`},
		// The flag given with nothing in it, as from `--only "$FLOWS"` with
		// the variable unset, is not the flag left out.
		{"", true, false, false, `--only: no flow named (valid: openrouter, ollama)`},
		{"  ", true, false, false, `--only: no flow named (valid: openrouter, ollama)`},
	}
	for _, tc := range cases {
		var o cloudSyncOpts
		err := selectFlows(tc.only, tc.given, &o)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("selectFlows(%q, given %v) err = %v, want %q", tc.only, tc.given, err, tc.wantErr)
			}
			continue
		}
		if err != nil || o.openrouter != tc.openrouter || o.ollama != tc.ollama {
			t.Errorf("selectFlows(%q, given %v) = openrouter %v, ollama %v, err %v", tc.only, tc.given, o.openrouter, o.ollama, err)
		}
	}
}

// TestCloudSyncCommandRefusals pins what the command refuses before it does
// anything: an ollama-only flag with the ollama flow left out (the flag
// would be silently ignored, and --force or a digest ignored is a user
// believing a gate was passed), an unknown flow, --only with no flow in it, a
// stray argument, and a config that could not be loaded, which gets the same
// repair hint every other command gives.
func TestCloudSyncCommandRefusals(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	run := func(a *app, args ...string) error {
		cmd := cloudSyncCmd(a)
		cmd.SetArgs(args)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		return cmd.Execute()
	}
	ok := &app{cfg: cfg}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--only", "openrouter", "--force"}, "--force is for the ollama flow, which --only openrouter leaves out"},
		// The message names the flows --only selected, not the spelling it
		// was given: it is meant to be read back and re-run.
		{[]string{"--only", " openrouter ", "--force"}, "--force is for the ollama flow, which --only openrouter leaves out"},
		{[]string{"--only", "openrouter", "--html", "x.html"}, "--html is for the ollama flow, which --only openrouter leaves out"},
		{[]string{"--only", "openrouter", "--approve-removals", "abc"}, "--approve-removals is for the ollama flow, which --only openrouter leaves out"},
		{[]string{"--only", "routes"}, `--only: unknown flow "routes" (valid: openrouter, ollama)`},
		{[]string{"--only", "", "--dry-run"}, `--only: no flow named (valid: openrouter, ollama)`},
		{[]string{"--only=", "--dry-run"}, `--only: no flow named (valid: openrouter, ollama)`},
		{[]string{"extra"}, `unknown command "extra" for "cloud-sync"`},
	} {
		err := run(ok, tc.args...)
		if err == nil || err.Error() != tc.want || exitCodeOf(err) != 1 {
			t.Errorf("wt cloud-sync %v: err = %v (exit %d), want %q and exit 1", tc.args, err, exitCodeOf(err), tc.want)
		}
	}

	broken := &app{cfg: &config.Config{}, loadErr: fmt.Errorf("%w at /x/registry.toml — seed it with `wt model init`", config.ErrRegistryMissing)}
	err := run(broken, "--dry-run")
	if err == nil || !strings.HasPrefix(err.Error(), "config error: model registry not found") || strings.Contains(err.Error(), "wt config") {
		t.Errorf("with an unloadable config: err = %v, want the config error with no second repair", err)
	}
}

// TestCloudSyncOpenRouterDryRun pins the dry run of the openrouter flow: the plan as
// `id: old -> new` under the openrouter: prefix, and nothing else — the registry
// byte-identical, no lock file beside it, no question asked, no route sync.
func TestCloudSyncOpenRouterDryRun(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	asked := stubConfirm(t, true, nil, nil)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, dryRun: true})
	want := "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (1):\n" +
		"openrouter:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15\n" +
		"openrouter: Unchanged prices: 0\n"
	if stdout != want || stderr != "" || code != 0 {
		t.Errorf("stdout = %q\nstderr = %q, exit %d\nwant stdout %q, no stderr, exit 0", stdout, stderr, code, want)
	}
	if mustRead(t, path) != cloudSyncRegistry {
		t.Error("a dry run changed the registry")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("a dry run left %d files beside the registry, want only registry.toml", len(entries))
	}
	if *asked != 0 || *synced != 0 {
		t.Errorf("a dry run asked %d time(s) and synced %d time(s), want neither", *asked, *synced)
	}
	if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) {
		t.Errorf("fetched %v, want only OpenRouter's model list", urls)
	}
}

// TestCloudSyncOpenRouterApply pins the openrouter flow end to end with --yes: the
// new prices and the stamp are in registry.toml, the result line says how
// many moved, the routes are synced exactly once (LiteLLM gets a route's
// prices only through a sync, #179), and nothing was asked.
func TestCloudSyncOpenRouterApply(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	asked := stubConfirm(t, false, nil, nil)
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "openrouter: refreshed 1 model(s); 1 price(s) changed\n") {
		t.Fatalf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
	}
	if *asked != 0 || *synced != 1 {
		t.Errorf("asked %d time(s), synced %d time(s); want no question under --yes and one sync", *asked, *synced)
	}
	wantRow := "model_name = \"vendor/gpt\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at = \"" + cloudSyncStamp + "\"\n\n" +
		"[models.cost]\ninput_price_per_million = 3.0\noutput_price_per_million = 15.0\n"
	if text := mustRead(t, path); !strings.Contains(text, wantRow) {
		t.Errorf("registry.toml lacks the refreshed row:\n%s\n\nfile:\n%s", wantRow, text)
	}
}

// TestCloudSyncStampOnlyRunSkipsTheRouteSync pins the run that finds every
// price current. The matched model is still stamped (that stamp is the
// stale-price notice's "last refreshed"), the unchanged price is not
// rewritten, and the routes are not synced: a sync can restart the LiteLLM
// proxy under running agents, and no route changed.
func TestCloudSyncStampOnlyRunSkipsTheRouteSync(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: sameOpenRouterBody})
	synced := stubRouteSync(t, "")

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "openrouter: Unchanged prices: 1\nopenrouter: refreshed 1 model(s); 0 price(s) changed\n") {
		t.Fatalf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
	}
	if *synced != 0 {
		t.Errorf("a stamp-only run synced the routes %d time(s)", *synced)
	}
	want := strings.Replace(cloudSyncRegistry, "tags = []\n\n[models.cost]\ninput_price_per_million = 2.5\noutput_price_per_million = 10\n",
		"tags = []\npricing_updated_at = \""+cloudSyncStamp+"\"\n\n[models.cost]\ninput_price_per_million = 2.5\noutput_price_per_million = 10\n", 1)
	if got := mustRead(t, path); got != want {
		t.Errorf("registry.toml after a stamp-only run:\n%s\nwant the stamp added and nothing else:\n%s", got, want)
	}
	// The stamp is what the launch notice reads.
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := agents.LastPriceRefresh(loaded, cloudSyncClock); !ok || !at.Equal(cloudSyncClock) {
		t.Errorf("LastPriceRefresh after the run = (%v, %v), want %v", at, ok, cloudSyncClock)
	}
}

// TestCloudSyncOpenRouterFailuresExit1 pins the openrouter flow's failures: a fetch
// that fails, and an answer that is not the model list. Each is one error
// line under the openrouter: prefix, exit 1, and the registry untouched — never
// a run of "no OpenRouter match" warnings for every model. The command's own
// error, the last line main prints, points back at those lines.
func TestCloudSyncOpenRouterFailuresExit1(t *testing.T) {
	for name, pages := range map[string]map[string]string{
		"could not read OpenRouter's prices: HTTP 404":                                {},
		"could not read OpenRouter's prices: OpenRouter response missing 'data' list": {cloudsync.OpenRouterModelsURL: `{"error": "rate limited"}`},
	} {
		path, cfg := cloudSyncHome(t, cloudSyncRegistry)
		stubCloudFetch(t, pages)
		synced := stubRouteSync(t, "")
		stdout, stderr, final, code := runCSFinal(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if want := "openrouter: error: " + name + "; no price was changed\n"; stderr != want || stdout != "" || code != 1 {
			t.Errorf("stdout = %q, stderr = %q, exit %d\nwant stderr %q and exit 1", stdout, stderr, code, want)
		}
		if want := "cloud-sync: a step failed; see the error lines above"; final != want {
			t.Errorf("the command's error = %q, want %q", final, want)
		}
		if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
			t.Error("a failed fetch changed the registry or synced the routes")
		}
	}
}

// TestCloudSyncWithNoOpenRouterModelFetchesNothing pins the registry that
// has only ollama models: the openrouter flow says there is nothing to refresh
// and makes no request (#151: nothing to fetch, nothing to warn about).
func TestCloudSyncWithNoOpenRouterModelFetchesNothing(t *testing.T) {
	registry := cloudSyncRegistry[:strings.Index(cloudSyncRegistry, "[[models]]\nid = \"openrouter/vendor--gpt\"")] +
		cloudSyncRegistry[strings.Index(cloudSyncRegistry, "[[models]]\nid = \"ollama/deepseek-v4-pro:cloud\""):]
	path, cfg := cloudSyncHome(t, registry)
	got := stubCloudFetch(t, nil)
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if stdout != "openrouter: no OpenRouter-priced model in the registry; nothing to refresh\n" || stderr != "" || code != 0 {
		t.Errorf("stdout = %q, stderr = %q, exit %d", stdout, stderr, code)
	}
	if len(got.all()) != 0 || mustRead(t, path) != registry {
		t.Errorf("fetched %v or changed the registry; want neither", got.all())
	}
}

// TestCloudSyncAsksOnceAndTakesNoForAnAnswer pins the confirmation without
// --yes: one question, a "no" leaves everything as it was and exits 0, and
// when the question cannot be asked (no terminal, as under an agent's shell)
// the run changes nothing, exits 1 and names --yes. The no-terminal half
// runs the real prompt with the terminal taken away, so the message is the
// one a user gets, not one the test supplied.
func TestCloudSyncAsksOnceAndTakesNoForAnAnswer(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")

	asked := stubConfirm(t, false, nil, nil)
	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true})
	if *asked != 1 || code != 0 || stderr != "" || !strings.HasSuffix(stdout, "openrouter: not applied (declined)\n") {
		t.Errorf("declined: asked %d, exit %d, stdout %q, stderr %q", *asked, code, stdout, stderr)
	}

	noTerminal(t)
	_, stderr, code = runCS(t, cfg, cloudSyncOpts{openrouter: true})
	if want := "openrouter: error: not applied: there is no terminal to confirm on — rerun with --yes to apply without asking\n"; stderr != want || code != 1 {
		t.Errorf("no terminal: stderr = %q, exit %d; want %q and exit 1", stderr, code, want)
	}
	if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
		t.Error("an unconfirmed run changed the registry or synced the routes")
	}

	// A plan with nothing in it is not asked about. A fetch that matched no
	// model refreshed nothing, so it writes nothing: a stamp here would tell
	// the stale-price notice that prices are fresh when none was read.
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: `{"data": []}`})
	asked = stubConfirm(t, true, nil, nil)
	stdout, _, code = runCS(t, cfg, cloudSyncOpts{openrouter: true})
	if *asked != 0 || code != 0 || !strings.Contains(stdout, "openrouter: warning: No OpenRouter match for openrouter/vendor--gpt (vendor/gpt)\n") {
		t.Errorf("a plan that matches no model: asked %d time(s), exit %d; want no question, exit 0 and the no-match warning\n%s", *asked, code, stdout)
	}
	if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
		t.Error("a run that matched no model stamped the registry or synced the routes")
	}
}

// TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo runs the real prompt
// against a stand-in terminal (one end of a socket pair). It pins the
// question as it is shown, with [y/N], and that only y or yes applies: a
// bare Enter, any other word, and a terminal that closes without an answer
// are all "no". A sync that deletes models must never be approved by
// accident.
func TestPromptCloudSyncAsksOnTheTerminalAndDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		typed string
		want  bool
	}{
		{"y\n", true}, {"YES\n", true}, {"n\n", false}, {"\n", false}, {"sure\n", false}, {"", false},
	} {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal(err)
		}
		tty, user := os.NewFile(uintptr(fds[0]), "tty"), os.NewFile(uintptr(fds[1]), "user")
		old := openTTY
		openTTY = func() (*os.File, error) { return tty, nil }
		shown := make(chan string, 1)
		go func() {
			// The user reads the question, types the answer and leaves.
			buf := make([]byte, 256)
			n, _ := user.Read(buf)
			_, _ = user.WriteString(tc.typed)
			_ = user.Close()
			shown <- string(buf[:n])
		}()
		got, err := promptCloudSync("Apply these changes?")
		openTTY = old
		if err != nil || got != tc.want {
			t.Errorf("typed %q: promptCloudSync = (%v, %v), want (%v, nil)", tc.typed, got, err, tc.want)
		}
		if q := <-shown; q != "Apply these changes? [y/N] " {
			t.Errorf("the question shown = %q", q)
		}
	}
}

// TestCloudSyncPrefixesTheRouteSyncsLines pins how the route sync's own
// output reaches the user: its stdout lines and its stderr lines (a model
// that could not be routed) under routes:, like its warning. An unprefixed
// "<id>: …" between openrouter: and ollama: lines reads as a third flow, or as
// part of the one above it.
func TestCloudSyncPrefixesTheRouteSyncsLines(t *testing.T) {
	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	old := syncRoutesAfterWrite
	syncRoutesAfterWrite = func(out, errOut io.Writer) string {
		fmt.Fprintln(out, "openrouter/vendor--gpt: routed")
		fmt.Fprintln(errOut, "openrouter/other: secret_ref \"X\" resolved empty")
		return "LiteLLM routes not synced: one or more models could not be applied"
	}
	t.Cleanup(func() { syncRoutesAfterWrite = old })

	stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	if code != 0 || !strings.HasSuffix(stdout, "openrouter: refreshed 1 model(s); 1 price(s) changed\nroutes: openrouter/vendor--gpt: routed\n") {
		t.Errorf("exit %d (a sync that warns does not fail the run); stdout:\n%s", code, stdout)
	}
	want := "routes: openrouter/other: secret_ref \"X\" resolved empty\n" +
		"routes: warning: LiteLLM routes not synced: one or more models could not be applied\n"
	if stderr != want {
		t.Errorf("stderr = %q\nwant     %q", stderr, want)
	}
}

// TestCloudSyncOpenRouterRefusesWhenTheRegistryChangedAfterThePlan pins the
// re-plan under the registry lock: when the registry changes between the
// printed plan and the write (here, while the user reads the question), what
// would now be applied is not what was approved, so nothing is applied and
// the run says to run it again. The other writer's change survives.
func TestCloudSyncOpenRouterRefusesWhenTheRegistryChangedAfterThePlan(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")
	raced := strings.Replace(cloudSyncRegistry, "input_price_per_million = 2.5", "input_price_per_million = 7", 1)
	stubConfirm(t, true, nil, func() {
		if err := os.WriteFile(path, []byte(raced), 0o600); err != nil {
			t.Fatal(err)
		}
	})

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true})
	if want := "openrouter: error: the registry changed after the plan was printed; no price was changed — run it again\n"; stderr != want || code != 1 {
		t.Errorf("stderr = %q, exit %d; want %q and exit 1", stderr, code, want)
	}
	if mustRead(t, path) != raced || *synced != 0 {
		t.Error("the refused run changed the registry or synced the routes")
	}
}

// TestCloudSyncReportsARefusedRegistryWrite pins what the user is told when
// the one registry write is refused: here a row the refresh must stamp has
// lost its family to a hand edit, so the writer will not write it. The run
// names the row, says the registry was not changed, exits 1 and syncs
// nothing. A write that skipped the bad row silently would leave a price
// stale with nothing saying why.
func TestCloudSyncReportsARefusedRegistryWrite(t *testing.T) {
	broken := strings.Replace(cloudSyncRegistry, "family = \"gpt\"\n", "", 1)
	path, cfg := cloudSyncHome(t, broken)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
	want := "openrouter: error: registry.toml was not changed: invalid registry entry: model \"openrouter/vendor--gpt\": family is required\n"
	if stderr != want || code != 1 {
		t.Errorf("stderr = %q, exit %d\nwant %q and exit 1", stderr, code, want)
	}
	if mustRead(t, path) != broken || *synced != 0 {
		t.Error("a refused write changed the registry or synced the routes")
	}
}

// TestCloudSyncUnderARedirectedRegistry pins the scratch-registry rule for
// this writer, with the real route sync: the registry write succeeds either
// way and the run exits 0; without WT_LITELLM_CONFIG the sync is refused
// with a warning and the default config.yaml is not touched, and with it the
// named file is the one synced. This is what keeps a trial run against a
// copy of the registry from rewriting the real proxy's routes.
func TestCloudSyncUnderARedirectedRegistry(t *testing.T) {
	setup := func(t *testing.T) (registry, defaultYAML string, cfg *config.Config) {
		home := t.TempDir()
		registry = filepath.Join(home, "scratch", "registry.toml")
		defaultYAML = filepath.Join(home, ".config", "litellm", "config.yaml")
		for path, content := range map[string]string{registry: cloudSyncRegistry, defaultYAML: "model_list: []\n"} {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("WT_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		t.Setenv("WT_TEST_OPENROUTER_KEY", "sk-test-not-a-real-key")
		realRouteSync(t)
		stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		// realRouteSync probes nothing; say instead that every provider
		// answered, so the sync has no probe failure to warn about.
		probeInventory = localmodels.OnDiskSnapshotForTest
		return registry, defaultYAML, cfg
	}

	t.Run("config.yaml not named: registry written, routes refused, exit 0", func(t *testing.T) {
		registry, defaultYAML, cfg := setup(t)
		_, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if code != 0 {
			t.Fatalf("exit %d; the registry write succeeded, so the command must too. stderr: %s", code, stderr)
		}
		if !strings.Contains(mustRead(t, registry), "input_price_per_million = 3.0") {
			t.Error("the redirected registry was not written")
		}
		want := "routes: warning: LiteLLM routes not touched: the registry is " + registry + " but config.yaml is the default " + defaultYAML +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to\n"
		if stderr != want {
			t.Errorf("stderr = %q\nwant %q", stderr, want)
		}
		if mustRead(t, defaultYAML) != "model_list: []\n" {
			t.Error("the default config.yaml was touched")
		}
	})

	t.Run("config.yaml named: that file synced, no warning", func(t *testing.T) {
		registry, defaultYAML, cfg := setup(t)
		named := filepath.Join(t.TempDir(), "scratch-config.yaml")
		if err := os.WriteFile(named, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WT_LITELLM_CONFIG", named)
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if code != 0 || stderr != "" {
			t.Fatalf("exit %d, stderr %q", code, stderr)
		}
		if !strings.Contains(mustRead(t, registry), "input_price_per_million = 3.0") {
			t.Error("the redirected registry was not written")
		}
		if !strings.Contains(stdout, "routes: openrouter/vendor--gpt: ") {
			t.Errorf("stdout lacks the route sync's line for the repriced model, under routes:\n%s", stdout)
		}
		if yaml := mustRead(t, named); !strings.Contains(yaml, "openrouter/vendor--gpt") || !strings.Contains(yaml, "input_cost_per_token: 3e-06") {
			t.Errorf("the named config.yaml does not carry the route at its new price:\n%s", yaml)
		}
		if mustRead(t, defaultYAML) != "model_list: []\n" {
			t.Error("the default config.yaml was touched")
		}
	})
}

// TestCloudSyncWithNoOpenRouterPricedModelDoesNothingHoweverItIsRun pins the
// skip for every way the command is run: plain, --dry-run and --yes. A
// registry that prices nothing through OpenRouter (here it still has the
// openrouter provider row, with no model under it) gets the one "nothing to
// refresh" line and exit 0: nothing fetched, nothing asked, no lock file, the
// registry byte-identical, no route sync. A machine that uses only ollama
// must be able to run `wt cloud-sync` from a script without it failing for
// want of a terminal or restarting the proxy.
func TestCloudSyncWithNoOpenRouterPricedModelDoesNothingHoweverItIsRun(t *testing.T) {
	registry := cloudSyncRegistry[:strings.Index(cloudSyncRegistry, "[[models]]\nid = \"openrouter/vendor--gpt\"")] +
		cloudSyncRegistry[strings.Index(cloudSyncRegistry, "[[models]]\nid = \"ollama/deepseek-v4-pro:cloud\""):]
	for name, o := range map[string]cloudSyncOpts{
		"plain":     {openrouter: true},
		"--dry-run": {openrouter: true, dryRun: true},
		"--yes":     {openrouter: true, yes: true},
	} {
		t.Run(name, func(t *testing.T) {
			path, cfg := cloudSyncHome(t, registry)
			got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
			asked := stubConfirm(t, true, nil, nil)
			synced := stubRouteSync(t, "")

			stdout, stderr, final, code := runCSFinal(t, cfg, o)
			if stdout != "openrouter: no OpenRouter-priced model in the registry; nothing to refresh\n" || stderr != "" || final != "" || code != 0 {
				t.Errorf("stdout = %q, stderr = %q, error %q, exit %d; want the one skip line and exit 0", stdout, stderr, final, code)
			}
			if urls := got.all(); len(urls) != 0 {
				t.Errorf("fetched %v, want no request", urls)
			}
			if *asked != 0 || *synced != 0 {
				t.Errorf("asked %d time(s), synced %d time(s); want neither", *asked, *synced)
			}
			if mustRead(t, path) != registry {
				t.Error("the skipped flow changed the registry")
			}
			if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
				t.Errorf("the skipped flow left %d files beside the registry, want only registry.toml", len(entries))
			}
		})
	}
}

// TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels pins who the
// openrouter flow refreshes: the models whose provider_id is "openrouter",
// and no other. The provider key openrouter_priced, which until #322 could
// put another provider's models in or take them out, is no longer read. A
// registry that still carries it must go on working: it loads, the key
// decides nothing either way (a cloud gateway marked true is not fetched
// for, and the openrouter provider's own models are refreshed although their
// row says false), nothing is printed about the key, and a write leaves it
// in the file, because wt never deletes a key it does not model.
func TestCloudSyncRefreshesOnlyTheOpenRouterProvidersModels(t *testing.T) {
	const relay = `[[providers]]
id = "relay"
name = "Relay"
location = "cloud"
openrouter_priced = true

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_RELAY_KEY"

[[models]]
id = "relay/vendor--gpt"
family = "gpt"
provider_id = "relay"
model_name = "vendor/gpt"
location = "cloud"
source = "curated"
tags = []
`

	t.Run("another cloud provider's model, marked true: not refreshed", func(t *testing.T) {
		path, cfg := cloudSyncHome(t, relay)
		if len(cfg.Models) != 1 || cfg.OpenRouterPriced(cfg.Models[0]) {
			t.Fatalf("config.OpenRouterPriced is true for a model of provider relay (%d models)", len(cfg.Models))
		}
		got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		synced := stubRouteSync(t, "")
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		if stdout != "openrouter: no OpenRouter-priced model in the registry; nothing to refresh\n" || stderr != "" || code != 0 {
			t.Errorf("stdout = %q, stderr = %q, exit %d", stdout, stderr, code)
		}
		if len(got.all()) != 0 || *synced != 0 || mustRead(t, path) != relay {
			t.Errorf("fetched %v, synced %d time(s) or changed the registry; want none of them", got.all(), *synced)
		}
	})

	t.Run("the openrouter provider's model, marked false: refreshed, and the key kept", func(t *testing.T) {
		text := strings.Replace(cloudSyncRegistry, "id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\n",
			"id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\nopenrouter_priced = false\n", 1) + "\n" + relay
		if strings.Count(text, "openrouter_priced = ") != 2 {
			t.Fatal("the fixture does not carry the key on both provider rows")
		}
		path, cfg := cloudSyncHome(t, text)
		got := stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
		synced := stubRouteSync(t, "")
		stdout, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true, yes: true})
		const want = "openrouter: openrouter.ai: 1 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
			"openrouter: Price updates (1):\n" +
			"openrouter:   openrouter/vendor--gpt: 2.5/-/10 -> 3/-/15\n" +
			"openrouter: Unchanged prices: 0\n" +
			"openrouter: refreshed 1 model(s); 1 price(s) changed\n"
		if stdout != want || stderr != "" || code != 0 {
			t.Errorf("stdout = %q\nstderr = %q, exit %d\nwant stdout %q", stdout, stderr, code, want)
		}
		if urls := got.all(); !reflect.DeepEqual(urls, []string{cloudsync.OpenRouterModelsURL}) || *synced != 1 {
			t.Errorf("fetched %v, synced %d time(s); want one request and one sync", urls, *synced)
		}
		after := mustRead(t, path)
		for _, kept := range []string{
			"id = \"openrouter\"\nname = \"OpenRouter\"\nlocation = \"cloud\"\nopenrouter_priced = false\n",
			"id = \"relay\"\nname = \"Relay\"\nlocation = \"cloud\"\nopenrouter_priced = true\n",
		} {
			if !strings.Contains(after, kept) {
				t.Errorf("the write did not keep a provider row as it was:\n%s\nfile:\n%s", kept, after)
			}
		}
		if strings.Contains(after, "id = \"relay/vendor--gpt\"\nfamily = \"gpt\"\nprovider_id = \"relay\"\nmodel_name = \"vendor/gpt\"\nlocation = \"cloud\"\nsource = \"curated\"\ntags = []\npricing_updated_at") {
			t.Error("the relay model was stamped: it is not the openrouter flow's")
		}
		if _, err := config.Load(); err != nil {
			t.Errorf("config.Load after the write: %v", err)
		}
	})
}

// TestCloudSyncSaysWhenNoModelCouldBeRefreshed pins the line a run prints
// when the registry has OpenRouter-priced models and OpenRouter priced none
// of them: a model it does not list, or one it prices "-1" (a router model).
// Such a run stamps nothing, so wt's stale-pricing notice goes on naming `wt
// cloud-sync` after every launch; without this line nothing says that
// running it again will not help, or where to look (the warnings, each of
// which names a model and what is wrong with it). A run that matched a model
// does not print it.
func TestCloudSyncSaysWhenNoModelCouldBeRefreshed(t *testing.T) {
	const line = "openrouter: no model could be refreshed, so nothing is stamped and wt's stale-pricing notice is not cleared; " +
		"the warnings above say why for each model\n"
	for name, body := range map[string]string{
		"OpenRouter does not list the model": `{"data": []}`,
		"OpenRouter prices the model at -1":  `{"data": [{"id": "vendor/gpt", "pricing": {"prompt": "-1", "completion": "-1"}}]}`,
	} {
		for _, o := range []cloudSyncOpts{{openrouter: true, dryRun: true}, {openrouter: true, yes: true}} {
			path, cfg := cloudSyncHome(t, cloudSyncRegistry)
			stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: body})
			synced := stubRouteSync(t, "")
			stdout, stderr, code := runCS(t, cfg, o)
			if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "\n"+line) || !strings.Contains(stdout, "openrouter: warning: ") {
				t.Errorf("%s (%+v): exit %d, stderr %q, stdout:\n%s\nwant exit 0, the model's warning, and last the line\n%s", name, o, code, stderr, stdout, line)
			}
			if mustRead(t, path) != cloudSyncRegistry || *synced != 0 {
				t.Errorf("%s (%+v): the run changed the registry or synced the routes", name, o)
			}
		}
	}

	_, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	if stdout, _, _ := runCS(t, cfg, cloudSyncOpts{openrouter: true, dryRun: true}); strings.Contains(stdout, "no model could be refreshed") {
		t.Errorf("a run that matched a model says none could be refreshed:\n%s", stdout)
	}
}

// TestCloudSyncRefusesADuplicatedModelIDBeforeThePlan pins a registry in
// which a model the refresh would price is there twice. The write addresses
// a row by its id and refuses such an id, so the plan could never be
// applied: the dry run must say so (exit 1) instead of printing price
// changes that the apply then refuses, and the apply must refuse before it
// asks. Both say the same line, which names the model and the file to fix.
func TestCloudSyncRefusesADuplicatedModelIDBeforeThePlan(t *testing.T) {
	start := strings.Index(cloudSyncRegistry, "[[models]]\nid = \"openrouter/vendor--gpt\"")
	end := strings.Index(cloudSyncRegistry, "[[models]]\nid = \"ollama/deepseek-v4-pro:cloud\"")
	registry := cloudSyncRegistry[:end] + cloudSyncRegistry[start:end] + cloudSyncRegistry[end:]
	for name, o := range map[string]cloudSyncOpts{
		"--dry-run": {openrouter: true, dryRun: true},
		"plain":     {openrouter: true},
		"--yes":     {openrouter: true, yes: true},
	} {
		t.Run(name, func(t *testing.T) {
			path, cfg := cloudSyncHome(t, registry)
			stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
			asked := stubConfirm(t, true, nil, nil)
			synced := stubRouteSync(t, "")
			stdout, stderr, code := runCS(t, cfg, o)
			want := "openrouter: error: no price was changed: model \"openrouter/vendor--gpt\" is in the registry twice (providers openrouter, openrouter); " +
				"wt cannot tell which one you mean — fix the entry in " + path + "\n"
			if stderr != want || stdout != "" || code != 1 {
				t.Errorf("stdout = %q\nstderr = %q, exit %d\nwant no plan, stderr %q and exit 1", stdout, stderr, code, want)
			}
			if *asked != 0 || *synced != 0 || mustRead(t, path) != registry {
				t.Errorf("asked %d time(s), synced %d time(s), or the registry changed; want none of them", *asked, *synced)
			}
		})
	}
}

// TestCloudSyncRefusedPlanDoesNotCreateARegistry pins the refusal of a plan
// that changed when the registry is gone by the time of the write (removed
// while the user reads the question). Nothing is applied, and no registry.toml
// is left behind: an empty one would make every later command load an empty
// registry instead of saying that there is none and naming `wt model init`.
func TestCloudSyncRefusedPlanDoesNotCreateARegistry(t *testing.T) {
	path, cfg := cloudSyncHome(t, cloudSyncRegistry)
	stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: openRouterBody})
	synced := stubRouteSync(t, "")
	stubConfirm(t, true, nil, func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	})

	_, stderr, code := runCS(t, cfg, cloudSyncOpts{openrouter: true})
	if want := "openrouter: error: the registry changed after the plan was printed; no price was changed — run it again\n"; stderr != want || code != 1 {
		t.Errorf("stderr = %q, exit %d; want %q and exit 1", stderr, code, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("registry.toml after the refused run: stat err = %v, want it still absent", err)
	}
	if *synced != 0 {
		t.Error("the refused run synced the routes")
	}
}

// timeOfDayRegistry holds two models OpenRouter prices by time of day, as a
// sync before #322 was fixed left them: deepseek at the price of the window
// that sync ran in (the cheap one), hy3 not yet priced.
const timeOfDayRegistry = `[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"

[providers.auth]
type = "api_key"
secret_ref = "WT_TEST_OPENROUTER_KEY"

[[models]]
id = "openrouter/deepseek--deepseek-v4-pro-0813"
family = "deepseek"
provider_id = "openrouter"
model_name = "deepseek/deepseek-v4-pro-0813"
location = "cloud"
source = "curated"
tags = []

[models.cost]
input_price_per_million = 0.66
cache_price_per_million = 0.022
output_price_per_million = 1.9800000000000002

[[models]]
id = "openrouter/tencent--hy3"
family = "hy"
provider_id = "openrouter"
model_name = "tencent/hy3"
location = "cloud"
source = "curated"
tags = []
`

// TestCloudSyncTimeOfDayPricesDoNotFollowTheClock is #322 end to end, on a
// saved OpenRouter response and a fixed clock. The same registry is planned
// from the list as OpenRouter serves it in deepseek's cheap window and in its
// dear one, at two clock times: the two dry runs print the same bytes. After
// one apply, a run on the other window's list finds no price to update,
// stamps the models and does not sync the routes — before the fix it
// reported an update each time the clock had crossed a window, and each one
// rewrote LiteLLM's config and could restart the proxy under running agents.
func TestCloudSyncTimeOfDayPricesDoNotFollowTheClock(t *testing.T) {
	// The fixture was fetched on a Friday at 14:06 UTC, in deepseek's cheap
	// window. In its dear window OpenRouter serves the same list with that
	// model's three top-level prices doubled, and nothing else different.
	raw, err := os.ReadFile("../../internal/cloudsync/testdata/openrouter_models.json")
	if err != nil {
		t.Fatal(err)
	}
	cheap := string(raw)
	const cheapTop = `"id": "deepseek/deepseek-v4-pro-0813", "pricing": {"prompt": "0.00000066", "completion": "0.00000198", "input_cache_read": "0.000000022",`
	const dearTop = `"id": "deepseek/deepseek-v4-pro-0813", "pricing": {"prompt": "0.00000132", "completion": "0.00000396", "input_cache_read": "0.000000044",`
	if strings.Count(cheap, cheapTop) != 1 {
		t.Fatal("the fixture does not hold deepseek's top-level prices as this test expects them")
	}
	dear := strings.Replace(cheap, cheapTop, dearTop, 1)

	path, _ := cloudSyncHome(t, timeOfDayRegistry)
	synced := stubRouteSync(t, "")
	run := func(body string, now time.Time, o cloudSyncOpts) string {
		t.Helper()
		stubCloudFetch(t, map[string]string{cloudsync.OpenRouterModelsURL: body})
		cloudSyncNow = func() time.Time { return now }
		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("config.Load: %v", err)
		}
		o.openrouter = true
		stdout, stderr, code := runCS(t, cfg, o)
		if stderr != "" || code != 0 {
			t.Fatalf("stdout = %q\nstderr = %q, exit %d", stdout, stderr, code)
		}
		return stdout
	}
	inCheapWindow := time.Date(2026, 10, 9, 14, 6, 0, 0, time.UTC)
	inDearWindow := time.Date(2026, 10, 12, 2, 45, 0, 0, time.UTC)

	const plan = "openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (2):\n" +
		"openrouter:   openrouter/deepseek--deepseek-v4-pro-0813: 0.66/0.022/1.98 -> 1.32/0.044/3.96\n" +
		"openrouter:   openrouter/tencent--hy3: no cost -> 0.132/0.033/0.528\n" +
		"openrouter: Unchanged prices: 0\n"
	if got := run(cheap, inCheapWindow, cloudSyncOpts{dryRun: true}); got != plan {
		t.Errorf("dry run in the cheap window:\n%s\nwant:\n%s", got, plan)
	}
	if got := run(dear, inDearWindow, cloudSyncOpts{dryRun: true}); got != plan {
		t.Errorf("dry run in the dear window:\n%s\nwant the same plan:\n%s", got, plan)
	}
	if mustRead(t, path) != timeOfDayRegistry || *synced != 0 {
		t.Fatal("a dry run changed the registry or synced the routes")
	}

	if got := run(cheap, inCheapWindow, cloudSyncOpts{yes: true}); !strings.HasSuffix(got, "openrouter: refreshed 2 model(s); 2 price(s) changed\n") || *synced != 1 {
		t.Fatalf("the apply printed %q and synced %d time(s), want 2 prices changed and one sync", got, *synced)
	}
	applied := mustRead(t, path)
	for _, want := range []string{
		"input_price_per_million = 1.32\ncache_price_per_million = 0.044\noutput_price_per_million = 3.9600000000000004\n\n[[models]]\n",
		"pricing_updated_at = \"2026-10-09T14:06:00+00:00\"",
	} {
		if !strings.Contains(applied, want) {
			t.Errorf("registry.toml after the apply lacks:\n%s\n\nfile:\n%s", want, applied)
		}
	}

	const settled = "openrouter: openrouter.ai: 2 OpenRouter-priced models in the registry (prices are input/cached/output per million tokens)\n" +
		"openrouter: Price updates (0):\n" +
		"openrouter: Unchanged prices: 2\n"
	if got := run(dear, inDearWindow, cloudSyncOpts{dryRun: true}); got != settled {
		t.Errorf("dry run in the dear window after the apply:\n%s\nwant:\n%s", got, settled)
	}
	if got := run(dear, inDearWindow, cloudSyncOpts{yes: true}); got != settled+"openrouter: refreshed 2 model(s); 0 price(s) changed\n" {
		t.Errorf("apply in the dear window after the first apply:\n%s", got)
	}
	if *synced != 1 {
		t.Errorf("the routes were synced %d time(s) in all, want once: the second apply changed no price", *synced)
	}
	if got, want := mustRead(t, path), strings.ReplaceAll(applied, "2026-10-09T14:06:00+00:00", "2026-10-12T02:45:00+00:00"); got != want {
		t.Errorf("registry.toml after the second apply:\n%s\nwant the first apply's file with the new stamp and nothing else:\n%s", got, want)
	}
}
