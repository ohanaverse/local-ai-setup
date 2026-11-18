package modelsetup

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

const (
	fakeMaster = "sk-test-master"
	fakeSalt   = "salt-test"
)

func litellmTestSpec() LiteLLMSpec {
	return LiteLLMSpec{
		Binary:     "/Users/user_name/.local/bin/litellm",
		ConfigPath: "/Users/user_name/.config/litellm/config.yaml",
		Path:       "/Users/user_name/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
		Home:       "/Users/user_name",
		MasterKey:  fakeMaster,
		SaltKey:    fakeSalt,
		Port:       4000,
	}
}

// userPlist is a hand-written plist with a second, similar key that a
// replacement must leave alone.
const userPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>local.litellm.proxy</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>LITELLM_SALT_KEY</key>
		<string>keep-me</string>
		<key>LITELLM_MASTER_KEY</key>
		<string>sk-old</string>
		<key>OTHER</key>
		<string>sk-old</string>
	</dict>
</dict>
</plist>
`

// TestReplaceMasterKeyChangesOnlyThatValue proves the edit is a text edit:
// every byte before and after the replaced string element is identical, the
// result still parses as XML, and another key holding the same old value is
// untouched.
func TestReplaceMasterKeyChangesOnlyThatValue(t *testing.T) {
	out, err := ReplaceMasterKey([]byte(userPlist), "sk-new")
	if err != nil {
		t.Fatal(err)
	}
	old := "<string>sk-old</string>"
	first := strings.Index(userPlist, old)
	prefix, suffix := userPlist[:first], userPlist[first+len(old):]
	want := prefix + "<string>sk-new</string>" + suffix
	if string(out) != want {
		t.Fatalf("bytes differ outside the value:\n%s", out)
	}
	if !bytes.HasPrefix(out, []byte(prefix)) || !bytes.HasSuffix(out, []byte(suffix)) {
		t.Fatal("prefix or suffix changed")
	}
	if err := xml.Unmarshal(out, new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if !strings.Contains(string(out), "<key>OTHER</key>\n\t\t<string>sk-old</string>") {
		t.Fatal("the other key was touched")
	}
}

// TestReplaceMasterKeyRefusesBinaryAmbiguousAndMissing pins every refusal:
// nothing is returned and the error never carries the key.
func TestReplaceMasterKeyRefusesBinaryAmbiguousAndMissing(t *testing.T) {
	two := strings.Replace(userPlist, "<key>OTHER</key>", "<key>LITELLM_MASTER_KEY</key>", 1)
	cases := map[string]string{
		"binary":    "bplist00\x00\x01",
		"missing":   strings.Replace(userPlist, "LITELLM_MASTER_KEY", "LITELLM_OTHER_KEY", 1),
		"twice":     two,
		"data":      strings.Replace(userPlist, "<string>sk-old</string>\n\t\t<key>OTHER", "<data>c2s=</data>\n\t\t<key>OTHER", 1),
		"no value":  strings.Replace(userPlist, "<string>sk-old</string>\n\t\t<key>OTHER", "<key>OTHER", 1),
		"cdata":     strings.Replace(userPlist, "<string>sk-old</string>\n\t\t<key>OTHER", "<string><![CDATA[sk-old]]></string>\n\t\t<key>OTHER", 1),
		"bad value": strings.Replace(userPlist, "<string>sk-old</string>\n\t\t<key>OTHER", "<string>a<b/></string>\n\t\t<key>OTHER", 1),
	}
	for name, in := range cases {
		out, err := ReplaceMasterKey([]byte(in), "sk-secret-new")
		if err == nil || out != nil {
			t.Errorf("%s: want error and nil output, got %v / %q", name, err, out)
			continue
		}
		if strings.Contains(err.Error(), "sk-secret-new") || strings.Contains(err.Error(), "sk-old") {
			t.Errorf("%s: error leaks a key: %v", name, err)
		}
	}
	// Characters that cannot appear in XML 1.0 are refused, not escaped.
	for _, bad := range []string{"sk-\x00", "sk-\x01x", "sk-\x1b", "sk-\xff"} {
		if out, err := ReplaceMasterKey([]byte(userPlist), bad); err == nil || out != nil {
			t.Errorf("invalid key %q accepted", bad)
		}
	}
}

// TestReplaceMasterKeyReplacesAnEmptySelfClosedString records the decision
// that <string/> is a valid, empty value: a plist whose key was blanked is
// exactly what a user wants repaired, and the element is unambiguous.
func TestReplaceMasterKeyReplacesAnEmptySelfClosedString(t *testing.T) {
	in := strings.Replace(userPlist, "<string>sk-old</string>\n\t\t<key>OTHER", "<string/>\n\t\t<key>OTHER", 1)
	out, err := ReplaceMasterKey([]byte(in), "sk-new")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(in, "<string/>", "<string>sk-new</string>", 1)
	if string(out) != want {
		t.Fatalf("got:\n%s", out)
	}
}

// TestReplaceMasterKeyEscapesTheValue keeps the document well formed when the
// new value holds XML metacharacters.
func TestReplaceMasterKeyEscapesTheValue(t *testing.T) {
	out, err := ReplaceMasterKey([]byte(userPlist), `a&b<c>d"e'f`+"\tg")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "a&b") || strings.Contains(string(out), "<c>") {
		t.Fatalf("value not escaped:\n%s", out)
	}
	p := struct {
		Strings []string `xml:"dict>dict>string"`
	}{}
	if err := xml.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Strings) < 2 || p.Strings[1] != `a&b<c>d"e'f`+"\tg" {
		t.Fatalf("value does not round-trip: %q", p.Strings)
	}
}

