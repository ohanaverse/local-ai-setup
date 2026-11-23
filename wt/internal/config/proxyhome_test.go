package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
)

// ptr returns a pointer to v; SetProxy takes optional *string fields.
func ptr[T any](v T) *T { return &v }

// proxyEnv points the config home at a scratch directory holding the given
// legacy config.toml body ("" writes none) and returns the home directory.
// The package TestMain already isolates the config home; the per-case
// redirect keeps one test's files from reaching another's.
func proxyEnv(t *testing.T, legacyBody string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	if legacyBody != "" {
		must(t, filepath.Join(home, "agent-wt", "config.toml"), legacyBody)
	}
	return home
}

// writeProxyHome writes the wt-model home's config.toml under home and
// returns its path.
func writeProxyHome(t *testing.T, home, body string) string {
	t.Helper()
	p := filepath.Join(home, "wt-model", "config.toml")
	must(t, p, body)
	return p
}

// writeProxyKey writes the wt-model home's default key file at the given mode
// and returns its path. The mode is forced after the write: os.WriteFile
// applies its perm only on creation, and cases here rewrite one key file at
// several modes.
func writeProxyKey(t *testing.T, home, key string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(home, "wt-model", "litellm.key")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(key), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestProxyHomeFixtureMatchesTheTypes checks that the contract fixture
// docs/contracts/wt-model-config.sample.toml decodes strictly into the same
// struct ResolveProxy reads (no undecoded key) and round-trips to the same
// document, and that it opens with the exact header comment block SetProxy
// re-prepends. It matters because the fixture is the one document later tasks
// and hand edits read: a renamed key or a new field must break this test, not
// a user's home.
func TestProxyHomeFixtureMatchesTheTypes(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/contracts/wt-model-config.sample.toml")
	if err != nil {
		t.Fatal(err)
	}
	var doc proxyHomeDoc
	md, err := toml.Decode(string(raw), &doc)
	if err != nil {
		t.Fatalf("fixture does not decode into proxyHomeDoc: %v", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) != 0 {
		t.Fatalf("fixture has keys the type does not name: %v", undecoded)
	}
	if doc.Litellm == nil {
		t.Fatal("fixture's [litellm] table did not decode to a present table")
	}
	if doc.Litellm.URL != "http://localhost:4000" || doc.Litellm.KeyFile != "litellm.key" {
		t.Errorf("[litellm] = %+v, want url http://localhost:4000 and key_file litellm.key", doc.Litellm)
	}
	// The hand-edited [gateway] table is part of the document, and the
	// fixture's switch is off: a sample never shows passthrough on.
	if v, ok := doc.Gateway["subscription_passthrough"]; !ok || v != false {
		t.Errorf("[gateway] subscription_passthrough = %#v (present %t), want false", v, ok)
	}
	if gc, warns := gatewayFromDoc(doc); gc.SubscriptionPassthrough || len(warns) != 0 {
		t.Errorf("fixture [gateway] reads as %+v, %q; want off with no warning", gc, warns)
	}
	if !bytes.HasPrefix(raw, []byte(proxyHomeHeader)) {
		t.Errorf("fixture does not open with the header SetProxy writes:\n%s", proxyHomeHeader)
	}

	// Round-trip: re-encode the decoded document and compare parsed
	// documents (key set + values), never byte order — the encoder's
	// layout is not the contract.
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&doc); err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if _, err := toml.Decode(string(raw), &want); err != nil {
		t.Fatal(err)
	}
	if _, err := toml.Decode(buf.String(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("fixture does not round-trip through the type:\nfixture: %s\nencode:  %s", raw, buf.String())
	}
}

// TestProxyHomePathIsUnderTheScratchHome pins where the home resolves: under
// the config home the environment names, exactly — a suffix-only assertion
// would still pass if a regression stopped honoring XDG and landed in the
// developer's real ~/.config. ProxyHomePath with no home at all must be "" so
// nothing resolves or writes anywhere.
func TestProxyHomePathIsUnderTheScratchHome(t *testing.T) {
	t.Setenv("HOME", "/dev/null/no-home")
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	want := filepath.Join("/custom/xdg", "wt-model", "config.toml")
	if got := ProxyHomePath(); got != want {
		t.Errorf("ProxyHomePath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := ProxyHomePath(); got != "" {
		t.Errorf("ProxyHomePath() with no home = %q, want \"\"", got)
	}
}

// TestProxyHomeFollowsTheConfigHome pins the home's resolution against the
// real isolation helper (a Review Focus regression pin): after
// IsolateConfigHomeForTest the home is exactly the scratch config home's
// wt-model/config.toml, never anything under the real ~/.config — a
// regression that dropped the XDG handling would resolve, and then write,
// the developer's own wt-model home. The exact-path assertion is the
// load-bearing one (a suffix-only check would still pass a resolve into the
// real ~/.config), and a path query writes nothing anywhere: no wt-model
// directory appears even under the scratch home.
func TestProxyHomeFollowsTheConfigHome(t *testing.T) {
	realConfigHome := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		realConfigHome = filepath.Join(home, ".config")
	}
	// t.Setenv restores the helper's os.Setenv after the test — the pattern
	// TestIsolateConfigHomeForTestClearsBothRegistryNames relies on.
	t.Setenv("XDG_CONFIG_HOME", "/somewhere/else")
	scratch, cleanup := IsolateConfigHomeForTest()
	defer cleanup()

	got := ProxyHomePath()
	if want := filepath.Join(scratch, "wt-model", "config.toml"); got != want {
		t.Fatalf("ProxyHomePath() = %q, want the scratch config home's %q", got, want)
	}
	if realConfigHome != "" && strings.HasPrefix(got, realConfigHome+string(filepath.Separator)) {
		t.Errorf("ProxyHomePath() = %q falls under the real %q; the real home must stay untouched", got, realConfigHome)
	}
	if st, err := os.Lstat(filepath.Join(scratch, "wt-model")); !os.IsNotExist(err) {
		t.Errorf("wt-model under the scratch home after the query: %v, %v; want no directory at all", st, err)
	}
}

// TestModelHomeIsTheProxyHomesDirectory pins that servestate and the proxy
// pair name one directory: ModelHome is the directory ProxyHomePath's
// config.toml sits in, so serve's runtime record never lands beside a
// different home. With no config home at all both are "" (nothing resolves
// or writes).
func TestModelHomeIsTheProxyHomesDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if got, want := ModelHome(), filepath.Join(dir, "wt-model"); got != want {
		t.Errorf("ModelHome() = %q, want %q", got, want)
	}
	if got, want := ProxyHomePath(), filepath.Join(ModelHome(), "config.toml"); got != want {
		t.Errorf("ProxyHomePath() = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := ModelHome(); got != "" {
		t.Errorf("ModelHome() with no home = %q, want \"\"", got)
	}
	if got := ProxyHomePath(); got != "" {
		t.Errorf("ProxyHomePath() with no home = %q, want \"\"", got)
	}
}

// TestResolveProxyPrefersTheHomeTable pins rule 1 of the merge: a home
// [litellm] table is authoritative and complete — its url and its key file's
// content win over a differently-valued legacy [litellm] table. The home is
// wt-model's own; without this precedence the migration would keep reading
// the pair from the file it is leaving.
func TestResolveProxyPrefersTheHomeTable(t *testing.T) {
	home := proxyEnv(t, "default_tag = \"code\"\n\n[litellm]\nurl = \"http://legacy:4000\"\napi_key = \"sk-legacy\"\n")
	writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4001\"\nkey_file = \"litellm.key\"\n")
	writeProxyKey(t, home, "sk-test", 0o600)
	pair, err := ResolveProxy()
	if err != nil {
		t.Fatal(err)
	}
	want := ProxyPair{URL: "http://localhost:4001", APIKey: "sk-test", Source: ProxySourceHome}
	if pair != want {
		t.Errorf("ResolveProxy() = %+v, want %+v", pair, want)
	}
}

// TestResolveProxyNeverMixesSources pins that one pair comes from one source:
// a home table that supplies no key never borrows the legacy one (a mix would
// silently pair a new url with a stale key), a home file without the table
// falls back whole to the legacy table, nothing configured anywhere is
// ProxySourceNone, and an empty home [litellm] table is authoritative — an
// empty pair, not absence — so a user who cleared the home cannot half-return
// to the legacy values.
func TestResolveProxyNeverMixesSources(t *testing.T) {
	const legacy = "default_tag = \"code\"\n\n[litellm]\nurl = \"http://legacy:4000\"\napi_key = \"sk-legacy\"\n"
	legacyPair := ProxyPair{URL: "http://legacy:4000", APIKey: "sk-legacy", Source: ProxySourceLegacy}
	for _, tc := range []struct {
		name   string
		legacy string
		home   string // "" writes no home config.toml
		want   ProxyPair
	}{
		{"home url and no key file", legacy, "[litellm]\nurl = \"http://localhost:4001\"\n",
			ProxyPair{URL: "http://localhost:4001", Source: ProxySourceHome}},
		{"home url and a missing key file", legacy, "[litellm]\nurl = \"http://localhost:4001\"\nkey_file = \"gone.key\"\n",
			ProxyPair{URL: "http://localhost:4001", Source: ProxySourceHome}},
		{"empty home table is authoritative", legacy, "[litellm]\n",
			ProxyPair{Source: ProxySourceHome}},
		{"home file without the table falls back", legacy, "default_tag = \"code\"\n", legacyPair},
		{"legacy only", legacy, "", legacyPair},
		{"nothing anywhere", "", "", ProxyPair{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := proxyEnv(t, tc.legacy)
			if tc.home != "" {
				writeProxyHome(t, home, tc.home)
			}
			pair, err := ResolveProxy()
			if err != nil {
				t.Fatal(err)
			}
			if pair != tc.want {
				t.Errorf("ResolveProxy() = %+v, want %+v", pair, tc.want)
			}
		})
	}
}

// TestResolveProxyRefusesALooseKeyFile pins the key file rules: group or
// other bits on the key file are a hard error carrying the chmod 600 fix
// (a secret others can read must not be used silently), while a missing or
// empty key file is "no key" — the consumer's not-configured path reports it
// — and the key is whitespace-trimmed on the way in.
func TestResolveProxyRefusesALooseKeyFile(t *testing.T) {
	home := proxyEnv(t, "")
	homeCfg := "[litellm]\nurl = \"http://localhost:4001\"\nkey_file = \"litellm.key\"\n"

	writeProxyHome(t, home, homeCfg)
	writeProxyKey(t, home, "sk-loose", 0o644)
	if _, err := ResolveProxy(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("ResolveProxy() on a 0644 key file = %v, want an error naming chmod 600", err)
	}

	writeProxyKey(t, home, "", 0o600)
	pair, err := ResolveProxy()
	if err != nil {
		t.Fatalf("empty key file: %v, want no error", err)
	}
	if pair.APIKey != "" || pair.Source != ProxySourceHome {
		t.Errorf("empty key file resolved %+v, want an empty key from the home", pair)
	}

	writeProxyKey(t, home, "  sk-pad\n ", 0o600)
	pair, err = ResolveProxy()
	if err != nil {
		t.Fatal(err)
	}
	if pair.APIKey != "sk-pad" {
		t.Errorf("APIKey = %q, want the whitespace-trimmed %q", pair.APIKey, "sk-pad")
	}
}

// TestResolveProxyRefusesABrokenFile pins the resolution's hard-error half
// (fix round 1, Finding 7): a file the resolution needs but cannot read is
// the resolution's error and the zero pair, never a fall-through to another
// source's values. A regression that turned readProxyHome's parse error into
// "absent" — or swallowed readConfigFile's — would silently serve legacy
// values exactly as the constraints forbid and nothing else in the file
// would notice. The unparseable-home case keeps a valid legacy table present
// precisely so the forbidden fall-through would be visible.
func TestResolveProxyRefusesABrokenFile(t *testing.T) {
	t.Run("an unparseable home config.toml", func(t *testing.T) {
		home := proxyEnv(t, "default_tag = \"code\"\n\n[litellm]\nurl = \"http://legacy:4000\"\napi_key = \"sk-legacy\"\n")
		writeProxyHome(t, home, "this is = = not toml\n")
		pair, err := ResolveProxy()
		if err == nil {
			t.Fatalf("ResolveProxy() on an unparseable home = %+v, nil; want an error and never the legacy pair", pair)
		}
		if pair != (ProxyPair{}) {
			t.Errorf("ResolveProxy() returned %+v beside the error, want the zero pair", pair)
		}
	})
	t.Run("an unparseable legacy config.toml", func(t *testing.T) {
		home := proxyEnv(t, "")
		must(t, filepath.Join(home, "agent-wt", "config.toml"), "this is = = not toml\n")
		pair, err := ResolveProxy()
		if err == nil {
			t.Fatalf("ResolveProxy() on an unparseable legacy config = %+v, nil; want an error", pair)
		}
		if pair != (ProxyPair{}) {
			t.Errorf("ResolveProxy() returned %+v beside the error, want the zero pair", pair)
		}
	})
	t.Run("an unreadable key file", func(t *testing.T) {
		home := proxyEnv(t, "")
		writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4001\"\nkey_file = \"litellm.key\"\n")
		writeProxyKey(t, home, "sk-unreadable", 0o000)
		pair, err := ResolveProxy()
		if err == nil {
			t.Fatalf("ResolveProxy() on a 0000 key file = %+v, nil; want an error, never a silent proceed", pair)
		}
		if pair != (ProxyPair{}) {
			t.Errorf("ResolveProxy() returned %+v beside the error, want the zero pair", pair)
		}
	})
}

