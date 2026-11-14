package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/rootclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// RecordFetch runs one explicit fetch — fn, a caller's per-call hook
// (GatherInputs.FetchRemotes), or worktree.FetchRemotes when fn is nil — at
// root and records the outcome the way the header's fetch note reads it: ok,
// or failed with git's line. A failure is a value, never an error: the list
// still opens and the header says how the fetch went. It is the one home of
// that wording for the picker's `F`, the manage list's `F` and `wt worktrees
// --fetch`.
func RecordFetch(fn func(root string) error, root string) FetchResult {
	if fn == nil {
		fn = worktree.FetchRemotes
	}
	if err := fn(root); err != nil {
		return FetchResult{At: time.Now(), Err: err.Error()}
	}
	return FetchResult{OK: true, At: time.Now()}
}

// GatherInputs are what Gather cannot learn from git.
type GatherInputs struct {
	CWD   string // the caller's directory (Current worktrees)
	Fetch bool   // run `git fetch --prune <remote>` for every remote first
	// Fetched is the result of an earlier fetch of this run, reported as the
	// snapshot's when Fetch is false: a re-read after a removal (the tick
	// list's refresh) must not suddenly find every origin row "not fetched".
	Fetched FetchResult
	// FetchRemotes is this call's fetch hook for a caller that fetches
	// outside Gather (the picker's rows load): what that load runs in place
	// of worktree.FetchRemotes when set, so a test can count the fetches and
	// the hook rides with the call (as PRList does). Gather itself never
	// calls it: its own Fetch is the per-remote fetch in base.go.
	FetchRemotes   func(root string) error
	OwnRepo        string             // owner/repo for PR filtering; "" parses origin's URL
	ProtectedPaths []string           // registered local_path model artifacts
	Claim          sessionclaim.Claim // this process's claim (its PID); self and its ancestors are never holders
	Parallel       int                // concurrent gh calls (<1 means 1)
	// OriginRows says the caller will show or act on origin rows (the cleanup
	// list, `wt remove origin/...`): only then is the live `gh pr list --base`
	// query made, one gh call per origin candidate. Without it an origin row
	// that could be deleted stays unverifiable (cannot_verify), which is fine
	// for a caller that never offers it. Rescan asks regardless.
	OriginRows bool
	// PRList and PRBaseList are this call's gh pr-list hooks. When set they
	// serve every lookup this call makes (and the fan-out it spawns) in place
	// of the legacy package seams (ghPRList / ghPRBaseList, swapped globally
	// by StubPRListForTest / StubPRBaseListForTest, which keep working); nil
	// takes the legacy seam, so an unhooked call is byte-identical to before.
	// The hook rides with the call (Ruling 1): concurrent runs can no longer
	// cross-talk through a package var.
	PRList     func(dir, branch string) ([]byte, error) // `gh pr list --head <branch>` JSON
	PRBaseList func(dir, base string) ([]byte, error)   // `gh pr list --base <base> --state open` JSON
	// SkipPRs makes Gather make no gh call: the Snapshot comes back with
	// PRsAsked false and PRsSkipped true, and every row reads "PR ?" and is
	// never proven until FillPRs runs the lookups.
	SkipPRs bool
	// AllRemotes keeps every remote's branches in Remotes. Without it only
	// origin's and those of remotes a local head's upstream names are kept
	// (what `wt cleanup --json` and --plain list). The interactive lists set
	// it, so a branch of a remote nothing tracks is still shown.
	AllRemotes bool
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
	Created  time.Time // when the ref was made (see AgeSource); zero when unknown or not read
	// AgeSource says what Created is. The zero value is the reflog (or
	// unknown, when Created is zero); see refAge for the rule.
	AgeSource AgeSource
}

// AgeSource is where a ref's age came from.
type AgeSource string

const (
	AgeFromReflog AgeSource = ""      // Created is the oldest reflog entry
	AgeFromMtime  AgeSource = "mtime" // no reflog, logging on: the mtime of the loose ref file or of packed-refs
)

