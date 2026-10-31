package slotedit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
)

const applyBase = `# keep me
[[profile]]
name    = "alpha"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "m/b"
sonnet = "m/c"
`

func applyOpts(target string) Options {
	return Options{
		Drivers:  agents.SlotDrivers(),
		ModelIDs: []string{"m/a", "m/b", "m/c"},
		Target:   target,
		Now:      func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
	}
}

func setup(t *testing.T, content string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := filepath.Join(t.TempDir(), "slot-profiles.toml")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func replace(old, new string) func([]byte) ([]byte, error) {
	return func(b []byte) ([]byte, error) {
		return []byte(strings.Replace(string(b), old, new, 1)), nil
	}
}

func assertUntouched(t *testing.T, p, want string) {
	t.Helper()
	got, _ := os.ReadFile(p)
	if string(got) != want {
		t.Errorf("file changed:\n%s", got)
	}
	if m, _ := filepath.Glob(p + ".bak-*"); len(m) != 0 {
		t.Errorf("backup left behind: %v", m)
	}
}

// TestApplyWritesBackupAndFile checks the happy path end to end: a valid
// edit replaces the file, and the old bytes land in a .bak-<stamp> beside
// it — the backup is the user's undo for every write the CLI makes.
func TestApplyWritesBackupAndFile(t *testing.T) {
	p := setup(t, applyBase)
	bak, err := Apply(p, replace(`opus   = "m/b"`, `opus   = "m/a"`), applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if rp, _ := filepath.EvalSymlinks(p); bak != rp+".bak-20261030-120000" {
		t.Errorf("backup = %q", bak)
	}
	if b, _ := os.ReadFile(bak); string(b) != applyBase {
		t.Errorf("backup content differs")
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"m/a"`) || !strings.Contains(string(b), "# keep me") {
		t.Errorf("file = %s", b)
	}
}

// TestApplyRefusesUnknownModelID checks that a slot naming an id the
// registry does not hold is refused before any write: the file and its
// backups stay untouched, so a typo can never break a launchable profile.
func TestApplyRefusesUnknownModelID(t *testing.T) {
	p := setup(t, applyBase)
	_, err := Apply(p, replace(`"m/b"`, `"m/zzz"`), applyOpts("alpha"))
	if err == nil || !strings.Contains(err.Error(), "m/zzz") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, p, applyBase)
}

// TestApplyRefusesDefaultOnUnfilledSlot checks that a target profile whose
// default names a slot it does not fill is refused: the edit's whole point
// is that what lands on disk still validates.
func TestApplyRefusesDefaultOnUnfilledSlot(t *testing.T) {
	p := setup(t, applyBase)
	_, err := Apply(p, replace(`default = "sonnet"`, `default = "haiku"`), applyOpts("alpha"))
	if err == nil || !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "haiku") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, p, applyBase)
}

// TestApplyRefusesBreakingAnotherProfile checks that an edit which keeps its
// target usable but breaks a different profile is refused: a surgical edit
// must not damage entries it never names.
func TestApplyRefusesBreakingAnotherProfile(t *testing.T) {
	two := applyBase + "\n[[profile]]\nname = \"beta\"\nagent = \"claude\"\ndefault = \"sonnet\"\n[profile.slots]\nsonnet = \"m/c\"\n"
	p := setup(t, two)
	// Target alpha stays fine; beta's default is broken.
	_, err := Apply(p, replace("default = \"sonnet\"\n[profile.slots]\nsonnet = \"m/c\"\n", "default = \"haiku\"\n[profile.slots]\nsonnet = \"m/c\"\n"), applyOpts("alpha"))
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, p, two)
}

// TestApplyRemoveDoesNotFailOnOwnAbsence checks that a removal succeeds even
// though the removed profile is gone from the after-file: Target is "" for
// a removal, so its own absence must not read as "target not found".
func TestApplyRemoveDoesNotFailOnOwnAbsence(t *testing.T) {
	p := setup(t, applyBase)
	bak, err := Apply(p, func(b []byte) ([]byte, error) { return Remove(b, "alpha") }, applyOpts(""))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "alpha") {
		t.Errorf("alpha still present: %s", b)
	}
	if b, _ := os.ReadFile(bak); string(b) != applyBase {
		t.Errorf("backup missing or wrong: %q", bak)
	}
}

// TestApplyRefusesParseError checks that text that is no longer valid TOML
// is refused before the file or a backup is touched — the write is the last
// step, never the test of the edit.
func TestApplyRefusesParseError(t *testing.T) {
	p := setup(t, applyBase)
	if _, err := Apply(p, replace("[profile.slots]", "[profile.slots"), applyOpts("alpha")); err == nil {
		t.Fatal("want refusal")
	}
	assertUntouched(t, p, applyBase)
}

// TestApplyMissingFileCreatesWithoutBackup checks the first-write case:
// `wt profile slots add` on a machine with no slot-profiles.toml creates
// the file and writes no backup, since there is nothing to back up.
func TestApplyMissingFileCreatesWithoutBackup(t *testing.T) {
	p := setup(t, "")
	bak, err := Apply(p, func([]byte) ([]byte, error) { return []byte(applyBase), nil }, applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if bak != "" {
		t.Errorf("backup = %q", bak)
	}
	if b, _ := os.ReadFile(p); string(b) != applyBase {
		t.Errorf("file = %s", b)
	}
	if m, _ := filepath.Glob(p + ".bak-*"); len(m) != 0 {
		t.Errorf("backups: %v", m)
	}
}

// TestApplyBackupNamesDoNotCollideSameSecond checks the -2 suffix: two
// writes inside one timestamp must both keep a distinct backup, never
// overwrite the first.
func TestApplyBackupNamesDoNotCollideSameSecond(t *testing.T) {
	p := setup(t, applyBase)
	b1, err := Apply(p, replace(`"m/b"`, `"m/a"`), applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := Apply(p, replace(`"m/a"`, `"m/b"`), applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if b2 != b1+"-2" {
		t.Errorf("b1=%q b2=%q", b1, b2)
	}
}

// TestApplyKeepsFileModeAndBackupMode checks that neither the file nor its
// backup drifts to a different permission mode: a hand-managed 0600 file
// must stay private across wt's writes.
func TestApplyKeepsFileModeAndBackupMode(t *testing.T) {
	p := setup(t, applyBase)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := Apply(p, replace(`"m/b"`, `"m/a"`), applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p, bak} {
		if st, _ := os.Stat(f); st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v", f, st.Mode().Perm())
		}
	}
}

// TestApplyWritesThroughSymlink checks that a symlinked slot-profiles.toml
// stays a link and the entry behind it is edited: an atomic rename would
// otherwise replace the link with a plain file and silently fork the user's
// setup.
func TestApplyWritesThroughSymlink(t *testing.T) {
	real, _ := filepath.EvalSymlinks(setup(t, applyBase))
	link := filepath.Join(t.TempDir(), "link.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	bak, err := Apply(link, replace(`"m/b"`, `"m/a"`), applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink == 0 {
		t.Error("link replaced by a regular file")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), `"m/a"`) {
		t.Errorf("target not edited: %s", b)
	}
	if filepath.Dir(bak) != filepath.Dir(real) || !strings.HasPrefix(bak, real+".bak-") {
		t.Errorf("backup %q not beside the target %q", bak, real)
	}
}

// TestApplyRefusesNewUnknownKey checks that an edit may not introduce a key
// the schema does not name: wt warns about unknown keys at load, so a write
// must never add one.
func TestApplyRefusesNewUnknownKey(t *testing.T) {
	p := setup(t, applyBase)
	_, err := Apply(p, replace("[profile.slots]", "[profile.slotz]"), applyOpts("alpha"))
	if err == nil {
		t.Fatal("want refusal")
	}
	assertUntouched(t, p, applyBase)
}

// TestApplyKeepsExistingUnknownKey checks the other side of the unknown-key
// rule: a key that was already there never blocks an otherwise-fine edit —
// the file belongs to the user, and wt only refuses to make it worse.
func TestApplyKeepsExistingUnknownKey(t *testing.T) {
	src := "[[profiles]]\nname = \"x\"\n\n" + applyBase
	p := setup(t, src)
	if _, err := Apply(p, replace(`"m/b"`, `"m/a"`), applyOpts("alpha")); err != nil {
		t.Fatalf("a pre-existing unknown key must not block an edit: %v", err)
	}
}

// TestApplyWhenExistingFileDoesNotParseTreatsBeforeAsEmpty checks that a
// repair of an already-broken file is allowed: with nothing usable to
// compare against, only the result is judged.
func TestApplyWhenExistingFileDoesNotParseTreatsBeforeAsEmpty(t *testing.T) {
	broken := "[profile.slots\n"
	p := setup(t, broken)
	bak, err := Apply(p, func([]byte) ([]byte, error) { return []byte(applyBase), nil }, applyOpts("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bak); string(b) != broken {
		t.Errorf("backup = %q", b)
	}
}

// TestApplyNilNowUsesClock checks the default timestamp: production passes
// no Now, so the backup name must come from the real clock.
func TestApplyNilNowUsesClock(t *testing.T) {
	p := setup(t, applyBase)
	o := applyOpts("alpha")
	o.Now = nil
	bak, err := Apply(p, replace(`"m/b"`, `"m/a"`), o)
	rp, _ := filepath.EvalSymlinks(p)
	if err != nil || !strings.HasPrefix(bak, rp+".bak-") {
		t.Fatalf("bak=%q err=%v", bak, err)
	}
}

// TestApplyRefusesDanglingSymlink checks that a link whose target is gone
// is reported, not silently replaced by a fresh file that would strand
// whatever the link was meant to become.
func TestApplyRefusesDanglingSymlink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	link := filepath.Join(dir, "slot-profiles.toml")
	if err := os.Symlink(filepath.Join(dir, "missing.toml"), link); err != nil {
		t.Fatal(err)
	}
	_, err := Apply(link, replace("x", "y"), applyOpts(""))
	if err == nil || !strings.Contains(err.Error(), "dangling") {
		t.Fatalf("err = %v", err)
	}
	if st, serr := os.Lstat(link); serr != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v %v", st, serr)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("dir has %d entries, want only the link", len(ents))
	}
}