// TestResolveProxyNeverQuotesAnUnquotedURL pins #640's source fix: a pair url
// typed without quotes (url = USERX:PASSX@hq) stops the TOML parser, whose
// message quotes the token in front of the '@' (found "USERX") without the
// '@' itself, so no '@' heuristic downstream can catch it. Both readers, the
// wt-model home and the legacy agent-wt table, report the line and the key
// only.
func TestResolveProxyNeverQuotesAnUnquotedURL(t *testing.T) {
	for _, body := range []string{
		"[litellm]\nurl = USERX:PASSX@hq\n",
		"[litellm]\nurl = PASSX@litellm:4000\n",
		"[litellm]\nurl = \"http://USERX:PASSX@hq\n",
	} {
		for _, where := range []string{"home", "legacy"} {
			home := proxyEnv(t, "")
			if where == "home" {
				writeProxyHome(t, home, body)
			} else {
				must(t, filepath.Join(home, "agent-wt", "config.toml"), body)
			}
			_, err := ResolveProxy()
			if err == nil {
				t.Fatalf("%s %q: ResolveProxy() = nil error, want a parse error", where, body)
			}
			msg := err.Error()
			if strings.Contains(msg, "USERX") || strings.Contains(msg, "PASSX") {
				t.Errorf("%s %q: the error quotes the url: %q", where, body, msg)
			}
			if !strings.Contains(msg, "line 2") || !strings.Contains(msg, "url") {
				t.Errorf("%s %q: the error %q does not name the line and the key", where, body, msg)
			}
		}
	}
}