// Proof is the git-only verdict for one tip, computed once per commit.
type Proof struct {
	Evidence Evidence
	Verdict  Verdict
	Weak     []Evidence // only read when Verdict is NotMerged
	// Unpushed is the count of commits on the tip that no refs/remotes/* ref
	// reaches; -1 when the count failed. Counted for a local head with no
	// git proof (Ruling 1: unpushed matters only when nothing proves a
	// merge), and for one proven by trees-match or no-added-changes (Ruling
	// 17: those prove equal trees, not that the commits are anywhere else).
	Unpushed    int
	UnpushedErr string
	// NetZero is whether the tip's tree is its merge-base's tree (the branch
	// changes nothing overall); read only for trees-match and no-added-changes
	// evidence. TriUnknown when not determined.
	NetZero Tri
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
	Gates    Gates  // with GatesErr set, only Blockers (those found before the error)
	GatesErr string // CheckGates failed: nothing about the worktree is known clean
	// HeadSince is when a detached HEAD last moved (the newest entry of the
	// worktree's HEAD reflog); zero when unknown or not read. Min-age needs it.
	HeadSince time.Time
	// Preserved is set when a rescue record covers this worktree (see
	// preservedOf); nil otherwise. Classify decides what it changes.
	Preserved *Preserved
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
	// RootDrift is the root checkout's drift line (rootclaim.Drift): `root is
	// on <b>, not <d>`, `root is detached`, or "" when the root sits on the
	// default branch or no default branch is known (no drift is invented
	// without one). Read live by Gather and re-read by Rescan through
	// readRootDrift, never carried from one to the other.
	RootDrift string
	Worktrees []WorktreeState
	Heads     []RefFact           // refs/heads/*, the default branch's forms excluded
	Remotes   []RefFact           // refs/remotes/<r>/* for origin and every upstream's remote, default forms excluded
	Proofs    map[string]Proof    // by tip commit id
	PRs       map[string]PRResult // by branch name; a miss is LookupUnknown
	// BasePRs is, for an origin branch that could be a deletion candidate, the
	// live answer to "which open PRs (any head repository) target this
	// branch?", by branch name. A miss is LookupUnknown.
	BasePRs map[string]PRResult
	Merges  map[string]Tri // merged PR merge commit -> is it an ancestor of the base
	// PRsAsked is true once the PR lookups ran (a full Gather, FillPRs). A
	// row whose lookup is then unknown reads "PR ?" and is never proven.
	PRsAsked bool
	// PRsSkipped is true after Gather(SkipPRs) until FillPRs: no lookup has
	// run, so every row's PR state is unknown and none is proven. A
	// hand-built Snapshot has neither flag and keeps the older behavior.
	PRsSkipped bool
	// PRNotApplicable is true when the repository has no known own
	// repository (origin is not on GitHub): there is no PR lookup to fail, so
	// no row reads "PR ?", "PR lookup failed" or "needs gh".
	PRNotApplicable bool
	// Claims are every live claim recorded in this repository's wt-sessions
	// (sessionclaim.Holders, read only: one Claim per holding session, the
	// paths sorted and each path's claims PID-ascending — keyed by the
	// recorded path, which may name a directory that is no longer a
	// checkout); ClaimsErr is why they could not be read. HeldOn finds a
	// row's holders among them.
	Claims    []sessionclaim.Claim
	ClaimsErr string
	// Records are the well-formed rescue records; a failed listing leaves none.
	Records []RescueRecord
	// FactsOnly marks a snapshot from GatherFacts whose fill has not run
	// yet: the worktree facts, refs, base and claims are read, the per-worktree
	// gates, records, proofs and PR lookups are not. Classify floors such a
	// report to Pending rows (never proven, State "-"); FillDetails clears
	// the flag. A hand-built Snapshot has it false and classifies fully.
	FactsOnly bool
}

// refFormat is one for-each-ref record: fields split by NUL.
const refFormat = "%(refname)%00%(objectname)%00%(objecttype)%00%(symref)%00%(upstream)%00%(upstream:remotename)%00%(upstream:track)%00%(committerdate:unix)"

