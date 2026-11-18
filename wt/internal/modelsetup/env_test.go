package modelsetup

import (
	"context"
	"errors"
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

// backupName is the first candidate exists reports free, so a second backup
// in the same second never lands on the first (the fake FS uses it; the real
// one asks the file system with O_EXCL instead, which is also race-free).
func backupName(p string, t time.Time, exists func(string) bool) string {
	for n := 1; ; n++ {
		if c := backupCandidate(p, t, n); !exists(c) || n >= maxBackupTries {
			return c
		}
	}
}

// TestRealWriteAtomicKeepsASymlink pins that a write through a symlink lands
// on the link's target and leaves the link in place (a dotfiles-managed
// plist must stay a link).
func TestRealWriteAtomicKeepsASymlink(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "real.plist"), filepath.Join(dir, "link.plist")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := (realFS{}).WriteAtomic(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Errorf("target = %q, want new", b)
	}
}

// outsideProbe is a path outside os.TempDir() that nothing can create: its
// parent is /dev/null, a character device, so even a write the guard let
// through would fail at the MkdirAll and leave the machine unchanged.
const outsideProbe = "/dev/null/wt-modelsetup-guard-probe"

// TestRealWriteAtomicGuardsTheResolvedPath pins the test guard on the path a
// symlink resolves to: a link inside the temp directory whose target is
// outside it is refused with errOutsideTempUnderTest, so a test can never
// write through a link to the developer's files. The resolver is stubbed to
// name outsideProbe, so no link ever leaves the temp directory and nothing
// outside it is touched.
func TestRealWriteAtomicGuardsTheResolvedPath(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "real.plist"), filepath.Join(dir, "link.plist")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fsys := realFS{resolve: func(string) (string, error) { return outsideProbe, nil }}
	err := fsys.WriteAtomic(link, []byte("new"), 0o600)
	if !errors.Is(err, errOutsideTempUnderTest) {
		t.Errorf("WriteAtomic through a link resolving outside the temp dir: err = %v, want errOutsideTempUnderTest", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Errorf("the link's own target changed to %q", b)
	}
	if _, err := os.Lstat(outsideProbe); err == nil {
		t.Errorf("%s exists", outsideProbe)
	}
}

// TestRealChmodGuardsTheResolvedPath pins the same guard for Chmod, which
// follows a link: a link whose resolved path is outside the temp directory
// is refused and no mode changes (the resolver is stubbed as above).
func TestRealChmodGuardsTheResolvedPath(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "real.plist"), filepath.Join(dir, "link.plist")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fsys := realFS{resolve: func(string) (string, error) { return outsideProbe, nil }}
	if err := fsys.Chmod(link, 0o644); !errors.Is(err, errOutsideTempUnderTest) {
		t.Errorf("Chmod through a link resolving outside the temp dir: err = %v, want errOutsideTempUnderTest", err)
	}
	if st, err := os.Stat(target); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("target mode %v (%v), want 0600 untouched", st.Mode().Perm(), err)
	}
}

// TestRealFileSeamsGuardASymlinkedParent pins the test guard on a path
// whose parent directory (not the file) is a symlink leaving the temp
// directory: WriteAtomic, Remove and Backup each resolve the nearest existing
// ancestor and refuse with errOutsideTempUnderTest, so a test can never
// write, remove or back up through a linked directory into the developer's
// files. The resolver is stubbed to send that one directory to outsideProbe,
// so no link ever leaves the temp directory and nothing outside is touched.
func TestRealFileSeamsGuardASymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	linkdir := filepath.Join(dir, "linkdir")
	if err := os.Mkdir(linkdir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing, src := filepath.Join(linkdir, "file"), filepath.Join(dir, "src")
	for _, f := range []string{existing, src} {
		if err := os.WriteFile(f, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fsys := realFS{resolve: func(p string) (string, error) {
		if filepath.Clean(p) == linkdir {
			return outsideProbe, nil
		}
		return filepath.EvalSymlinks(p)
	}}
	for name, err := range map[string]error{
		"WriteAtomic, file absent":   fsys.WriteAtomic(filepath.Join(linkdir, "new"), []byte("x"), 0o600),
		"WriteAtomic, file present":  fsys.WriteAtomic(existing, []byte("x"), 0o600),
		"Remove":                     fsys.Remove(existing),
		"Backup":                     func() error { _, _, err := fsys.Backup(src, linkdir); return err }(),
		"Backup, dir under the link": func() error { _, _, err := fsys.Backup(src, filepath.Join(linkdir, "a", "b")); return err }(),
	} {
		if !errors.Is(err, errOutsideTempUnderTest) {
			t.Errorf("%s: err = %v, want errOutsideTempUnderTest", name, err)
		}
	}
	if b, err := os.ReadFile(existing); err != nil || string(b) != "old" {
		t.Errorf("the file under the link changed: %q %v", b, err)
	}
	if ents, _ := os.ReadDir(linkdir); len(ents) != 1 {
		t.Errorf("linkdir holds %d entries, want only the original file", len(ents))
	}
}

// TestRealWriteAtomicRefusesAnUnresolvableSymlink pins that a symlink that
// does not resolve (dangling, or a loop) is refused with nothing written: a
// rename over it would replace the link with a regular file holding a new
// secret where the user did not mean one to be. The error names the path and
// no content.
func TestRealWriteAtomicRefusesAnUnresolvableSymlink(t *testing.T) {
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling.plist")
	if err := os.Symlink(filepath.Join(dir, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	a, b := filepath.Join(dir, "loop-a"), filepath.Join(dir, "loop-b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	for _, link := range []string{dangling, a} {
		err := (realFS{}).WriteAtomic(link, []byte("sk-secret-content"), 0o600)
		if err == nil || !strings.Contains(err.Error(), link+" is a symlink that does not resolve; nothing was written") {
			t.Errorf("%s: err = %v", link, err)
		} else if strings.Contains(err.Error(), "sk-secret-content") {
			t.Errorf("%s: the error carries the content", link)
		}
		if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: the link was replaced: %v %v", link, fi, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "missing")); err == nil {
		t.Errorf("the dangling link's target was created")
	}
}
