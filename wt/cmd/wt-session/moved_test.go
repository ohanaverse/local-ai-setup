package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/spf13/cobra"
)

// TestMovedErrorIsOneLineExitOne pins the pointer for a command that moved to
// wt-model: main prints exactly one "wt: ..." line naming the new command and
// saying nothing was changed, and exits 1.
func TestMovedErrorIsOneLineExitOne(t *testing.T) {
	err := movedError("model", "model")
	var buf bytes.Buffer
	code := exitcode.Report("wt", err, &buf)
	if code != 1 || buf.String() != "wt: model moved to wt-model — run 'wt-model model'; nothing was changed\n" {
		t.Fatalf("code %d, %q", code, buf.String())
	}
}

// conditionalMoves are pointers that are not stubs from movedCommands: a live
// command that points at wt-model only in some shape (bare `wt model`, and
// `wt model list` without --server, `wt stop` with a provider name or --all).
var conditionalMoves = []struct {
	args     []string
	old, new string
}{
	{[]string{"model"}, "model", "model"},
	{[]string{"model", "list"}, "model list", "model list"},
	{[]string{"model", "list", "--json"}, "model list", "model list"},
	{[]string{"stop", "ollama"}, "stop ollama", "stop ollama"},
	{[]string{"stop", "--all"}, "stop --all", "stop --all"},
	{[]string{"stop", "--all", "--yes"}, "stop --all", "stop --all"},
}

// wantMoved is every command that must be a moved stub, written out by hand
// rather than read from movedCommands: deleting a production row then fails
// the tests below instead of deleting the test row with it.
var wantMoved = []movedCommand{
	{"model init", "model init"},
	{"model add", "model add"},
	{"model edit", "model edit"},
	{"model rm", "model rm"},
	{"served", "served"},
	{"warm", "warm"},
	{"cloud-sync", "cloud-sync"},
	{"litellm list", "litellm list"},
	{"litellm providers", "litellm providers"},
	{"litellm sync", "litellm sync"},
	{"litellm restart", "litellm restart"},
}

// explicitMoves are argument rows written out by hand (not derived from
// movedCommands) that must reach a pointer, with real-looking arguments.
var explicitMoves = []struct {
	args     []string
	old, new string
}{
	{[]string{"served", "omlx", "--json", "x"}, "served", "served"},
	{[]string{"served", "omlx"}, "served", "served"},
	{[]string{"warm", "omlx", "m"}, "warm", "warm"},
	{[]string{"model", "add", "omlx", "m", "--family", "f"}, "model add", "model add"},
	// cloud-sync's own flags reach the pointer unparsed: a reviewed
	// `--approve-removals` run changes nothing under wt.
	{[]string{"cloud-sync", "--only", "ollama", "--yes", "--approve-removals", "0bcc560b956e"}, "cloud-sync", "cloud-sync"},
	// litellm list|providers sit under a live parent with no Run: without the
	// stub, a bare `wt litellm list` would print the litellm help and exit 0.
	{[]string{"litellm", "list"}, "litellm list", "litellm list"},
	{[]string{"litellm", "list", "--json"}, "litellm list", "litellm list"},
	{[]string{"litellm", "providers", "--json"}, "litellm providers", "litellm providers"},
	// sync's own flags reach the pointer unparsed: a scripted
	// `wt litellm sync --dry-run --json` gets the pointer, not a plan.
	{[]string{"litellm", "sync"}, "litellm sync", "litellm sync"},
	{[]string{"litellm", "sync", "--dry-run", "--json"}, "litellm sync", "litellm sync"},
	// An old bin/update/litellm runs a bare `wt litellm restart`: it must
	// fail with the pointer, never restart the proxy from a session.
	{[]string{"litellm", "restart"}, "litellm restart", "litellm restart"},
}