// Gather reads one snapshot of g's repository: GatherFacts then
// FillDetails. It never writes: no claim is joined or pruned, nothing is
// deleted. The error is set only when nothing can be classified: not a
// repository, a git failure on the porcelain list or the refs, or no default
// branch (ErrNoDefaultBranch). With ErrNoDefaultBranch the Snapshot is still
// returned (NoDefault set) so every row can show Unknown.
//
// GatherFacts is the cheap first stage: the worktree list, refs, base
// resolution and the session claims — the facts the always-shown cells need —
// with FactsOnly set. It runs no per-worktree gates, no rescue records, no
// proof ladder and no gh, so a caller can paint immediately. The NoDefault
// answer comes back here too (rows read Unknown, as always); the gates a
// full run would read come with FillDetails either way.
func GatherFacts(g Git, in GatherInputs) (Snapshot, error) {
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

	// Every proof and removal check expects 40-hex commit ids: say once,
	// before anything else, that another object format is not supported (a
	// git too old to know the option echoes it back and is let through).
	if o, err := g.Run("rev-parse", "--show-object-format"); err == nil && o.Code == 0 {
		if f := strings.TrimSpace(o.Stdout); f != "" && f != "sha1" && !strings.HasPrefix(f, "-") {
			return Snapshot{}, fmt.Errorf("cleanup: this repository uses the %s object format, which is not supported by wt worktrees and wt remove (sha1 only)", OneLine(f))
		}
	}

	facts, err := ListWorktrees(g, in.CWD)
	if err != nil {
		return Snapshot{}, err
	}
	s.Repo = facts[0].Path

	if in.Fetch {
		s.Fetch = Fetch(g)
	} else {
		s.Fetch = in.Fetched
	}
	// The drift is read after the fetch: one that set origin/HEAD just named
	// the default, and the root's drift is against that.
	s.RootDrift = readRootDrift(facts[0])
	var baseErr error
	s.Base, baseErr = ResolveBase(g)
	switch {
	case errors.Is(baseErr, ErrNoDefaultBranch):
		s.NoDefault = true
	case baseErr != nil:
		return Snapshot{}, baseErr
	}
	if s.Fetch.OK {
		// Only a fetch that worked says how current the base is.
		s.Base.FetchedAt = s.Fetch.At
	}
	if !s.NoDefault {
		var ok bool
		if s.BaseTip, ok = commitOf(g, qualifiedBase(s.Base)); !ok {
			return Snapshot{}, fmt.Errorf("cleanup: base %s does not resolve to a commit", s.Base.Ref)
		}
	}

	if err := s.readRefs(g, in.AllRemotes); err != nil {
		return Snapshot{}, err
	}

	s.readClaims(in)
	s.Proofs = map[string]Proof{}
	s.PRs = map[string]PRResult{}
	s.Merges = map[string]Tri{}
	// The rows' names and paths are facts; their gates come with the fill.
	for _, w := range facts {
		s.Worktrees = append(s.Worktrees, WorktreeState{Fact: w})
	}
	s.FactsOnly = true
	if s.NoDefault {
		return s, ErrNoDefaultBranch
	}
	return s, nil
}

// FillDetails completes a GatherFacts snapshot in place: the rescue records,
// the per-worktree gates (through gateState, so Gather and a later Rescan
// read them alike), the proof ladder, the own-repo answer and the PR
// lookups — everything stage 1 left out — and clears FactsOnly. The rows keep
// their order: the gates fill the same list the facts laid out. The error is
// ErrNoDefaultBranch when the snapshot has no base (the gates have run, as
// in a one-shot Gather; nothing can be proven).
func FillDetails(g Git, s Snapshot, in GatherInputs) (Snapshot, error) {
	s.Records, _ = ListRecords(g) // an error reads as no records
	gin := GateInputs{Base: s.BaseTip, ProtectedPaths: in.ProtectedPaths}
	for i := range s.Worktrees {
		s.Worktrees[i] = worktreeState(g, s.Worktrees[i].Fact, gin, s.Records)
	}
	s.FactsOnly = false
	if s.NoDefault {
		return s, ErrNoDefaultBranch
	}

	s.prove(g)

	s.OwnRepo = in.OwnRepo
	if s.OwnRepo == "" {
		s.OwnRepo = ownRepoFromURL(originURL(g))
	}
	s.PRNotApplicable = s.OwnRepo == ""
	if in.SkipPRs {
		// With no own repository (origin is not on GitHub) no PR can exist:
		// PR state is not applicable, so nothing is pending.
		s.PRsSkipped = s.OwnRepo != ""
		return s, nil
	}
	s.askPRs(g, in)
	return s, nil
}

// Gather reads one finished snapshot of g's repository, synchronously:
// the two stages back to back, with the same error contract as always.
// A caller that can paint twice (the list screens) runs GatherFacts, shows
// it, then FillDetails; --json and --plain run this.
func Gather(g Git, in GatherInputs) (Snapshot, error) {
	s, err := GatherFacts(g, in)
	if err != nil && !errors.Is(err, ErrNoDefaultBranch) {
		return s, err
	}
	filled, fillErr := FillDetails(g, s, in)
	if err != nil {
		return filled, err // ErrNoDefaultBranch, where the one-shot always raised it
	}
	return filled, fillErr
}

