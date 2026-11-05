package cleanup

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// GatherInputs are what Gather cannot learn from git.
type GatherInputs struct {
	CWD            string             // the caller's directory (Current worktrees)
	Fetch          bool               // run `git fetch --prune` first
	OwnRepo        string             // owner/repo for PR filtering; "" parses origin's URL
	ProtectedPaths []string           // registered local_path model artifacts
	Claim          sessionclaim.Claim // this process's claim (its PID); Path is set to the root
	Parallel       int                // concurrent gh calls (<1 means 1)
}

// RefFact is one branch ref from for-each-ref.
type RefFact struct {
	Ref      string    // full refname: refs/heads/<Name> or refs/remotes/<Remote>/<Name>
	Name     string    // the branch name
	Remote   string    // remote-tracking ref: its remote; local head: its upstream's remote ("" none)
	Tip      string    // commit id
	Upstream string    // local head: the upstream's full refname ("" none)
	Gone     bool      // local head: the upstream ref no longer exists
	TipTime  time.Time // committer date of the tip
	Created  time.Time // oldest reflog entry; zero when unknown or not read
}

// Proof is the git-only verdict for one tip, computed once per commit.
type Proof struct {
	Evidence Evidence
	Verdict  Verdict
	Weak     []Evidence // only read when Verdict is NotMerged
	// Unpushed is the count of commits on the tip that no refs/remotes/* ref
	// reaches; -1 when the count failed. Read only for a local head with no
	// git proof (ruling: unpushed matters only when nothing proves a merge).
	Unpushed    int
	UnpushedErr string
}

// Tri is a yes / no / could-not-tell answer.
type Tri int

const (
	TriUnknown Tri = iota // the zero value: a map miss is never an answer
	TriYes
	TriNo
)

// WorktreeState is one worktree's facts and gates.
type WorktreeState struct {
	Fact     WorktreeFact
	Gates    Gates
	GatesErr string // CheckGates failed: nothing about the worktree is known clean
}

// Snapshot is every fact Classify reads. Gather fills it with git, gh and
// the claims directory; Classify does no I/O.
type Snapshot struct {
	Repo      string // the primary worktree's path
	CommonDir string
	NoDefault bool // the default branch could not be resolved: every row is Unknown
	Base      Base
	BaseTip   string // commit id of the base ref
	Fetch     FetchResult
	OwnRepo   string
	Worktrees []WorktreeState
	Heads     []RefFact           // refs/heads/*, the default branch's forms excluded
	Remotes   []RefFact           // refs/remotes/<r>/* for origin and every upstream's remote, default forms excluded
	Proofs    map[string]Proof    // by tip commit id
	PRs       map[string]PRResult // by branch name; a miss is LookupUnknown
	Merges    map[string]Tri      // merged PR merge commit -> is it an ancestor of the base
	Claims    []sessionclaim.Claim
	ClaimsErr string
}

// refFormat is one for-each-ref record: fields split by NUL.
const refFormat = "%(refname)%00%(objectname)%00%(objecttype)%00%(symref)%00%(upstream)%00%(upstream:remotename)%00%(upstream:track)%00%(committerdate:unix)"