// TestMovedCommandsPointAtWtModel is the one table test of every moved-command
// pointer: through the real tree each prints exactly the one pointer line,
// exits 1 and leaves registry.toml and config.toml byte-for-byte unchanged.
// Table rows get "--json x" after the old words, proving flag parsing is off.
func TestMovedCommandsPointAtWtModel(t *testing.T) {
	type row struct {
		args     []string
		old, new string
	}
	var rows []row
	for _, m := range movedCommands {
		rows = append(rows, row{append(strings.Fields(m.Old), "--json", "x"), m.Old, m.New})
	}
	for _, c := range conditionalMoves {
		rows = append(rows, row{c.args, c.old, c.new})
	}
	for _, c := range explicitMoves {
		rows = append(rows, row{c.args, c.old, c.new})
	}
	if len(rows) == 0 {
		t.Fatal("no moved command to check; the test is vacuous")
	}
	for _, r := range rows {
		t.Run(r.old, func(t *testing.T) {
			registry := modelHome(t, writeRegistry)
			cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt", "config.toml")
			if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte("default_tag = \"code\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			regBefore, cfgBefore := mustRead(t, registry), mustRead(t, cfg)
			out, code := runRoot(t, r.args...)
			want := "wt: " + r.old + " moved to wt-model — run 'wt-model " + r.new + "'; nothing was changed\n"
			if code != 1 || out != want {
				t.Fatalf("wt %v: code %d, output %q; want 1, %q", r.args, code, out, want)
			}
			if got := mustRead(t, registry); got != regBefore {
				t.Errorf("registry.toml changed:\n%s", got)
			}
			if got := mustRead(t, cfg); got != cfgBefore {
				t.Errorf("config.toml changed:\n%s", got)
			}
		})
	}
}

// TestMovedStubsAreHiddenAndAnnotated checks every movedCommands row, and
// every hand-written wantMoved row, is registered in the real tree as a
// hidden, flag-parsing-off stub carrying the moved annotation (which
// TestNoWtCommandIsHidden skips on), and that movedCommands holds each
// wantMoved row. An empty table fails: the test would check nothing.
func TestMovedStubsAreHiddenAndAnnotated(t *testing.T) {
	if len(movedCommands) == 0 {
		t.Fatal("movedCommands is empty; the test is vacuous")
	}
	for _, w := range wantMoved {
		found := false
		for _, m := range movedCommands {
			if m == w {
				found = true
			}
		}
		if !found {
			t.Errorf("movedCommands lacks %+v", w)
		}
	}
	root := rootCmdWith(testHooks())
	for _, m := range append(append([]movedCommand{}, movedCommands...), wantMoved...) {
		words := strings.Fields(m.Old)
		c, _, err := root.Find(words)
		if err != nil || c == nil || c.Name() != words[len(words)-1] {
			got := "<nil>"
			if c != nil {
				got = c.CommandPath()
			}
			t.Errorf("%s: not found (got %s, %v)", m.Old, got, err)
			continue
		}
		if !c.Hidden || !c.DisableFlagParsing || !c.SilenceUsage || c.Annotations[removedCmdAnnotation] != "moved to wt-model" {
			t.Errorf("%s: hidden=%v flags-off=%v silence=%v annotation=%q", m.Old, c.Hidden, c.DisableFlagParsing, c.SilenceUsage, c.Annotations[removedCmdAnnotation])
		}
	}
}

// TestAddMovedStubsFindsOrAddsTheParent pins addMovedStubs' placement: a stub
// goes under an existing parent, a missing parent is created (not hidden), and
// a row naming a live command panics so a stub never shadows one. Each placed
// stub then runs through the scratch root: a stray flag and arguments reach
// the one pointer line and exit 1, with no usage text.
func TestAddMovedStubsFindsOrAddsTheParent(t *testing.T) {
	isStub := func(c *cobra.Command) bool {
		return c != nil && c.Hidden && c.Annotations[removedCmdAnnotation] == movedAnnotation
	}
	child := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}
	root := &cobra.Command{Use: "wt", SilenceErrors: true}
	model := &cobra.Command{Use: "model"}
	list := &cobra.Command{Use: "list", Run: func(*cobra.Command, []string) {}}
	model.AddCommand(list)
	root.AddCommand(model)

	addMovedStubs(root, []movedCommand{{"model add", "model add"}, {"served", "served"}, {"group sub", "x"}})

	if got := child(model, "add"); !isStub(got) {
		t.Errorf("model add is not a hidden stub under the existing model: %v", got)
	}
	if got := child(model, "list"); got != list || isStub(got) {
		t.Errorf("model list was touched: %v", got)
	}
	if got := child(root, "served"); !isStub(got) {
		t.Errorf("served is not a hidden stub under root: %v", got)
	}
	group := child(root, "group")
	if group == nil || group.Hidden || isStub(group) {
		t.Fatalf("group was not created as a visible parent: %v", group)
	}
	if got := child(group, "sub"); !isStub(got) {
		t.Errorf("group sub is not a hidden stub: %v", got)
	}
	for _, c := range []*cobra.Command{child(model, "add"), child(root, "served"), child(group, "sub")} {
		if c != nil && (!c.DisableFlagParsing || !c.SilenceUsage) {
			t.Errorf("%s: flags-off=%v silence=%v", c.CommandPath(), c.DisableFlagParsing, c.SilenceUsage)
		}
	}

	for _, r := range []struct {
		args []string
		want string
	}{
		{[]string{"model", "add", "--json", "x"}, "wt: model add moved to wt-model — run 'wt-model model add'; nothing was changed\n"},
		{[]string{"group", "sub", "--bogus", "y"}, "wt: group sub moved to wt-model — run 'wt-model x'; nothing was changed\n"},
	} {
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(r.args)
		code := reportExit(root.Execute(), &buf)
		if code != 1 || buf.String() != r.want {
			t.Errorf("wt %v: code %d, output %q; want 1, %q", r.args, code, buf.String(), r.want)
		}
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("a row naming the live model list did not panic")
			}
		}()
		addMovedStubs(root, []movedCommand{{"model list", "model list"}})
	}()
}

