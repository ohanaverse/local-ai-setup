package main

import (
	"bytes"
	"errors"
	"os"
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
	got, err := protectedPaths(protectApp(localModel("a", "~"), localModel("b", "~/x")))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{home, filepath.Join(home, "x")}; !slices.Equal(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

// TestProtectedPathsRelativeFailsClosed verifies a relative local_path
// (fetch or draft) is an error naming the model, the value and the fix
// ("write it as an absolute path"), from any working directory. Why: no tool
// agrees on what a relative local_path is relative to (wt model add makes it
// absolute, llmbench resolves it from its own cwd, modeladmin from wt's), so
// cleanup cannot know which directory to protect: it fails closed.
func TestProtectedPathsRelativeFailsClosed(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, c := range []struct {
		m    config.Model
		want string
	}{
		{localModel("omlx/rel", "models/q4"), `model omlx/rel: fetch.local_path "models/q4" is relative; write it as an absolute path`},
		{config.Model{ID: "mlx/d", Draft: config.ModelArtifact{LocalPath: "./draft"}}, `model mlx/d: draft.local_path "./draft" is relative; write it as an absolute path`},
	} {
		got, err := protectedPaths(protectApp(localModel("ok", "/x"), c.m))
		if err == nil || err.Error() != c.want {
			t.Errorf("err = %v (paths %q), want %q", err, got, c.want)
		}
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
		_, err := protectedPaths(protectApp(localModel("ok", "/x"), c.m))
		if err == nil || !strings.Contains(err.Error(), "omlx/bad") {
			t.Errorf("%s: err = %v, want an error naming omlx/bad", c.name, err)
		}
	}
	t.Setenv("HOME", "")
	if _, err := protectedPaths(protectApp(localModel("omlx/home", "~/m"))); err == nil || !strings.Contains(err.Error(), "omlx/home") {
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
	if _, err := protectedPaths(a); !errors.Is(err, errRegistryUnavailable) {
		t.Errorf("loadErr set: err = %v, want errRegistryUnavailable", err)
	}
	if _, err := protectedPaths(&app{}); !errors.Is(err, errRegistryUnavailable) {
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
		config.Model{ID: "c", Fetch: config.ModelArtifact{LocalPath: "/z/./m"}, Draft: config.ModelArtifact{LocalPath: filepath.Join(root, "rel") + "/"}},
		localModel("d", filepath.Join(root, "rel")),
	))
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
// with the load error on that same line after "could not be read: " and no
// repeated wrapper text ("failed to load"); and that the --json document is
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
	if !strings.HasPrefix(line, "registry: could not be read: registry.toml: line 3") ||
		strings.Contains(line, "failed to load") ||
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

// registryApp is newApp under a scratch HOME whose default config home is
// spelled out (XDG_CONFIG_HOME = $HOME/.config, which is not a redirect),
// with WT_LITELLM_CONFIG named as every scratch registry needs. setup may
// write a registry or redirect it; it gets the default registry path.
func registryApp(t *testing.T, setup func(home, defaultReg string)) *app {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("WT_LITELLM_CONFIG", filepath.Join(home, "config.yaml"))
	t.Setenv("WT_REGISTRY", "")
	t.Setenv("MODELMAN_REGISTRY", "")
	setup(home, filepath.Join(home, ".config", "local-ai", "registry.toml"))
	a, err := newApp()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestProtectedPathsMissingRegistryIsNothingToProtect verifies that with no
// registry file at the default path (not redirected) protectedPaths returns
// an empty list and no error, and `wt cleanup` says so neutrally in its
// header ("registry: none at <path>; ...") with no protection-OFF line.
// Why: no registry means nothing is registered, so there is nothing to
// protect; refusing removal there would make cleanup useless on a machine
// that never set up models.
func TestProtectedPathsMissingRegistryIsNothingToProtect(t *testing.T) {
	var reg string
	a := registryApp(t, func(_, def string) { reg = def })
	if !errors.Is(a.loadErr, config.ErrRegistryMissing) {
		t.Fatalf("loadErr = %v, want ErrRegistryMissing", a.loadErr)
	}
	got, err := protectedPaths(a)
	if err != nil || len(got) != 0 {
		t.Fatalf("protectedPaths = %q, %v; want none and no error", got, err)
	}
	stubCleanupGH(t)
	dir := cleanupRepo(t)
	plain, _, err := runCleanup(t, a, dir, "--plain", "--no-fetch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "\nregistry: none at "+reg+"; no model artifacts are registered\n") {
		t.Errorf("no neutral registry line for %s:\n%s", reg, plain)
	}
	if strings.Contains(plain, "protection is OFF") {
		t.Errorf("missing registry reads as a failure:\n%s", plain)
	}
}

// TestProtectedPathsRegistryLoadFailuresFailClosed verifies the registry
// failures other than "no file at the default path" are errRegistryUnavailable:
// a missing registry the environment redirected (WT_REGISTRY), invalid
// TOML, and a symlink to nothing. Why: each may hide registered artifacts
// (a typo in WT_REGISTRY, a dotfiles link on an unmounted volume, a hand
// edit gone wrong), so removal must refuse rather than protect nothing.
func TestProtectedPathsRegistryLoadFailuresFailClosed(t *testing.T) {
	for name, setup := range map[string]func(home, def string){
		"missing redirected": func(home, _ string) {
			t.Setenv("WT_REGISTRY", filepath.Join(home, "elsewhere", "registry.toml"))
		},
		"invalid toml": func(home, _ string) {
			p := filepath.Join(home, "bad.toml")
			if err := os.WriteFile(p, []byte("models = [\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WT_REGISTRY", p)
		},
		"dangling symlink": func(_, def string) {
			if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(def), "gone.toml"), def); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := registryApp(t, setup)
			if a.loadErr == nil {
				t.Fatal("registry loaded, want a load error")
			}
			if _, err := protectedPaths(a); !errors.Is(err, errRegistryUnavailable) {
				t.Errorf("err = %v, want errRegistryUnavailable", err)
			}
		})
	}
}
