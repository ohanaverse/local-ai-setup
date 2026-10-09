package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// scratchRegistry gives the test its own config home holding registry.toml
// with content ("" for no file) and returns the registry path.
func scratchRegistry(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	path := filepath.Join(home, "local-ai", "registry.toml")
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// dirNames lists a directory, for asserting that nothing was left behind.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func setFamily(id, family string) func(*RegistryDoc) error {
	return func(d *RegistryDoc) error {
		return d.PatchModel(id, map[string]any{"family": family}, nil)
	}
}

// TestUpdateRegistryWritesAPatch is the write path end to end: a patch lands
// in the file, wt's own reader sees it, and the rest of the file is as it
// was. If this breaks, `wt model edit` reports success and changes nothing.
func TestUpdateRegistryWritesAPatch(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	changed, err := UpdateRegistry(setFamily("ollama/beta", "renamed"))
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	want := strings.Replace(docRegistry, "id = \"ollama/beta\"\nfamily = \"fam\"", "id = \"ollama/beta\"\nfamily = \"renamed\"", 1)
	if got := readFile(t, path); got != want {
		t.Errorf("file after the write:\n%s\nwant:\n%s", got, want)
	}
	_, models, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[1].Family != "renamed" {
		t.Errorf("the reader should see the new family, got %+v", models)
	}
	if got, want := dirNames(t, filepath.Dir(path)), []string{"registry.toml", "registry.toml.lock"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("registry directory holds %v, want %v (no temp file left behind)", got, want)
	}
}

// TestUpdateRegistryNoOpLeavesTheFileByteIdentical is the byte-stability
// promise on the cross-language fixture: a patch that asks for the values a
// row already has — on the row with integer prices, an empty tags array and a
// model_info table — writes nothing, so the bytes and the modification time
// are untouched. A no-op that rewrote the file would make every `wt model
// init` look like a change to whatever watches it: an open editor would
// report the file changed on disk, and a dotfiles repository a new diff.
func TestUpdateRegistryNoOpLeavesTheFileByteIdentical(t *testing.T) {
	fixture := readFile(t, "../../../docs/contracts/registry.written.sample.toml")
	path := scratchRegistry(t, fixture)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.PatchModel("ollama/written-fixture:int", map[string]any{
			"cost.input_price_per_million":  3.0,
			"cost.output_price_per_million": 15.0,
			"tags":                          []string{},
			"model_info": map[string]any{
				"max_input_tokens": 131072, "supports_function_calling": true, "supports_vision": false,
				"x_aliases": []map[string]any{{"alias": "wf"}, {"alias": "written", "weight": 0.5}},
			},
		}, []string{"cost.cache_price_per_million"})
	})
	if err != nil || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if got := readFile(t, path); got != fixture {
		t.Errorf("a no-op write changed the file:\n%s", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("a no-op write replaced the file (its modification time changed)")
	}
}

// TestUpdateRegistryNoOpOnARowWithNoTagsKey pins the spec's byte-stability
// case: integer prices, a model_info table and no tags key at all. A writer
// that went through config.Model would add `tags = []` and rewrite 3 as 3.0
// on every edit, so every save by either tool would flip the same lines.
func TestUpdateRegistryNoOpOnARowWithNoTagsKey(t *testing.T) {
	const reg = "[[models]]\nid = \"ollama/a\"\nfamily = \"f\"\nprovider_id = \"ollama\"\nmodel_name = \"a\"\n\n[models.cost]\ninput_price_per_million = 3\noutput_price_per_million = 15\n\n[models.model_info]\nmax_input_tokens = 131072\n"
	path := scratchRegistry(t, reg)
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		return d.PatchModel("ollama/a", map[string]any{
			"cost.input_price_per_million": 3.0, "cost.output_price_per_million": 15.0,
			"model_info": map[string]any{"max_input_tokens": 131072},
		}, []string{"cost.cache_price_per_million"})
	})
	if err != nil || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if got := readFile(t, path); got != reg {
		t.Errorf("a no-op write changed the file:\n%s", got)
	}
}