// TestResolveProxyLegacyNeedsNoMigrations pins the legacy read's contract:
// readConfigFile only — no Load, no schema migration, no write. The fixture
// is a config.toml Load would rewrite (its google agent is renamed to agy),
// so a byte change here would mean resolution migrated the file behind the
// user's back.
func TestResolveProxyLegacyNeedsNoMigrations(t *testing.T) {
	const legacyBody = `default_tag = "code"

[[agents]]
name = "google"
supported_providers = ["ollama"]

[litellm]
url = "http://legacy:4000"
api_key = "sk-legacy"
`
	home := proxyEnv(t, legacyBody)
	before, err := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := ResolveProxy()
	if err != nil {
		t.Fatal(err)
	}
	if want := (ProxyPair{URL: "http://legacy:4000", APIKey: "sk-legacy", Source: ProxySourceLegacy}); pair != want {
		t.Errorf("ResolveProxy() = %+v, want %+v", pair, want)
	}
	after, err := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("legacy config.toml was rewritten by a resolve:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestMaskKey pins how both binaries show a secret: never the key itself.
// "(unset)" when there is none, bare "***" for a key of four characters or
// fewer (its last four would be the whole secret), "***" plus the last four
// otherwise — enough for the user to recognise the key in a shell history.
func TestMaskKey(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"", "(unset)"},
		{"ab", "***"},
		{"abcd", "***"},
		{"abcde", "***bcde"},
		{"sk-12345678", "***5678"},
	} {
		if got := MaskKey(tc.key); got != tc.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// TestWriteProxyStatusMatchesTheFixtureMember pins the `status` document both
// CLIs print: the JSON member must equal docs/contracts/litellm-cli.sample.json's
// status golden (compared as parsed documents — key set and values, never
// byte order) and the text block is byte-for-byte, with the url printing
// (unset) when empty and the key printing masked. A script pins the JSON and
// a user reads the text; either drifting breaks them silently.
func TestWriteProxyStatusMatchesTheFixtureMember(t *testing.T) {
	var out bytes.Buffer
	pair := ProxyPair{URL: "http://localhost:4000", APIKey: "sk-host1234"}
	if err := WriteProxyStatus(&out, pair, true); err != nil {
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
	var want, got map[string]any
	if err := json.Unmarshal(fixture["status"], &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("status --json = %q: %v", out.String(), err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status --json = %v, fixture = %v", got, want)
	}

	out.Reset()
	if err := WriteProxyStatus(&out, pair, false); err != nil {
		t.Fatal(err)
	}
	if wantText := "litellm:\n  url: http://localhost:4000\n  api_key: ***1234\n"; out.String() != wantText {
		t.Errorf("status text = %q, want %q", out.String(), wantText)
	}

	// The url keeps today's trailing-slash trim (runLitellmStatus printed
	// LitellmBaseURL()), and the empty pair prints (unset) twice.
	out.Reset()
	if err := WriteProxyStatus(&out, ProxyPair{URL: "http://localhost:4000/"}, false); err != nil {
		t.Fatal(err)
	}
	if wantText := "litellm:\n  url: http://localhost:4000\n  api_key: (unset)\n"; out.String() != wantText {
		t.Errorf("status text with a trailing slash = %q, want %q", out.String(), wantText)
	}
	out.Reset()
	if err := WriteProxyStatus(&out, ProxyPair{}, false); err != nil {
		t.Fatal(err)
	}
	if wantText := "litellm:\n  url: (unset)\n  api_key: (unset)\n"; out.String() != wantText {
		t.Errorf("status text for the empty pair = %q, want %q", out.String(), wantText)
	}
}

// TestProxyFallbackNote pins the one note line verbatim: both binaries print
// it with their own name (BinaryName twice) and it names the home path, so a
// rewording must land here — the line is quoted in prose and in later tasks'
// command output.
func TestProxyFallbackNote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	defer func(old string) { BinaryName = old }(BinaryName)
	BinaryName = "wt-model"
	want := "wt-model: note: config.toml: [litellm] is a read-only fallback — 'wt-model litellm set' writes /custom/xdg/wt-model/config.toml now"
	if got := ProxyFallbackNote(); got != want {
		t.Errorf("ProxyFallbackNote() = %q, want %q", got, want)
	}
	if got := ProxyFallbackNote(); strings.Contains(got, "\n") {
		t.Errorf("ProxyFallbackNote() = %q, want no trailing newline", got)
	}
}

// TestSetProxyWritesOnlyTheNewHome pins the writer's contract for a fresh
// home: config.toml at 0644 with the header comment and [litellm]'s url plus
// the explicit key_file default, the key in litellm.key at 0600 — and the
// point, the legacy agent-wt/config.toml byte-identical after. SetProxy
// writes the new home and nothing else, so a broken legacy config or
// registry can never block the one command that fixes the pair.
func TestSetProxyWritesOnlyTheNewHome(t *testing.T) {
	home := proxyEnv(t, "default_tag = \"code\"\n\n[litellm]\nurl = \"http://legacy:4000\"\napi_key = \"sk-legacy\"\n")
	legacyPath := filepath.Join(home, "agent-wt", "config.toml")
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(ptr("http://localhost:4002"), ptr("sk-new")); err != nil {
		t.Fatal(err)
	}

	cfgPath := ProxyHomePath()
	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("home config.toml: %v", err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("home config.toml mode = %v, want 0644", st.Mode().Perm())
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := proxyHomeHeader + "[litellm]\n  url = \"http://localhost:4002\"\n  key_file = \"litellm.key\"\n"
	if string(body) != wantBody {
		t.Errorf("home config.toml =\n%s\nwant\n%s", body, wantBody)
	}

	keyPath := filepath.Join(home, "wt-model", defaultProxyKeyFile)
	st, err = os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %v, want 0600", st.Mode().Perm())
	}
	if key, _ := os.ReadFile(keyPath); string(key) != "sk-new" {
		t.Errorf("key file content = %q, want %q", key, "sk-new")
	}

	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacyBefore, legacyAfter) {
		t.Errorf("legacy config.toml changed:\nbefore: %s\nafter:  %s", legacyBefore, legacyAfter)
	}
}

// TestSetProxyClearsTheKeyWithoutDeletingTheFile pins the clear operation:
// a set of the empty key writes a 0600 empty key file — wt never deletes a
// key file, because key_file may name a user-managed file (a credential
// helper's) — and a nil field is left unchanged, url included (its TOML is
// not even rewritten).
func TestSetProxyClearsTheKeyWithoutDeletingTheFile(t *testing.T) {
	home := proxyEnv(t, "")
	if err := SetProxy(ptr("http://localhost:4005"), ptr("sk-old")); err != nil {
		t.Fatal(err)
	}
	cfgBefore, err := os.ReadFile(ProxyHomePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(nil, ptr("")); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, "wt-model", defaultProxyKeyFile)
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file after clear: %v, want a 0600 empty file, never deleted", err)
	}
	if st.Mode().Perm() != 0o600 || st.Size() != 0 {
		t.Errorf("key file after clear: mode %v size %d, want 0600 and empty", st.Mode().Perm(), st.Size())
	}
	cfgAfter, err := os.ReadFile(ProxyHomePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfgBefore, cfgAfter) {
		t.Errorf("url changed on a key-only set:\nbefore: %s\nafter:  %s", cfgBefore, cfgAfter)
	}
}

// TestSetProxyNoOpPreservesBytes pins ruling 10 (the UpdateRegistry
// contract, byte for byte): a set that changes nothing writes nothing, so
// hand comments in config.toml and the key file's exact bytes survive; a
// write that does change something re-encodes (the hand comments die) and
// re-prepends the fixed header comment block. A key file differing from the
// set value only in whitespace is "unchanged" too — the key's identity is
// tokenfile.Load's trimmed token — and its bytes, newline included, survive.
func TestSetProxyNoOpPreservesBytes(t *testing.T) {
	home := proxyEnv(t, "")
	if err := SetProxy(ptr("http://localhost:4003"), ptr("sk-same")); err != nil {
		t.Fatal(err)
	}
	cfgPath := ProxyHomePath()
	hand := proxyHomeHeader + "[litellm]\n# keep this note\n  url = \"http://localhost:4003\"\n  key_file = \"litellm.key\"\n# and this one\n"
	must(t, cfgPath, hand)
	cfgBefore, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, "wt-model", defaultProxyKeyFile)
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := SetProxy(ptr("http://localhost:4003"), ptr("sk-same")); err != nil {
		t.Fatal(err)
	}
	cfgAfter, _ := os.ReadFile(cfgPath)
	if !bytes.Equal(cfgBefore, cfgAfter) {
		t.Errorf("a no-op set rewrote config.toml (hand comments lost):\nbefore: %s\nafter:  %s", cfgBefore, cfgAfter)
	}
	keyAfter, _ := os.ReadFile(keyPath)
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Errorf("a no-op set rewrote the key file: %q → %q", keyBefore, keyAfter)
	}

	// A changing write re-encodes and re-prepends the header.
	if err := SetProxy(ptr("http://localhost:4004"), nil); err != nil {
		t.Fatal(err)
	}
	cfgAfter, _ = os.ReadFile(cfgPath)
	wantBody := proxyHomeHeader + "[litellm]\n  url = \"http://localhost:4004\"\n  key_file = \"litellm.key\"\n"
	if string(cfgAfter) != wantBody {
		t.Errorf("config.toml after a changing write =\n%s\nwant\n%s", cfgAfter, wantBody)
	}
	keyAfter, _ = os.ReadFile(keyPath)
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Errorf("a url-only write touched the key file: %q → %q", keyBefore, keyAfter)
	}

	// The key's identity is what a reader sees — tokenfile.Load's trimmed
	// token (fix round 1, Finding 2 keeps this raw-byte preservation) — so a
	// hand-written key file that differs from the set value only in
	// whitespace is unchanged and its exact bytes, newline included, survive.
	writeProxyKey(t, home, "sk-newline\n", 0o600)
	keyBefore, err = os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(nil, ptr("sk-newline")); err != nil {
		t.Fatal(err)
	}
	keyAfter, _ = os.ReadFile(keyPath)
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Errorf("a whitespace-only difference rewrote the key file: %q → %q", keyBefore, keyAfter)
	}
}