// askPRs runs the gh lookups (PRs, the merge-commit checks, BasePRs) and
// marks the snapshot as asked.
func (s *Snapshot) askPRs(g Git, in GatherInputs) {
	s.PRs = LookupPRs(g.Dir, s.branchNames(), s.OwnRepo, in.Parallel, in)
	s.checkMerges(g)
	if in.OriginRows {
		s.BasePRs = LookupBaseOf(g.Dir, s.baseCandidates(), in.Parallel, in)
	}
	// An unknown own repository (a non-GitHub origin) has no PRs to ask
	// about: the state is "not applicable", never "unknown", so the floor
	// does not apply and no row reads "PR ?".
	s.PRsAsked, s.PRsSkipped = s.OwnRepo != "", false
}

// FillPRs runs the lookups a Gather with in.SkipPRs left out, so that
// Gather(SkipPRs) then FillPRs equals a full Gather. in is the same
// GatherInputs and g the Git Gather was given. A snapshot with no default
// branch (nothing was looked up) is left as it is.
func FillPRs(g Git, s *Snapshot, in GatherInputs) {
	if s.NoDefault {
		return
	}
	s.askPRs(g, in)
}

// baseCandidates are the names of the origin branches that could be offered
// for deletion: content already in the base, or a merged PR into the default
// branch whose head is the tip. Only these need the live "who targets this
// branch" query (one gh call each).
func (s *Snapshot) baseCandidates() []string {
	var out []string
	for _, r := range s.Remotes {
		if r.Remote != "origin" || slices.Contains(out, r.Name) {
			continue
		}
		if p, ok := s.Proofs[r.Tip]; ok && p.Verdict == Merged {
			out = append(out, r.Name)
			continue
		}
		for _, pr := range s.PRs[r.Name].PRs {
			if pr.State == "MERGED" && pr.BaseRef == s.Base.Default && pr.HeadRefOid == r.Tip {
				out = append(out, r.Name)
				break
			}
		}
	}
	return out
}

// readClaims fills Claims and ClaimsErr with every live claim holding a
// checkout of this repository (sessionclaim.Holders: read only — nothing is
// joined, pruned or written) and returns the gate inputs beside them. Gather
// and Rescan both read claims through it.
func (s *Snapshot) readClaims(in GatherInputs) GateInputs {
	byPath, err := sessionclaim.Holders(s.CommonDir, in.Claim)
	if err != nil {
		s.ClaimsErr = gitDetail(err) // a row detail: one sanitised line
	}
	// Flatten the map deterministically: paths sorted, each path's claims
	// already PID-ascending as Holders returns them. Claims is filled, not
	// grown: a re-read replaces whatever was there.
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	var claims []sessionclaim.Claim
	for _, p := range paths {
		claims = append(claims, byPath[p]...)
	}
	s.Claims = claims
	return GateInputs{Base: s.BaseTip, ProtectedPaths: in.ProtectedPaths}
}

// readRootDrift is the root checkout w's live drift line: rootclaim.Drift over
// w's branch as the worktree list just read it (Site's spelling — "" on a
// detached HEAD) and the default origin/HEAD names. The default is
// worktree.DefaultBranch (origin/HEAD only), never Base.Default: the base
// falls back to main/master and would invent a drift where no default is
// known. Gather and Rescan both read the drift through it, so a rescan
// re-renders the line rather than copying the list's. A bare repository has
// no checkout to drift.
func readRootDrift(w WorktreeFact) string {
	if w.Bare {
		return ""
	}
	branch := strings.TrimPrefix(w.Branch, "refs/heads/")
	return rootclaim.Drift(rootclaim.Site{Path: w.Path, Branch: branch}, worktree.DefaultBranch(w.Path))
}

// HeldOn returns the live claims holding path — the sessions whose claim
// names it — matched through resolve so /var and /private/var are one
// directory; nil when none do. It is the read-only answer to "who holds this
// checkout", rendered into the held reason by sessionclaim.DescribeAll.
func (s Snapshot) HeldOn(path string) []sessionclaim.Claim {
	var out []sessionclaim.Claim
	p := resolve(path)
	for _, c := range s.Claims {
		if resolve(c.Path) == p {
			out = append(out, c)
		}
	}
	return out
}

