package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// protectApp is an app whose registry loaded and holds models.
func protectApp(models ...config.Model) *app {
	return &app{cfg: &config.Config{Models: models}}
}

// localModel is a registry model whose fetch side is a local_path.
func localModel(id, path string) config.Model {
	return config.Model{ID: id, ProviderID: "omlx", ModelName: id, Fetch: config.ModelArtifact{LocalPath: path}}
}

// TestMinAgeFloorRejected verifies a --min-age below cleanup.MinAgeFloor (1m)
// is a usage error: exit 1, one "wt:" line from main's reporter, no cobra
// usage block and nothing on stdout; 1m and 10m are accepted. Why: any
// positive value (1ns) turned the fresh-branch rule off, so a branch made a
// second ago could read as proven merged once removal acts on that verdict.
func TestMinAgeFloorRejected(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	stdout, stderr, err := runCleanup(t, a, dir, "--plain", "--no-fetch", "--min-age", "30s")
	if err == nil {
		t.Fatal("--min-age 30s accepted, want a usage error")
	}
	if stdout != "" || stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want both empty (no usage block)", stdout, stderr)
	}
	var w bytes.Buffer
	if code := reportExit(err, &w); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimRight(w.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "wt: ") || !strings.Contains(lines[0], "--min-age") || !strings.Contains(lines[0], "1m") {
		t.Errorf("reported %q, want one wt: line naming --min-age and the 1m floor", w.String())
	}
	for _, ok := range []string{"1m", "10m"} {
		if _, _, err := runCleanup(t, a, dir, "--plain", "--no-fetch", "--min-age", ok); err != nil {
			t.Errorf("--min-age %s: err = %v, want accepted", ok, err)
		}
	}
}

