package cleanup

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ErrTargetGone means the rescanned target no longer exists: its worktree is
// not in `git worktree list`, or its ref is not among the refs.
var ErrTargetGone = errors.New("cleanup: the target no longer exists")

// Target names one row to rescan: a worktree by Path, a local branch by its
// full refname Ref (refs/heads/<b>), an origin or other remote-tracking branch
// by its refs/remotes/<r>/<b> name. For a worktree, Ref is the identity the
// caller saw and is enforced: its branch's full refname, or "" for a
// detached worktree.
type Target struct {
	Kind Kind
	Path string
	Ref  string
}

// notReadByRescan is the GatesErr of a non-target worktree in a rescan's
// snapshot: its gates are not read, and its row is never returned.
const notReadByRescan = "not read by a rescan"

// Rescan re-classifies one target immediately before it is acted on (spec
// "Re-check in PR B"). It reads git afresh for that target only: the
// worktree list (to find the target and tell which branches worktrees hold),
// the target's own refs (one for-each-ref naming them), the target
// worktree's gates, the ladder for the fresh tip, its unpushed count and age,
// the merge-commit checks of its PRs, and the session claims (read-only). It
// re-resolves the base ref's tip locally, so a fetch another process made
// since the list is honoured; it never fetches and never runs gh. From prior
// it reuses Base, CommonDir, OwnRepo, Fetch, NoDefault and PRs, and the
// non-target Heads and Remotes entries (whose rows are discarded). For a
// worktree or branch the PR facts are as of the first Gather: a PR opened
// after the list is not seen (the spec accepts this); a merged PR still proves
// only the head it merged, so a tip that moved past it stops counting. For a
// remote target they are live.
//
// The snapshot handed to the pure Classify is prior's with the target's
// entries replaced: the other heads and remotes stay as listed, and the other
// worktrees are the fresh list's entries with no gates read (their rows are
// discarded too). That is enough for Classify to judge the target exactly as
// a full run would: a worktree row reads only its own gates, its branch's
// head and that head's proof; a branch row additionally depends on which
// worktrees hold branches (fresh here). Proofs hold the target's tips only,
// so every other row is Unknown and is discarded.
//
// A worktree target whose worktree is now on a different branch, or on a
// branch when it was detached (or detached when it was on one), is an error
// naming the change: Target.Ref is enforced, never re-read as whatever the
// path holds now.
//
// A target that no longer exists is ErrTargetGone. A malformed or remote
// target, a failure to list the worktrees or read the refs, a ref that is
// symbolic, a base that no longer resolves, or a branch a worktree now holds
// is another error: never a clean row. A gate that cannot
// be read is not an error: the row carries it (gates_unavailable,
// Losses.GatesUnknown) as the full run's would. Rescan writes nothing.
func Rescan(g Git, prior Snapshot, in GatherInputs, o Options, t Target) (Row, error) {
	facts, err := ListWorktrees(g, in.CWD)
	if err != nil {
		return Row{}, err
	}
	if len(facts) == 0 {
		// git always lists the primary checkout; an empty list is not a
		// repository we can judge (fail closed rather than index it).
		return Row{}, errors.New("cleanup: git worktree list named no worktree")
	}
	s := Snapshot{
		Repo:      facts[0].Path,
		CommonDir: prior.CommonDir,
		NoDefault: prior.NoDefault,
		Base:      prior.Base,
		Fetch:     prior.Fetch,
		OwnRepo:   prior.OwnRepo,
		PRs:       prior.PRs,
	}
	if !s.NoDefault {
		var ok bool
		if s.BaseTip, ok = commitOf(g, qualifiedBase(s.Base)); !ok {
			return Row{}, fmt.Errorf("cleanup: base %s no longer resolves to a commit", flat(s.Base.Ref))
		}
	}
	gin := s.readClaims(in)

	// mini holds the target's fresh entries only; prove runs on it, so the
	// ladder, unpushed counts and ages are computed for nothing else.
	mini := Snapshot{CommonDir: s.CommonDir, NoDefault: s.NoDefault, Base: s.Base, BaseTip: s.BaseTip, PRs: s.PRs}
	var self string // the target worktree's resolved path
	var refs []string
	switch t.Kind {
	case KindWorktree:
		self = resolve(t.Path)
		i := slices.IndexFunc(facts, func(w WorktreeFact) bool { return resolve(w.Path) == self })
		if i < 0 {
			return Row{}, ErrTargetGone
		}
		w := facts[i]
		// Identity: the branch (or detached HEAD) the caller saw. A worktree
		// now on another branch would give a correct row for a branch nobody
		// approved.
		switch {
		case t.Ref == "" && w.Branch != "":
			return Row{}, fmt.Errorf("cleanup: worktree %s is now on %s (was detached)", flat(w.Path), flat(w.Branch))
		case t.Ref != "" && !strings.EqualFold(w.Branch, t.Ref):
			now := "detached"
			if w.Branch != "" {
				now = "on " + w.Branch
			}
			return Row{}, fmt.Errorf("cleanup: worktree %s is now %s (was %s)", flat(w.Path), flat(now), flat(t.Ref))
		}
		// Records are listed afresh, never taken from prior; a failed
		// listing is no records, not an error.
		recs, _ := ListRecords(g)
		mini.Worktrees = []WorktreeState{worktreeState(g, w, gin, recs)}
		if w.Branch != "" {
			// The worktree's spelling and, on a case-insensitive volume, the
			// spelling for-each-ref gave in the list; a branch not found is
			// branch_unknown on the row, as in a full run.
			refs = []string{w.Branch}
			for _, h := range prior.Heads {
				if strings.EqualFold(h.Ref, w.Branch) && h.Ref != w.Branch {
					refs = append(refs, h.Ref)
				}
			}
		}
	case KindBranch:
		if !strings.HasPrefix(t.Ref, "refs/heads/") || !qualified(t.Ref) {
			return Row{}, fmt.Errorf("cleanup: rescan: branch target %q is not a refs/heads/ name", flat(t.Ref))
		}
		// A branch a worktree holds (checked out, or held by a rebase or
		// bisect there, whose HEAD is then detached) is not deletable;
		// what a worktree holds unreadably fails closed.
		for _, w := range facts {
			held, err := HeldRefs(g, w)
			if err != nil {
				return Row{}, fmt.Errorf("cleanup: could not tell which branches worktree %s holds: %w", flat(w.Path), err)
			}
			for _, h := range held {
				if strings.EqualFold(h, t.Ref) {
					return Row{}, fmt.Errorf("cleanup: %s is now checked out, rebased or bisected in worktree %s", flat(t.Ref), flat(w.Path))
				}
			}
		}
		refs = []string{t.Ref}
	case KindRemote:
		if !strings.HasPrefix(t.Ref, "refs/remotes/") || !qualified(t.Ref) {
			return Row{}, fmt.Errorf("cleanup: rescan: remote target %q is not a refs/remotes/ name", flat(t.Ref))
		}
		refs = []string{t.Ref}
	default:
		return Row{}, fmt.Errorf("cleanup: rescan: unknown target kind %q", flat(string(t.Kind)))
	}
	if len(refs) > 0 {
		heads, remotes, err := readNamedRefs(g, refs)
		if err != nil {
			return Row{}, err
		}
		for _, h := range heads {
			if !s.isDefault(h.Name) {
				mini.Heads = append(mini.Heads, h)
			}
		}
		for _, r := range remotes {
			if !s.isDefault(r.Name) {
				mini.Remotes = append(mini.Remotes, r)
			}
		}
		if (t.Kind == KindBranch && len(heads) == 0) || (t.Kind == KindRemote && len(remotes) == 0) {
			return Row{}, ErrTargetGone
		}
		if t.Kind != KindWorktree && len(mini.Heads)+len(mini.Remotes) == 0 {
			return Row{}, fmt.Errorf("cleanup: %s is the default branch", flat(t.Ref))
		}
	}

	if t.Kind == KindRemote && len(mini.Remotes) == 1 && mini.Remotes[0].Remote == "origin" {
		// The delete of an origin branch is public: ask gh again, for this
		// branch only, both who has it as a head and who targets it. The
		// maps are copied; the prior snapshot is never written.
		name := mini.Remotes[0].Name
		s.PRs = maps.Clone(prior.PRs)
		if s.PRs == nil {
			s.PRs = map[string]PRResult{}
		}
		s.PRs[name] = LookupPRs(g.Dir, []string{name}, s.OwnRepo, 1)[name]
		s.BasePRs = maps.Clone(prior.BasePRs)
		if s.BasePRs == nil {
			s.BasePRs = map[string]PRResult{}
		}
		s.BasePRs[name] = LookupBaseOf(g.Dir, []string{name}, 1)[name]
		mini.PRs = s.PRs
	} else {
		s.BasePRs = prior.BasePRs
	}

	mini.Proofs = map[string]Proof{}
	mini.Merges = map[string]Tri{}
	if !s.NoDefault {
		mini.prove(g)
		mini.checkMerges(g)
	}
	s.Proofs, s.Merges = mini.Proofs, mini.Merges
	s.Heads = replaceRefs(prior.Heads, refs, mini.Heads)
	s.Remotes = replaceRefs(prior.Remotes, refs, mini.Remotes)
	for _, w := range facts {
		if t.Kind == KindWorktree && resolve(w.Path) == self {
			s.Worktrees = append(s.Worktrees, mini.Worktrees[0])
			continue
		}
		s.Worktrees = append(s.Worktrees, WorktreeState{Fact: w, GatesErr: notReadByRescan})
	}

	for _, row := range Classify(s, o).Rows {
		switch {
		case t.Kind == KindWorktree && row.Kind == KindWorktree && resolve(row.Path) == self:
			return row, nil
		case t.Kind != KindWorktree && row.Kind == t.Kind && row.Ref == t.Ref:
			return row, nil
		}
	}
	if t.Kind == KindBranch {
		for _, w := range facts {
			if strings.EqualFold(w.Branch, t.Ref) {
				return Row{}, fmt.Errorf("cleanup: %s is now checked out in worktree %s", flat(t.Ref), flat(w.Path))
			}
		}
	}
	return Row{}, fmt.Errorf("cleanup: rescan of %s %s gave no row", t.Kind, flat(t.Ref))
}