// SessionsOn counts the live sessions holding path — the row's session
// digit. Claims are read through sessionclaim.Holders, which counts only
// running processes other than this session and its ancestors, so the count
// is running sessions by construction and the browsing session never counts
// itself. Branch and remote rows have no directory: no digit.
func (s Snapshot) SessionsOn(path string) int { return len(s.HeldOn(path)) }

// checkGates is CheckGates; a seam so a test can count the gate checks a
// rescan runs (TestRescanCostIsScoped).
var checkGates = CheckGates

// gateState is one worktree's state as Gather and Rescan both read it: its
// gates, or the blockers found before a gate error and that error's text.
func gateState(g Git, w WorktreeFact, gin GateInputs) WorktreeState {
	ws := WorktreeState{Fact: w}
	var err error
	if ws.Gates, err = checkGates(g, w, gin); err != nil {
		// Gates still holds the blockers found before it. The text is a
		// row detail: one sanitised line.
		ws.GatesErr = gitDetail(err)
	}
	return ws
}

// qualifiedBase is the base as a full refname (Prove accepts no short name).
func qualifiedBase(b Base) string {
	if b.Fallback {
		return "refs/heads/" + b.Ref
	}
	return "refs/remotes/" + b.Ref
}

// readRefs fills Heads and Remotes from one for-each-ref. Symbolic refs
// (origin/HEAD) and non-commits are left out; the default branch's head and
// remote ref are kept (they are rows with the default_branch floor).
// Remote refs are kept for origin and for every remote a local head's
// upstream names, or for every remote with all.
func (s *Snapshot) readRefs(g Git, all bool) error {
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
		r, head, ok, err := parseRefRecord(line)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if head {
			if r.Remote != "" && r.Remote != "." {
				remotes[r.Remote] = true
			}
			s.Heads = append(s.Heads, r)
			continue
		}
		remoteRefs = append(remoteRefs, r)
	}
	for _, r := range remoteRefs {
		if all || remotes[r.Remote] {
			s.Remotes = append(s.Remotes, r)
		}
	}
	return nil
}

// parseRefRecord parses one refFormat record. head is true for a
// refs/heads/* ref. ok is false for a record to leave out: a symbolic ref
// (origin/HEAD), a non-commit, or a remote ref with no <remote>/<name> split.
func parseRefRecord(line string) (r RefFact, head, ok bool, err error) {
	f := strings.Split(line, "\x00")
	if len(f) != 8 {
		return RefFact{}, false, false, fmt.Errorf("cleanup: for-each-ref: unexpected record %q", line)
	}
	if f[2] != "commit" || f[3] != "" {
		return RefFact{}, false, false, nil
	}
	r = RefFact{Ref: f[0], Tip: f[1]}
	if secs, err := strconv.ParseInt(f[7], 10, 64); err == nil {
		r.TipTime = time.Unix(secs, 0)
	}
	if name, isHead := strings.CutPrefix(f[0], "refs/heads/"); isHead {
		r.Name, r.Upstream, r.Remote, r.Gone = name, f[4], f[5], f[6] == "[gone]"
		return r, true, true, nil
	}
	remote, name, split := strings.Cut(strings.TrimPrefix(f[0], "refs/remotes/"), "/")
	if !split {
		return RefFact{}, false, false, nil
	}
	r.Remote, r.Name = remote, name
	return r, false, true, nil
}

// isDefault reports whether name is the default branch's own name. Only the
// exact name counts: for-each-ref hands this the bare branch name, so this
// caller can tell a branch from a remote-tracking ref and need not fall back
// to the picker's suffix form (worktree.IsDefaultBranchForm), which would
// also match a branch called feature/<default> — a normal branch the list
// must show, with its worktree in use for its own verdict, not as "the
// default branch" (enumerate.go spells that caller duty out). With no default
// branch nothing is excluded: every row is Unknown anyway.
func (s *Snapshot) isDefault(name string) bool {
	return !s.NoDefault && name == s.Base.Default
}

