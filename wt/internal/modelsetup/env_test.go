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
// distinct names in the backup directory, each a full copy at the source's
// mode, and the first is never overwritten by the second.
func TestRealBackupNamesNeverCollide(t *testing.T) {
	src := t.TempDir()
	dir := filepath.Join(t.TempDir(), "backups")
	p := filepath.Join(src, "local.litellm.proxy.plist")
	if err := os.WriteFile(p, []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	fsys := realFS{}
	a, _, err := fsys.Backup(p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}
	b, _, err := fsys.Backup(p, dir)
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
			t.Errorf("backup %s is not in the backup directory %s", path, dir)
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
		dst, _, err := realFS{}.Backup(p, filepath.Join(t.TempDir(), "backups"))
		if err != nil {
			t.Fatal(err)
		}
		if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != c.want {
			t.Errorf("%04o: backup mode %v (%v), want %04o", c.from, st.Mode().Perm(), err, c.want)
		}
	}
}

// TestRealBackupDirectoryIsMadeAndKept0700 pins the backup directory on the
// real file system: created 0700 when absent (and not reported tightened),
// set to 0700 when it was looser (and reported, so the Result can say so),
// and a symlink in its place is refused with nothing copied.
func TestRealBackupDirectoryIsMadeAndKept0700(t *testing.T) {
	p := filepath.Join(t.TempDir(), "local.litellm.proxy.plist")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "wt-model", "backups")
	if _, tightened, err := (realFS{}).Backup(p, dir); err != nil || tightened {
		t.Fatalf("Backup into a new dir: tightened %t, err %v", tightened, err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("new backup dir mode %v (%v), want 0700", st.Mode().Perm(), err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, tightened, err := (realFS{}).Backup(p, dir); err != nil || !tightened {
		t.Errorf("Backup into a 0755 dir: tightened %t, err %v", tightened, err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("loose backup dir mode %v (%v), want 0700", st.Mode().Perm(), err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if dst, _, err := (realFS{}).Backup(p, link); err == nil {
		t.Errorf("Backup through a symlinked dir made %s", dst)
	}
}