// Gather reads one snapshot of g's repository. It never writes: no claim is
// joined or pruned, nothing is deleted. The error is set only when nothing can
// be classified: not a repository, a git failure on the porcelain list or the
// refs, or no default branch (ErrNoDefaultBranch). With ErrNoDefaultBranch the
// Snapshot is still returned (NoDefault set) so every row can show Unknown.
func Gather(g Git, in GatherInputs) (Snapshot, error) {
	var s Snapshot
	args := []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
	o, err := g.Run(args...)
	if err != nil {
		return Snapshot{}, err
	}
	if o.Code != 0 {
		return Snapshot{}, gitFailure(o, args...)
	}
	s.CommonDir = strings.TrimSpace(o.Stdout)

	facts, err := ListWorktrees(g, in.CWD)
	if err != nil {
		return Snapshot{}, err
	}
	s.Repo = facts[0].Path

	if in.Fetch {
		s.Fetch = Fetch(g)
	}
	var baseErr error
	s.Base, baseErr = ResolveBase(g)
	switch {
	case errors.Is(baseErr, ErrNoDefaultBranch):
		s.NoDefault = true
	case baseErr != nil:
		return Snapshot{}, baseErr
	}
	s.Base.FetchedAt = s.Fetch.At
	if !s.NoDefault {
		var ok bool
		if s.BaseTip, ok = commitOf(g, qualifiedBase(s.Base)); !ok {
			return Snapshot{}, fmt.Errorf("cleanup: base %s does not resolve to a commit", s.Base.Ref)
		}
	}

	if err := s.readRefs(g); err != nil {
		return Snapshot{}, err
	}

	// Claims: read-only, on the root's physical path.
	claim := in.Claim
	claim.Path = resolve(s.Repo)
	if s.Claims, err = sessionclaim.Others(s.CommonDir, claim); err != nil {
		s.ClaimsErr = err.Error()
	}
	held := func(p string) bool { return len(s.Claims) > 0 && resolve(p) == claim.Path }

	gin := GateInputs{Base: s.BaseTip, HeldBy: held, ProtectedPaths: in.ProtectedPaths}
	for _, w := range facts {
		ws := WorktreeState{Fact: w}
		if gates, err := CheckGates(g, w, gin); err != nil {
			ws.GatesErr = err.Error()
		} else {
			ws.Gates = gates
		}
		s.Worktrees = append(s.Worktrees, ws)
	}

	s.Proofs = map[string]Proof{}
	s.PRs = map[string]PRResult{}
	s.Merges = map[string]Tri{}
	if s.NoDefault {
		return s, ErrNoDefaultBranch
	}

	s.prove(g)

	s.OwnRepo = in.OwnRepo
	if s.OwnRepo == "" {
		s.OwnRepo = ownRepoFromURL(originURL(g))
	}
	s.PRs = LookupPRs(g.Dir, s.branchNames(), s.OwnRepo, in.Parallel)
	s.checkMerges(g)
	return s, nil
}

// qualifiedBase is the base as a full refname (Prove accepts no short name).
func qualifiedBase(b Base) string {
	if b.Fallback {
		return "refs/heads/" + b.Ref
	}
	return "refs/remotes/" + b.Ref
}

// readRefs fills Heads and Remotes from one for-each-ref. Symbolic refs
// (origin/HEAD), non-commits and the default branch in any form are left out.
// Remote refs are kept for origin and for every remote a local head's
// upstream names.
func (s *Snapshot) readRefs(g Git) error {
	args := []string{"for-each-ref", "--format=" + refFormat, "refs/heads", "refs/remotes"}
	o, err := g.Run(args...)
	if err != nil {
		return err
	}
	if o.Code != 0 {
		return gitFailure(o, args...)
	}
	remotes := map[string]bool{"origin": true}
	var remoteRefs []RefFact
	for _, line := range strings.Split(strings.TrimRight(o.Stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) != 8 {
			return fmt.Errorf("cleanup: for-each-ref: unexpected record %q", line)
		}
		if f[2] != "commit" || f[3] != "" {
			continue
		}
		r := RefFact{Ref: f[0], Tip: f[1]}
		if secs, err := strconv.ParseInt(f[7], 10, 64); err == nil {
			r.TipTime = time.Unix(secs, 0)
		}
		if name, ok := strings.CutPrefix(f[0], "refs/heads/"); ok {
			r.Name, r.Upstream, r.Remote, r.Gone = name, f[4], f[5], f[6] == "[gone]"
			if r.Remote != "" && r.Remote != "." {
				remotes[r.Remote] = true
			}
			if !s.isDefault(name) {
				s.Heads = append(s.Heads, r)
			}
			continue
		}
		rest := strings.TrimPrefix(f[0], "refs/remotes/")
		remote, name, ok := strings.Cut(rest, "/")
		if !ok {
			continue
		}
		r.Remote, r.Name = remote, name
		if !s.isDefault(name) {
			remoteRefs = append(remoteRefs, r)
		}
	}
	for _, r := range remoteRefs {
		if remotes[r.Remote] {
			s.Remotes = append(s.Remotes, r)
		}
	}
	return nil
}

// isDefault reports whether name is the default branch in any form. With no
// default branch nothing is excluded: every row is Unknown anyway.
func (s *Snapshot) isDefault(name string) bool {
	return !s.NoDefault && worktree.IsDefaultBranchForm(name, s.Base.Default)
}