// TestSetProxyRefusesAPartialFirstSet pins the review ruling on #7: the first
// set creates the authoritative [litellm] table and sources never mix, so a
// first set with --url alone (or --api-key alone) would silently drop the
// legacy table's key (or url) — `status` then shows it unset and serve
// refuses. It is refused instead, writing nothing, and the legacy pair keeps
// resolving; once the table exists, a one-field set works as before. With no
// legacy table there is nothing to drop, so a one-field first set works.
func TestSetProxyRefusesAPartialFirstSet(t *testing.T) {
	home := litellmStateEnv(t, `default_tag = "code"

[litellm]
url = "http://legacy:4000"
api_key = "sk-legacy"
`)
	for _, tc := range []struct {
		name     string
		url, key *string
	}{
		{"url alone", ptr("http://new:4000"), nil},
		{"api key alone", nil, ptr("sk-new")},
	} {
		err := SetProxy(tc.url, tc.key)
		if err == nil || !strings.Contains(err.Error(), "the first set needs both --url and --api-key") {
			t.Fatalf("%s: SetProxy = %v, want the first-set refusal", tc.name, err)
		}
		if _, err := os.Lstat(filepath.Join(home, "wt-model", "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("%s: home config.toml exists after the refusal: %v", tc.name, err)
		}
		if _, err := os.Lstat(filepath.Join(home, "wt-model", defaultProxyKeyFile)); !os.IsNotExist(err) {
			t.Fatalf("%s: key file exists after the refusal: %v", tc.name, err)
		}
	}
	pair, err := ResolveProxy()
	if err != nil || pair.Source != ProxySourceLegacy || pair.APIKey != "sk-legacy" {
		t.Fatalf("after the refusals ResolveProxy = %+v, %v; want the legacy pair", pair, err)
	}
	if err := SetProxy(ptr("http://new:4000"), ptr("sk-new")); err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(ptr("http://newer:4000"), nil); err != nil {
		t.Fatalf("a url-only set over an existing table = %v, want it to work", err)
	}
	if pair, err := ResolveProxy(); err != nil || pair.URL != "http://newer:4000" || pair.APIKey != "sk-new" {
		t.Errorf("ResolveProxy = %+v, %v; want the newer url and the kept key", pair, err)
	}

	t.Run("no legacy table", func(t *testing.T) {
		proxyEnv(t, "")
		if err := SetProxy(ptr("http://fresh:4000"), nil); err != nil {
			t.Fatalf("a url-only first set with no legacy table = %v, want it to work", err)
		}
	})
}

// TestSetProxyNeverObservesATornPairUnderConcurrentSets pins fix round 1,
// Finding 4: ResolveProxy reads the home TOML and its key file under the same
// flock SetProxy writes with, so an observer always sees one whole generation
// of the pair — a url and a key from the same writer state — never
// {old url, new key} across the two-file write (applyProxyState caches a torn
// pair for the process lifetime, so a tear is exactly the "sources never
// mix" violation). Writers publish paired url-i/key-i generations while
// readers resolve concurrently; every observation must be the empty
// pre-publication pair or one generation's exact pair. Meant to run under
// -race.
func TestSetProxyNeverObservesATornPairUnderConcurrentSets(t *testing.T) {
	proxyEnv(t, "")
	const writersN, gens, readersN = 4, 12, 4
	total := writersN * gens
	check := func(pair ProxyPair) error {
		if pair == (ProxyPair{}) {
			return nil // the pre-publication pair: coherent by construction
		}
		if pair.Source != ProxySourceHome {
			return fmt.Errorf("resolved (%q) from source %v, want ProxySourceHome", pair.URL, pair.Source)
		}
		u := strings.TrimSuffix(strings.TrimPrefix(pair.URL, "http://gen-"), ":4000")
		k := strings.TrimPrefix(pair.APIKey, "sk-gen-")
		ui, uerr := strconv.Atoi(u)
		ki, kerr := strconv.Atoi(k)
		if uerr != nil || kerr != nil || ui != ki || ui < 0 || ui >= total {
			return fmt.Errorf("torn pair: url generation %q and key generation %q (url %q)", u, k, pair.URL)
		}
		return nil
	}

	stop := make(chan struct{})
	var writers, readers sync.WaitGroup
	writers.Add(writersN)
	for w := range writersN {
		go func(w int) {
			defer writers.Done()
			for g := range gens {
				i := w*gens + g
				if err := SetProxy(ptr(fmt.Sprintf("http://gen-%d:4000", i)), ptr(fmt.Sprintf("sk-gen-%d", i))); err != nil {
					t.Errorf("writer %d generation %d: %v", w, i, err)
					return
				}
			}
		}(w)
	}
	readers.Add(readersN)
	for range readersN {
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				pair, err := ResolveProxy()
				if err != nil {
					t.Errorf("ResolveProxy: %v", err)
					return
				}
				if err := check(pair); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	writers.Wait()
	close(stop)
	readers.Wait()
}

// TestSetProxyConcurrentSetsLoseNoField pins the flock plus fresh re-read:
// two writers setting disjoint fields must both land — the final pair is the
// merged intent every round, never one writer's change swallowing the other's
// (issue #143's lost-update class) and never a torn url/key pairing, because
// both files are written under the one lock.
func TestSetProxyConcurrentSetsLoseNoField(t *testing.T) {
	proxyEnv(t, "")
	for round := range 20 {
		base := fmt.Sprintf("http://base:%d", round)
		if err := SetProxy(ptr(base), ptr(fmt.Sprintf("sk-base%d", round))); err != nil {
			t.Fatal(err)
		}
		url := fmt.Sprintf("http://a:%d", round)
		key := fmt.Sprintf("sk-b%d", round)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := SetProxy(ptr(url), nil); err != nil {
				t.Errorf("round %d url write: %v", round, err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := SetProxy(nil, ptr(key)); err != nil {
				t.Errorf("round %d key write: %v", round, err)
			}
		}()
		wg.Wait()
		pair, err := ResolveProxy()
		if err != nil {
			t.Fatal(err)
		}
		if want := (ProxyPair{URL: url, APIKey: key, Source: ProxySourceHome}); pair != want {
			t.Fatalf("round %d lost a field: got %+v, want %+v", round, pair, want)
		}
	}
}

// TestSetProxyWritesThroughSymlinks pins ruling 11 (the internal/litellm Save
// precedent): a config.toml or key file that is a symlink — a dotfiles repo —
// is written through, the link surviving with its target updated, while a
// dangling link is an error and never silently replaced by a fresh file (that
// would quietly un-adopt the user's dotfiles pointer).
func TestSetProxyWritesThroughSymlinks(t *testing.T) {
	home := proxyEnv(t, "")
	homeDir := filepath.Join(home, "wt-model")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dotfiles := t.TempDir()
	realCfg := filepath.Join(dotfiles, "wt-model-config.toml")
	must(t, realCfg, "[litellm]\nurl = \"http://old:1\"\nkey_file = \"litellm.key\"\n")
	realKey := filepath.Join(dotfiles, "litellm.key")
	if err := os.WriteFile(realKey, []byte("sk-link"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgLink := filepath.Join(homeDir, "config.toml")
	keyLink := filepath.Join(homeDir, defaultProxyKeyFile)
	if err := os.Symlink(realCfg, cfgLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realKey, keyLink); err != nil {
		t.Fatal(err)
	}

	if err := SetProxy(ptr("http://localhost:4006"), ptr("sk-target")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{cfgLink, keyLink} {
		st, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s was replaced, want the symlink to survive", p)
		}
	}
	pair, err := ResolveProxy()
	if err != nil {
		t.Fatal(err)
	}
	if want := (ProxyPair{URL: "http://localhost:4006", APIKey: "sk-target", Source: ProxySourceHome}); pair != want {
		t.Errorf("write-through resolved %+v, want %+v", pair, want)
	}

	// A dangling key link errors when the key write needs it, and the link
	// is left as it is.
	if err := os.Remove(realKey); err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(nil, ptr("sk-newer")); err == nil {
		t.Error("SetProxy wrote through a dangling key link, want an error")
	}
	if st, err := os.Lstat(keyLink); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dangling key link after the refusal: %v, %v; want the link untouched", st, err)
	}

	// The same for a dangling config.toml link.
	if err := os.Remove(cfgLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(homeDir, "missing.toml"), cfgLink); err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(ptr("http://localhost:4007"), nil); err == nil {
		t.Error("SetProxy replaced a dangling config.toml link, want an error")
	}
	if st, err := os.Lstat(cfgLink); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dangling config link after the refusal: %v, %v; want the link untouched", st, err)
	}
}

// TestSetProxyRefusesALooseKeyFile pins fix round 1, Finding 2: the set's
// no-op comparison reads the key file through tokenfile.Load — the one
// definition of "what the key is" — so a loose (group or other accessible)
// key file errors out of the set instead of a matching value computing "no
// change" and returning silent success while every read refuses it. The file
// is left exactly as it is — the set never rewrites bytes or "fixes" the mode
// behind the user's back on a refusal — and no home config.toml appears
// beside it.
func TestSetProxyRefusesALooseKeyFile(t *testing.T) {
	home := proxyEnv(t, "")
	keyPath := writeProxyKey(t, home, "sk-old", 0o644)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	// The finding's scenario: the set value equals the file's key. Before
	// the fix this computed "unchanged" and returned nil — a silent success
	// over a key every read errors on.
	if err := SetProxy(nil, ptr("sk-old")); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("SetProxy with a loose key file = %v, want the chmod 600 error (never a silent no-op success)", err)
	}
	if err := SetProxy(nil, ptr("sk-new")); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("SetProxy changing a loose key file = %v, want the chmod 600 error", err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("loose key file was rewritten on a refusal: %q → %q", before, after)
	}
	if st, err := os.Stat(keyPath); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("loose key file mode = %v, %v; want the 0644 left as it was", st, err)
	}
	if _, err := os.Lstat(ProxyHomePath()); !os.IsNotExist(err) {
		t.Errorf("home config.toml appeared despite the refusal: %v", err)
	}
}

// TestSetProxyRefusesAnUnparseableHome pins ruling 8's loud half: a home
// config.toml wt cannot parse fails the set (merging onto a document it
// cannot read would drop whatever it could not see), the file is left
// untouched, and no key file appears beside it.
func TestSetProxyRefusesAnUnparseableHome(t *testing.T) {
	home := proxyEnv(t, "")
	cfgPath := writeProxyHome(t, home, "this is = = not toml\n")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetProxy(ptr("http://localhost:4008"), ptr("sk-x")); err == nil {
		t.Fatal("SetProxy on a broken home succeeded, want an error")
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("broken home config.toml was touched:\nbefore: %s\nafter:  %s", before, after)
	}
	if _, err := os.Lstat(filepath.Join(home, "wt-model", defaultProxyKeyFile)); !os.IsNotExist(err) {
		t.Errorf("key file created despite the refusal: %v", err)
	}
}

// TestLoadResolvesTheProxyPairIntoTheRuntimeState pins ruling 1 (the merge
// at load, not a resolver at each call site): after Load the runtime pair behind LitellmBaseURL/LitellmAPIKey is the home's when the
// home has a [litellm] table and the legacy table's otherwise, with the
// trailing-slash trim kept, and ProxySource says which. LitellmTable stays
// the untouched legacy parse (TestLegacyLitellmKeysSurviveASave's rule), so
// every existing consumer keeps working with zero call-site changes while the
// pair itself comes from the new home. LoadSession never opens the home (the
// spec's "No session-behavior change" anti-goal): it keeps the inert copy of
// the legacy table it always made, with no source.
func TestLoadResolvesTheProxyPairIntoTheRuntimeState(t *testing.T) {
	const legacy = `default_tag = "code"

[litellm]
url = "http://legacy:4000"
api_key = "sk-legacy"
`
	check := func(t *testing.T, cfg *Config, url, key string, source ProxySource) {
		t.Helper()
		if cfg.LitellmBaseURL() != url || cfg.LitellmAPIKey() != key {
			t.Errorf("runtime pair = %q/%q, want %q/%q", cfg.LitellmBaseURL(), cfg.LitellmAPIKey(), url, key)
		}
		if cfg.ProxySource() != source {
			t.Errorf("ProxySource() = %v, want %v", cfg.ProxySource(), source)
		}
	}

	t.Run("the home pair wins", func(t *testing.T) {
		home := litellmStateEnv(t, legacy)
		writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4001/\"\nkey_file = \"litellm.key\"\n")
		writeProxyKey(t, home, "sk-test", 0o600)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		// The trailing-slash trim LitellmBaseURL always applied is kept.
		check(t, cfg, "http://localhost:4001", "sk-test", ProxySourceHome)
		if cfg.LitellmTable == nil || cfg.LitellmTable.URL != "http://legacy:4000" || cfg.LitellmTable.APIKey != "sk-legacy" {
			t.Errorf("LitellmTable = %+v, want the untouched legacy parse", cfg.LitellmTable)
		}
		sess, err := LoadSession("claude")
		if err != nil {
			t.Fatal(err)
		}
		check(t, sess, "http://legacy:4000", "sk-legacy", ProxySourceNone)
	})

	t.Run("the legacy pair with no home", func(t *testing.T) {
		litellmStateEnv(t, legacy)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		check(t, cfg, "http://legacy:4000", "sk-legacy", ProxySourceLegacy)
		sess, err := LoadSession("claude")
		if err != nil {
			t.Fatal(err)
		}
		check(t, sess, "http://legacy:4000", "sk-legacy", ProxySourceNone)
	})
}