// prove runs the ladder once per distinct tip (heads, remotes, detached
// worktree HEADs), the weak signals where nothing proves, whether a
// tree-equality proof nets to nothing, the unpushed count for local heads
// with no proof or a tree-equality proof, and the creation time of refs
// whose tip is the base (the min-age rule needs it).
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
		if treeProof(p.Evidence) {
			p.NetZero = netZero(g, s.BaseTip, tip, p.Evidence)
		}
		s.Proofs[tip] = p
		return p
	}
	var logOn, logRead bool
	logging := func() bool { // read once, only if some ref is age-gated
		if !logRead {
			logOn, logRead = refLogging(g), true
		}
		return logOn
	}
	counted := map[string]bool{}
	for i := range s.Heads {
		h := &s.Heads[i]
		p := run(h.Tip)
		// The local default when it is the base itself (Base.Fallback):
		// its proof compares it with itself, so its own commits are counted
		// too (classifier.selfBase reads them).
		selfBase := s.Base.Fallback && h.Name == s.Base.Default
		if (p.Verdict != Merged || treeProof(p.Evidence) || selfBase) && !counted[h.Tip] {
			counted[h.Tip] = true
			p.Unpushed, p.UnpushedErr = unpushed(g, h.Tip)
			s.Proofs[h.Tip] = p
		}
		if ageGated(p.Evidence) {
			h.Created, h.AgeSource = refAge(g, s.CommonDir, h.Ref, logging())
		}
	}
	for i := range s.Remotes {
		r := &s.Remotes[i]
		if ageGated(run(r.Tip).Evidence) {
			r.Created, r.AgeSource = refAge(g, s.CommonDir, r.Ref, logging())
		}
	}
	for i := range s.Worktrees {
		ws := &s.Worktrees[i]
		if !ws.Fact.Detached || !fullCommitID.MatchString(ws.Fact.Head) {
			continue
		}
		// Only for a worktree whose gates read cleanly: git -C on a broken
		// one would answer with an enclosing repository's HEAD.
		if ageGated(run(ws.Fact.Head).Evidence) && ws.GatesErr == "" {
			ws.HeadSince = reflogTime(Git{Dir: ws.Fact.Path, Timeout: g.Timeout}, "HEAD", false)
		}
	}
}

// treeProof is the evidence that proves only that two trees are equal, not
// that the branch's commits are in the base (Ruling 17): trees-match and
// no-added-changes.
func treeProof(ev Evidence) bool { return ev == EvTreesMatch || ev == EvNoAddedChanges }

// netZero answers whether tip's tree is the tree of its merge-base with base:
// the branch changes nothing overall. no-added-changes is that very check,
// so it is yes without asking git again. Any git failure is TriUnknown.
func netZero(g Git, base, tip string, ev Evidence) Tri {
	if ev == EvNoAddedChanges {
		return TriYes
	}
	mb, ok := mergeBase(g, base, tip)
	if !ok {
		return TriUnknown
	}
	mt, ok := treeOf(g, mb)
	if !ok {
		return TriUnknown
	}
	tt, ok := treeOf(g, tip)
	if !ok {
		return TriUnknown
	}
	if mt == tt {
		return TriYes
	}
	return TriNo
}

// unpushed counts the commits on tip that no remote-tracking ref reaches.
// -1 and a message (a row detail: one sanitised line) when git fails.
func unpushed(g Git, tip string) (int, string) {
	args := []string{"rev-list", "--count", tip, "--not", "--remotes"}
	o, err := g.Run(args...)
	if err != nil {
		return -1, gitDetail(err)
	}
	if o.Code != 0 {
		return -1, gitFailure(o, args...).Error()
	}
	n, err := strconv.Atoi(strings.TrimSpace(o.Stdout))
	if err != nil {
		return -1, gitErrText("rev-list --count: " + err.Error())
	}
	return n, ""
}

// reflogTime is the time of ref's oldest reflog entry (its creation) or, with
// oldest false, its newest (its last move); zero when there is no reflog or
// git fails (an unknown age never counts as old).
func reflogTime(g Git, ref string, oldest bool) time.Time {
	t, _, ok := reflogEntry(g, ref, oldest)
	if !ok {
		return time.Time{}
	}
	return t
}

// reflogEntry reads ref's oldest (or newest) reflog entry. empty is true when
// git answered with no entries (no reflog, or an expired one); ok is false on
// any failure to read or parse.
func reflogEntry(g Git, ref string, oldest bool) (t time.Time, empty, ok bool) {
	o, err := g.Run("reflog", "show", "--date=unix", "--format=%gd", ref, "--")
	if err != nil || o.Code != 0 {
		return time.Time{}, false, false
	}
	out := strings.TrimSpace(o.Stdout)
	if out == "" {
		return time.Time{}, true, true
	}
	lines := strings.Split(out, "\n")
	last := lines[0]
	if oldest {
		last = lines[len(lines)-1]
	}
	i, j := strings.LastIndex(last, "@{"), strings.LastIndex(last, "}")
	if i < 0 || j < i {
		return time.Time{}, false, false
	}
	secs, err := strconv.ParseInt(last[i+2:j], 10, 64)
	if err != nil {
		return time.Time{}, false, false
	}
	return time.Unix(secs, 0), false, true
}