// TestUpdateRegistryNoOpKeepsAHandFormattedFile pins the same promise for a
// registry a person formatted: comments, their own layout. wt cannot emit
// that form, so the rule is that it does not have to — a write that changes
// nothing leaves the file alone, and only the first real change lays it out
// in tomli-w's form, the one form wt writes.
func TestUpdateRegistryNoOpKeepsAHandFormattedFile(t *testing.T) {
	const hand = `# my models
[[providers]]
id = "ollama"   # the local daemon
name = "Ollama"
location = "local"
auth = { type = "none" }

[[models]]
id = "ollama/a"
family = "f"
provider_id = "ollama"
model_name = "a"
tags = ["code"]
`
	path := scratchRegistry(t, hand)
	changed, err := UpdateRegistry(setFamily("ollama/a", "f"))
	if err != nil || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (false, nil)", changed, err)
	}
	if got := readFile(t, path); got != hand {
		t.Errorf("a no-op write reformatted a hand-written file:\n%s", got)
	}
	if changed, err := UpdateRegistry(setFamily("ollama/a", "g")); err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	if got := readFile(t, path); strings.Contains(got, "#") || !strings.Contains(got, "family = \"g\"") {
		t.Errorf("a real change should write the file in tomli-w form:\n%s", got)
	}
}

// TestUpdateRegistryCreatesAMissingRegistry pins the first-run case: no
// file, no directory. A successful apply creates both, the file private
// (0600), even when it adds nothing — which is how `wt model init` leaves a
// registry behind on a machine with nothing installed.
func TestUpdateRegistryCreatesAMissingRegistry(t *testing.T) {
	path := scratchRegistry(t, "")
	changed, err := UpdateRegistry(func(*RegistryDoc) error { return nil })
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the registry was not created: %v", err)
	}
	if info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Errorf("new registry: size %d mode %v, want empty and 0600", info.Size(), info.Mode().Perm())
	}
	if _, _, err := loadRegistry(); err != nil {
		t.Errorf("the reader should load an empty registry: %v", err)
	}
	if changed, err := UpdateRegistry(func(*RegistryDoc) error { return nil }); err != nil || changed {
		t.Errorf("a second no-op = (%v, %v), want (false, nil)", changed, err)
	}
}

// TestUpdateRegistryKeepsTheFilesMode pins that a write does not tighten or
// loosen an existing registry's permissions: a temp file is created 0600, and
// renaming it over a 0644 file would silently change who can read it.
func TestUpdateRegistryKeepsTheFilesMode(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	for _, mode := range []os.FileMode{0o644, 0o600, 0o640} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateRegistry(setFamily("ollama/beta", "mode-"+mode.String())); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("mode after a write = %v, want %v", info.Mode().Perm(), mode)
		}
	}
}

// TestUpdateRegistryWritesThroughASymlink pins the write side of #248: a
// registry linked into a dotfiles checkout is written in the checkout, and
// the link is still a link afterwards. Renaming onto the link itself would
// replace it with a regular file and the checkout would silently stop
// receiving edits. The lock file belongs beside the link, not in the
// checkout.
func TestUpdateRegistryWritesThroughASymlink(t *testing.T) {
	link, target := linkedRegistry(t)
	if err := os.WriteFile(target, []byte(docRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := UpdateRegistry(setFamily("ollama/beta", "linked")); err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the registry path is no longer a symlink")
	}
	if got := readFile(t, target); !strings.Contains(got, `family = "linked"`) {
		t.Errorf("the link's target was not written:\n%s", got)
	}
	if got := dirNames(t, filepath.Dir(target)); fmt.Sprint(got) != "[registry.toml]" {
		t.Errorf("the checkout directory holds %v, want only registry.toml (no lock, no temp file)", got)
	}
	if got := dirNames(t, filepath.Dir(link)); fmt.Sprint(got) != "[registry.toml registry.toml.lock]" {
		t.Errorf("the config directory holds %v, want the link and its lock", got)
	}
}

// TestUpdateRegistryRefusesADanglingSymlink pins the other half of #248: a
// registry link whose target is gone is an error, and nothing is created.
// Writing the target would recreate a registry on a volume that is not
// mounted, from an empty document, shadowing the real one when it comes back.
func TestUpdateRegistryRefusesADanglingSymlink(t *testing.T) {
	link, target := linkedRegistry(t)
	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, ErrRegistryLink) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryLink", changed, err)
	}
	if ran {
		t.Error("apply ran against a registry that could not be read")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("the link's target must not be created; Lstat error = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link itself must be left alone (err %v)", err)
	}
}

