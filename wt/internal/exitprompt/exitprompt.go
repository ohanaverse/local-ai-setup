// Package exitprompt holds what the post-exit worktree prompt needs to know
// about the launch: which branch and worktree this process created, and the
// tip the branch had then. The record lives in memory only; nothing is
// written to disk to remember it.
package exitprompt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
)

// Created is a worktree and branch this launch created. Branch is the short
// name; Tip is the full sha the branch pointed at when it was created.
type Created struct {
	Path, Branch, Tip string
}

var (
	mu       sync.Mutex
	recorded Created
	have     bool
)

// Record notes that this launch created branch in the worktree at path. It
// reads the worktree's HEAD at once, so the tip is the creation tip. The
// latest call wins. On error nothing is recorded.
func Record(path, branch string) error {
	out, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("read the tip of %s: %w", path, err)
	}
	mu.Lock()
	defer mu.Unlock()
	recorded = Created{Path: path, Branch: branch, Tip: strings.TrimSpace(string(out))}
	have = true
	return nil
}

// Recorded returns what Record stored, and false when nothing was.
func Recorded() (Created, bool) {
	mu.Lock()
	defer mu.Unlock()
	return recorded, have
}

// Launched tells the prompt the agent ran in dir (empty means the current
// directory). A record whose worktree is neither dir nor an ancestor of it is
// dropped: the launch did not create and use that worktree, so it is never
// offered. Call it where the agent is started.
func Launched(dir string) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			Reset()
			return
		}
		dir = wd
	}
	mu.Lock()
	defer mu.Unlock()
	if !have {
		return
	}
	if !cleanup.Within(cleanup.ResolvePath(dir), cleanup.ResolvePath(recorded.Path)) {
		recorded, have = Created{}, false
	}
}

// Reset forgets the record (tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	recorded, have = Created{}, false
}