// TestRemovedRotateExitsOneAndChangesNothing pins the stub `wt rotate` became
// when rotation was removed (2026-11-27): with no model server configured,
// `wt rotate code` and `wt rotate --json` each print exactly the one removal
// line and exit 1, open no picker, build no agent command (noServerHome's
// hooks fail the test on either), run no agent and leave the config home as
// it was. Without the stub the root's ArbitraryArgs would take "rotate code"
// as agent arguments and open the picker. The command is hidden and carries
// its removal date, so `wt --help` no longer lists it.
func TestRemovedRotateExitsOneAndChangesNothing(t *testing.T) {
	const want = "wt: rotate was removed: the model picker no longer rotates, it opens on what you launched last; nothing was changed"
	for _, args := range [][]string{{"rotate", "code"}, {"rotate", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home, marker, h := noServerHome(t)
			cfgDir := filepath.Join(home, ".config", "agent-wt")
			before := dirSnapshot(t, cfgDir)
			out, code := runRootWith(t, h, args...)
			if code != 1 {
				t.Errorf("exit = %d, want 1\n%s", code, out)
			}
			if lines := wtLines(out); len(lines) != 1 || lines[0] != want || strings.Count(out, "rotate was removed") != 1 {
				t.Errorf("output %q, want exactly the one line %q", out, want)
			}
			if fileExists(marker) {
				t.Error("an agent process ran")
			}
			if after := dirSnapshot(t, cfgDir); after != before {
				t.Errorf("the config home changed:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
	c, _, err := rootCmdWith(testHooks()).Find([]string{"rotate"})
	if err != nil || c == nil || c.Name() != "rotate" {
		t.Fatalf("rotate is not registered: %v, %v", c, err)
	}
	if !c.Hidden || !c.DisableFlagParsing || !c.SilenceUsage || c.Annotations[removedCmdAnnotation] != "2026-11-27 (rotation removed)" {
		t.Errorf("rotate: hidden=%v flags-off=%v silence=%v annotation=%q", c.Hidden, c.DisableFlagParsing, c.SilenceUsage, c.Annotations[removedCmdAnnotation])
	}
	help, err := runWT(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(help, "rotate") {
		t.Errorf("wt --help still names rotate:\n%s", help)
	}
}

// dirSnapshot is every file under dir with its contents, as one string, so a
// test can tell that a command wrote, removed or changed nothing there.
func dirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b.WriteString(p + "=" + string(data) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