// TestUpdateRegistryRefusesARegistryUnderABrokenDirectoryLink pins the same
// rule one level up: when the registry's directory is the link that leads
// nowhere, the write is ErrRegistryLink naming the link and its target, and
// nothing is created. Without the check before the lock the user is told only
// `mkdir ...: file exists`, with no word about the link they have to fix.
func TestUpdateRegistryRefusesARegistryUnderABrokenDirectoryLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	link := filepath.Join(home, "local-ai")
	target := filepath.Join(t.TempDir(), "dotfiles", "local-ai")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, ErrRegistryLink) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryLink", changed, err)
	}
	for _, want := range []string{link, target} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if ran {
		t.Error("apply ran against a registry that could not be read")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Errorf("the link's target must not be created; Lstat error = %v", err)
	}
	if got := dirNames(t, home); len(got) != 1 {
		t.Errorf("the config home holds %v, want only the link", got)
	}
}

// TestUpdateRegistryRefusesAnUnknownTopLevelKey pins #247 for the writer: a
// registry with a section wt does not know is not written at all, the error
// names the key and the file, and apply never runs. Writing it would either
// carry forward a section the Python tools refuse to load, or drop it.
func TestUpdateRegistryRefusesAnUnknownTopLevelKey(t *testing.T) {
	content := "schema_version = 1\n" + docRegistry
	path := scratchRegistry(t, content)
	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, ErrRegistryTopLevel) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryTopLevel", changed, err)
	}
	for _, want := range []string{"`schema_version`", path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err, want)
		}
	}
	if ran {
		t.Error("apply ran on a registry the writer refused")
	}
	if got := readFile(t, path); got != content {
		t.Error("the refused registry was modified")
	}
}

// TestUpdateRegistryRefusesAFileItCannotParse pins that a malformed registry
// is an error naming the file, never an empty document to write over — the
// data loss #240 was about.
func TestUpdateRegistryRefusesAFileItCannotParse(t *testing.T) {
	const broken = "[[models]\nid = \"a\"\n"
	path := scratchRegistry(t, broken)
	changed, err := UpdateRegistry(func(*RegistryDoc) error { return nil })
	if err == nil || changed || !strings.Contains(err.Error(), "parse "+path) {
		t.Fatalf("UpdateRegistry = (%v, %v), want a parse error naming the file", changed, err)
	}
	if got := readFile(t, path); got != broken {
		t.Error("the malformed registry was modified")
	}
}

// TestUpdateRegistryReturnsApplysErrorAndWritesNothing pins the failure path
// every caller relies on: an apply that fails (an unknown id, a duplicate)
// leaves the file as it was, with its error intact for errors.Is.
func TestUpdateRegistryReturnsApplysErrorAndWritesNothing(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		if err := d.PatchModel("ollama/beta", map[string]any{"family": "half-done"}, nil); err != nil {
			return err
		}
		return d.PatchModel("ollama/nope", map[string]any{"family": "x"}, nil)
	})
	if !errors.Is(err, ErrModelNotFound) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrModelNotFound", changed, err)
	}
	if got := readFile(t, path); got != docRegistry {
		t.Errorf("a failed apply changed the file:\n%s", got)
	}
}

