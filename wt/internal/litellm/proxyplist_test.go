package litellm

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureSecret = "sk-fake-not-a-real-key"

// TestReadProxyPlistFixture reads the synthetic fixture and checks every field
// of ProxyPlistInfo, including that EnvNames holds names only and Mode comes
// from os.Stat. The setup's checks and plans lean on each of these answers.
func TestReadProxyPlistFixture(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("testdata/proxy.plist")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "proxy.plist")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadProxyPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != p || info.Label != ProxyLabel {
		t.Errorf("path/label = %q/%q", info.Path, info.Label)
	}
	wantArgs := []string{"/opt/fake/bin/litellm", "--config", "/Users/user_name/.config/litellm/config.yaml", "--port", "4000"}
	if !reflect.DeepEqual(info.ProgramArguments, wantArgs) {
		t.Errorf("args = %q", info.ProgramArguments)
	}
	if info.Port != 4000 {
		t.Errorf("port = %d", info.Port)
	}
	if info.ConfigPath != "/Users/user_name/.config/litellm/config.yaml" {
		t.Errorf("config = %q", info.ConfigPath)
	}
	if !info.RunAtLoad || !info.KeepAlive {
		t.Errorf("RunAtLoad/KeepAlive = %v/%v", info.RunAtLoad, info.KeepAlive)
	}
	wantNames := []string{"DATABASE_URL", "FAKE_PROVIDER_KEY", "LITELLM_MASTER_KEY"}
	if !reflect.DeepEqual(info.EnvNames, wantNames) {
		t.Errorf("env names = %q", info.EnvNames)
	}
	if info.Mode != 0o600 {
		t.Errorf("mode = %v", info.Mode)
	}
	if strings.Contains(strings.Join(info.EnvNames, " ")+info.Label+info.ConfigPath, fixtureSecret) {
		t.Error("a secret value reached the info struct")
	}
}

// TestReadProxyPlistNoPort pins that a plist without --port reports Port 0,
// so a caller can tell "unset" from a real port.
func TestReadProxyPlistNoPort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "p.plist")
	body := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>x</string>
<key>ProgramArguments</key><array><string>litellm</string></array>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict></dict></plist>`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadProxyPlist(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Port != 0 || info.ConfigPath != "" || info.KeepAlive || info.RunAtLoad {
		t.Errorf("info = %+v", info)
	}
}

// TestReadProxyPlistRejectsABinaryPlist pins the error naming the conversion
// command, since the reader is XML-only and the user needs the fix.
func TestReadProxyPlistRejectsABinaryPlist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.plist")
	if err := os.WriteFile(p, []byte("bplist00\x00\x01"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadProxyPlist(p)
	if err == nil || !strings.Contains(err.Error(), "plutil -convert xml1") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = ReadProxyPlistVar(p, "X")
	if err == nil || !strings.Contains(err.Error(), "plutil -convert xml1") {
		t.Fatalf("var err = %v", err)
	}
}

// TestReadProxyPlistVarReturnsOnlyTheNamedValue pins that the single value
// reader answers one name, reports absence as ok=false, and leaks no value
// into an error.
func TestReadProxyPlistVarReturnsOnlyTheNamedValue(t *testing.T) {
	p, _ := fixtureCopy(t)
	v, ok, err := ReadProxyPlistVar(p, "LITELLM_MASTER_KEY")
	if err != nil || !ok || v != fixtureSecret {
		t.Fatalf("got %q %v %v", v, ok, err)
	}
	v, ok, err = ReadProxyPlistVar(p, "NOPE")
	if err != nil || ok || v != "" {
		t.Fatalf("absent: %q %v %v", v, ok, err)
	}
}

// TestReadProxyPlistMissingFile pins that an absent plist is an error, never
// an empty answer.
func TestReadProxyPlistMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "none.plist")
	if _, err := ReadProxyPlist(p); err == nil {
		t.Fatal("want an error")
	}
	if _, _, err := ReadProxyPlistVar(p, "X"); err == nil {
		t.Fatal("want an error")
	}
}

func fixtureCopy(t *testing.T) (string, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/proxy.plist")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "proxy.plist")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, data
}