// TestABrokenHomeFailsOnlyWhatUsesThePair pins the review ruling on #1/#2:
// a pair the resolution cannot resolve (here a group-readable key file, a
// secret others can read) is never served as the legacy values (fix round 1,
// Findings 1/3) — Load's pair is empty and ProxyErr carries the chmod 600
// error for the commands that use the pair — but the load itself succeeds, so
// a loose key file no longer blocks `wt-model model list`, `wt stop` or the model
// server's per-request reload. LoadSession never opens the home at all, so a
// session launch cannot fail on it (the spec's "No session-behavior change").
func TestABrokenHomeFailsOnlyWhatUsesThePair(t *testing.T) {
	home := litellmStateEnv(t, `default_tag = "code"

[litellm]
url = "http://legacy:4000"
api_key = "sk-legacy"
`)
	writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4001\"\nkey_file = \"litellm.key\"\n")
	writeProxyKey(t, home, "sk-loose", 0o644)
	if _, err := ResolveProxy(); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("ResolveProxy on a 0644 key file = %v, want the chmod 600 error", err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load on a broken home = %v, want it to load (only the pair's users fail)", err)
	}
	var phe *ProxyHomeError
	if perr := cfg.ProxyErr(); !errors.As(perr, &phe) || !strings.Contains(perr.Error(), "chmod 600") {
		t.Errorf("ProxyErr() = %v, want a *ProxyHomeError carrying the chmod 600 error", perr)
	}
	if cfg.LitellmBaseURL() != "" || cfg.LitellmAPIKey() != "" || cfg.ProxySource() != ProxySourceNone {
		t.Errorf("pair = %q/%q source %v, want empty — never the legacy pair",
			cfg.LitellmBaseURL(), cfg.LitellmAPIKey(), cfg.ProxySource())
	}
	sess, err := LoadSession("claude")
	if err != nil {
		t.Fatalf("LoadSession on a broken home = %v, want it to load: a session never reads the home", err)
	}
	if sess.ProxyErr() != nil {
		t.Errorf("LoadSession ProxyErr() = %v, want nil: the home was never opened", sess.ProxyErr())
	}
}

