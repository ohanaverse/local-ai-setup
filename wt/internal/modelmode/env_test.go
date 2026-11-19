package modelmode

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outsideProbe is a path outside os.TempDir() that nothing can create: its
// parent is /dev/null, a character device, so even a write the guard let
// through would fail at the MkdirAll and leave the machine unchanged.
const outsideProbe = "/dev/null/wt-modelsetup-guard-probe"

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
	if err := (RealFS{}).WriteAtomic(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" {
		t.Errorf("target = %q, want new", b)
	}
}

// TestRealWriteAtomicGuardsTheResolvedPath pins the test guard on the path a
// symlink resolves to: a link inside the temp directory whose target is
// outside it is refused with ErrOutsideTempUnderTest, so a test can never
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
	fsys := RealFS{Resolve: func(string) (string, error) { return outsideProbe, nil }}
	err := fsys.WriteAtomic(link, []byte("new"), 0o600)
	if !errors.Is(err, ErrOutsideTempUnderTest) {
		t.Errorf("WriteAtomic through a link resolving outside the temp dir: err = %v, want ErrOutsideTempUnderTest", err)
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
	fsys := RealFS{Resolve: func(string) (string, error) { return outsideProbe, nil }}
	if err := fsys.Chmod(link, 0o644); !errors.Is(err, ErrOutsideTempUnderTest) {
		t.Errorf("Chmod through a link resolving outside the temp dir: err = %v, want ErrOutsideTempUnderTest", err)
	}
	if st, err := os.Stat(target); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("target mode %v (%v), want 0600 untouched", st.Mode().Perm(), err)
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
		err := (RealFS{}).WriteAtomic(link, []byte("sk-secret-content"), 0o600)
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

// TestRealFSRefusesPathsOutsideTheTempDirUnderTest pins that the real file
// seams never change a file outside os.TempDir() from a test binary, so a
// test that reaches DefaultEnv cannot touch the developer's LaunchAgents or
// config home. (modelsetup keeps a same-named test for its Remove seam.)
func TestRealFSRefusesPathsOutsideTheTempDirUnderTest(t *testing.T) {
	outside := "/nonexistent-wt-test-root/Library/LaunchAgents/x.plist"
	fsys := RealFS{}
	for name, err := range map[string]error{
		"WriteAtomic": fsys.WriteAtomic(outside, []byte("x"), 0o600),
		"Chmod":       fsys.Chmod(outside, 0o600),
	} {
		if err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Errorf("%s outside the temp dir: err = %v, want a test-binary refusal", name, err)
		}
	}
	inside := filepath.Join(t.TempDir(), "f")
	if err := fsys.WriteAtomic(inside, []byte("x"), 0o600); err != nil {
		t.Errorf("WriteAtomic inside the temp dir: %v", err)
	}
}

// TestRealHostHomeRefusesTheDevelopersHomeUnderTest pins that the real home
// seam hands a test binary no home outside os.TempDir(): a test that wants one
// sets HOME to a temp dir.
func TestRealHostHomeRefusesTheDevelopersHomeUnderTest(t *testing.T) {
	t.Setenv("HOME", "/nonexistent-wt-test-home")
	if h, err := (RealHost{}).Home(); err == nil {
		t.Errorf("Home() = %q under test with HOME outside the temp dir", h)
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if h, err := (RealHost{}).Home(); err != nil || h != dir {
		t.Errorf("Home() = %q, %v; want %q", h, err, dir)
	}
}

// TestDefaultEnvIsTheRealMachine pins that DefaultEnv is the four real
// seams, each under its exported type (so a caller can type-assert the
// machine it got).
func TestDefaultEnvIsTheRealMachine(t *testing.T) {
	e := DefaultEnv()
	if _, ok := e.FS.(RealFS); !ok {
		t.Errorf("FS is %T, want RealFS", e.FS)
	}
	if _, ok := e.Config.(RealConfig); !ok {
		t.Errorf("Config is %T, want RealConfig", e.Config)
	}
	if _, ok := e.Net.(RealNet); !ok {
		t.Errorf("Net is %T, want RealNet", e.Net)
	}
	if _, ok := e.Host.(RealHost); !ok {
		t.Errorf("Host is %T, want RealHost", e.Host)
	}
}

// TestDanglingLinkIsALinkStatCannotFollow pins DanglingLink over the FS
// seam: only a symlink whose Stat fails reads as dangling — a regular file
// and a missing path are not.
func TestDanglingLinkIsALinkStatCannotFollow(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "missing"), link); err != nil {
		t.Fatal(err)
	}
	if !DanglingLink(RealFS{}, link) {
		t.Errorf("a link to a missing target is not dangling")
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if DanglingLink(RealFS{}, file) {
		t.Errorf("a regular file reads dangling")
	}
	if DanglingLink(RealFS{}, filepath.Join(dir, "absent")) {
		t.Errorf("a missing path reads dangling")
	}
}
