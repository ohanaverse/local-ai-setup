// Pointers for commands that moved to wt-model.
//
// A command that moved is not simply deleted: cobra prints the parent's help
// and exits 0 for an unknown name, so a script running the old
// `model add … && …` under wt would "succeed" having changed nothing. Each
// moved command is instead a hidden stub that prints one line and exits 1:
//
//	wt: <old> moved to wt-model — run 'wt-model <new>'; nothing was changed
//
// <old> is the command as typed after "wt " (no "wt " prefix), <new> the
// wt-model command that replaces it. Only this binary (wt / wt-session)
// registers the stubs; wt-model has nothing to point at.
package main

import (
	"errors"
	"strings"

	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/spf13/cobra"
)

// removedCmdAnnotation marks a hidden stub that only reports a removed
// command; its value names the issue that removed it, the date it was
// retired, or movedAnnotation. TestNoWtCommandIsHidden skips a command
// carrying it.
const removedCmdAnnotation = "wt-removed"

// movedAnnotation is the removedCmdAnnotation value every moved-command stub
// carries.
const movedAnnotation = "moved to wt-model"

// movedCommand is one command that moved: Old is the wt command ("model add",
// under wt), New the wt-model command that replaces it ("model add", under
// wt-model).
type movedCommand struct{ Old, New string }

// movedCommands is every command registered as a pointer stub.
var movedCommands = []movedCommand{
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

// movedError is the one wording of a command that moved to wt-model: exit 1, one line, nothing changed.
// old is the wt command as typed (after "wt "), new the wt-model command that replaces it.
func movedError(old, new string) error {
	return exitcode.New(1, errors.New(old+" moved to wt-model — run 'wt-model "+new+"'; nothing was changed"), false)
}

// movedStub is the hidden stub for m, named name (the last word of m.Old).
// Flag parsing is off and any arguments are accepted, so a stray flag or
// argument reaches the same pointer instead of a usage error.
func movedStub(name string, m movedCommand) *cobra.Command {
	return &cobra.Command{
		Use: name, Hidden: true, DisableFlagParsing: true, SilenceUsage: true,
		Annotations: map[string]string{removedCmdAnnotation: movedAnnotation},
		Args:        cobra.ArbitraryArgs,
		RunE: func(*cobra.Command, []string) error {
			return movedError(m.Old, m.New)
		},
	}
}

// addMovedStubs registers a stub under root for every row of table. The words
// of Old before the last are a path of exact command names from root; a
// missing one is added as a plain (not hidden) group. A row whose last word is
// still registered under that parent panics: a stub must never shadow a live
// command.
func addMovedStubs(root *cobra.Command, table []movedCommand) {
	for _, m := range table {
		words := strings.Fields(m.Old)
		if len(words) == 0 {
			panic("moved command row has an empty Old")
		}
		parent := root
		for _, w := range words[:len(words)-1] {
			next := childNamed(parent, w)
			if next == nil {
				next = &cobra.Command{Use: w}
				parent.AddCommand(next)
			}
			parent = next
		}
		name := words[len(words)-1]
		if childNamed(parent, name) != nil {
			panic("moved command " + m.Old + " is still registered")
		}
		parent.AddCommand(movedStub(name, m))
	}
}

// childNamed is parent's direct subcommand answering to name (its own name or
// an alias, the way cobra's Find resolves it), or nil.
func childNamed(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return c
		}
	}
	return nil
}
