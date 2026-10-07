package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkedRegistry points the registry path at a symlink under a fresh config
// home and returns the link and the path it points at (which the caller
// creates, or leaves missing).
func linkedRegistry(t *testing.T) (link, target string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("MODELMAN_REGISTRY", "")
	link = filepath.Join(home, "local-ai", "registry.toml")
	target = filepath.Join(t.TempDir(), "dotfiles", "registry.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	return link, target
}

// TestResolveRegistryFile pins the symlink rule the registry's reader and
// (from the next step) its writer share: a link that leads to a file resolves
// to that file, so a write can rename onto it and keep the link; a link that
// leads nowhere is ErrRegistryLink; only a path with nothing at it is
// "missing".
func TestResolveRegistryFile(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(real, []byte("models = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name, dest string) string {
		p := filepath.Join(dir, name)
		if err := os.Symlink(dest, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	direct := mk("direct.toml", real)
	chained := mk("chained.toml", direct)
	relative := mk("relative.toml", "real.toml")
	dangling := mk("dangling.toml", filepath.Join(dir, "gone", "registry.toml"))
	loopA := filepath.Join(dir, "loop-a.toml")
	loopB := mk("loop-b.toml", loopA)
	mk("loop-a.toml", loopB)

	ok := []struct {
		name, path, want string
		exists           bool
	}{
		{"a regular file", real, real, true},
		{"nothing there", filepath.Join(dir, "absent.toml"), filepath.Join(dir, "absent.toml"), false},
		{"a link to a file", direct, resolvedReal, true},
		{"a link to a link", chained, resolvedReal, true},
		{"a relative link", relative, resolvedReal, true},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			got, exists, err := resolveRegistryFile(c.path)
			if err != nil || got != c.want || exists != c.exists {
				t.Errorf("resolveRegistryFile = (%q, %v, %v), want (%q, %v, nil)", got, exists, err, c.want, c.exists)
			}
		})
	}
	for name, path := range map[string]string{"a dangling link": dangling, "a link loop": loopA} {
		t.Run(name, func(t *testing.T) {
			_, exists, err := resolveRegistryFile(path)
			if !errors.Is(err, ErrRegistryLink) || exists {
				t.Fatalf("resolveRegistryFile = (exists %v, %v), want ErrRegistryLink", exists, err)
			}
			if errors.Is(err, ErrRegistryMissing) {
				t.Error("a broken link must not read as a missing registry")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q should name the link %s", err, path)
			}
		})
	}
	if _, _, err := resolveRegistryFile(dangling); !strings.Contains(err.Error(), filepath.Join(dir, "gone", "registry.toml")) || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a dangling link's error should name its target and say it does not exist, got %q", err)
	}
}

// TestLoadReadsARegistryThroughASymlink pins the half of #248 that already
// worked: a registry linked into a dotfiles checkout loads like any other.
func TestLoadReadsARegistryThroughASymlink(t *testing.T) {
	_, target := linkedRegistry(t)
	if err := os.WriteFile(target, []byte(minimalRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "ollama/gemma4:9b" {
		t.Errorf("models = %+v, want the linked registry's one model", cfg.Models)
	}
}

// TestLoadRefusesADanglingRegistrySymlink pins the read side of #248 in wt: a
// registry link whose target is gone (an unmounted volume, a dotfiles checkout
// not cloned yet) is a broken pointer, not an absent registry. Read as
// "missing", an unconfigured agent launches with no model routing and the
// user is told to seed a registry they already have. Load must also return no
// Config: only a missing registry keeps the parsed agents.
func TestLoadRefusesADanglingRegistrySymlink(t *testing.T) {
	link, target := linkedRegistry(t)
	cfg, err := Load()
	if !errors.Is(err, ErrRegistryLink) {
		t.Fatalf("Load error = %v, want ErrRegistryLink", err)
	}
	if errors.Is(err, ErrRegistryMissing) {
		t.Error("a dangling link must not be reported as ErrRegistryMissing")
	}
	if cfg != nil {
		t.Errorf("Load returned a Config (%+v) for a broken registry link; want nil, as for any unreadable registry", cfg)
	}
	for _, want := range []string{link, target} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "seed it") {
		t.Errorf("error %q tells the user to seed a registry they already have", err)
	}
	if _, statErr := os.Lstat(target); !os.IsNotExist(statErr) {
		t.Errorf("reading must not create the link's target; Lstat error = %v", statErr)
	}
}

// TestRegistryFixHintForABrokenLink pins the hint a broken registry link
// gets. Without it the commands that refuse to run on a config error append
// "run `wt config` to repair", and `wt config` cannot repair a symlink.
func TestRegistryFixHintForABrokenLink(t *testing.T) {
	linkedRegistry(t)
	_, err := Load()
	const want = "fix the link or move it aside"
	if got := RegistryFixHint(err); got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
}