// writeUnstatableKeyFixture makes the home's key file unstatable the
// deterministic way fix round 2, Finding 1 needs: the home config.toml names
// key_file "regular-file/key", which traverses a regular file as though it
// were a directory, so every stat of the key path fails with ENOTDIR ("not a
// directory") — a non-ENOENT KindStat failure, the branch the mode-0000 case
// (KindRead) never exercises. It returns the unstatable key path.
func writeUnstatableKeyFixture(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, "wt-model")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	filler := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(filler, []byte("a regular file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4011\"\nkey_file = \"regular-file/key\"\n")
	return filepath.Join(filler, "key")
}

// TestKeyTokenTreatsOnlyAMissingFileAsNoKey pins fix round 2, Finding 1 on
// the one key-file reading both directions share: only a missing or empty key
// file is "no key"; any other stat failure is the error it is. Before the fix
// keyToken swallowed every KindStat — tokenfile.Load assigns that Kind to
// every os.Stat failure — so an ENOTDIR key_file read as "" in ResolveProxy's
// resolve and in SetProxy's no-op comparison alike: `status` printed
// `api_key: (unset)` and a matching `set` computed "unchanged" and returned
// silent success over a key nobody could read.
func TestKeyTokenTreatsOnlyAMissingFileAsNoKey(t *testing.T) {
	dir := t.TempDir()
	if key, err := keyToken(filepath.Join(dir, "missing")); err != nil || key != "" {
		t.Fatalf("a missing key file = %q, %v; want \"\" and nil — the preserved no-key reading", key, err)
	}
	filler := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(filler, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := keyToken(filepath.Join(filler, "key")) // ENOTDIR
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("an unstatable key file = %q, %v; want the ENOTDIR stat error, never \"no key\"", key, err)
	}
}

// TestResolveProxyRefusesAnUnstatableKeyFile pins fix round 2, Finding 1 at
// the resolver every reader goes through: a key file the OS cannot even stat
// (the key_file traverses a regular file — ENOTDIR) is the resolution's error
// and the zero pair, exactly like the loose and unparseable cases — the spec
// makes only a missing or empty key file mean "no key". The second sub-case
// pins that preserved reading: a fresh home with only a url and no key file
// at all resolves the home pair with an empty key and no error. The review
// reproduced the old behavior on `wt-model litellm status`, which exited 0
// over a key it had failed to inspect and displayed `api_key: (unset)`.
func TestResolveProxyRefusesAnUnstatableKeyFile(t *testing.T) {
	t.Run("an unstatable key file is the error", func(t *testing.T) {
		home := proxyEnv(t, "")
		writeUnstatableKeyFixture(t, home)
		pair, err := ResolveProxy()
		if err == nil {
			t.Fatalf("ResolveProxy() over an ENOTDIR key_file = %+v, nil; want the stat error — an uninspectable key must never read as no key", pair)
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("err = %v, want the OS reason \"not a directory\"", err)
		}
		if pair != (ProxyPair{}) {
			t.Errorf("ResolveProxy() returned %+v beside the error, want the zero pair", pair)
		}
	})
	t.Run("a missing key file is still no key", func(t *testing.T) {
		home := proxyEnv(t, "")
		writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4011\"\n")
		pair, err := ResolveProxy()
		if err != nil {
			t.Fatalf("a fresh home with only a url: %v, want no error", err)
		}
		if want := (ProxyPair{URL: "http://localhost:4011", Source: ProxySourceHome}); pair != want {
			t.Errorf("ResolveProxy() = %+v, want %+v (the missing key file reads as no key)", pair, want)
		}
	})
}