// otherWriter plays another program (an editor) saving the registry in the
// window between wt encoding its write and replacing the file: it rewrites
// the file the first `times` times the seam fires, appending a model each
// time.
func otherWriter(t *testing.T, path string, times int) *int {
	t.Helper()
	fired := 0
	old := registryBeforeRename
	registryBeforeRename = func() {
		if fired >= times {
			return
		}
		fired++
		extra := fmt.Sprintf("\n[[models]]\nid = \"ollama/other-%d\"\nfamily = \"fam\"\nprovider_id = \"ollama\"\nmodel_name = \"other-%d\"\n", fired, fired)
		if err := os.WriteFile(path, []byte(readFile(t, path)+extra), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { registryBeforeRename = old })
	return &fired
}

// TestUpdateRegistryRetriesWhenTheFileChangesUnderIt pins the re-check that
// covers the writer wt cannot lock out: an editor takes no lock on
// registry.toml, so a hand edit can be saved between wt's read and wt's
// rename. wt must then start over on the new file, so both edits survive —
// wt's blind rename would have reverted the hand edit.
func TestUpdateRegistryRetriesWhenTheFileChangesUnderIt(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	fired := otherWriter(t, path, 1)
	runs := 0
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		runs++
		return d.PatchModel("ollama/beta", map[string]any{"family": "mine"}, nil)
	})
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	if runs != 2 || *fired != 1 {
		t.Errorf("apply ran %d time(s) with %d outside write(s), want 2 and 1", runs, *fired)
	}
	got := readFile(t, path)
	if !strings.Contains(got, `family = "mine"`) || !strings.Contains(got, `id = "ollama/other-1"`) {
		t.Errorf("both edits should survive:\n%s", got)
	}
}

// TestUpdateRegistryGivesUpAfterThreeAttempts pins the bound on that retry:
// apply runs at most three times, then the caller gets ErrRegistryBusy and
// the file is whatever the other writer left — never a mix, and never a wt
// write on top of a file wt has not read.
func TestUpdateRegistryGivesUpAfterThreeAttempts(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	fired := otherWriter(t, path, 99)
	runs := 0
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		runs++
		return d.PatchModel("ollama/beta", map[string]any{"family": "mine"}, nil)
	})
	if !errors.Is(err, ErrRegistryBusy) || changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want ErrRegistryBusy", changed, err)
	}
	if runs != 3 || *fired != 3 {
		t.Errorf("apply ran %d time(s) with %d outside write(s), want 3 and 3", runs, *fired)
	}
	got := readFile(t, path)
	if strings.Contains(got, `family = "mine"`) || !strings.Contains(got, `id = "ollama/other-3"`) {
		t.Errorf("the file should be the other writer's, without wt's edit:\n%s", got)
	}
}

// TestUpdateRegistrySerializesConcurrentWriters pins the lock: eight writers
// at once each add one model and all eight are in the file. Without the flock
// two of them read the same bytes and the second rename drops the first's
// model — the lost update `wt cloud-sync` beside an open `wt config` would hit.
func TestUpdateRegistrySerializesConcurrentWriters(t *testing.T) {
	path := scratchRegistry(t, docRegistry)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("ollama/concurrent-%d", i)
			_, err := UpdateRegistry(func(d *RegistryDoc) error {
				return d.AddModel(map[string]any{"id": id, "family": "fam", "provider_id": "ollama", "model_name": id})
			})
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	got := readFile(t, path)
	for i := 0; i < 8; i++ {
		if !strings.Contains(got, fmt.Sprintf(`id = "ollama/concurrent-%d"`, i)) {
			t.Errorf("writer %d's model is missing from the file", i)
		}
	}
}