// readNamedRefs reads the named full refnames with one for-each-ref and
// returns those that exist (exact spelling; a pattern also matches refs
// below it, which are dropped), split into heads and remote-tracking refs.
// A git failure is an error.
func readNamedRefs(g Git, names []string) (heads, remotes []RefFact, err error) {
	args := append([]string{"for-each-ref", "--format=" + refFormat}, names...)
	o, err := g.Run(args...)
	if err != nil {
		return nil, nil, err
	}
	if o.Code != 0 {
		return nil, nil, gitFailure(o, args...)
	}
	for _, line := range strings.Split(strings.TrimRight(o.Stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		r, head, ok, err := parseRefRecord(line)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			// A named ref that exists but is a symbolic ref or names a
			// non-commit is not a branch with its own tip: an error, not gone.
			if name, _, _ := strings.Cut(line, "\x00"); slices.Contains(names, name) {
				return nil, nil, fmt.Errorf("cleanup: %s is not a branch on a commit (a symbolic ref or a non-commit)", flat(name))
			}
			continue
		}
		if !slices.Contains(names, r.Ref) {
			continue
		}
		if head {
			heads = append(heads, r)
		} else {
			remotes = append(remotes, r)
		}
	}
	return heads, remotes, nil
}

// replaceRefs is listed without any ref named in names (case folded, as
// Classify matches a worktree's branch), followed by fresh.
func replaceRefs(listed []RefFact, names []string, fresh []RefFact) []RefFact {
	var out []RefFact
	for _, r := range listed {
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, r.Ref) }) {
			out = append(out, r)
		}
	}
	return append(out, fresh...)
}