// Env is what Offer needs from the caller, which owns the registry and the
// session claim: Inputs builds the gather inputs for cwd (fetch off, the
// protected artifacts set), Procs finds live processes in a worktree, Now is
// the clock (nil means time.Now), Flush (may be nil) discards typeahead from
// the terminal just before the question, and Keep is the [launch] exit_prompt =
// "keep" setting.
type Env struct {
	Inputs func(cwd string) (cleanup.GatherInputs, error)
	Procs  removal.ProcFinder
	Now    func() time.Time
	Flush  func()
	Keep   bool
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Eligible reports whether the previewed worktree is one this launch created
// and nothing touched, so removing it with its branch loses nothing. why
// names the first failing condition ("" when ok). It is a pure function of
// the engine's preview and the creation record.
func Eligible(p removal.ItemPreview, c Created) (ok bool, why string) {
	f := p.Fresh
	switch {
	case p.Approval == removal.ApprovalRefused:
		return false, "the engine refuses it: " + p.Reason
	case f.Status == "":
		return false, "no re-check of the worktree"
	case f.Ref != "refs/heads/"+c.Branch || f.Branch != c.Branch:
		return false, "the worktree is no longer on branch " + c.Branch
	case f.Tip == "" || f.Tip != c.Tip:
		return false, "the branch tip moved since the worktree was created"
	case len(p.LossLines) > 0 || f.Losses != (cleanup.Losses{}):
		return false, "removing it would lose something"
	case len(p.Procs) > 0:
		return false, "a process is running in it"
	case procCheckFailed(p):
		return false, "running processes could not be checked"
	case len(f.Blockers) > 0 || f.Status == cleanup.StatusInUse:
		return false, "it is in use"
	case f.Unknown:
		return false, "something about it could not be read"
	case f.Status != cleanup.StatusLessSure && f.Status != cleanup.StatusProven:
		return false, "its status is " + string(f.Status)
	}
	for _, r := range f.Reasons {
		if r.Code != cleanup.ReasonFreshBranch && r.Code != cleanup.ReasonBaseIsLocal {
			return false, "a reason beyond a fresh branch: " + r.Code
		}
	}
	if f.Evidence != cleanup.EvSameCommit && f.Evidence != cleanup.EvAncestor {
		return false, "the branch is not at or behind the base (" + string(f.Evidence) + ")"
	}
	// A session longer than the min-age floor reads proven, and the engine
	// then deletes the branch with a plain confirmation; a younger one is
	// less sure and needs the branch-delete opt-in.
	if f.Status == cleanup.StatusProven {
		if p.Plan.Branch != removal.BrDeleted {
			return false, "its branch would be kept"
		}
		return true, ""
	}
	if !p.CanDeleteBranch {
		return false, "its branch cannot be deleted"
	}
	return true, ""
}

// procCheckFailed reports whether the preview carries the engine's note that
// the process lookup could not run, which leaves "no process" unverified.
func procCheckFailed(p removal.ItemPreview) bool {
	for _, w := range p.Warnings {
		if strings.HasPrefix(w, "could not check for running processes") {
			return true
		}
	}
	return false
}

// Offer asks whether to remove the worktree and branch this launch created.
// It returns at once when nothing was recorded, for Keep and without a
// terminal. Eligibility is Eligible over the engine's own preview;
// "y" removes through the engine with its rescan, compare-and-swap and hard
// blockers. It never returns an error: a failed check prints one note and
// keeps the worktree.
func Offer(env Env, in io.Reader, out io.Writer, isTTY bool) {
	c, ok := Recorded()
	if !ok || env.Keep || !isTTY {
		return
	}
	note := func(cause error) {
		fmt.Fprintf(out, "wt: note: could not check %s for removal (%s); kept\n", c.Path, cleanup.OneLine(cause.Error()))
	}
	defer func() {
		if r := recover(); r != nil {
			note(fmt.Errorf("internal error: %v", r))
		}
	}()
	cwd, err := os.Getwd()
	if err != nil {
		note(err)
		return
	}
	if env.Inputs == nil {
		note(errors.New("no gather inputs"))
		return
	}
	gin, err := env.Inputs(cwd)
	if err != nil {
		note(err)
		return
	}
	gin.Fetch = false
	snap, err := cleanup.Gather(cleanup.Git{Dir: cwd}, gin)
	if err != nil {
		note(err)
		return
	}
	copts := cleanup.Options{Now: env.now()}
	report := cleanup.Classify(snap, copts)
	want := cleanup.ResolvePath(c.Path)
	var row *cleanup.Row
	for i, r := range report.Rows {
		if r.Kind == cleanup.KindWorktree && r.Path != "" && cleanup.ResolvePath(r.Path) == want {
			row = &report.Rows[i]
			break
		}
	}
	if row == nil {
		note(errors.New("no such worktree in the repository"))
		return
	}
	eng := removal.Engine{G: cleanup.Git{Dir: cwd}, Prior: snap, In: gin, Classify: copts,
		Opts: removal.Options{Now: env.now, Procs: env.Procs}}
	previews := eng.Preview([]removal.Item{removal.ItemFromRow(*row)})
	if len(previews) != 1 {
		note(errors.New("no preview"))
		return
	}
	p := previews[0]
	if ok, _ := Eligible(p, c); !ok {
		return
	}
	if env.Flush != nil {
		env.Flush()
	}
	if !rootclaim.AskYesNo(fmt.Sprintf("%s was created for this session and nothing changed in it. Remove it and its branch %s?", c.Path, c.Branch), in, out) {
		return
	}
	var item removal.Item
	if p.Fresh.Status == cleanup.StatusProven {
		item, err = p.Confirmed(p.Needs)
	} else {
		item, err = p.ConfirmedDeletingBranch(removal.Needs{Confirm: true, Delete: true})
	}
	if err != nil {
		fmt.Fprintf(out, "wt: could not remove %s (%s); kept\n", c.Path, cleanup.OneLine(err.Error()))
		return
	}
	for _, o := range eng.Run([]removal.Item{item}, nil) {
		fmt.Fprintln(out, removal.Headline(o))
		if o.Reason != "" && o.Worktree != removal.WtRemoved {
			fmt.Fprintln(out, "reason: "+o.Reason)
		}
		for _, l := range removal.DetailLines(o) {
			fmt.Fprintln(out, l)
		}
	}
}