// TestSetProxyRefusesAnUnstatableKeyFile pins fix round 2, Finding 1 on the
// write side: the set's no-op comparison reads the key through the shared
// keyToken, and a key file the OS cannot stat is not "no key" — the set fails
// loud and writes nothing, the same contract as the loose-key refusal. The
// swallowed stat error is exactly what a silent success hides: "" over ""
// computed "unchanged" and a clear over a key nobody could read returned
// `litellm: updated`.
func TestSetProxyRefusesAnUnstatableKeyFile(t *testing.T) {
	home := proxyEnv(t, "")
	keyPath := writeUnstatableKeyFixture(t, home)
	cfgBefore, err := os.ReadFile(ProxyHomePath())
	if err != nil {
		t.Fatal(err)
	}
	// The clear is the case the swallowed error turned into a silent success.
	if err := SetProxy(nil, ptr("")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("SetProxy clear over an unstatable key file = %v, want the stat error", err)
	}
	if err := SetProxy(nil, ptr("sk-new")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("SetProxy over an unstatable key file = %v, want the stat error", err)
	}
	cfgAfter, err := os.ReadFile(ProxyHomePath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cfgBefore, cfgAfter) {
		t.Errorf("config.toml changed on a refusal:\nbefore: %s\nafter:  %s", cfgBefore, cfgAfter)
	}
	if _, err := os.Stat(keyPath); err == nil {
		t.Error("the unstatable key path became statable: something was written there on a refusal")
	}
}

// TestSetProxyFreshExplicitClearWritesTheEmptyKeyFile pins fix round 2,
// Finding 2 (the spec's key-file rule: `set --api-key ""` writes a 0600 empty
// file and wt never deletes a key file): a first-ever set with an explicitly
// empty key writes the empty 0600 key file. Absent and empty both read ""
// before the fix, so the no-op comparison skipped the write and a fresh
// `wt-model litellm set --url URL --api-key ""` printed `litellm: updated`
// and left config.toml without any litellm.key beside it.
func TestSetProxyFreshExplicitClearWritesTheEmptyKeyFile(t *testing.T) {
	home := proxyEnv(t, "")
	if err := SetProxy(ptr("http://localhost:4010"), ptr("")); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(home, "wt-model", defaultProxyKeyFile)
	st, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file after a fresh explicit clear: %v; want the empty 0600 file the spec requires, never absence", err)
	}
	if st.Mode().Perm() != 0o600 || st.Size() != 0 {
		t.Errorf("key file: mode %v size %d, want 0600 and empty", st.Mode().Perm(), st.Size())
	}
}

// TestResolveProxyResolvesNothingWithNoConfigHome pins fix round 2, Finding 3
// and ProxyHomePath's contract ("nothing resolves or writes"): with no config
// home at all — HOME and XDG_CONFIG_HOME both empty — the unconfigured pair is
// the answer even when the process's working directory holds an
// agent-wt/config.toml with a legacy [litellm] pair. Without the fix the
// legacy read opened that relative path, so a decoy file under the CWD
// supplied the pair despite the spec's "empty home → nothing resolves" rule.
func TestResolveProxyResolvesNothingWithNoConfigHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	cwd := t.TempDir()
	must(t, filepath.Join(cwd, "agent-wt", "config.toml"), "default_tag = \"code\"\n\n[litellm]\nurl = \"http://decoy:4000\"\napi_key = \"sk-decoy\"\n")
	t.Chdir(cwd)
	pair, err := ResolveProxy()
	if err != nil {
		t.Fatalf("ResolveProxy() with no config home: %v, want no error", err)
	}
	if pair != (ProxyPair{}) {
		t.Errorf("ResolveProxy() with no config home = %+v, want the unconfigured pair — the working directory's agent-wt/config.toml must never supply the legacy pair", pair)
	}
}

// TestResolveProxyRefusesADanglingHomeLink pins that a home config.toml link
// whose target is missing is an error, never "no home": falling through would
// silently serve the legacy pair (a dotfiles link to an unmounted volume),
// the very fall-back the fail-closed rule forbids — and SetProxy already
// refuses the same link (writeTarget).
func TestResolveProxyRefusesADanglingHomeLink(t *testing.T) {
	home := litellmStateEnv(t, "[litellm]\nurl = \"http://legacy:4000\"\napi_key = \"sk-legacy\"\n")
	link := filepath.Join(home, "wt-model", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing", "config.toml"), link); err != nil {
		t.Fatal(err)
	}
	if pair, err := ResolveProxy(); err == nil || !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("ResolveProxy() = %+v, %v; want the dangling symlink error, never the legacy pair", pair, err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v; want it to load with the error in ProxyErr", err)
	}
	if perr := cfg.ProxyErr(); perr == nil || !strings.Contains(perr.Error(), "dangling symlink") {
		t.Fatalf("ProxyErr() = %v; want the dangling symlink error", perr)
	}
	if cfg.ProxySource() != ProxySourceNone || cfg.LitellmBaseURL() != "" {
		t.Errorf("pair source %v url %q, want empty: never the legacy pair", cfg.ProxySource(), cfg.LitellmBaseURL())
	}
}

// TestLoadFixHintNeverSendsAHomeFailureToWtConfig pins that a load's ProxyErr
// from the wt-model home is not given config.toml's repair: `wt config` cannot
// touch the home. A loose key file gets no hint (its error carries the chmod
// sentence); any other home failure names the home.
func TestLoadFixHintNeverSendsAHomeFailureToWtConfig(t *testing.T) {
	home := litellmStateEnv(t, `default_tag = "code"`+"\n")
	writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4001\"\n")
	writeProxyKey(t, home, "sk-loose", 0o644)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.ProxyErr()
	if err == nil {
		t.Fatal("ProxyErr is nil on a loose key file")
	}
	if hint := LoadFixHint(err); hint != "" {
		t.Errorf("LoadFixHint(loose key) = %q, want none (the error carries chmod 600)", hint)
	}
	writeProxyHome(t, home, "this is = = not toml")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.ProxyErr()
	if err == nil {
		t.Fatal("ProxyErr is nil on an unparseable home")
	}
	if hint := LoadFixHint(err); strings.Contains(hint, "wt config") || !strings.Contains(hint, "wt-model config home") {
		t.Errorf("LoadFixHint(unparseable home) = %q, want the wt-model home named and not `wt config`", hint)
	}
}

