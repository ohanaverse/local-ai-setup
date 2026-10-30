package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"
)

// slotsFixture points XDG_CONFIG_HOME at a scratch dir and returns an app
// over the slot fixture registry, with the app's slot state read from disk.
func slotsFixture(t *testing.T, initial string) *app {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if initial != "" {
		if err := os.MkdirAll(filepath.Dir(profiles.SlotPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profiles.SlotPath(), []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &app{cfg: slotFixtureConfig()}
}

func readSlotFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(profiles.SlotPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const slotsBase = `[[profile]]
name = "base"
agent = "claude"
default = "sonnet"

[profile.slots]
sonnet = "openrouter/a"
haiku = "openrouter/b"
`

func TestSlotsAddWritesProfile(t *testing.T) {
	a := slotsFixture(t, "")
	out, err := runProfileCmd(t, a, "slots", "add", "mine", "--slot", "sonnet=openrouter/a", "--slot", "haiku=omlx/q", "--env", "FOO=bar", "--comment", "my note")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if !strings.Contains(out, "created") || !strings.Contains(out, "claude: profile/mine ok") {
		t.Errorf("output = %q", out)
	}
	st, err := profiles.LoadSlots()
	if err != nil || len(st.Profiles) != 1 {
		t.Fatalf("LoadSlots = %+v, %v", st, err)
	}
	p := st.Profiles[0]
	if p.Agent != "claude" || p.Default != "sonnet" || p.Slots["haiku"] != "omlx/q" || p.Env["FOO"] != "bar" {
		t.Errorf("profile = %+v", p)
	}
	if !strings.Contains(readSlotFile(t), "# my note") {
		t.Error("comment not written")
	}
}

func TestSlotsAddPrintsBackupPath(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	out, err := runProfileCmd(t, a, "slots", "add", "two", "--slot", "sonnet=openrouter/a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "backup: ") || !strings.Contains(out, ".bak-") {
		t.Errorf("output = %q", out)
	}
	if !strings.Contains(out, "profile/base ok") || !strings.Contains(out, "profile/two ok") {
		t.Errorf("list missing: %q", out)
	}
}

func TestSlotsAddRejectsUnknownModel(t *testing.T) {
	a := slotsFixture(t, "")
	_, err := runProfileCmd(t, a, "slots", "add", "mine", "--slot", "sonnet=nope/x")
	if err == nil || !strings.Contains(err.Error(), "nope/x") {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(profiles.SlotPath()); serr == nil {
		t.Error("file was created")
	}
}

func TestSlotsAddRejectsBadName(t *testing.T) {
	a := slotsFixture(t, "")
	for _, n := range []string{"Bad", "-x", "a b", "a/b"} {
		if _, err := runProfileCmd(t, a, "slots", "add", n, "--slot", "sonnet=openrouter/a"); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestSlotsAddRejectsBadSlotFlags(t *testing.T) {
	a := slotsFixture(t, "")
	for _, args := range [][]string{
		{"--slot", "bogus=openrouter/a"},
		{"--slot", "sonnet"},
		{"--slot", "sonnet=openrouter/a", "--env", "NOEQUALS"},
		{"--slot", "opus=openrouter/a"}, // no sonnet slot, default sonnet
	} {
		if _, err := runProfileCmd(t, a, append([]string{"slots", "add", "mine"}, args...)...); err == nil {
			t.Errorf("args %v accepted", args)
		}
	}
}

func TestSlotsNoConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := runProfileCmd(t, &app{}, "slots", "add", "x", "--slot", "sonnet=a"); err == nil {
		t.Error("nil cfg accepted")
	}
}

func TestSlotsSetChangesOneSlot(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	out, err := runProfileCmd(t, a, "slots", "set", "base", "--slot", "haiku=omlx/q")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	st, _ := profiles.LoadSlots()
	p := st.Profiles[0]
	if p.Slots["haiku"] != "omlx/q" || p.Slots["sonnet"] != "openrouter/a" {
		t.Errorf("slots = %v", p.Slots)
	}
	if !strings.Contains(out, "backup: ") || !strings.Contains(out, "profile/base ok") {
		t.Errorf("output = %q", out)
	}
}

func TestSlotsSetUnsetEnv(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "set", "base", "--env", "A=1", "--env", "B=2"); err != nil {
		t.Fatal(err)
	}
	if _, err := runProfileCmd(t, a, "slots", "set", "base", "--unset-env", "A"); err != nil {
		t.Fatal(err)
	}
	st, _ := profiles.LoadSlots()
	env := st.Profiles[0].Env
	if _, ok := env["A"]; ok || env["B"] != "2" {
		t.Errorf("env = %v", env)
	}
}

func TestSlotsSetDefault(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "set", "base", "--default", "haiku"); err != nil {
		t.Fatal(err)
	}
	st, _ := profiles.LoadSlots()
	if st.Profiles[0].Default != "haiku" {
		t.Errorf("default = %q", st.Profiles[0].Default)
	}
}

func TestSlotsSetUnknownName(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "set", "nope", "--default", "haiku"); err == nil {
		t.Error("unknown name accepted")
	}
}

func TestSlotsRmRemovesEntryKeepsOthers(t *testing.T) {
	a := slotsFixture(t, slotsBase+"\n[[profile]]\nname = \"other\"\nagent = \"claude\"\ndefault = \"sonnet\"\n\n[profile.slots]\nsonnet = \"openrouter/b\"\n")
	out, err := runProfileCmd(t, a, "slots", "rm", "base")
	if err != nil {
		t.Fatalf("rm: %v\n%s", err, out)
	}
	st, _ := profiles.LoadSlots()
	if len(st.Profiles) != 1 || st.Profiles[0].Name != "other" {
		t.Errorf("profiles = %+v", st.Profiles)
	}
	if !strings.Contains(out, "backup: ") || strings.Contains(out, "profile/base") || !strings.Contains(out, "profile/other ok") {
		t.Errorf("output = %q", out)
	}
}

func TestSlotsRmUnknownName(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	if _, err := runProfileCmd(t, a, "slots", "rm", "nope"); err == nil {
		t.Error("unknown name accepted")
	}
	if readSlotFile(t) != slotsBase {
		t.Error("file changed")
	}
}

func TestSlotsWriteRefusedLeavesFileUnchanged(t *testing.T) {
	a := slotsFixture(t, slotsBase)
	_, err := runProfileCmd(t, a, "slots", "set", "base", "--slot", "haiku=nope/x")
	if err == nil {
		t.Fatal("refused write returned nil")
	}
	if readSlotFile(t) != slotsBase {
		t.Error("file changed")
	}
	if m, _ := filepath.Glob(profiles.SlotPath() + ".bak-*"); len(m) != 0 {
		t.Errorf("backup written: %v", m)
	}
}
