package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// execLitellm runs `wt litellm <args>`, discarding both streams, and
// returns the command's error.
func execLitellm(args ...string) error {
	c := litellmCmd()
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetArgs(args)
	return c.Execute()
}

// litellmEnv points the config path at a temp file and the restart command
// at a no-op so no test can touch the real proxy or config.
func litellmEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_LITELLM_CONFIG", p)
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	return p
}

// TestLitellmStatusAndSet pins the proxy url/key functions the host keeps:
// `set` updates url/key in the wt-model home, and status never prints the key
// (only api_key_set) nor the retired enabled.
func TestLitellmStatusAndSet(t *testing.T) {
	litellmStateEnv(t)
	var out, errOut bytes.Buffer
	u, k := "http://localhost:4000", "sk-123456"
	if err := runLitellmSet(&out, &u, &k); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLitellmStatus(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sk-123456") {
		t.Fatalf("status leaked the api key: %s", out.String())
	}
	var st struct {
		URL       string `json:"url"`
		APIKeySet bool   `json:"api_key_set"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || strings.Contains(out.String(), "enabled") || st.URL != u || !st.APIKeySet {
		t.Fatalf("status = %s (%v)", out.String(), err)
	}
}

// litellmStateEnv is a scratch config home (empty registry) for the
// proxy-pair commands, and returns the legacy config.toml path — the
// read-only [litellm] fallback. The commands under test read and write the
// proxy pair alone (the wt-model home), so nothing here loads wt's own
// config.
func litellmStateEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	writeEmptyRegistry(t, home)
	return filepath.Join(home, ".config", "agent-wt", "config.toml")
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

// TestLitellmStatusTextMasksKey pins that text-mode status shows only
// `***last4` for a normal key and NEVER reveals a key of 4 or fewer chars
// (last-4 of a short key would print the whole secret), and that a home pair
// prints nothing on stderr (the fallback note is only for a legacy pair).
func TestLitellmStatusTextMasksKey(t *testing.T) {
	litellmStateEnv(t)
	u := "http://localhost:4000"
	for _, tc := range []struct{ key, want string }{{"sk-abcdef1234", "***1234"}, {"abcd", "***"}, {"ab", "***"}} {
		k := tc.key
		if err := runLitellmSet(&bytes.Buffer{}, &u, &k); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := runLitellmStatus(&out, &errOut, false); err != nil {
			t.Fatal(err)
		}
		if errOut.Len() != 0 {
			t.Errorf("key %q: stderr = %q, want none for a home pair", tc.key, errOut.String())
		}
		if !strings.Contains(out.String(), "api_key: "+tc.want+"\n") {
			t.Errorf("key %q: status = %q, want api_key: %s", tc.key, out.String(), tc.want)
		}
		if len(tc.key) <= 4 && strings.Contains(strings.TrimPrefix(out.String(), "litellm:"), tc.key) && tc.want == "***" {
			// key text (e.g. "ab") could collide with other words; only fail on the api_key line.
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.Contains(line, "api_key:") && strings.Contains(line, tc.key) {
					t.Errorf("short key %q leaked: %q", tc.key, line)
				}
			}
		}
	}
}

// TestLitellmSetIndependentAndClear pins `set`'s Changed semantics (C4) and
// where the pair lives now: a one-flag first set over a legacy pair is refused
// (it would drop the other field), then a flag left out never clobbers the other field,
// --api-key "" clears the key without deleting the key file, and every change
// lands in the wt-model home — the url in its config.toml (never the key),
// the key verbatim in the 0600 file its key_file names — where ResolveProxy,
// and so every reader, sees it. The legacy [litellm] table of config.toml is
// read-only now and its bytes are never touched.
func TestLitellmSetIndependentAndClear(t *testing.T) {
	legacyPath := litellmLegacyHome(t, false)
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	u, k := "http://localhost:4000", "sk-secret"
	// While the legacy table holds the pair, a one-flag first set would drop
	// the other field (sources never mix), so it is refused, writing nothing.
	if err := runLitellmSet(&bytes.Buffer{}, &u, nil); err == nil || !strings.Contains(err.Error(), "the first set needs both --url and --api-key") {
		t.Fatalf("url-only first set = %v, want the first-set refusal", err)
	}
	if _, err := os.Lstat(config.ProxyHomePath()); !os.IsNotExist(err) {
		t.Fatalf("home config.toml exists after the refused first set: %v", err)
	}
	first := "sk-first"
	if err := runLitellmSet(&bytes.Buffer{}, &u, &first); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u || pair.APIKey != first || pair.Source != config.ProxySourceHome {
		t.Fatalf("after the first set: %+v, want the home pair", pair)
	}
	if err := runLitellmSet(&bytes.Buffer{}, nil, &k); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u || pair.APIKey != k {
		t.Fatalf("key-only set clobbered the url or the key did not land: %+v", pair)
	}
	u2 := "http://other:4000"
	if err := runLitellmSet(&bytes.Buffer{}, &u2, nil); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u2 || pair.APIKey != k {
		t.Fatalf("url-only set clobbered the key: %+v", pair)
	}

	// The home's files: the url in its config.toml and the key verbatim in
	// the 0600 file its key_file names — the key itself never enters a TOML.
	home := config.ProxyHomePath()
	raw, err := os.ReadFile(home)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Litellm struct {
			URL     string `toml:"url"`
			KeyFile string `toml:"key_file"`
		} `toml:"litellm"`
	}
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		t.Fatalf("home config.toml does not parse: %v\n%s", err, raw)
	}
	if doc.Litellm.URL != u2 || doc.Litellm.KeyFile != "litellm.key" {
		t.Errorf("home [litellm] = %+v, want url %q and key_file \"litellm.key\"", doc.Litellm, u2)
	}
	if strings.Contains(string(raw), k) {
		t.Errorf("the api key leaked into config.toml:\n%s", raw)
	}
	keyPath := filepath.Join(filepath.Dir(home), "litellm.key")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("litellm.key mode = %v, want 0600", fi.Mode().Perm())
	}
	if b, err := os.ReadFile(keyPath); err != nil || string(b) != k {
		t.Errorf("litellm.key = %q (%v), want the key verbatim", b, err)
	}

	empty := ""
	if err := runLitellmSet(&bytes.Buffer{}, nil, &empty); err != nil {
		t.Fatal(err)
	}
	if pair := resolveProxy(t); pair.URL != u2 || pair.APIKey != "" {
		t.Fatalf("--api-key \"\" did not clear (or clobbered the url): %+v", pair)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("clearing the key deleted the key file: %v", err)
	}

	// The legacy table is never written.
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyAfter, legacyBefore) {
		t.Errorf("set rewrote the legacy config.toml:\n%s", legacyAfter)
	}
}

// TestLitellmRemovedExposeCommandsFailAndPointToSync pins the hidden stubs
// for the subcommands #179 removed: `wt litellm expose|unexpose` must exit
// non-zero and name `wt-model litellm sync`, with or without a legacy flag, and
// must not be advertised in help. Without the stubs cobra prints the parent's
// help and exits 0, so a script chaining `wt litellm expose X && ...` would
// report success while writing nothing.
func TestLitellmRemovedExposeCommandsFailAndPointToSync(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	for _, args := range [][]string{
		{"expose", "openrouter/x"},
		{"expose", "openrouter/x", "--json", "--skip-ready-gate", "--dry-run"},
		{"unexpose", "openrouter/x"},
		{"unexpose", "openrouter/x", "--json"},
	} {
		err := execLitellm(args...)
		if err == nil || !strings.Contains(err.Error(), "wt-model litellm sync") || !strings.Contains(err.Error(), "removed") {
			t.Errorf("%v: err = %v, want a removal error pointing to `wt-model litellm sync`", args, err)
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "model_list: []\n" {
		t.Fatalf("removed commands modified config.yaml:\n%s", b)
	}
	var help bytes.Buffer
	c := litellmCmd()
	c.SetOut(&help)
	c.SetErr(&bytes.Buffer{})
	c.SetArgs([]string{"--help"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	// sync moved to wt-model (its wt name is a hidden pointer the root adds),
	// so the live litellm help lists status and nothing removed.
	if !strings.Contains(help.String(), "status") || strings.Contains(help.String(), "expose") {
		t.Fatalf("help must list status and hide expose/unexpose:\n%s", help.String())
	}
}

// TestLitellmStateCommandsFailOnAnUnreadableProxyConfig pins ruling 8: `set`
// is gateless (the escape hatch when a config file is broken) and `status` is
// loud, each failing only on the files its own resolution needs. status fails
// on an unparseable wt-model home always, and on an unparseable legacy
// config.toml only when the home holds no [litellm] table — behind a home
// pair the legacy file is never read. set writes the home over a broken
// legacy file and refuses only when the home itself will not parse (never
// merging onto a document wt cannot read). `set` with no flags stays the one
// usage error and writes nothing. Every failure is one `wt:` line and exit 1
// (M3/C9); `sync`'s load-error refusal is pinned by
// TestLitellmLoadErrorNamesTheRepairThatWorks.
func TestLitellmStateCommandsFailOnAnUnreadableProxyConfig(t *testing.T) {
	const garbage = "this is [not toml\n"
	// newHome is a scratch home (empty registry) plus the two paths the
	// resolution reads: the legacy config.toml and the wt-model home's
	// config.toml.
	newHome := func(t *testing.T) (legacyPath, homePath string) {
		t.Helper()
		dir := t.TempDir()
		writeEmptyRegistry(t, dir)
		return filepath.Join(dir, ".config", "agent-wt", "config.toml"), config.ProxyHomePath()
	}
	write := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("status fails on an unparseable home", func(t *testing.T) {
		_, homePath := newHome(t)
		write(t, homePath, garbage)
		out, code := runRoot(t, "litellm", "status")
		if code != 1 || len(wtLines(out)) != 1 || !strings.Contains(out, homePath) {
			t.Errorf("exit %d, output %q; want 1 and one error line naming %s", code, out, homePath)
		}
	})
	t.Run("status fails on an unparseable legacy without a home table", func(t *testing.T) {
		legacyPath, _ := newHome(t)
		write(t, legacyPath, garbage)
		out, code := runRoot(t, "litellm", "status")
		if code != 1 || len(wtLines(out)) != 1 || !strings.Contains(out, "parse") {
			t.Errorf("exit %d, output %q; want 1 and one parse-error line", code, out)
		}
	})
	t.Run("status reads the home pair past an unparseable legacy", func(t *testing.T) {
		legacyPath, homePath := newHome(t)
		write(t, legacyPath, garbage)
		write(t, homePath, "[litellm]\nurl = \"http://localhost:4000\"\nkey_file = \"litellm.key\"\n")
		write(t, filepath.Join(filepath.Dir(homePath), "litellm.key"), "sk-home1234")
		out, code := runRoot(t, "litellm", "status")
		if code != 0 || !strings.Contains(out, "url: http://localhost:4000") || !strings.Contains(out, "api_key: ***1234") {
			t.Errorf("exit %d, output %q; want 0 and the home pair's status block", code, out)
		}
	})
	t.Run("set writes the home over an unparseable legacy", func(t *testing.T) {
		legacyPath, _ := newHome(t)
		write(t, legacyPath, garbage)
		out, code := runRoot(t, "litellm", "set", "--url", "http://x:4000", "--api-key", "sk-new1234")
		if code != 0 || strings.TrimSpace(out) != "litellm: updated" {
			t.Errorf("exit %d, output %q; want 0 and the one success line", code, out)
		}
		if pair := resolveProxy(t); pair.URL != "http://x:4000" || pair.APIKey != "sk-new1234" || pair.Source != config.ProxySourceHome {
			t.Fatalf("set did not leave the home pair: %+v", pair)
		}
	})
	t.Run("set fails on an unparseable home and writes nothing", func(t *testing.T) {
		_, homePath := newHome(t)
		write(t, homePath, garbage)
		out, code := runRoot(t, "litellm", "set", "--url", "http://x:4000")
		if code != 1 || len(wtLines(out)) != 1 {
			t.Errorf("exit %d, output %q; want 1 and one error line", code, out)
		}
		if b, _ := os.ReadFile(homePath); string(b) != garbage {
			t.Errorf("set rewrote a home it could not parse:\n%s", b)
		}
	})
	t.Run("set with no flags writes nothing", func(t *testing.T) {
		_, homePath := newHome(t)
		out, code := runRoot(t, "litellm", "set")
		if code != 1 || !strings.Contains(out, "nothing to set: pass --url and/or --api-key") {
			t.Errorf("exit %d, output %q; want 1 and the nothing-to-set message", code, out)
		}
		if _, err := os.Stat(filepath.Dir(homePath)); !os.IsNotExist(err) {
			t.Errorf("no-flag set wrote the config home (%v)", err)
		}
	})
}

// validationOnlyHome points a scratch config home at a registry with a model
// naming a missing provider: Load succeeds but Validate fails (a row whose
// provider has no row; `wt-model model init` seeds one for a provider it has
// a default for, any other is a hand edit of registry.toml).
func validationOnlyHome(t *testing.T) {
	t.Helper()
	sample, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "contracts", "registry.sample.toml"))
	if err != nil {
		t.Fatal(err)
	}
	modelHome(t, string(sample)+"\n[[models]]\nid = \"ghost/m\"\nprovider_id = \"ghost\"\nmodel_name = \"m\"\n")
	// The sample has a claude-subscription model, but the tests that use this
	// home run only `wt litellm status|set`, which never sync, so nothing
	// here reads the proxy's environment (cmd/wt-session links no route
	// code). The scratch WT_LITELLM_PLIST is only a backstop.
	t.Setenv("WT_LITELLM_PLIST", filepath.Join(t.TempDir(), "proxy.plist"))
}

// TestLitellmRoutingCommandsWorkOnValidationOnlyError pins that status/set
// read only the proxy pair (the wt-model home), so a registry that fails
// validation (a model referencing an unknown provider) cannot reach them and
// lock the user out of the one surface that repairs the pair, and the change
// persists where every reader resolves it (ResolveProxy).
func TestLitellmRoutingCommandsWorkOnValidationOnlyError(t *testing.T) {
	litellmEnv(t, "model_list: []\n")
	validationOnlyHome(t)
	if cfg, err := config.Load(); err != nil || cfg.Validate() == nil {
		t.Fatalf("fixture: load err=%v, want a registry that loads and fails validation", err)
	}
	for _, args := range [][]string{{"set", "--url", "http://localhost:4000", "--api-key", "sk-abcdef"}, {"status"}} {
		if err := execLitellm(args...); err != nil {
			t.Fatalf("%v refused on validation-only error: %v", args, err)
		}
	}
	if pair := resolveProxy(t); pair.URL != "http://localhost:4000" || pair.APIKey != "sk-abcdef" || pair.Source != config.ProxySourceHome {
		t.Fatalf("state not persisted where every reader resolves it: %+v", pair)
	}
}

// TestWtLitellmHelpListsOnlyTheProxyPairCommands pins how `wt litellm
// --help` reads after the move: status and set, and no line for a moved
// command (their pointer stubs are hidden). An old bin/update/litellm
// grepped this help for restart; it must not find one and run a pointer.
func TestWtLitellmHelpListsOnlyTheProxyPairCommands(t *testing.T) {
	out, code := runRoot(t, "litellm", "--help")
	if code != 0 {
		t.Fatalf("exit %d, output %q; want 0", code, out)
	}
	for _, sub := range []string{"status", "set"} {
		if !regexp.MustCompile(`(?m)^\s+` + sub + `\s`).MatchString(out) {
			t.Errorf("help has no %q line:\n%s", sub, out)
		}
	}
	if m := regexp.MustCompile(`(?m)^\s+(sync|list|providers|restart|expose|unexpose|on|off)\s`).FindString(out); m != "" {
		t.Errorf("help lists a moved or removed command %q:\n%s", m, out)
	}
}

// jsonKeySet returns a JSON object's top-level keys, sorted, or fails the test
// (helper for the contract pins below).
func jsonKeySet(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// litellmLegacyHome is a scratch config home whose config.toml holds the
// retired [litellm] enabled key beside a url and key, and an empty registry.
// It returns the config.toml path.
func litellmLegacyHome(t *testing.T, enabled bool) string {
	t.Helper()
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	writeEmptyRegistry(t, home)
	p := filepath.Join(home, ".config", "agent-wt", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("default_tag = \"code\"\n\n[litellm]\nenabled = %v\nurl = \"http://localhost:4000\"\napi_key = \"sk-host1234\"\n", enabled)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLitellmOnOffAreRetired pins the retirement of `wt litellm on|off`
// (Task 18, C5): each is a hidden stub, dated 2026-11-15, that exits 1 with
// one line saying every session routes through the model server and naming
// the [model-server] settings, and changes nothing in config.toml. Left
// working, `on` would flip a key nothing reads and let a user believe they had
// re-routed their agents; removed outright, cobra would print help and exit 0.
func TestLitellmOnOffAreRetired(t *testing.T) {
	for _, word := range []string{"on", "off"} {
		t.Run(word, func(t *testing.T) {
			p := litellmLegacyHome(t, false)
			before, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			out, code := runRoot(t, "litellm", word)
			want := "wt: litellm " + word + " is retired — every session routes through the model server; set url and token_file under [model-server] in config.toml; nothing was changed\n"
			if code != 1 || !strings.HasSuffix(out, want) || len(wtLines(out)) != 1 {
				t.Errorf("exit %d, output %q; want 1 and %q", code, out, want)
			}
			if after, _ := os.ReadFile(p); !bytes.Equal(after, before) {
				t.Errorf("config.toml changed:\n%s", after)
			}
		})
	}
	c := litellmCmd()
	for _, sub := range c.Commands() {
		if sub.Name() != "on" && sub.Name() != "off" {
			continue
		}
		if !sub.Hidden || sub.Annotations[removedCmdAnnotation] != "2026-11-15" {
			t.Errorf("litellm %s: hidden=%v annotation=%q, want hidden and dated 2026-11-15", sub.Name(), sub.Hidden, sub.Annotations[removedCmdAnnotation])
		}
	}
}

// TestLitellmStatusHasNoEnabledKey pins `wt litellm status` after the flip:
// its JSON is exactly {url, api_key_set} (the fixture's status member, which
// lost enabled), the text names no on/off mode, and the key itself is never
// printed. enabled no longer decides anything, so reporting it would tell a
// script that routing could be switched off. A pair the legacy [litellm]
// fallback supplies prints the one note on stderr (ruling 7), never on stdout.
func TestLitellmStatusHasNoEnabledKey(t *testing.T) {
	litellmLegacyHome(t, true)
	var out, errOut bytes.Buffer
	if err := runLitellmStatus(&out, &errOut, true); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("status --json = %q: %v", out.String(), err)
	}
	if want := map[string]any{"url": "http://localhost:4000", "api_key_set": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("status --json = %v, want %v", got, want)
	}
	raw, err := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if gotKeys, wantKeys := jsonKeySet(t, out.Bytes()), jsonKeySet(t, fixture["status"]); !slices.Equal(gotKeys, wantKeys) {
		t.Errorf("status keys = %v, fixture = %v", gotKeys, wantKeys)
	}
	out.Reset()
	errOut.Reset()
	if err := runLitellmStatus(&out, &errOut, false); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); strings.Contains(s, "litellm: on") || strings.Contains(s, "litellm: off") || strings.Contains(s, "sk-host1234") || !strings.Contains(s, "url: http://localhost:4000") {
		t.Errorf("status text = %q, want the url, no mode and no key", s)
	}
	if want := config.ProxyFallbackNote() + "\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want the one fallback note %q", errOut.String(), want)
	}
}

// TestLitellmEnabledNoteOncePerProcess pins the one note for the retired key:
// a config.toml with [litellm] enabled = true prints `wt: note: config.toml:
// [litellm] enabled is no longer used and is ignored` once per process, however
// many commands the process builds, and a file without it prints nothing.
// Printed on every command it would bury real output; never printed, a user
// would not learn their toggle stopped doing anything.
func TestLitellmEnabledNoteOncePerProcess(t *testing.T) {
	const note = "wt: note: config.toml: [litellm] enabled is no longer used and is ignored\n"
	resetIgnoredLitellmNote(t)
	litellmLegacyHome(t, false)
	errs := captureStderr(t)
	if _, err := newApp(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errs.String(), "[litellm] enabled") {
		t.Errorf("enabled = false noted: %q", errs.String())
	}
	litellmLegacyHome(t, true)
	for range 3 {
		if _, err := newApp(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(errs.String(), note); n != 1 {
		t.Errorf("note printed %d times, want once: %q", n, errs.String())
	}
}

// resetIgnoredLitellmNote forgets, for this test, that the process already
// printed the [litellm] enabled note.
func resetIgnoredLitellmNote(t *testing.T) {
	t.Helper()
	old := ignoredLitellmNoted.Load()
	ignoredLitellmNoted.Store(false)
	t.Cleanup(func() { ignoredLitellmNoted.Store(old) })
}

// TestLitellmSetRefusesAnAtURL pins the write side of #640 on the `wt
// litellm set` twin: a url with an '@' anywhere exits 1 with one `wt:` line,
// the fixed refusal, quoting no piece of the url, and writes no wt-model
// home, even with --api-key beside it.
func TestLitellmSetRefusesAnAtURL(t *testing.T) {
	for _, raw := range []string{"USERY:PASSY@litellm:4000", "http://USERY:PASSY@localhost:4000"} {
		dir := t.TempDir()
		writeEmptyRegistry(t, dir)
		out, code := runRoot(t, "litellm", "set", "--url", raw, "--api-key", "sk-new1234")
		if code != 1 || len(wtLines(out)) != 1 || !strings.Contains(out, config.ErrProxyURLHasAt.Error()) {
			t.Errorf("%q: exit %d, output %q; want 1 and the one fixed refusal line", raw, code, out)
		}
		if strings.Contains(out, "USERY") || strings.Contains(out, "PASSY") {
			t.Errorf("%q: the output quotes the url: %q", raw, out)
		}
		if _, err := os.Stat(filepath.Dir(config.ProxyHomePath())); !os.IsNotExist(err) {
			t.Errorf("%q: the wt-model home was written (%v)", raw, err)
		}
	}
}