// TestPairReadInAnUnwritableHomeSucceeds pins #628: the proxy-pair read
// takes a shared lock and tolerates a home the process cannot write — a
// lock file that cannot be created must not fail the read (the O_CREATE's
// EACCES used to reach the resolve's error), and none is created.
func TestPairReadInAnUnwritableHomeSucceeds(t *testing.T) {
	_, cleanup := IsolateConfigHomeForTest()
	defer cleanup()
	if err := SetProxy(ptr("http://localhost:4099"), ptr("sk-x")); err != nil {
		t.Fatal(err)
	}
	home := ProxyHomePath()
	// The pair write leaves its lock file behind (lock files are never
	// removed); the failing shape is a home whose lock file does not exist
	// and cannot be created — the O_CREATE's EACCES.
	if err := os.Remove(home + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(home), 0o500); err != nil {
		t.Skipf("cannot make the config home unwritable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(home), 0o755) })

	pair, found, err := resolveProxyHome(home)
	if err != nil {
		t.Fatalf("the pair read failed in an unwritable home: %v", err)
	}
	if !found || pair.URL != "http://localhost:4099" {
		t.Fatalf("pair = %+v, found %v", pair, found)
	}
	if _, err := os.Stat(home + ".lock"); !os.IsNotExist(err) {
		t.Errorf("the read created a lock file in the unwritable home: %v", err)
	}
}

// TestGatewayTableRoundTripsThroughSetProxy pins decision 1's round trip: the
// hand-edited [gateway] table survives this binary's SetProxy re-encode, both
// an on switch and a wrong-typed value kept as written (GatewaySettings, not
// the writer, interprets it). A writer that dropped the table would turn the
// host switch off behind the operator's back on every `litellm set`.
func TestGatewayTableRoundTripsThroughSetProxy(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       any
	}{
		{"on", "subscription_passthrough = true", true},
		{"wrong type kept", `subscription_passthrough = "true"`, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := proxyEnv(t, "")
			writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4000\"\nkey_file = \"litellm.key\"\n\n[gateway]\n"+tc.line+"\n")
			writeProxyKey(t, home, "sk-old", 0o600)
			if err := SetProxy(ptr("http://localhost:4002"), ptr("sk-new")); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(ProxyHomePath())
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]map[string]any
			if _, err := toml.Decode(string(body), &got); err != nil {
				t.Fatalf("written home does not parse: %v\n%s", err, body)
			}
			if v, ok := got["gateway"]["subscription_passthrough"]; !ok || v != tc.want {
				t.Errorf("[gateway] subscription_passthrough = %#v (present %t), want %#v kept as written:\n%s", v, ok, tc.want, body)
			}
			if got["litellm"]["url"] != "http://localhost:4002" {
				t.Errorf("[litellm] url = %v, want the new url", got["litellm"]["url"])
			}
		})
	}
}

// TestGatewaySettingsAbsentIsOff pins the default: no home config.toml, a
// home without [gateway], an empty [gateway] and an explicit false are all
// off with no warning and no error, because passthrough is opt-in.
func TestGatewaySettingsAbsentIsOff(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no file", ""},
		{"no table", "[litellm]\nurl = \"http://localhost:4000\"\n"},
		{"empty table", "[gateway]\n"},
		{"false", "[gateway]\nsubscription_passthrough = false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := proxyEnv(t, "")
			if tc.body != "" {
				writeProxyHome(t, home, tc.body)
			}
			gc, warns, err := GatewaySettings()
			if err != nil || len(warns) != 0 || gc.SubscriptionPassthrough {
				t.Errorf("GatewaySettings = %+v, %q, %v; want off, no warning, no error", gc, warns, err)
			}
		})
	}
	t.Run("on", func(t *testing.T) {
		home := proxyEnv(t, "")
		writeProxyHome(t, home, "[gateway]\nsubscription_passthrough = true\n")
		gc, warns, err := GatewaySettings()
		if err != nil || len(warns) != 0 || !gc.SubscriptionPassthrough {
			t.Errorf("GatewaySettings = %+v, %q, %v; want on", gc, warns, err)
		}
	})
}

// TestGatewaySettingsWrongTypeIsOffWithWarning pins decision 1's safe
// direction: a value that is not a boolean ("true", 1) is off with exactly
// one warning naming the key and the type it must be, and it never breaks
// the [litellm] pair the same file holds.
func TestGatewaySettingsWrongTypeIsOffWithWarning(t *testing.T) {
	for _, val := range []string{`"true"`, "1"} {
		t.Run(val, func(t *testing.T) {
			home := proxyEnv(t, "")
			writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4000\"\nkey_file = \"litellm.key\"\n\n[gateway]\nsubscription_passthrough = "+val+"\n")
			writeProxyKey(t, home, "sk-k", 0o600)
			gc, warns, err := GatewaySettings()
			if err != nil {
				t.Fatalf("GatewaySettings err = %v, want a warning only", err)
			}
			if gc.SubscriptionPassthrough {
				t.Error("a wrong-typed value turned the mode on")
			}
			if len(warns) != 1 || !strings.Contains(warns[0], "subscription_passthrough") || !strings.Contains(warns[0], "boolean") {
				t.Errorf("warnings = %q, want one naming subscription_passthrough and boolean", warns)
			}
			pair, err := ResolveProxy()
			if err != nil || pair.URL != "http://localhost:4000" || pair.APIKey != "sk-k" {
				t.Errorf("ResolveProxy = %+v, %v; want the [litellm] pair intact", pair, err)
			}
		})
	}
}

// TestGatewaySettingsSyntaxErrorIsAnError pins that a home config.toml that
// does not parse is an error (serve refuses to start), never "off", and the
// error does not quote a value from the file.
func TestGatewaySettingsSyntaxErrorIsAnError(t *testing.T) {
	home := proxyEnv(t, "")
	writeProxyHome(t, home, "[litellm]\nurl = \"http://SECRETHOST:4000\n[gateway]\nsubscription_passthrough = true\n")
	_, _, err := GatewaySettings()
	if err == nil {
		t.Fatal("GatewaySettings on a syntax error = nil, want an error")
	}
	if strings.Contains(err.Error(), "SECRETHOST") {
		t.Errorf("error quotes a value: %v", err)
	}
}

// TestHomeParseErrorsNameOnlyKnownKeys pins #640 for the wt-model home's
// value-free parse errors: the text left of an '=' on a bad line is printed
// only when it is one of the home's known keys, so a token pasted on its own
// line (a bare `X==`, or a line inside a multi-line url array) never reaches
// serve's refusal, `litellm set` or `litellm status`. A known key still names
// itself and the line, with the neutral suffix unless secretSafe's rule
// calls the key secret.
func TestHomeParseErrorsNameOnlyKnownKeys(t *testing.T) {
	sentinels := []string{"sk-ant-oat01-wtSENTINEL", "wt-SENTINEL-tok", "abc123"}
	for _, body := range []string{
		"[litellm]\nurl = \"http://x\"\nsk-ant-oat01-wtSENTINEL==\n",
		"[gateway]\nwt-SENTINEL-tok=\n",
		"[litellm]\nurl = \"http://x\"\nabc123==\n",
		"[litellm]\nurl = [\n  \"http://x\",\n  sk-ant-oat01-wtSENTINEL=x\n]\n",
		"[litellm]\nurl = [\n  \"http://x\",\n  wt-SENTINEL-tok=x\n]\n",
		"[gateway]\nsubscription_passthrough = [\n  abc123==\n]\n",
	} {
		home := proxyEnv(t, "")
		writeProxyHome(t, home, body)
		for name, f := range map[string]func() error{
			"ResolveProxy":    func() error { _, err := ResolveProxy(); return err },
			"GatewaySettings": func() error { _, _, err := GatewaySettings(); return err },
		} {
			err := f()
			if err == nil {
				t.Fatalf("%s on %q = nil, want a parse error", name, body)
			}
			for _, s := range sentinels {
				if strings.Contains(err.Error(), s) {
					t.Errorf("%s on %q: the error quotes %q: %v", name, body, s, err)
				}
			}
		}
	}

	home := proxyEnv(t, "")
	writeProxyHome(t, home, "[gateway]\nsubscription_passthrough = 1x\n")
	_, _, err := GatewaySettings()
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "subscription_passthrough") {
		t.Fatalf("known key: err = %v, want the line and the key named", err)
	}
	if strings.Contains(err.Error(), "1x") || strings.Contains(err.Error(), "may hold a secret") {
		t.Errorf("known key: err = %v, want no value and the neutral suffix", err)
	}
}