// TestProtectedPathsExpandsBareHome verifies a local_path of "~" and of
// "~/x" are protected as $HOME and $HOME/x. Why: only "~/" was expanded, so
// a bare "~" was taken as a relative directory named "~" and the home
// directory itself went unprotected.
func TestProtectedPathsExpandsBareHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := protectedPaths(protectApp(localModel("a", "~"), localModel("b", "~/x")), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{home, filepath.Join(home, "x")}; !slices.Equal(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

// TestProtectedPathsRelativeIsAgainstRepoRoot verifies a relative local_path
// is resolved against the repository root passed in, not the working
// directory (the test runs from an unrelated directory to prove it). Why:
// `wt cleanup` run from a linked worktree or a subdirectory resolved the same
// registry entry to a different directory, so the artifact it named could go
// unprotected.
func TestProtectedPathsRelativeIsAgainstRepoRoot(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	t.Chdir(elsewhere)
	got, err := protectedPaths(protectApp(localModel("a", "models/q4"), localModel("b", "./out/../draft")), root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(root, "draft"), filepath.Join(root, "models", "q4")}; !slices.Equal(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
	if _, err := protectedPaths(protectApp(localModel("a", "models/q4")), ""); err == nil {
		t.Error("relative local_path with no repository root: err = nil, want an error (cannot be protected)")
	}
}

// TestProtectedPathsMalformedFailsClosed verifies a local_path the loader
// tolerated as malformed (present but not a string, or a fetch/draft that is
// not a table; config.ArtifactFaults) is an error naming the model id, as is
// a "~" path when the home directory cannot be found. Why: the loader reads
// such a value as absent, so cleanup would silently not protect a path the
// user registered: a path wt cannot enumerate is a path it cannot protect.
func TestProtectedPathsMalformedFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name string
		m    config.Model
	}{
		{"fetch.local_path not a string", config.Model{ID: "omlx/bad", Fetch: config.ModelArtifact{Malformed: config.ArtifactFaults{LocalPath: true}}}},
		{"draft.local_path not a string", config.Model{ID: "omlx/bad", Draft: config.ModelArtifact{Malformed: config.ArtifactFaults{LocalPath: true}}}},
		{"fetch not a table", config.Model{ID: "omlx/bad", Fetch: config.ModelArtifact{Malformed: config.ArtifactFaults{NotTable: true}}}},
	} {
		_, err := protectedPaths(protectApp(localModel("ok", "/x"), c.m), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "omlx/bad") {
			t.Errorf("%s: err = %v, want an error naming omlx/bad", c.name, err)
		}
	}
	t.Setenv("HOME", "")
	if _, err := protectedPaths(protectApp(localModel("omlx/home", "~/m")), t.TempDir()); err == nil || !strings.Contains(err.Error(), "omlx/home") {
		t.Errorf("~ path with HOME unset: err = %v, want an error naming omlx/home", err)
	}
}

// TestProtectedPathsRegistryFailedIsError verifies a registry that failed to
// load is errRegistryUnavailable, not an empty list. Why: an empty list
// reads as "nothing to protect", which would let a removal delete a
// registered model artifact.
func TestProtectedPathsRegistryFailedIsError(t *testing.T) {
	a := protectApp()
	a.loadErr = errors.New("registry.toml: parse error")
	if _, err := protectedPaths(a, t.TempDir()); !errors.Is(err, errRegistryUnavailable) {
		t.Errorf("loadErr set: err = %v, want errRegistryUnavailable", err)
	}
	if _, err := protectedPaths(&app{}, t.TempDir()); !errors.Is(err, errRegistryUnavailable) {
		t.Errorf("nil cfg: err = %v, want errRegistryUnavailable", err)
	}
}

// TestProtectedPathsSortedDeduped verifies the list is absolute, cleaned,
// sorted and holds each path once, whatever spelling the registry used.
// Why: two models sharing one artifact (fetch and draft, or a 4-bit and a
// 6-bit row) must not produce two blockers, and a stable order keeps the
// gates' output deterministic.
func TestProtectedPathsSortedDeduped(t *testing.T) {
	root := t.TempDir()
	got, err := protectedPaths(protectApp(
		localModel("a", "/z/m"),
		localModel("b", "/a/m/"),
		config.Model{ID: "c", Fetch: config.ModelArtifact{LocalPath: "/z/./m"}, Draft: config.ModelArtifact{LocalPath: "rel"}},
		localModel("d", filepath.Join(root, "rel")),
	), root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/a/m", "/z/m", filepath.Join(root, "rel")}; !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("paths = %q, want %q", got, slices.Sorted(slices.Values(want)))
	}
}

// TestCleanupListHeaderWarnsWhenRegistryFailed verifies that with a registry
// that failed to load `wt cleanup` still lists (exit 0) and the plain header
// carries one line saying artifact protection is OFF and removal is refused,
// with the load error on that same line; and that the --json document is
// byte-identical to the one a loaded registry produces. Why: listing is
// read-only and still useful, but the user must know a protected artifact is
// not marked; the JSON contract must not change.
func TestCleanupListHeaderWarnsWhenRegistryFailed(t *testing.T) {
	a, _ := newTestApp(t)
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	cgit(t, dir, "branch", "topic")
	// A fixed clock: the two JSON runs must not differ by an age.
	old := cleanupClassify
	now := time.Now()
	cleanupClassify = func(s cleanup.Snapshot, o cleanup.Options) cleanup.Report { o.Now = now; return old(s, o) }
	t.Cleanup(func() { cleanupClassify = old })

	before, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	a.loadErr = errors.New("registry.toml: line 3\nexpected '='")
	plain, _, err := runCleanup(t, a, dir, "--plain", "--no-fetch")
	if err != nil {
		t.Fatalf("err = %v, want exit 0", err)
	}
	var line string
	for _, l := range strings.Split(plain, "\n") {
		if strings.HasPrefix(l, "registry:") {
			line = l
		}
		if strings.HasPrefix(l, "expected") {
			t.Errorf("the load error broke onto its own line:\n%s", plain)
		}
	}
	if !strings.HasPrefix(line, "registry: could not be read") ||
		!strings.HasSuffix(line, "artifact protection is OFF and removal is refused") ||
		!strings.Contains(line, "line 3 expected '='") {
		t.Errorf("registry line = %q\n%s", line, plain)
	}
	after, _, err := runCleanup(t, a, dir, "--json", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("JSON changed when the registry failed:\nbefore %s\nafter  %s", before, after)
	}
}
