package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

// asWTModel sets config.BinaryName to "wt-model" the way main() does, so a
// shared line (the fallback note) a test reads outside a command run names
// this binary too.
func asWTModel(t *testing.T) {
	t.Helper()
	old := config.BinaryName
	config.BinaryName = "wt-model"
	t.Cleanup(func() { config.BinaryName = old })
}

// execLitellm runs `wt-model litellm <args>` over the real root tree (as main
// builds it, so the command's registration is part of what is exercised) with
// the two streams separate, and returns them with the command's error.
func execLitellm(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	asWTModel(t)
	cmd := rootCmd(fakeDeps, testRuntime(t))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append([]string{"litellm"}, args...))
	err = cmd.ExecuteContext(t.Context())
	return out.String(), errOut.String(), err
}

// proxyEnv points the config home at a fresh scratch directory and returns
// it. The package TestMain already isolates the developer's home for the
// whole process; this per-case redirect keeps one test's home files from
// reaching another's (the internal/config proxyEnv precedent).
func proxyEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

// setPair runs `wt-model litellm set <args>` and fails the test unless it
// prints the one success line and nothing on stderr.
func setPair(t *testing.T, args ...string) {
	t.Helper()
	stdout, stderr, err := execLitellm(t, append([]string{"set"}, args...)...)
	if err != nil {
		t.Fatalf("wt-model litellm set %v: %v", args, err)
	}
	if stdout != "litellm: updated\n" || stderr != "" {
		t.Fatalf("wt-model litellm set %v: stdout %q stderr %q, want the one success line", args, stdout, stderr)
	}
}

