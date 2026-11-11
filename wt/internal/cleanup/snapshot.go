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

	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
)

// GatherInputs are what Gather cannot learn from git.
type GatherInputs struct {
	CWD   string // the caller's directory (Current worktrees)
	Fetch bool   // run `git fetch --prune <remote>` for every remote first
	// Fetched is the result of an earlier fetch of this run, reported as the
	// snapshot's when Fetch is false: a re-read after a removal (the tick
	// list's refresh) must not suddenly find every origin row "not fetched".
	Fetched        FetchResult
	OwnRepo        string             // owner/repo for PR filtering; "" parses origin's URL
	ProtectedPaths []string           // registered local_path model artifacts
	Claim          sessionclaim.Claim // this process's claim (its PID); Path is set to the root
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
	Claims          []sessionclaim.Claim
	ClaimsErr       string
	// Records are the well-formed rescue records; a failed listing leaves none.
	Records []RescueRecord
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

	s.Records, _ = ListRecords(g) // an error reads as no records
	gin := s.readClaims(in)
	for _, w := range facts {
		s.Worktrees = append(s.Worktrees, worktreeState(g, w, gin, s.Records))
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

// readClaims fills Claims and ClaimsErr (read-only, on the root's physical
// path; s.Repo must be set) and returns the gate inputs that use them. Gather
// and Rescan both read claims through it.
func (s *Snapshot) readClaims(in GatherInputs) GateInputs {
	claim := in.Claim
	claim.Path = resolve(s.Repo)
	var err error
	if s.Claims, err = sessionclaim.Others(s.CommonDir, claim); err != nil {
		s.ClaimsErr = gitDetail(err) // a row detail: one sanitised line
	}
	claims := s.Claims
	held := func(p string) bool { return len(claims) > 0 && resolve(p) == claim.Path }
	return GateInputs{Base: s.BaseTip, HeldBy: held, ProtectedPaths: in.ProtectedPaths}
}

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
		if (p.Verdict != Merged || treeProof(p.Evidence)) && !counted[h.Tip] {
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
// to owner/repo; "" for anything else, which leaves PR facts unknown.
func ownRepoFromURL(url string) string {
	m := githubURL.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}
