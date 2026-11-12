package launchwarn

import (
	"fmt"
	"io"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
)

// Check shows a launch's warnings and says whether to go on. Every line it
// prints starts "wt: ". info is printed only when w is empty and creation
// is nil (the creation line is then the one informational line). A non-nil
// creation adds one line saying what the launch will create, before any
// prompt; on its own it never prompts.
//
// Without a terminal it prints and returns true, never reading in. On a
// terminal with warnings it asks one "[Y/n]" question (default yes) and
// returns the answer; with none it prints and returns true without reading.
func Check(in io.Reader, out io.Writer, isTTY bool, w []Warning, info string, creation *Creation) bool {
	if len(w) == 0 && creation == nil && info != "" {
		fmt.Fprintf(out, "wt: %s\n", info)
	}
	if creation != nil {
		fmt.Fprintf(out, "wt: %s\n", creationLine(creation))
	}
	for _, x := range w {
		fmt.Fprintf(out, "wt: %s\n", x.Text)
	}
	if len(w) == 0 || !isTTY {
		return true
	}
	fmt.Fprint(out, "wt: continue? [Y/n]: ")
	return rootclaim.AskContinue(in, out)
}

func creationLine(c *Creation) string {
	switch {
	case c.NewBranch && c.Tracks != "":
		return fmt.Sprintf("creating branch %s tracking %s and worktree %s", c.Branch, c.Tracks, c.Path)
	case c.NewBranch:
		return fmt.Sprintf("creating branch %s and worktree %s", c.Branch, c.Path)
	case c.Tracks != "":
		return fmt.Sprintf("creating worktree %s for branch %s tracking %s", c.Path, c.Branch, c.Tracks)
	}
	return fmt.Sprintf("creating worktree %s for branch %s", c.Path, c.Branch)
}
