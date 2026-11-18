package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
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
	cmd := rootCmd(fakeDeps)
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