// resolveProxy is the proxy pair as every reader resolves it (ResolveProxy),
// so these tests assert persistence the way production sees it.
func resolveProxy(t *testing.T) config.ProxyPair {
	t.Helper()
	pair, err := config.ResolveProxy()
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// TestLitellmStatusMatchesTheFixture pins `wt-model litellm status --json` to
// the shared contract: with a home pair set, the document is exactly
// docs/contracts/litellm-cli.sample.json's status member — key set and
// values — the same assertion TestLitellmStatusHasNoEnabledKey makes on the
// wt side. The fixture is the golden both sides' tests assert against, so the
// twins cannot diverge (J3).
func TestLitellmStatusMatchesTheFixture(t *testing.T) {
	proxyEnv(t)
	setPair(t, "--url", "http://localhost:4000", "--api-key", "sk-test")
	stdout, _, err := execLitellm(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("status --json = %q: %v", stdout, err)
	}
	if err := json.Unmarshal(fixture["status"], &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status --json = %v, fixture = %v", got, want)
	}
}

// TestLitellmStatusTextMasksKey pins the text block byte-for-byte: the
// `litellm:\n  url: …\n  api_key: …\n` shape the wt side prints, the url
// printing (unset) when empty and the key printing masked — (unset) with no
// key, bare *** for a key of four characters or fewer, *** plus the last four
// otherwise — and never the key itself.
func TestLitellmStatusTextMasksKey(t *testing.T) {
	proxyEnv(t)
	for _, tc := range []struct {
		url, key, want string
	}{
		{"", "", "litellm:\n  url: (unset)\n  api_key: (unset)\n"},
		{"", "sk-12345678", "litellm:\n  url: (unset)\n  api_key: ***5678\n"},
		{"http://localhost:4000", "sk-abcdef1234", "litellm:\n  url: http://localhost:4000\n  api_key: ***1234\n"},
		{"http://localhost:4000", "abcd", "litellm:\n  url: http://localhost:4000\n  api_key: ***\n"},
		{"http://localhost:4000", "ab", "litellm:\n  url: http://localhost:4000\n  api_key: ***\n"},
	} {
		if tc.url != "" {
			setPair(t, "--url", tc.url)
		}
		if tc.key != "" {
			setPair(t, "--api-key", tc.key)
		}
		stdout, stderr, err := execLitellm(t, "status")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if stdout != tc.want {
			t.Errorf("url %q key %q: status =\n  %q\nwant\n  %q", tc.url, tc.key, stdout, tc.want)
		}
		if stderr != "" {
			t.Errorf("url %q key %q: stderr = %q, want none", tc.url, tc.key, stderr)
		}
	}
}

// TestLitellmSetIndependentAndClear pins `wt-model litellm set`'s Changed
// semantics (C4): a flag left out never clobbers the other field, --api-key
// "" clears the key without deleting anything, every success prints the one
// `litellm: updated` line, and each change is persisted where ResolveProxy —
// and so every reader — sees it.
func TestLitellmSetIndependentAndClear(t *testing.T) {
	proxyEnv(t)
	setPair(t, "--url", "http://localhost:4000")
	if pair := resolveProxy(t); pair.URL != "http://localhost:4000" || pair.APIKey != "" || pair.Source != config.ProxySourceHome {
		t.Fatalf("after url-only: %+v", pair)
	}
	setPair(t, "--api-key", "sk-secret")
	if pair := resolveProxy(t); pair.URL != "http://localhost:4000" || pair.APIKey != "sk-secret" {
		t.Fatalf("url-only set clobbered or key did not land: %+v", pair)
	}
	setPair(t, "--url", "http://other:4000")
	if pair := resolveProxy(t); pair.URL != "http://other:4000" || pair.APIKey != "sk-secret" {
		t.Fatalf("url-only set clobbered the key: %+v", pair)
	}
	setPair(t, "--api-key", "")
	if pair := resolveProxy(t); pair.URL != "http://other:4000" || pair.APIKey != "" {
		t.Fatalf("--api-key \"\" did not clear (or clobbered the url): %+v", pair)
	}
}

// TestLitellmSetNeedsSomething pins the usage error: `set` with neither flag
// refuses with the one message naming both, and writes nothing.
func TestLitellmSetNeedsSomething(t *testing.T) {
	home := proxyEnv(t)
	_, _, err := execLitellm(t, "set")
	if err == nil || err.Error() != "nothing to set: pass --url and/or --api-key" {
		t.Fatalf("set with no flags: err = %v, want the nothing-to-set message", err)
	}
	if _, serr := os.Stat(filepath.Join(home, "wt-model")); !os.IsNotExist(serr) {
		t.Errorf("no-flag set wrote the config home (%v)", serr)
	}
}

// TestLitellmStatusNotesTheLegacyFallback pins the one note line (M4/N1): a
// pair that comes from the legacy [litellm] table prints the shared
// ProxyFallbackNote on stderr — once, `wt-model: note:`-prefixed, verbatim —
// and never on stdout; with --json stdout stays the single JSON document and
// the note still goes to stderr (ruling 7: the note fires at `status`, and
// stderr is where it cannot pollute a document).
func TestLitellmStatusNotesTheLegacyFallback(t *testing.T) {
	asWTModel(t)
	home := proxyEnv(t)
	cfgPath := filepath.Join(home, "agent-wt", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "default_tag = \"code\"\n\n[litellm]\nurl = \"http://localhost:4000\"\napi_key = \"sk-legacy1234\"\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	note := config.ProxyFallbackNote() + "\n"
	if !strings.HasPrefix(note, "wt-model: note: ") {
		t.Fatalf("the shared note does not start %q: %q", "wt-model: note: ", note)
	}

	stdout, stderr, err := execLitellm(t, "status")
	if err != nil {
		t.Fatal(err)
	}
	if want := "litellm:\n  url: http://localhost:4000\n  api_key: ***1234\n"; stdout != want {
		t.Errorf("status = %q, want %q", stdout, want)
	}
	if stderr != note {
		t.Errorf("stderr = %q, want the one note line %q", stderr, note)
	}

	stdout, stderr, err = execLitellm(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || strings.Contains(stdout, "note") {
		t.Fatalf("status --json = %q (%v), want the single JSON document", stdout, err)
	}
	if want := map[string]any{"url": "http://localhost:4000", "api_key_set": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("status --json = %v, want %v", got, want)
	}
	if stderr != note {
		t.Errorf("--json stderr = %q, want the one note line %q", stderr, note)
	}
}

// TestLitellmSetHelpNamesTheSetupCommand pins M6 on the one help text that
// names the setup command: `wt-model litellm set --help` must show
// `wt-model litellm set --url URL --api-key KEY` contiguous on one unwrapped
// line, so copying the quoted command out of the help runs as-is (a wrap
// mid-command turns the copy-paste into two shell words and a second
// command). The fallback note's short `'wt-model litellm set'` wording is
// separately mandated verbatim and is not this string.
func TestLitellmSetHelpNamesTheSetupCommand(t *testing.T) {
	stdout, _, err := execLitellm(t, "set", "--help")
	if err != nil {
		t.Fatal(err)
	}
	const want = "wt-model litellm set --url URL --api-key KEY"
	if !strings.Contains(stdout, want) {
		t.Errorf("`wt-model litellm set --help` does not show %q contiguous:\n%s", want, stdout)
	}
}

// TestLitellmStatusRefusesAnUnstatableKeyFile pins fix round 2, Finding 1 on
// the command the review reproduced it on: a key file the OS cannot stat (its
// key_file traverses a regular file — ENOTDIR) must fail `status` loud with
// the stat reason, never exit 0 displaying `api_key: (unset)` over a key it
// could not inspect. The spec makes only a missing or empty key file "no key".
func TestLitellmStatusRefusesAnUnstatableKeyFile(t *testing.T) {
	home := proxyEnv(t)
	dir := filepath.Join(home, "wt-model")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "regular-file"), []byte("a regular file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "[litellm]\nurl = \"http://localhost:4000\"\nkey_file = \"regular-file/key\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := execLitellm(t, "status")
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("status over an unstatable key file: stdout %q err %v; want the stat error", stdout, err)
	}
	if strings.Contains(stdout, "(unset)") {
		t.Errorf("status printed %q over a key it could not inspect; want no status document", stdout)
	}
}

// TestLitellmSetFreshClearWritesTheEmptyKeyFile pins fix round 2, Finding 2
// at the command the review reproduced it on: a fresh `wt-model litellm set`
// with a url and an explicitly empty key must create the key file the spec
// requires (empty, 0600, never deleted and never left absent) — before the
// fix it printed `litellm: updated`, created config.toml and left
// litellm.key missing.
func TestLitellmSetFreshClearWritesTheEmptyKeyFile(t *testing.T) {
	home := proxyEnv(t)
	setPair(t, "--url", "http://localhost:4000", "--api-key", "")
	keyPath := filepath.Join(home, "wt-model", "litellm.key")
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file after a fresh explicit clear: %v; want the empty 0600 file the spec requires, never absence", err)
	}
	if st.Mode().Perm() != 0o600 || st.Size() != 0 {
		t.Errorf("key file: mode %v size %d, want 0600 and empty", st.Mode().Perm(), st.Size())
	}
}

// TestLitellmSetRefusesAnAtURL pins the write side of #640 through the real
// command tree: `wt-model litellm set --url` with an '@' anywhere in the url
// exits 1 with the fixed refusal (as main reports it: one `wt-model:` line)
// that quotes no piece of the url, prints nothing else, and writes nothing,
// even with --api-key beside it.
func TestLitellmSetRefusesAnAtURL(t *testing.T) {
	for _, raw := range []string{"USERY:PASSY@litellm:4000", "http://USERY:PASSY@localhost:4000", "http://localhost:4000/?k=USERY@PASSY"} {
		home := proxyEnv(t)
		stdout, stderr, err := execLitellm(t, "set", "--url", raw, "--api-key", "sk-new")
		var rep bytes.Buffer
		code := exitcode.Report("wt-model", err, &rep)
		if code != 1 || !errors.Is(err, config.ErrProxyURLHasAt) {
			t.Errorf("%q: exit %d err %v, want 1 and the fixed refusal", raw, code, err)
		}
		all := stdout + stderr + rep.String()
		if strings.Contains(all, "USERY") || strings.Contains(all, "PASSY") || stdout != "" {
			t.Errorf("%q: output quotes the url or reports success: stdout %q stderr %q report %q", raw, stdout, stderr, rep.String())
		}
		if rep.String() != "wt-model: "+config.ErrProxyURLHasAt.Error()+"\n" {
			t.Errorf("%q: report %q, want the one fixed line", raw, rep.String())
		}
		if _, serr := os.Stat(filepath.Join(home, "wt-model")); !os.IsNotExist(serr) {
			t.Errorf("%q: the wt-model home was written (%v)", raw, serr)
		}
	}
}

// TestLitellmListAndProviders pins the read-only commands' JSON shapes: the
// "routed" id list and ownership rows of `list --json`, and the per-provider
// policy of `providers --json` (cloud or not). They are what a script reads
// to learn what the proxy serves; a renamed key breaks it.
func TestLitellmListAndProviders(t *testing.T) {
	litellmEnv(t, "model_list:\n  - model_name: a/b\n    litellm_params: {model: x}\n")
	var out bytes.Buffer
	if err := runLitellmList(t.Context(), &out, io.Discard, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"routed":["a/b"],"rows":[{"id":"a/b","managed":false,"store":"file"}]}` {
		t.Fatalf("list = %s", out.String())
	}
	out.Reset()
	if err := runLitellmProviders(&out, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Providers map[string]struct{ Cloud bool } `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.Providers["openrouter"].Cloud || got.Providers["ollama"].Cloud {
		t.Fatalf("providers = %s (%v)", out.String(), err)
	}
}

// TestLitellmListTextOutput pins list's human-readable output: a plain table,
// one line per row, with where the route lives (STORE) and who owns it
// (OWNER: wt or hand-written). A user reads OWNER to tell which rows are
// their own; a missing one misleads them about which rows wt may rewrite or
// remove. (Before #598 a wt row printed as its bare id; database routes made
// a second column necessary.)
func TestLitellmListTextOutput(t *testing.T) {
	litellmEnv(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
    model_info: {wt_managed: true}
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
`)
	var out bytes.Buffer
	if err := runLitellmList(t.Context(), &out, io.Discard, false, nil, nil); err != nil {
		t.Fatal(err)
	}
	const want = "ID               STORE  OWNER\nollama/gemma:9b  file   wt\nhand/alias       file   hand-written\n"
	if got := out.String(); got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
}

// TestLitellmListJSONRows pins list's ownership rows (with their store, #598)
// next to the older "routed" id list, which stays for scripts that already
// read it.
func TestLitellmListJSONRows(t *testing.T) {
	litellmEnv(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
    model_info: {wt_managed: true}
  - model_name: hand/alias
    litellm_params: {model: openrouter/x}
`)
	var out bytes.Buffer
	if err := runLitellmList(t.Context(), &out, io.Discard, true, nil, nil); err != nil {
		t.Fatal(err)
	}
	const want = `{"routed":["ollama/gemma:9b","hand/alias"],"rows":[{"id":"ollama/gemma:9b","managed":true,"store":"file"},{"id":"hand/alias","managed":false,"store":"file"}]}`
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("list = %s\nwant   %s", got, want)
	}
}