// TestRenderLiteLLMPlistMatchesTheGolden pins the exact bytes of the minimal
// LiteLLM LaunchAgent, built from obviously fake keys, so a change to it is a
// deliberate one. Run with UPDATE_GOLDEN=1 to regenerate.
func TestRenderLiteLLMPlistMatchesTheGolden(t *testing.T) {
	got := RenderLiteLLMPlist(litellmTestSpec())
	if !bytes.Equal(got, RenderLiteLLMPlist(litellmTestSpec())) {
		t.Fatal("render is not deterministic")
	}
	const golden = "testdata/litellm.min.golden.plist"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("plist differs from golden (fake keys only):\n%s", got)
	}
	if !strings.Contains(string(got), "<string>"+litellm.ProxyLabel+"</string>") || !strings.Contains(string(got), "<!-- "+agentMarker+" -->") {
		t.Error("label or marker missing")
	}
}

// TestRenderedPlistRoundTripsThroughTheReader checks that wt's own plist
// reader sees the environment the renderer wrote.
func TestRenderedPlistRoundTripsThroughTheReader(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/p.plist"
	if err := os.WriteFile(path, RenderLiteLLMPlist(litellmTestSpec()), 0o600); err != nil {
		t.Fatal(err)
	}
	v, ok, err := litellm.ReadProxyPlistVar(path, "LITELLM_MASTER_KEY")
	if err != nil || !ok || v != fakeMaster {
		t.Fatalf("reader: ok=%v err=%v value-matches=%v", ok, err, v == fakeMaster)
	}
}

// TestGenerateKeyShape pins the key format and that only the injected reader
// is used, including on its failure (no partial key).
func TestGenerateKeyShape(t *testing.T) {
	calls := 0
	k, err := GenerateKey(func(b []byte) error {
		calls++
		if len(b) != 32 {
			t.Errorf("asked for %d bytes", len(b))
		}
		for i := range b {
			b[i] = byte(i)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if !regexp.MustCompile(`^sk-[0-9a-f]{64}$`).MatchString(k) {
		t.Fatalf("shape: %q", k)
	}
	if k != "sk-000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f" {
		t.Fatalf("not derived from the injected bytes: %q", k)
	}
	boom := errors.New("no entropy")
	k, err = GenerateKey(func([]byte) error { return boom })
	if !errors.Is(err, boom) || k != "" {
		t.Fatalf("got %q, %v", k, err)
	}
}

// TestRenderedPlistHasNoSecretOutsideItsEnvironment fails if a key shows up
// anywhere but as the value of its own EnvironmentVariables entry: not in
// ProgramArguments, comments or log paths.
func TestRenderedPlistHasNoSecretOutsideItsEnvironment(t *testing.T) {
	out := string(RenderLiteLLMPlist(litellmTestSpec()))
	for _, secret := range []string{fakeMaster, fakeSalt} {
		if n := strings.Count(out, secret); n != 1 {
			t.Fatalf("a secret appears %d times, want 1", n)
		}
	}
	for _, c := range []struct{ key, secret string }{
		{"LITELLM_MASTER_KEY", fakeMaster}, {"LITELLM_SALT_KEY", fakeSalt},
	} {
		want := "<key>" + c.key + "</key>\n\t\t<string>" + c.secret + "</string>"
		if !strings.Contains(out, want) {
			t.Errorf("%s is not the value of its own environment entry", c.key)
		}
	}
	env := out[strings.Index(out, "<key>EnvironmentVariables</key>"):]
	if strings.Contains(out[:len(out)-len(env)], "test") {
		t.Error("a secret-looking value appears before EnvironmentVariables")
	}
}

// TestLiteLLMSpecNeverPrintsItsKeys makes an accidental %v, %+v or %#v of the
// spec safe.
func TestLiteLLMSpecNeverPrintsItsKeys(t *testing.T) {
	s := litellmTestSpec()
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, arg := range []any{s, &s} {
			out := fmt.Sprintf(f, arg)
			if strings.Contains(out, fakeMaster) || strings.Contains(out, fakeSalt) {
				t.Errorf("%s leaks a key: %s", f, out)
			}
		}
	}
}
