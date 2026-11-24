package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gatewayHome points the wt-model home at a scratch directory and writes body
// as its config.toml (nothing when body is empty).
func gatewayHome(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := ProxyHomePath()
	if body != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func backups(t *testing.T) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(ModelHome(), "backups", "config.toml.bak-*"))
	return m
}

// TestSetGatewayPassthroughWritesOnlyThatKey pins #693's writer: it changes
// subscription_passthrough and nothing else (the [litellm] table and the TLS
// pair survive), keeps the file's mode, leaves a 0600 backup of the file it
// replaced in a 0700 directory, and writes nothing when the value already is
// what was asked (no backup, the bytes untouched).
func TestSetGatewayPassthroughWritesOnlyThatKey(t *testing.T) {
	const start = "# my own note\n[litellm]\nurl = \"http://localhost:4000\"\nkey_file = \"litellm.key\"\n\n[gateway]\ntls_cert = \"/c.pem\"\ntls_key = \"/k.pem\"\n"
	path := gatewayHome(t, start)

	res, err := SetGatewayPassthrough(true)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(path); !res.Changed || res.Path != want {
		t.Errorf("result = %+v, want a change of %s", res, want)
	}
	gw, _, err := GatewaySettings()
	if err != nil || !gw.SubscriptionPassthrough || gw.TLSCert != "/c.pem" || gw.TLSKey != "/k.pem" {
		t.Errorf("settings = %+v, %v; want the switch on and the TLS pair kept", gw, err)
	}
	pair, err := ResolveProxy()
	if err != nil || pair.URL != "http://localhost:4000" || pair.Source != ProxySourceHome {
		t.Errorf("the proxy pair changed: %+v, %v", pair, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the 0640 it had", st.Mode().Perm())
	}
	bs := backups(t)
	if len(bs) != 1 || res.Backup != bs[0] {
		t.Fatalf("backups = %v, result %q; want exactly the one the write named", bs, res.Backup)
	}
	if got, _ := os.ReadFile(bs[0]); string(got) != start {
		t.Errorf("backup holds %q, want the file as it was", got)
	}
	if st, _ := os.Stat(bs[0]); st.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(bs[0])); st.Mode().Perm() != 0o700 {
		t.Errorf("backup directory mode = %v, want 0700", st.Mode().Perm())
	}

	before, _ := os.ReadFile(path)
	res, err = SetGatewayPassthrough(true)
	if err != nil || res.Changed || res.Backup != "" {
		t.Errorf("the same value again: %+v, %v; want no change and no backup", res, err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) || len(backups(t)) != 1 {
		t.Error("a no-op write touched the file or the backups")
	}

	res, err = SetGatewayPassthrough(false)
	if err != nil || !res.Changed {
		t.Fatalf("off: %+v, %v", res, err)
	}
	if gw, _, _ := GatewaySettings(); gw.SubscriptionPassthrough || gw.TLSCert != "/c.pem" {
		t.Errorf("settings after off = %+v", gw)
	}
}

// TestSetGatewayPassthroughOnAFreshHome pins that the first write creates the
// home with only the [gateway] table: it adds no [litellm] table, so the proxy
// pair still falls back to the legacy table exactly as before, and with no
// file to replace there is no backup.
func TestSetGatewayPassthroughOnAFreshHome(t *testing.T) {
	path := gatewayHome(t, "")
	res, err := SetGatewayPassthrough(true)
	if err != nil || !res.Changed || res.Backup != "" {
		t.Fatalf("fresh home: %+v, %v", res, err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "[gateway]") || !strings.Contains(string(body), "subscription_passthrough = true") || strings.Contains(string(body), "\n[litellm]\n") {
		t.Errorf("config.toml =\n%s", body)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("new file mode = %v, want 0644", st.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(ModelHome(), "backups")); err == nil {
		t.Error("a backup directory was made for a file that did not exist")
	}
	if gw, _, _ := GatewaySettings(); !gw.SubscriptionPassthrough {
		t.Error("the switch did not read back as on")
	}
}

// TestSetGatewayPassthroughReplacesAWrongTypedValue pins that a hand-written
// "true" (a string, which GatewaySettings reads as off with a warning) is
// replaced by the boolean the command was asked for, and the other keys stay.
func TestSetGatewayPassthroughReplacesAWrongTypedValue(t *testing.T) {
	gatewayHome(t, "[gateway]\nsubscription_passthrough = \"true\"\nextra = 7\n")
	if _, err := SetGatewayPassthrough(true); err != nil {
		t.Fatal(err)
	}
	gw, warns, err := GatewaySettings()
	if err != nil || !gw.SubscriptionPassthrough || len(warns) != 0 {
		t.Errorf("settings = %+v, warnings %q, %v; want a clean on", gw, warns, err)
	}
	body, _ := os.ReadFile(ProxyHomePath())
	if !strings.Contains(string(body), "extra = 7") {
		t.Errorf("an unrelated [gateway] key was lost:\n%s", body)
	}
}

// TestSetGatewayPassthroughRefusesAFileItCannotRead pins that a config.toml
// that does not parse is never written over: the error names no value, the
// file is byte-for-byte as it was and no backup is made.
func TestSetGatewayPassthroughRefusesAFileItCannotRead(t *testing.T) {
	const bad = "[gateway\nsubscription_passthrough = true\n"
	path := gatewayHome(t, bad)
	_, err := SetGatewayPassthrough(false)
	if err == nil {
		t.Fatal("a file that does not parse was written over")
	}
	if got, _ := os.ReadFile(path); string(got) != bad || len(backups(t)) != 0 {
		t.Errorf("the refused write touched the file or made a backup: %q", got)
	}
}

// TestSetGatewayPassthroughWritesThroughASymlink pins that a symlinked
// config.toml keeps its link and the target is written, as SetProxy does.
func TestSetGatewayPassthroughWritesThroughASymlink(t *testing.T) {
	path := gatewayHome(t, "")
	target := filepath.Join(t.TempDir(), "real.toml")
	if err := os.WriteFile(target, []byte("[gateway]\ntls_cert = \"/c.pem\"\ntls_key = \"/k.pem\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := SetGatewayPassthrough(true); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(path); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v, %v", st, err)
	}
	if got, _ := os.ReadFile(target); !strings.Contains(string(got), "subscription_passthrough = true") {
		t.Errorf("the target was not written:\n%s", got)
	}
}
