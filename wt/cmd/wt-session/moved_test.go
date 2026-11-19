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
// `wt model list` without --server). 4d-ii adds `stop`.
var conditionalMoves = []struct {
	args     []string
	old, new string
}{
	{[]string{"model"}, "model", "model"},
	{[]string{"model", "list"}, "model list", "model list"},
	{[]string{"model", "list", "--json"}, "model list", "model list"},
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

// TestMovedStubsAreHiddenAndAnnotated checks every movedCommands row is
// registered in the real tree as a hidden, flag-parsing-off stub carrying the
// moved annotation (which TestNoWtCommandIsHidden skips on). An empty table
// fails: the test would check nothing.
func TestMovedStubsAreHiddenAndAnnotated(t *testing.T) {
	if len(movedCommands) == 0 {
		t.Fatal("movedCommands is empty; the test is vacuous")
	}
	root := rootCmdWith(testHooks())
	for _, m := range movedCommands {
		words := strings.Fields(m.Old)
		c, _, err := root.Find(words)
		if err != nil || c == nil || c.Name() != words[len(words)-1] {
			t.Errorf("%s: not found (got %v, %v)", m.Old, c, err)
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