// prove runs the ladder once per distinct tip (heads, remotes, detached
// worktree HEADs), the weak signals where nothing proves, the unpushed count
// for local heads with no proof, and the creation time of refs whose tip is
// the base (the min-age rule needs it).
func (s *Snapshot) prove(g Git) {
	run := func(tip string) Proof {
		if p, ok := s.Proofs[tip]; ok {
			return p
		}
		var p Proof
		p.Evidence, p.Verdict = Prove(g, s.BaseTip, tip)
		if p.Verdict == NotMerged {
			p.Weak = WeakSignals(g, s.BaseTip, tip)
		}
		s.Proofs[tip] = p
		return p
	}
	counted := map[string]bool{}
	for i := range s.Heads {
		h := &s.Heads[i]
		p := run(h.Tip)
		if p.Verdict != Merged && !counted[h.Tip] {
			counted[h.Tip] = true
			p.Unpushed, p.UnpushedErr = unpushed(g, h.Tip)
			s.Proofs[h.Tip] = p
		}
		if p.Evidence == EvSameCommit {
			h.Created = created(g, h.Ref)
		}
	}
	for i := range s.Remotes {
		r := &s.Remotes[i]
		if run(r.Tip).Evidence == EvSameCommit {
			r.Created = created(g, r.Ref)
		}
	}
	for _, w := range s.Worktrees {
		if w.Fact.Detached && fullCommitID.MatchString(w.Fact.Head) {
			run(w.Fact.Head)
		}
	}
}

// unpushed counts the commits on tip that no remote-tracking ref reaches.
// -1 and a message when git fails.
func unpushed(g Git, tip string) (int, string) {
	args := []string{"rev-list", "--count", tip, "--not", "--remotes"}
	o, err := g.Run(args...)
	if err != nil {
		return -1, err.Error()
	}
	if o.Code != 0 {
		return -1, gitFailure(o, args...).Error()
	}
	n, err := strconv.Atoi(strings.TrimSpace(o.Stdout))
	if err != nil {
		return -1, "rev-list --count: " + err.Error()
	}
	return n, ""
}

// created is the time of ref's oldest reflog entry; zero when there is no
// reflog or git fails (an unknown age never counts as old).
func created(g Git, ref string) time.Time {
	o, err := g.Run("reflog", "show", "--date=unix", "--format=%gd", ref, "--")
	if err != nil || o.Code != 0 {
		return time.Time{}
	}
	lines := strings.Split(strings.TrimSpace(o.Stdout), "\n")
	last := lines[len(lines)-1]
	i, j := strings.LastIndex(last, "@{"), strings.LastIndex(last, "}")
	if i < 0 || j < i {
		return time.Time{}
	}
	secs, err := strconv.ParseInt(last[i+2:j], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(secs, 0)
}

// branchNames is every head and remote branch name, once each: the names gh
// is asked about.
func (s *Snapshot) branchNames() []string {
	seen := map[string]bool{}
	var out []string
	for _, rs := range [][]RefFact{s.Heads, s.Remotes} {
		for _, r := range rs {
			if !seen[r.Name] {
				seen[r.Name] = true
				out = append(out, r.Name)
			}
		}
	}
	return out
}

// checkMerges asks, for each merged PR into the default branch whose head is
// some ref's tip, whether its merge commit is an ancestor of the base (the PR
// rung's second half). Any failure leaves TriUnknown.
func (s *Snapshot) checkMerges(g Git) {
	tips := map[string]bool{}
	for _, rs := range [][]RefFact{s.Heads, s.Remotes} {
		for _, r := range rs {
			tips[r.Tip] = true
		}
	}
	for _, res := range s.PRs {
		for _, pr := range res.PRs {
			if pr.State != "MERGED" || pr.BaseRef != s.Base.Default || !tips[pr.HeadRefOid] {
				continue
			}
			if _, done := s.Merges[pr.MergeCommit]; done || !fullCommitID.MatchString(pr.MergeCommit) {
				continue
			}
			s.Merges[pr.MergeCommit] = isAncestor(g, pr.MergeCommit, s.BaseTip)
		}
	}
}

// isAncestor answers `merge-base --is-ancestor a b` by exit code.
func isAncestor(g Git, a, b string) Tri {
	o, err := g.Run("merge-base", "--is-ancestor", a, b)
	if err != nil {
		return TriUnknown
	}
	switch o.Code {
	case 0:
		return TriYes
	case 1:
		return TriNo
	}
	return TriUnknown
}

// originURL is remote.origin.url, or "" when unset or unreadable.
func originURL(g Git) string {
	o, err := g.Run("config", "--get", "remote.origin.url")
	if err != nil || o.Code != 0 {
		return ""
	}
	return strings.TrimSpace(o.Stdout)
}

var githubURL = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?github\.com/|ssh://git@github\.com(?::\d+)?/|git@github\.com:)([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)

// ownRepoFromURL parses a github.com remote URL (https, ssh:// or scp form)
// to owner/repo; "" for anything else, which leaves PR facts unknown.
func ownRepoFromURL(url string) string {
	m := githubURL.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