// refAge is when a ref was made, for the min-age rule. The rule:
//
//  1. A non-empty reflog: its oldest entry (the ref's creation).
//  2. An empty or absent reflog, with ref logging off (core.logAllRefUpdates
//     false, or unset in a bare repository): nothing tells a new ref from an
//     old one, so the age is unknown and the ref counts as fresh.
//  3. An empty or absent reflog with logging on: the log was expired (git gc,
//     gc.reflogExpire) or the ref was written without one (git clone writes
//     its remote-tracking refs straight into packed-refs and logs none of
//     them). The age is then the file that holds the ref: a loose ref file's
//     mtime (its last write), or, for a ref only in packed-refs, that file's
//     mtime. Clone, pack-refs, gc and ref deletion all rewrite packed-refs,
//     so its mtime is at or after the moment any packed ref entered it: this
//     fails closed.
//
// Cost, accepted: an old ref whose reflog expired reads fresh for MinAge after
// any rewrite of the file holding it (clone, gc, a `fetch --prune` deleting a
// packed ref), then heals by itself. A loose ref that is not a regular file
// (a symlink), a ref in neither place (reftable), or any read error is an
// unknown age (fresh). Known gap, accepted: a user who expires reflogs by
// hand and then backdates the ref file makes a new branch read old.
func refAge(g Git, commonDir, ref string, logging bool) (time.Time, AgeSource) {
	t, empty, ok := reflogEntry(g, ref, true)
	switch {
	case !ok:
		return time.Time{}, AgeFromReflog
	case !empty:
		return t, AgeFromReflog
	case !logging:
		return time.Time{}, AgeFromReflog
	}
	fi, err := os.Lstat(filepath.Join(commonDir, filepath.FromSlash(ref)))
	switch {
	case err == nil && fi.Mode().IsRegular():
		return fi.ModTime(), AgeFromMtime
	case err == nil, !errors.Is(err, fs.ErrNotExist):
		return time.Time{}, AgeFromReflog // not a regular file, or unreadable: unknown
	}
	if !packed(commonDir, ref) {
		return time.Time{}, AgeFromReflog // neither loose nor packed (reftable?): unknown
	}
	fi, err = os.Stat(filepath.Join(commonDir, "packed-refs"))
	if err != nil {
		return time.Time{}, AgeFromReflog
	}
	return fi.ModTime(), AgeFromMtime
}

// packed reports whether packed-refs in commonDir lists ref. A read error is
// false (unknown, so fresh).
func packed(commonDir, ref string) bool {
	b, err := os.ReadFile(filepath.Join(commonDir, "packed-refs"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if _, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "^") && name == ref {
			return true
		}
	}
	return false
}

// refLogging reports whether git logs ref updates here
// (core.logAllRefUpdates): unset means on for a non-bare repository and off
// for a bare one; "always" is on. Any read failure is off (fail closed: an
// unlogged ref's age is unknown).
func refLogging(g Git) bool {
	o, err := g.Run("config", "--get", "core.logAllRefUpdates")
	if err != nil {
		return false
	}
	switch o.Code {
	case 1: // unset
		b, err := g.Run("rev-parse", "--is-bare-repository")
		return err == nil && b.Code == 0 && strings.TrimSpace(b.Stdout) == "false"
	case 0:
	default:
		return false
	}
	if strings.EqualFold(strings.TrimSpace(o.Stdout), "always") {
		return true
	}
	o, err = g.Run("config", "--type=bool", "--get", "core.logAllRefUpdates")
	return err == nil && o.Code == 0 && strings.TrimSpace(o.Stdout) == "true"
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
// to owner/repo; "" for anything else (an ssh host alias, an insteadOf short
// form, GitHub Enterprise, a path), which makes PR state not applicable
// (Snapshot.PRNotApplicable): no row is floored by it.
func ownRepoFromURL(url string) string {
	m := githubURL.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