// TestLitellmListReadsNoRegistry pins that `wt-model litellm list` and
// `providers` read only config.yaml and the provider policy table: a
// registry.toml that does not parse must not stop the one read-only answer
// to "is this model routed?" (they never had a config gate in wt either).
func TestLitellmListReadsNoRegistry(t *testing.T) {
	home := proxyEnv(t)
	reg := filepath.Join(home, "local-ai", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(reg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg, []byte("models = [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	litellmEnv(t, "model_list:\n  - model_name: a/b\n    litellm_params: {model: x}\n")
	stdout, stderr, err := execLitellm(t, "list")
	if err != nil || stdout != "ID   STORE  OWNER\na/b  file   hand-written\n" || stderr != "" {
		t.Fatalf("list: err %v stdout %q stderr %q", err, stdout, stderr)
	}
	stdout, _, err = execLitellm(t, "providers", "--json")
	if err != nil {
		t.Fatalf("providers --json: %v", err)
	}
	var got struct {
		Providers map[string]struct{ Cloud bool } `json:"providers"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || !got.Providers["openrouter"].Cloud || got.Providers["ollama"].Cloud {
		t.Fatalf("providers = %s (%v)", stdout, err)
	}
}

// TestLitellmRestartWaitsAndReports pins `wt-model litellm restart`: the
// restart `wt-model litellm sync` runs (here the WT_LITELLM_RESTART_CMD
// override) and the wait after it, taken with no pre-restart probe so a proxy
// that was down is waited for too (the probe stub says nothing is listening),
// one line on stdout when the proxy answers again, and an error naming the
// cause when the restart command fails or the proxy never answers.
// bin/update/litellm runs it after an upgrade in place of `launchctl
// kickstart -k`, which kills a request in flight after 5 seconds; a failure
// that exited 0 would leave the updater reporting a proxy it never restarted.
func TestLitellmRestartWaitsAndReports(t *testing.T) {
	const url = "http://proxy.test:4000"
	for _, tc := range []struct {
		name, cmd string
		waitErr   error
		wantErr   string
	}{
		{name: "restarted and answering", cmd: "echo restart >> RESTARTS"},
		{name: "the restart command fails", cmd: "echo restart >> RESTARTS; exit 3", wantErr: "exit status 3"},
		{name: "the proxy never answers", cmd: "echo restart >> RESTARTS", waitErr: errors.New("LiteLLM proxy not ready at " + url + "/health/liveliness: context deadline exceeded"),
			wantErr: "LiteLLM proxy not ready at " + url},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restarts := filepath.Join(t.TempDir(), "restarts")
			t.Setenv("WT_LITELLM_RESTART_CMD", strings.ReplaceAll(tc.cmd, "RESTARTS", restarts))
			var steps []string
			t.Cleanup(litellm.StubProxyReadinessForTest(
				func(_ context.Context, u string, _ time.Duration) bool {
					steps = append(steps, "probe "+u)
					return false
				},
				func(_ context.Context, u string, _ time.Duration) error {
					b, _ := os.ReadFile(restarts)
					steps = append(steps, fmt.Sprintf("wait %s after %q", u, b))
					return tc.waitErr
				},
			))

			var out bytes.Buffer
			err := runLitellmRestart(&out, config.ProxyPair{URL: url}, nil)
			if want := []string{fmt.Sprintf("wait %s after %q", url, "restart\n")}; !slices.Equal(steps, want) {
				t.Errorf("steps = %q, want %q", steps, want)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				if got, want := out.String(), "restarted the LiteLLM proxy at "+url+"\n"; got != want {
					t.Errorf("stdout = %q, want %q", got, want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if out.Len() != 0 {
				t.Errorf("a failed restart printed %q on stdout", out.String())
			}
		})
	}
}

// TestLitellmRestartHidesAnAtURL pins the success line's one url rule
// (#640): a stored pair url holding an '@' (hand-written, or set by an older
// wt) is printed only as config.ProxyURLHidden, never in part.
func TestLitellmRestartHidesAnAtURL(t *testing.T) {
	const url = "http://USERX:PASSX@proxy.test:4000"
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	t.Cleanup(litellm.StubProxyReadinessForTest(
		func(context.Context, string, time.Duration) bool { return false },
		func(context.Context, string, time.Duration) error { return nil },
	))
	var out bytes.Buffer
	if err := runLitellmRestart(&out, config.ProxyPair{URL: url}, nil); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got, want := out.String(), "restarted the LiteLLM proxy at "+config.ProxyURLHidden+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestLitellmRestartReadsOnlyTheProxyPair pins what the restart reads: the
// pair alone. An unresolvable pair refuses before anything restarts (a
// restart that skipped its wait as "no url" would hide the broken pair);
// no url restarts unwaited with today's line; a trailing slash is trimmed
// as Config.LitellmBaseURL trimmed it.
func TestLitellmRestartReadsOnlyTheProxyPair(t *testing.T) {
	stubSteps := func(t *testing.T) *[]string {
		var steps []string
		t.Cleanup(litellm.StubProxyReadinessForTest(
			func(_ context.Context, u string, _ time.Duration) bool {
				steps = append(steps, "probe "+u)
				return false
			},
			func(_ context.Context, u string, _ time.Duration) error {
				b, _ := os.ReadFile(os.Getenv("RESTARTS_FILE"))
				steps = append(steps, fmt.Sprintf("wait %s after %q", u, b))
				return nil
			},
		))
		return &steps
	}
	restartsIn := func(t *testing.T) string {
		restarts := filepath.Join(t.TempDir(), "restarts")
		t.Setenv("RESTARTS_FILE", restarts)
		t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
		return restarts
	}
	t.Run("unresolvable pair", func(t *testing.T) {
		restarts := restartsIn(t)
		steps := stubSteps(t)
		var out bytes.Buffer
		err := runLitellmRestart(&out, config.ProxyPair{}, errors.New("bad home"))
		if err == nil || !strings.HasPrefix(err.Error(), "config error: ") {
			t.Fatalf("err = %v, want a config error", err)
		}
		if out.Len() != 0 || len(*steps) != 0 {
			t.Errorf("stdout %q, steps %q; want neither", out.String(), *steps)
		}
		if _, statErr := os.Stat(restarts); !os.IsNotExist(statErr) {
			t.Errorf("the restart ran over an unresolvable pair (%v)", statErr)
		}
	})
	t.Run("no url", func(t *testing.T) {
		restartsIn(t)
		steps := stubSteps(t)
		var out bytes.Buffer
		if err := runLitellmRestart(&out, config.ProxyPair{}, nil); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got, want := out.String(), "restarted the LiteLLM proxy (no [litellm] url, so it was not waited for)\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
		if len(*steps) != 0 {
			t.Errorf("steps = %q, want none", *steps)
		}
	})
	t.Run("trailing slash", func(t *testing.T) {
		restartsIn(t)
		steps := stubSteps(t)
		var out bytes.Buffer
		if err := runLitellmRestart(&out, config.ProxyPair{URL: "http://proxy.test:4000/"}, nil); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if want := []string{"wait http://proxy.test:4000 after \"restart\\n\""}; !slices.Equal(*steps, want) {
			t.Errorf("steps = %q, want %q", *steps, want)
		}
		if got, want := out.String(), "restarted the LiteLLM proxy at http://proxy.test:4000\n"; got != want {
			t.Errorf("stdout = %q, want %q", got, want)
		}
	})
}

// TestLitellmRestartNeedsNoRegistry runs `wt-model litellm restart` through
// the real tree over a registry.toml that does not parse: the restart reads
// only the proxy pair, never the registry, so bin/update/litellm can restart
// the proxy on a machine whose registry is broken. With a pair set the
// restart still waits for the pair's url (a registry read would lose the
// pair and skip the wait); with none it restarts unwaited.
func TestLitellmRestartNeedsNoRegistry(t *testing.T) {
	brokenRegistry := func(t *testing.T, home string) {
		t.Helper()
		registry := filepath.Join(home, "local-ai", "registry.toml")
		if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registry, []byte("models = [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restartsFile := func(t *testing.T) string {
		t.Helper()
		restarts := filepath.Join(t.TempDir(), "restarts")
		t.Setenv("WT_LITELLM_RESTART_CMD", "echo restart >> "+restarts)
		return restarts
	}
	t.Run("pair set", func(t *testing.T) {
		home := proxyEnv(t)
		setPair(t, "--url", "http://proxy.test:4000", "--api-key", "sk-abcdef")
		brokenRegistry(t, home)
		restarts := restartsFile(t)
		var waited []string
		t.Cleanup(litellm.StubProxyReadinessForTest(
			func(context.Context, string, time.Duration) bool { return false },
			func(_ context.Context, u string, _ time.Duration) error {
				waited = append(waited, u)
				return nil
			},
		))
		stdout, _, err := execLitellm(t, "restart")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if want := []string{"http://proxy.test:4000"}; !slices.Equal(waited, want) {
			t.Errorf("waited for %q, want %q", waited, want)
		}
		if want := "restarted the LiteLLM proxy at http://proxy.test:4000\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if b, _ := os.ReadFile(restarts); string(b) != "restart\n" {
			t.Errorf("restarts = %q, want one restart", b)
		}
	})
	t.Run("no pair", func(t *testing.T) {
		home := proxyEnv(t)
		brokenRegistry(t, home)
		restarts := restartsFile(t)
		t.Cleanup(litellm.StubProxyReadinessForTest(
			func(context.Context, string, time.Duration) bool { return false },
			func(_ context.Context, u string, _ time.Duration) error {
				t.Errorf("waited for %s with no pair url", u)
				return nil
			},
		))
		stdout, _, err := execLitellm(t, "restart")
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if want := "restarted the LiteLLM proxy (no [litellm] url, so it was not waited for)\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		if b, _ := os.ReadFile(restarts); string(b) != "restart\n" {
			t.Errorf("restarts = %q, want one restart", b)
		}
	})
}

// TestWtModelLitellmHelpListsRestart pins the line bin/update/litellm
// feature-detects (`wt-model litellm --help | grep -Eq
// '^[[:space:]]+restart[[:space:]]'`): without it the updater falls back to
// launchctl kickstart -k, which cuts off a request in flight.
func TestWtModelLitellmHelpListsRestart(t *testing.T) {
	stdout, _, err := execLitellm(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"restart", "sync", "list", "providers", "status", "set"} {
		if !regexp.MustCompile(`(?m)^\s+` + sub + `\s`).MatchString(stdout) {
			t.Errorf("help has no %q line:\n%s", sub, stdout)
		}
	}
}
