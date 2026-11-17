package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
// re-prepends the fixed header comment block.
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
// at load, not a resolver at each call site): after Load — and LoadSession —
// the runtime pair behind LitellmBaseURL/LitellmAPIKey is the home's when the
// home has a [litellm] table and the legacy table's otherwise, with the
// trailing-slash trim kept, and ProxySource says which. LitellmTable stays
// the untouched legacy parse (TestLegacyLitellmKeysSurviveASave's rule), so
// every existing consumer keeps working with zero call-site changes while the
// pair itself comes from the new home.
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
		check(t, sess, "http://localhost:4001", "sk-test", ProxySourceHome)
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
		check(t, sess, "http://legacy:4000", "sk-legacy", ProxySourceLegacy)
	})
}

// TestApplyProxyStateKeepsTheLegacyPairWhenTheHomeIsBroken pins the merge's
// degradation rule: a resolution error (here a group-readable key file) must
// not fail every Load — Load is not credential validation, and the status
// and set commands call ResolveProxy and fail loud there (asserted below).
// What lands is exactly the pre-merge behavior, the legacy table's copy, and
// ProxySource says so; the home's half-resolved pair is never mixed in.
func TestApplyProxyStateKeepsTheLegacyPairWhenTheHomeIsBroken(t *testing.T) {
	home := litellmStateEnv(t, `default_tag = "code"

[litellm]
url = "http://legacy:4000"
api_key = "sk-legacy"
`)
	writeProxyHome(t, home, "[litellm]\nurl = \"http://localhost:4001\"\nkey_file = \"litellm.key\"\n")
	writeProxyKey(t, home, "sk-loose", 0o644)
	if _, err := ResolveProxy(); err == nil {
		t.Fatal("ResolveProxy accepted a 0644 key file, want the chmod 600 error")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LitellmBaseURL() != "http://legacy:4000" || cfg.LitellmAPIKey() != "sk-legacy" {
		t.Errorf("runtime pair = %q/%q, want the legacy pair", cfg.LitellmBaseURL(), cfg.LitellmAPIKey())
	}
	if cfg.ProxySource() != ProxySourceLegacy {
		t.Errorf("ProxySource() = %v, want ProxySourceLegacy", cfg.ProxySource())
	}
}
