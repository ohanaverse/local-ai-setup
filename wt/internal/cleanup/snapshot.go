package cleanup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	Gates    Gates  // with GatesErr set, only Blockers (those found before the error)
	GatesErr string // CheckGates failed: nothing about the worktree is known clean
	// HeadSince is when a detached HEAD last moved (the newest entry of the
	// worktree's HEAD reflog); zero when unknown or not read. Min-age needs it.
	HeadSince time.Time
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
		var err error
		if ws.Gates, err = CheckGates(g, w, gin); err != nil {
			ws.GatesErr = err.Error() // Gates still holds the blockers found before it
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
		if p.Verdict != Merged && !counted[h.Tip] {
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
