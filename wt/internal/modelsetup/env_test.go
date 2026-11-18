package modelsetup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultEnvRefusesLaunchdAndListenerLookupsUnderTest proves the real
// launchctl, lsof and ps seams fail closed in a test binary, so no test can
// load, unload or inspect the developer's real services.
func TestDefaultEnvRefusesLaunchdAndListenerLookupsUnderTest(t *testing.T) {
	env := DefaultEnv()
	ctx := context.Background()
	checks := map[string]error{
		"Bootstrap": env.Launchd.Bootstrap(ctx, "/nonexistent.plist"),
		"Bootout":   env.Launchd.Bootout(ctx, AgentLabel),
		"Kickstart": env.Launchd.Kickstart(ctx, AgentLabel, true),
	}
	_, err := env.Launchd.Print(ctx, AgentLabel)
	checks["Print"] = err
	_, _, err = env.Net.ListenerPID(ctx, 8787)
	checks["ListenerPID"] = err
	_, err = env.Net.ProcessStart(ctx, 1)
	checks["ProcessStart"] = err
	for name, err := range checks {
		if err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Errorf("%s: err = %v, want one mentioning \"test binary\"", name, err)
		}
	}
}

// TestRealBackupNamesNeverCollide pins carry-forward 1 on the real file
// system (a temp dir): two backups of the same file within one second get
// distinct names, each a full copy at the source's mode, and the first is
// never overwritten by the second.
func TestRealBackupNamesNeverCollide(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "local.litellm.proxy.plist")
	if err := os.WriteFile(p, []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	fsys := realFS{}
	a, err := fsys.Backup(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}
	b, err := fsys.Backup(p)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two backups share the name %s", a)
	}
	for path, want := range map[string]string{a: "one", b: "two"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q (%v), want %q", path, got, err, want)
		}
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v (%v), want 0600 (the source's 0640 without group or other bits)", path, st.Mode().Perm(), err)
		}
		if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "local.litellm.proxy.plist.bak-") {
			t.Errorf("backup %s is not beside the file", path)
		}
	}
}

// TestBackupNameAddsASuffixOnlyOnACollision pins the naming rule both FS
// implementations share: the dated name when free, else -2, -3, ...
func TestBackupNameAddsASuffixOnlyOnACollision(t *testing.T) {
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	taken := map[string]bool{}
	exists := func(c string) bool { return taken[c] }
	want := []string{"/p.bak-20261117-120000", "/p.bak-20261117-120000-2", "/p.bak-20261117-120000-3"}
	for _, w := range want {
		got := backupName("/p", at, exists)
		if got != w {
			t.Errorf("backupName = %q, want %q", got, w)
		}
		taken[got] = true
	}
}

// TestDefaultEnvFindsNoLiteLLMBinaryUnderTest pins that the real binary
// lookup never looks at the developer's machine from a test binary.
func TestDefaultEnvFindsNoLiteLLMBinaryUnderTest(t *testing.T) {
	if p, ok := DefaultEnv().LiteLLM.Binary(); ok || p != "" {
		t.Errorf("Binary() = %q, %t under test", p, ok)
	}
}

// TestRealBackupIsNeverLooserThanTheSource pins Important 1 on the real file
// system: the backup holds the plist's secrets, so it drops group and other
// bits (0644 -> 0600) and never loosens a stricter file (0400 stays 0400).
func TestRealBackupIsNeverLooserThanTheSource(t *testing.T) {
	for _, c := range []struct{ from, want os.FileMode }{{0o644, 0o600}, {0o400, 0o400}} {
		p := filepath.Join(t.TempDir(), "p.plist")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, c.from); err != nil {
			t.Fatal(err)
		}
		dst, err := realFS{}.Backup(p)
		if err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != c.want {
			t.Errorf("%04o: backup mode %v (%v), want %04o", c.from, st.Mode().Perm(), err, c.want)
		}
	}
}
