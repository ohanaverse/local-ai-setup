package rootmove

import (
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// excludeRemedy is the one line that makes a repository ignore .worktrees/
// locally, without touching a tracked .gitignore.
const excludeRemedy = `echo '.worktrees/' >> "$(git rev-parse --git-path info/exclude)"`

func refuse(code, format string, a ...any) *rescue.Refusal {
	return &rescue.Refusal{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Preflight decides whether the root (Rescue.Prior.Repo) can move into a
// linked worktree named dirName ("" for the root's branch) and says exactly
// what would happen. It reads only, creates nothing and never fetches; every
// refusal is made here, before anything exists. A refusal is a
// *rescue.Refusal; any other error is a failure to read (the caller refuses
// to go on either way).
//
// The order: the primary checkout (bare or unreadable: a read failure; no
// commit yet: no_commit); detached_root; no_default_branch (none resolves, or
// it exists only as origin/<d>); already_default; default_in_worktree;
// root_held (a live wt session of another process in the root);
// worktrees_not_ignored; hidden_index_flags (assume-unchanged or
// skip-worktree, a sparse checkout included); rescue's root-mode Preflight
// with its own refusals; ignored_collision (an ignored file the reset would
// destroy, removal.RootIgnoredAtRisk, or one at a path the default branch
// tracks, which the switch would overwrite). DefaultFF is then read and
// never refuses.
func (e Engine) Preflight(dirName string) (*Plan, error) {
	root := e.Rescue.Prior.Repo
	if root == "" {
		return nil, errors.New("the repository was not read (no primary checkout in the snapshot)")
	}
	rg := e.gitAt(root)

	facts, err := cleanup.ListWorktrees(rg, root)
	if err != nil {
		return nil, fmt.Errorf("could not list the worktrees: %w", err)
	}
	prim := facts[0]
	if !prim.Primary || prim.Bare {
		return nil, errors.New("the repository is bare: it has no root checkout to move")
	}
	if cleanup.ResolvePath(prim.Path) != cleanup.ResolvePath(root) {
		return nil, fmt.Errorf("the primary checkout is %s, not %s as read before", cleanup.OneLine(prim.Path), cleanup.OneLine(root))
	}
	if prim.Head == "" || strings.Trim(prim.Head, "0") == "" {
		return nil, refuse(CodeNoCommit, "the root has no commit yet, so there is nothing to move; commit first")
	}
	if !cleanup.IsFullCommitID(prim.Head) {
		return nil, fmt.Errorf("the root's HEAD %q is not a commit id", cleanup.OneLine(prim.Head))
	}
	branch, onBranch := strings.CutPrefix(prim.Branch, "refs/heads/")
	if prim.Detached || !onBranch || branch == "" {
		return nil, refuse(CodeDetachedRoot, "the root is on a detached HEAD, so there is no branch to move; give it one first (git switch -c <name>)")
	}

	base, err := cleanup.ResolveBase(rg)
	if errors.Is(err, cleanup.ErrNoDefaultBranch) {
		return nil, refuse(CodeNoDefaultBranch, "no default branch could be found (no origin/HEAD, main, master, origin/main or origin/master), so the root has nothing to go back to")
	} else if err != nil {
		return nil, fmt.Errorf("could not resolve the default branch: %w", err)
	}
	def := base.Default
	if ok, err := hasCommit(rg, "refs/heads/"+def); err != nil {
		return nil, err
	} else if !ok {
		return nil, refuse(CodeNoDefaultBranch, "the default branch %s exists only as origin/%s; create it locally first (git branch %s origin/%s)",
			cleanup.OneLine(def), cleanup.OneLine(def), cleanup.OneLine(def), cleanup.OneLine(def))
	}
	if branch == def {
		return nil, refuse(CodeAlreadyDefault, "the root is already on the default branch %s; there is nothing to move", cleanup.OneLine(def))
	}
	for _, w := range facts[1:] {
		if !w.Primary && w.Branch == "refs/heads/"+def {
			return nil, refuse(CodeDefaultInWorktree, "the default branch %s is checked out in %s, so the root cannot switch to it; switch that worktree away from it first",
				cleanup.OneLine(def), cleanup.OneLine(w.Path))
		}
	}

	if held, err := e.heldBy(rg, root); err != nil {
		return nil, err
	} else if held != "" {
		return nil, refuse(CodeRootHeld, "another wt session is running in the root: %s; end it first (Execute checks again just before the root changes)", held)
	}

	switch o, err := rg.Run("check-ignore", "-q", ".worktrees/"); {
	case err != nil:
		return nil, fmt.Errorf("could not check whether .worktrees/ is ignored: %w", err)
	case o.Code == 1:
		return nil, refuse(CodeWorktreesNotIgnored, "the root does not ignore .worktrees/, so the new worktree would show as untracked work in the root (and existing ones as nested repositories); ignore it locally first: %s", excludeRemedy)
	case o.Code != 0:
		return nil, fmt.Errorf("git check-ignore: exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}

	if hidden, err := cleanup.HiddenIndexFlags(rg); err != nil {
		return nil, fmt.Errorf("could not read the root's index flags: %w", err)
	} else if hidden {
		return nil, refuse(CodeHiddenIndex, "an index entry of the root is marked assume-unchanged or skip-worktree, so an edit to it is invisible to git and could not be carried (a sparse checkout is not supported); clear it first: an assume-unchanged entry with git update-index --no-assume-unchanged <path>, a sparse checkout with git sparse-checkout disable (a skip-worktree entry outside one with git update-index --no-skip-worktree <path>)")
	}

	r := e.Rescue
	r.ForRoot = true
	rp, err := r.Preflight(cleanup.Row{Kind: cleanup.KindWorktree, Path: prim.Path, Ref: prim.Branch}, dirName)
	if err != nil {
		return nil, err
	}
	if rp.Branch != branch || rp.Source.Head != prim.Head {
		return nil, refuse(rescue.CodeSourceChanged, "the root's branch or HEAD moved while it was being checked; run it again")
	}

	if why, err := removal.RootIgnoredAtRisk(rg, root); err != nil {
		return nil, fmt.Errorf("could not check the root's ignored files: %w", err)
	} else if why != "" {
		return nil, refuse(CodeIgnoredCollision, "%s; move it out of the root first", why)
	}
	if hit, err := ignoredDefaultTracks(rg, def); err != nil {
		return nil, fmt.Errorf("could not check the root's ignored files against %s: %w", cleanup.OneLine(def), err)
	} else if hit != "" {
		return nil, refuse(CodeIgnoredCollision, "the ignored %s is where the default branch %s tracks a file, so switching the root to it would overwrite it; move it out of the root first",
			cleanup.OneLine(hit), cleanup.OneLine(def))
	}

	ff, note, benign := defaultFF(rg, def)
	return &Plan{
		Repo:      rp.Repo,
		Branch:    rp.Branch,
		Default:   def,
		Head:      rp.Source.Head,
		DirName:   rp.DirName,
		DestPath:  rp.DestPath,
		DefaultFF: ff,
		FFNote:    note,
		FFBenign:  benign,
		Counts:    rp.Counts,
		Warnings:  slices.Clone(rp.Warnings),
		rp:        rp,
	}, nil
}

// heldBy describes the live wt sessions of other processes whose claim is on
// root ("" none). It reads the claims without the lock and prunes nothing; a
// stale claim (its process gone or restarted) does not hold the root. A
// claims directory that cannot be read is an error.
func (e Engine) heldBy(rg cleanup.Git, root string) (string, error) {
	common, err := gitText(rg, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("could not read the repository's common directory: %w", err)
	}
	others, err := sessionclaim.Others(common, sessionclaim.Claim{PID: os.Getpid(), Path: cleanup.ResolvePath(root)})
	if err != nil {
		return "", fmt.Errorf("could not read the root's session claims: %w", err)
	}
	now := time.Now()
	var names []string
	for _, o := range others {
		names = append(names, cleanup.OneLine(sessionclaim.Describe(o, now)))
	}
	return strings.Join(names, "; "), nil
}

// ignoredDefaultTracks returns an ignored file of the root at a path the
// default branch tracks, or one that sits where that branch needs a
// directory or a file ("" none): `git switch --no-overwrite-ignore` would
// fail on it rather than overwrite it, but only after the root was reset.
// Names are compared case-folded unless core.ignorecase is clearly false.
func ignoredDefaultTracks(rg cleanup.Git, def string) (string, error) {
	ignored, err := gitZ(rg, "ls-files", "-z", "--ignored", "--exclude-standard", "--others")
	if err != nil {
		return "", err
	}
	if len(ignored) == 0 {
		return "", nil
	}
	tracked, err := gitZ(rg, "ls-tree", "-r", "--name-only", "-z", "--full-tree", "refs/heads/"+def)
	if err != nil {
		return "", err
	}
	fold := foldOf(rg)
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, p := range tracked {
		files[fold(p)] = true
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			dirs[fold(d)] = true
		}
	}
	for _, ig := range ignored {
		k := fold(ig)
		if files[k] || dirs[k] {
			return ig, nil
		}
		for d := path.Dir(ig); d != "."; d = path.Dir(d) {
			if files[fold(d)] {
				return ig, nil
			}
		}
	}
	return "", nil
}

// foldOf is how the root's git compares path names: unchanged when
// core.ignorecase is false or unset, else case-folded (also when the setting
// cannot be read: folding only finds more collisions).
func foldOf(rg cleanup.Git) func(string) string {
	if o, err := rg.Run("config", "--get", "--type=bool", "core.ignorecase"); err == nil {
		v := strings.TrimSpace(o.Stdout)
		if (o.Code == 0 && v == "false") || (o.Code == 1 && v == "") {
			return func(s string) string { return s }
		}
	}
	return func(s string) string { return strings.ToLower(strings.ToUpper(s)) }
}

// defaultFF says whether origin/<def> is a strict descendant of the local
// def, so a fast-forward would move def forward without a merge; when it is
// not, the note says why. It never refuses: a failed read is "not known".
func defaultFF(rg cleanup.Git, def string) (ok bool, note string, benign bool) {
	d := cleanup.OneLine(def)
	local, err := gitText(rg, "rev-parse", "--verify", "--quiet", "refs/heads/"+def+"^{commit}")
	if err != nil {
		return false, fmt.Sprintf("could not read %s (%v), so it is not fast-forwarded", d, err), false
	}
	o, err := rg.Run("rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+def+"^{commit}")
	switch {
	case err != nil:
		return false, fmt.Sprintf("could not read origin/%s (%v), so %s is not fast-forwarded", d, err, d), false
	case o.Code == 1:
		return false, fmt.Sprintf("origin/%s does not exist, so there is nothing to fast-forward %s to", d, d), false
	case o.Code != 0:
		return false, fmt.Sprintf("could not read origin/%s (exit %d), so %s is not fast-forwarded", d, o.Code, d), false
	}
	remote := strings.TrimSpace(o.Stdout)
	if local == remote {
		return false, fmt.Sprintf("%s is already at origin/%s", d, d), true
	}
	isAncestor := func(a, b string) (bool, error) {
		o, err := rg.Run("merge-base", "--is-ancestor", a, b)
		if err != nil {
			return false, err
		}
		switch o.Code {
		case 0:
			return true, nil
		case 1:
			return false, nil
		}
		return false, fmt.Errorf("git merge-base: exit %d: %s", o.Code, cleanup.OneLine(o.Stderr))
	}
	behind, err := isAncestor(local, remote)
	if err != nil {
		return false, fmt.Sprintf("could not compare %s with origin/%s (%v), so it is not fast-forwarded", d, d, err), false
	}
	if behind {
		return true, "", false
	}
	ahead, err := isAncestor(remote, local)
	switch {
	case err != nil:
		return false, fmt.Sprintf("could not compare %s with origin/%s (%v), so it is not fast-forwarded", d, d, err), false
	case ahead:
		return false, fmt.Sprintf("%s is ahead of origin/%s (it has commits origin does not), so it is not fast-forwarded", d, d), false
	}
	return false, fmt.Sprintf("%s and origin/%s have diverged; no merge is made, so it is not fast-forwarded", d, d), false
}

// hasCommit says whether ref resolves to a commit; a git failure other than
// "absent" is an error.
func hasCommit(rg cleanup.Git, ref string) (bool, error) {
	o, err := rg.Run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return false, err
	}
	switch o.Code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fmt.Errorf("git rev-parse %s: exit %d: %s", cleanup.OneLine(ref), o.Code, cleanup.OneLine(o.Stderr))
}

// gitText runs a read and returns its trimmed output; a non-zero exit is an
// error.
func gitText(rg cleanup.Git, args ...string) (string, error) {
	o, err := rg.Run(args...)
	if err != nil {
		return "", err
	}
	if o.Code != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, cleanup.OneLine(o.Stderr))
	}
	return strings.TrimSpace(o.Stdout), nil
}

// gitZ runs a read and splits its NUL-separated output.
func gitZ(rg cleanup.Git, args ...string) ([]string, error) {
	o, err := rg.Run(args...)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, fmt.Errorf("git %s: exit %d: %s", args[0], o.Code, cleanup.OneLine(o.Stderr))
	}
	out := strings.Split(o.Stdout, "\x00")
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out, nil
}
