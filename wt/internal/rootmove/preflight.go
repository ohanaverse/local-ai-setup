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
// it exists only as origin/<d>); already_default; default_in_worktree; the
// root's held read (refuses nothing, but its read failure preempts what
// follows); worktrees_not_ignored; hidden_index_flags (assume-unchanged or
// skip-worktree, a sparse checkout included); rescue's root-mode Preflight
// with its own refusals; ignored_collision (an ignored file the reset would
// destroy, removal.RootIgnoredAtRisk, or one at a path the default branch
// tracks, which the switch would overwrite). DefaultFF is then read and
// never refuses. A live wt session of another process in the root refuses
// nothing: it rides the plan as one HeldWarningPrefix warning and the plan
// proceeds (Execute checks the claims again just before the root changes).
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
	_, ok, err := resolveCommit(rg, "refs/heads/"+def)
	if err != nil {
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

	common, err := commonDir(rg)
	if err != nil {
		return nil, err
	}
	// Held is a label on the plan (one HeldWarningPrefix warning naming the
	// holder), never a refusal: the move proceeds and the user decides. The
	// line is the established held rendering after the prefix.
	var heldWarn string
	if held, err := e.heldBy(common, root); err != nil {
		return nil, err
	} else if held != "" {
		heldWarn = HeldWarningPrefix + fmt.Sprintf("another wt session is running in the root: %s; end it first (Execute checks again just before the root changes)", held)
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

	// core.ignorecase is read once for the two ignored-file checks below.
	// Execute's release and fast-forward read it fresh: they guard writes.
	fold := cleanup.FoldFor(cleanup.IgnoreCase(rg))
	if why, err := removal.RootIgnoredAtRiskFold(rg, root, fold); err != nil {
		return nil, fmt.Errorf("could not check the root's ignored files: %w", err)
	} else if why != "" {
		return nil, refuse(CodeIgnoredCollision, "%s; move it out of the root first", why)
	}
	if hit, err := ignoredDefaultTracks(rg, def, fold); err != nil {
		return nil, fmt.Errorf("could not check the root's ignored files against %s: %w", cleanup.OneLine(def), err)
	} else if hit != "" {
		return nil, refuse(CodeIgnoredCollision, "the ignored %s is where the default branch %s tracks a file, so switching the root to it would overwrite it; move it out of the root first",
			cleanup.OneLine(hit), cleanup.OneLine(def))
	}

	warnings := slices.Clone(rp.Warnings)
	if heldWarn != "" {
		warnings = append(warnings, heldWarn)
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
		Warnings:  warnings,
		rp:        rp,
		common:    common,
	}, nil
}

// commonDir reads the repository's absolute common directory, where the
// session claims live.
func commonDir(rg cleanup.Git) (string, error) {
	common, err := rg.Text("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("could not read the repository's common directory: %w", err)
	}
	return common, nil
}

// heldNow is heldBy for a plan: the common dir Preflight read, or one read
// now for a plan without it. The claims are always read fresh.
func (e Engine) heldNow(rg cleanup.Git, p *Plan) (string, error) {
	common := p.common
	if common == "" {
		var err error
		if common, err = commonDir(rg); err != nil {
			return "", err
		}
	}
	return e.heldBy(common, p.Repo)
}

// resolveCommit resolves ref to a commit id; ok is false when the ref is
// absent, and a git failure other than "absent" is an error.
func resolveCommit(rg cleanup.Git, ref string) (id string, ok bool, err error) {
	o, err := rg.Run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", false, err
	}
	switch o.Code {
	case 0:
		return strings.TrimSpace(o.Stdout), true, nil
	case 1:
		return "", false, nil
	}
	return "", false, fmt.Errorf("git rev-parse %s: exit %d: %s", cleanup.OneLine(ref), o.Code, cleanup.OneLine(o.Stderr))
}

// heldBy describes the live wt sessions of other processes whose claim is on
// root ("" none). It reads the claims without the lock and prunes nothing; a
// stale claim (its process gone or restarted) does not hold the root. The
// match folds symlink spellings of one directory (cleanup.ResolvePath, the
// HeldOn keying), never raw string equality: a claim on a linked worktree's
// path is not a claim on the root. A claims directory that cannot be read is
// an error.
func (e Engine) heldBy(common, root string) (string, error) {
	holders, err := sessionclaim.Holders(common, sessionclaim.Claim{PID: os.Getpid()})
	if err != nil {
		return "", fmt.Errorf("could not read the root's session claims: %w", err)
	}
	key := cleanup.ResolvePath(root)
	var onRoot []sessionclaim.Claim
	for path, cs := range holders {
		if cleanup.ResolvePath(path) == key {
			onRoot = append(onRoot, cs...)
		}
	}
	slices.SortFunc(onRoot, func(a, b sessionclaim.Claim) int { return a.PID - b.PID })
	now := time.Now()
	names := make([]string, 0, len(onRoot))
	for _, o := range onRoot {
		names = append(names, cleanup.OneLine(sessionclaim.Describe(o, now)))
	}
	return strings.Join(names, "; "), nil
}

// ignoredDefaultTracks returns an ignored file of the root at a path the
// default branch tracks, or one that sits where that branch needs a
// directory or a file ("" none): `git switch --no-overwrite-ignore` would
// fail on it rather than overwrite it, but only after the root was reset.
// Names are compared with fold (case-folded unless core.ignorecase is clearly
// false, see cleanup.FoldFor).
func ignoredDefaultTracks(rg cleanup.Git, def string, fold func(string) string) (string, error) {
	ignored, err := rg.Lines("ls-files", "-z", "--ignored", "--exclude-standard", "--others")
	if err != nil {
		return "", err
	}
	if len(ignored) == 0 {
		return "", nil
	}
	tracked, err := rg.Lines("ls-tree", "-r", "--name-only", "-z", "--full-tree", "refs/heads/"+def)
	if err != nil {
		return "", err
	}
	files, dirs := cleanup.PathSets(tracked, fold)
	for _, ig := range ignored {
		k := fold(ig)
		if files.Has(k) || dirs.Has(k) {
			return ig, nil
		}
		for d := path.Dir(ig); d != "."; d = path.Dir(d) {
			if files.Has(fold(d)) {
				return ig, nil
			}
		}
	}
	return "", nil
}

// defaultFF says whether origin/<def> is a strict descendant of the local
// def, so a fast-forward would move def forward without a merge; when it is
// not, the note says why. It never refuses: a failed read is "not known".
func defaultFF(rg cleanup.Git, def string) (ok bool, note string, benign bool) {
	d := cleanup.OneLine(def)
	// The local tip is read here, last, not reused from the existence check
	// above, so the plan's fast-forward text describes the tip as it is now.
	local, found, lerr := resolveCommit(rg, "refs/heads/"+def)
	if lerr != nil {
		return false, fmt.Sprintf("could not read %s (%v), so it is not fast-forwarded", d, lerr), false
	} else if !found {
		return false, fmt.Sprintf("%s no longer exists locally, so it is not fast-forwarded", d), false
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