// TestUpdateRegistryWritesARedirectedRegistry pins that the registry write
// itself is not subject to the ErrRegistryRedirected rule. That rule protects
// LiteLLM's config.yaml, which follows neither registry variable; writing a
// scratch registry is exactly what a redirect is for. The refusal belongs to
// the route sync a command runs afterwards, in internal/litellm.
func TestUpdateRegistryWritesARedirectedRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	path := filepath.Join(t.TempDir(), "scratch", "registry.toml")
	t.Setenv("WT_REGISTRY", path)
	if !RegistryRedirected() {
		t.Fatal("fixture error: the registry should read as redirected")
	}
	changed, err := UpdateRegistry(func(d *RegistryDoc) error {
		// A model row is written only beside the provider row it names.
		if err := d.AddProvider(map[string]any{"id": "ollama", "location": "local", "auth": map[string]any{"type": "none"}}); err != nil {
			return err
		}
		return d.AddModel(map[string]any{"id": "ollama/a", "family": "f", "provider_id": "ollama", "model_name": "a"})
	})
	if err != nil || !changed {
		t.Fatalf("UpdateRegistry = (%v, %v), want (true, nil)", changed, err)
	}
	if got := readFile(t, path); !strings.Contains(got, `id = "ollama/a"`) {
		t.Errorf("the redirected registry was not written:\n%s", got)
	}
}

// TestUpdateRegistryAsksTheWriteGuardFirst pins the seam that keeps a test
// binary off the developer's registry: a guarded path is refused before
// anything touches the disk — no lock file, no directory, no write — and
// apply never runs. The guarded path here is a temp file; the next test
// checks what TestMain really guards.
func TestUpdateRegistryAsksTheWriteGuardFirst(t *testing.T) {
	path := scratchRegistry(t, "")
	old := registryWriteGuard
	registryWriteGuard = refuseRegistryPaths([]string{path})
	t.Cleanup(func() { registryWriteGuard = old })

	ran := false
	changed, err := UpdateRegistry(func(*RegistryDoc) error { ran = true; return nil })
	if !errors.Is(err, errRegistryGuarded) || changed || ran {
		t.Fatalf("UpdateRegistry = (%v, %v), apply ran = %v; want the guard's refusal and no apply", changed, err, ran)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("a guarded write must not create the registry directory or its lock; Stat error = %v", err)
	}

	// Through a symlink too: the guard sees the file the link resolves to.
	link, target := linkedRegistry(t)
	if err := os.WriteFile(target, []byte(docRegistry), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	registryWriteGuard = refuseRegistryPaths([]string{resolved})
	if _, err := UpdateRegistry(setFamily("ollama/beta", "x")); !errors.Is(err, errRegistryGuarded) {
		t.Errorf("a link (%s) to a guarded file: err = %v, want the guard's refusal", link, err)
	}
	if got := readFile(t, target); got != docRegistry {
		t.Error("a guarded registry was written through its link")
	}

	// And through a linked directory: the registry is named under a symlink
	// to the guarded file's config home (a dotfiles layout, or /tmp for
	// /private/tmp), and does not exist yet.
	realHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realHome, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", alias)
	registryWriteGuard = refuseRegistryPaths([]string{filepath.Join(realHome, "local-ai", "registry.toml")})
	if _, err := UpdateRegistry(func(*RegistryDoc) error { return nil }); !errors.Is(err, errRegistryGuarded) {
		t.Errorf("a guarded registry named through a linked directory: err = %v, want the guard's refusal", err)
	}
	if names := dirNames(t, realHome); len(names) != 0 {
		t.Errorf("a guarded write created %v under the linked directory", names)
	}
}

// TestTestMainGuardsTheDevelopersRegistry pins what this package's TestMain
// arms through IsolateConfigHomeForTest: UpdateRegistry refuses the registry
// under the developer's real home, whatever a test does to the environment.
// It asks the guard directly and writes nothing.
func TestTestMainGuardsTheDevelopersRegistry(t *testing.T) {
	if registryWriteGuard == nil {
		t.Fatal("registryWriteGuard is not set: TestMain must call IsolateConfigHomeForTest")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	real := filepath.Join(home, ".config", "local-ai", "registry.toml")
	if err := registryWriteGuard(real, ""); !errors.Is(err, errRegistryGuarded) {
		t.Errorf("guard(%s) = %v, want a refusal", real, err)
	}
	if err := registryWriteGuard(filepath.Join(t.TempDir(), "registry.toml"), ""); err != nil {
		t.Errorf("a temp registry should be allowed, got %v", err)
	}
}
