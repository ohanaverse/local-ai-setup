package cleanup

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Kind is what a row would remove.
type Kind string

const (
	KindWorktree Kind = "worktree" // a worktree, carrying its branch
	KindBranch   Kind = "branch"   // a local branch no worktree holds
	KindRemote   Kind = "remote"   // a remote-tracking branch (its remote's branch)
)

// Status is how sure cleanup is that a row is safe to remove.
type Status string

const (
	StatusProven     Status = "proven"      // a ladder rung holds, nothing else applies
	StatusLessSure   Status = "less_sure"   // a weak signal, a soft issue, or Unknown
	StatusUniqueWork Status = "unique_work" // work exists nowhere else
	StatusInUse      Status = "in_use"      // a hard blocker: cannot be removed
)

// GHState says how much of the PR lookup gh answered.
type GHState string

const (
	GHOK          GHState = "ok"          // every lookup answered
	GHUnavailable GHState = "unavailable" // no lookup answered
	GHPartial     GHState = "partial"     // some did, some did not
)

// Reason codes (Row.Reasons): stable strings, kept in the JSON.
const (
	ReasonNoDefaultBranch       = "no_default_branch"       // Unknown: no default branch, so no base
	ReasonMergeProofUnavailable = "merge_proof_unavailable" // Unknown: the ladder stopped (git error, no common history, configured merge driver)
	ReasonGatesUnavailable      = "gates_unavailable"       // Unknown: the worktree's state could not be read
	ReasonBranchUnknown         = "branch_unknown"          // Unknown: the worktree's branch is not among the refs
	ReasonPRUnknown             = "pr_unknown"              // Unknown: no git proof, and the PR lookup failed
	ReasonPRMergeUnknown        = "pr_merge_unknown"        // Unknown: a merged PR's merge commit could not be checked against the base
	ReasonUnpushedUnknown       = "unpushed_unknown"        // Unknown: counting unpushed commits failed
	ReasonCannotVerify          = "cannot_verify"           // gh did not answer for every branch, and this row needs it
	ReasonUnpushed              = "unpushed"                // unique: commits no remote-tracking ref reaches
	ReasonNetZeroUnpushed       = "net_zero_unpushed"       // soft: proven only by equal trees, the branch nets to nothing, and its commits are on no remote-tracking ref
	ReasonPRHeadMoved           = "pr_head_moved"           // unique: a merged PR's head is not the tip
	ReasonOpenPR                = "open_pr"                 // unique (local rows): a PR on this head is open
	ReasonDirty                 = "dirty"                   // unique: tracked changes or non-ignored untracked files
	ReasonDetachedUnique        = "detached_unique"         // unique: a detached HEAD the base does not reach
	ReasonNotMerged             = "not_merged"              // no proof and no signal (pushed local branch: less sure; remote: unique)
	ReasonIgnoredFiles          = "ignored_files"           // soft: ignored files die with the directory
	ReasonWeakSignal            = "weak_signal"             // soft: cherry or patch-id only, never a proof
	ReasonFreshBranch           = "fresh_branch"            // soft: same-commit or ancestor, younger than MinAge (or of unknown age)
	ReasonBaseIsLocal           = "base_is_local"           // soft: no remote default; the base is the local one
	ReasonNoMergedPR            = "no_merged_pr"            // soft (remote rows): merged content but no merged PR
	ReasonClaimsUnavailable     = "claims_unavailable"      // the session claims could not be read
)

// Blocker codes beyond CheckGates' own.
const (
	BlockDefaultBranch = "default_branch"  // a worktree on the default branch
	BlockOpenPR        = "open_pr"         // remote rows: deleting the head closes an open PR
	BlockBaseOfOpenPR  = "base_of_open_pr" // remote rows: an open PR targets this branch
)

// DefaultMinAge is how old a branch (or detached HEAD) at or behind the base
// must be before same-commit or ancestor proves anything: a branch made a
// minute ago looks merged.
const DefaultMinAge = 10 * time.Minute

// Options are Classify's only inputs besides the Snapshot.
type Options struct {
	Now    time.Time     // zero: every same-commit branch counts as fresh
	MinAge time.Duration // <= 0 means DefaultMinAge
}

// Row is one removable thing and its verdict.
type Row struct {
	Kind     Kind
	Name     string // branch name; a detached worktree's directory name
	Path     string // worktree rows
	Branch   string // worktree rows: the branch checked out ("" detached); branch and remote rows: the name
	Remote   string // remote rows: the remote; branch rows: the upstream's remote
	Status   Status
	Evidence Evidence // the proof found, even when something else lowers the status; "" none
	Unknown  bool     // some fact could not be determined; never proven then
	Reasons  []Reason
	Blockers []Reason
	PR       *PRInfo // the PR the verdict rests on, if any
	AgeDays  int
	AgeKnown bool // AgeDays is a real age (0 then means under a day), not "could not tell"
}

// Report is a classified snapshot.
type Report struct {
	Repo  string
	Base  Base
	Fetch FetchResult
	GH    GHState
	Rows  []Row
}

// judge collects one row's findings; status applies the precedence.
type judge struct {
	blockers, unique, unknown, soft []Reason
	ev                              Evidence
	pr                              *PRInfo
}

func (j *judge) block(code, detail string)  { j.blockers = append(j.blockers, Reason{code, detail}) }
func (j *judge) uniq(code, detail string)   { j.unique = append(j.unique, Reason{code, detail}) }
func (j *judge) unsure(code, detail string) { j.unknown = append(j.unknown, Reason{code, detail}) }
func (j *judge) weak(code, detail string)   { j.soft = append(j.soft, Reason{code, detail}) }

// status is first match wins: in use, unique work, less sure, proven.
func (j *judge) status() Status {
	switch {
	case len(j.blockers) > 0:
		return StatusInUse
	case len(j.unique) > 0:
		return StatusUniqueWork
	case len(j.unknown) > 0 || len(j.soft) > 0 || j.ev == "":
		return StatusLessSure
	}
	return StatusProven
}

func (j *judge) fill(r *Row) {
	if j.ev == "" && len(j.blockers)+len(j.unique)+len(j.unknown)+len(j.soft) == 0 {
		j.weak(ReasonNotMerged, "no proof found")
	}
	r.Status = j.status()
	r.Evidence = j.ev
	r.Unknown = len(j.unknown) > 0
	r.Blockers = j.blockers
	r.Reasons = slices.Concat(j.unique, j.unknown, j.soft)
	if j.pr != nil {
		pr := *j.pr
		r.PR = &pr
	}
}

type classifier struct {
	s      Snapshot
	o      Options
	gh     GHState
	ghNote string
}

// Classify judges every worktree, local branch and remote branch in s. It is
// pure: no I/O, and no clock but o.Now.
func Classify(s Snapshot, o Options) Report {
	if o.MinAge <= 0 {
		o.MinAge = DefaultMinAge
	}
	c := classifier{s: s, o: o, gh: ghState(s.PRs)}
	c.ghNote = "gh answered for only some branches"
	if c.gh == GHUnavailable {
		c.ghNote = "gh answered for no branch"
	}
	r := Report{Repo: s.Repo, Base: s.Base, Fetch: s.Fetch, GH: c.gh}
	held := map[string]bool{}
	for _, ws := range s.Worktrees {
		r.Rows = append(r.Rows, c.worktree(ws))
		if ws.Fact.Branch != "" {
			held[strings.ToLower(ws.Fact.Branch)] = true
		}
	}
	for _, h := range s.Heads {
		// Folded case: on a case-insensitive volume a worktree's HEAD may
		// spell the branch differently from its ref; an over-match only
		// hides a branch row, never lists a checked-out branch.
		if held[strings.ToLower(h.Ref)] {
			continue
		}
		r.Rows = append(r.Rows, c.branchRow(h))
	}
	for _, rf := range s.Remotes {
		r.Rows = append(r.Rows, c.remoteRow(rf))
	}
	slices.SortStableFunc(r.Rows, func(a, b Row) int {
		return cmp.Or(
			cmp.Compare(statusOrder(a.Status), statusOrder(b.Status)),
			cmp.Compare(kindOrder(a.Kind), kindOrder(b.Kind)),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Remote, b.Remote),
			cmp.Compare(a.Path, b.Path),
		)
	})
	return r
}

// statusOrder is the display order of the groups.
func statusOrder(s Status) int {
	return slices.Index([]Status{StatusProven, StatusLessSure, StatusUniqueWork, StatusInUse}, s)
}

func kindOrder(k Kind) int { return slices.Index([]Kind{KindWorktree, KindBranch, KindRemote}, k) }

// ghState is ok when no lookup is Unknown, unavailable when every one is (and
// there is at least one), else partial.
func ghState(prs map[string]PRResult) GHState {
	unknown := 0
	for _, r := range prs {
		if r.Lookup == LookupUnknown {
			unknown++
		}
	}
	switch {
	case unknown == 0:
		return GHOK
	case unknown == len(prs):
		return GHUnavailable
	}
	return GHPartial
}

func (c *classifier) isDefault(name string) bool {
	return !c.s.NoDefault && c.s.isDefault(name)
}

func (c *classifier) worktree(ws WorktreeState) Row {
	w := ws.Fact
	branch := strings.TrimPrefix(w.Branch, "refs/heads/")
	row := Row{Kind: KindWorktree, Path: w.Path, Branch: branch, Name: branch}
	if branch == "" {
		row.Name = filepath.Base(w.Path)
	}
	var j judge
	g := ws.Gates
	// The blockers found before a gates error still block: in use wins.
	j.blockers = append(j.blockers, g.Blockers...)
	if ws.GatesErr != "" {
		j.unsure(ReasonGatesUnavailable, ws.GatesErr)
	} else {
		if g.Dirty {
			j.uniq(ReasonDirty, "uncommitted or untracked files")
		}
		if g.DetachedUnique {
			j.uniq(ReasonDetachedUnique, "detached HEAD "+short(w.Head)+" is not in the base")
		}
		if g.IgnoredCount > 0 {
			entryWord, byteWord := "entries", "bytes"
			if g.IgnoredCount == 1 {
				entryWord = "entry"
			}
			if g.IgnoredBytes == 1 {
				byteWord = "byte"
			}
			j.weak(ReasonIgnoredFiles, fmt.Sprintf("%d ignored %s, %d %s", g.IgnoredCount, entryWord, g.IgnoredBytes, byteWord))
		}
	}
	if w.Primary && c.s.ClaimsErr != "" {
		j.weak(ReasonClaimsUnavailable, c.s.ClaimsErr)
	}

	switch {
	case c.s.NoDefault:
		j.unsure(ReasonNoDefaultBranch, "no default branch, so no base to compare with")
	case branch != "" && c.isDefault(branch):
		j.block(BlockDefaultBranch, branch)
	case branch != "":
		h, ok := c.head(w.Branch)
		if !ok {
			j.unsure(ReasonBranchUnknown, w.Branch+" is not among the branch refs")
			break
		}
		c.local(&j, h)
		c.setAge(&row, h)
	case w.Detached:
		p, ok := c.s.Proofs[w.Head]
		switch {
		case !ok || p.Verdict == Undetermined:
			j.unsure(ReasonMergeProofUnavailable, mergeProofDetail)
		case p.Verdict == Merged:
			j.ev = p.Evidence
		}
		// A detached HEAD's age is when it last moved there.
		c.minAge(&j, RefFact{Created: ws.HeadSince})
		c.setAge(&row, RefFact{Created: ws.HeadSince})
	}
	c.capFallback(&j)
	j.fill(&row)
	return row
}

// head finds the local head for a worktree's branch ref: exact, else with
// case folded (a case-insensitive volume).
func (c *classifier) head(ref string) (RefFact, bool) {
	for _, h := range c.s.Heads {
		if h.Ref == ref {
			return h, true
		}
	}
	for _, h := range c.s.Heads {
		if strings.EqualFold(h.Ref, ref) {
			return h, true
		}
	}
	return RefFact{}, false
}

func (c *classifier) branchRow(h RefFact) Row {
	row := Row{Kind: KindBranch, Name: h.Name, Branch: h.Name, Remote: h.Remote}
	var j judge
	if c.s.NoDefault {
		j.unsure(ReasonNoDefaultBranch, "no default branch, so no base to compare with")
	} else {
		c.local(&j, h)
		c.setAge(&row, h)
	}
	c.capFallback(&j)
	j.fill(&row)
	return row
}

const mergeProofDetail = "the merge checks could not finish (a git error, no common history, or a configured merge driver)"

// local judges a local branch's content: the ladder, the PR rung, the
// open-PR veto, and (only when nothing proves a merge) unpushed commits and a
// merged PR whose head moved on.
func (c *classifier) local(j *judge, h RefFact) {
	p, ok := c.s.Proofs[h.Tip]
	res := c.s.PRs[h.Name] // a miss is LookupUnknown
	switch {
	case !ok || p.Verdict == Undetermined:
		j.unsure(ReasonMergeProofUnavailable, mergeProofDetail)
	case p.Verdict == Merged:
		j.ev = p.Evidence
		c.netZeroUnpushed(j, p)
	default:
		c.prTip(j, h, res)
	}
	if res.Lookup == LookupKnown {
		if pr := findPR(res, func(pr PRInfo) bool { return pr.State == "OPEN" }); pr != nil {
			j.uniq(ReasonOpenPR, fmt.Sprintf("PR #%d is open", pr.Number))
			j.pr = pr
		}
	}
	if j.ev == "" {
		switch {
		case ok && p.Unpushed > 0:
			noun := "commits"
			if p.Unpushed == 1 {
				noun = "commit"
			}
			j.uniq(ReasonUnpushed, fmt.Sprintf("%d %s on no remote-tracking ref", p.Unpushed, noun))
		case ok && p.Unpushed < 0:
			j.unsure(ReasonUnpushedUnknown, p.UnpushedErr)
		}
		if res.Lookup == LookupKnown {
			if pr := c.movedPR(res, h.Tip); pr != nil {
				j.uniq(ReasonPRHeadMoved, fmt.Sprintf("merged PR #%d's head %s is not the tip %s", pr.Number, short(pr.HeadRefOid), short(h.Tip)))
				if j.pr == nil {
					j.pr = pr
				}
			}
		}
		if ok && p.Verdict == NotMerged {
			if res.Lookup == LookupUnknown {
				j.unsure(ReasonPRUnknown, cmp.Or(res.Err, "no PR lookup for this branch"))
			}
			if len(p.Weak) > 0 {
				j.weak(ReasonWeakSignal, weakDetail(p.Weak))
			}
		}
	}
	c.minAge(j, h)
}

// netZeroUnpushed is Ruling 17. trees-match and no-added-changes prove only
// that two trees are equal. When the tip's tree is its merge-base's (the
// branch nets to nothing: say a commit and its revert) and some of its
// commits are on no remote-tracking ref, those commits exist only here, so
// the row is less sure, never proven. A real squash changes the tree
// (NetZero no) and is untouched. An unknown NetZero or unpushed count with
// commits possibly unpushed is Unknown (fail closed).
func (c *classifier) netZeroUnpushed(j *judge, p Proof) {
	if !treeProof(p.Evidence) || p.NetZero == TriNo || p.Unpushed == 0 {
		return
	}
	switch {
	case p.Unpushed < 0:
		j.unsure(ReasonUnpushedUnknown, p.UnpushedErr)
	case p.NetZero == TriUnknown:
		j.unsure(ReasonMergeProofUnavailable, "could not tell whether the branch's commits undo each other")
	default:
		noun := "commits"
		if p.Unpushed == 1 {
			noun = "commit"
		}
		j.weak(ReasonNetZeroUnpushed, fmt.Sprintf("%d %s on no remote-tracking ref; together they change nothing", p.Unpushed, noun))
	}
}

// remoteRow judges a remote-tracking branch. Proven needs a complete gh
// answer, a merged PR for the head, no open PR on it or based on it, and
// content in the base.
func (c *classifier) remoteRow(rf RefFact) Row {
	row := Row{Kind: KindRemote, Name: rf.Name, Branch: rf.Name, Remote: rf.Remote}
	var j judge
	if c.s.NoDefault {
		j.unsure(ReasonNoDefaultBranch, "no default branch, so no base to compare with")
		j.fill(&row)
		return row
	}
	res := c.s.PRs[rf.Name] // a miss is LookupUnknown
	switch {
	case res.Lookup == LookupUnknown:
		j.unsure(ReasonCannotVerify, cmp.Or(res.Err, "no PR lookup for this branch"))
	case c.gh != GHOK:
		j.unsure(ReasonCannotVerify, c.ghNote)
	}
	p, ok := c.s.Proofs[rf.Tip]
	switch {
	case !ok || p.Verdict == Undetermined:
		j.unsure(ReasonMergeProofUnavailable, mergeProofDetail)
	case p.Verdict == Merged:
		j.ev = p.Evidence
	default:
		c.prTip(&j, rf, res)
	}
	if res.Lookup == LookupKnown {
		if pr := findPR(res, func(pr PRInfo) bool { return pr.State == "OPEN" }); pr != nil {
			j.block(BlockOpenPR, fmt.Sprintf("PR #%d is open on this branch", pr.Number))
			j.pr = pr
		}
	}
	for _, n := range c.openPRsBasedOn(rf.Name) {
		j.block(BlockBaseOfOpenPR, fmt.Sprintf("open PR #%d targets this branch", n))
	}
	if j.ev != "" {
		// Proven needs a MERGED PR for the head; an unknown lookup is already
		// unsure above, so the soft reason is only for an answered "none".
		merged := findPR(res, func(pr PRInfo) bool { return pr.State == "MERGED" })
		switch {
		case merged == nil && res.Lookup != LookupUnknown:
			j.weak(ReasonNoMergedPR, "content is in the base, but no PR for this branch merged")
		case merged != nil && j.pr == nil:
			j.pr = merged
		}
	} else if ok && p.Verdict == NotMerged {
		if len(p.Weak) > 0 {
			j.weak(ReasonWeakSignal, weakDetail(p.Weak))
		} else {
			j.uniq(ReasonNotMerged, "its commits are not in the base")
		}
	}
	c.minAge(&j, rf)
	c.capFallback(&j)
	j.fill(&row)
	return row
}

// prTip is the PR rung: a MERGED PR into the default branch whose head is the
// tip and whose merge commit is in the base.
func (c *classifier) prTip(j *judge, r RefFact, res PRResult) {
	if res.Lookup != LookupKnown {
		return
	}
	for _, pr := range res.PRs {
		if pr.State != "MERGED" || pr.BaseRef != c.s.Base.Default || pr.HeadRefOid != r.Tip {
			continue
		}
		switch c.s.Merges[pr.MergeCommit] {
		case TriYes:
			j.ev = EvPRTip
			j.pr = &pr
			if c.gh != GHOK {
				j.weak(ReasonCannotVerify, c.ghNote)
			}
			return
		case TriUnknown:
			j.unsure(ReasonPRMergeUnknown, fmt.Sprintf("PR #%d's merge commit could not be checked against the base", pr.Number))
		}
	}
}

// movedPR is a MERGED PR into the default branch whose head is not tip.
func (c *classifier) movedPR(res PRResult, tip string) *PRInfo {
	return findPR(res, func(pr PRInfo) bool {
		return pr.State == "MERGED" && pr.BaseRef == c.s.Base.Default && pr.HeadRefOid != tip
	})
}

// openPRsBasedOn lists the open PRs, among every lookup, whose base is name.
func (c *classifier) openPRsBasedOn(name string) []int {
	var out []int
	for _, res := range c.s.PRs {
		for _, pr := range res.PRs {
			if pr.State == "OPEN" && pr.BaseRef == name {
				out = append(out, pr.Number)
			}
		}
	}
	slices.Sort(out)
	return out
}

func findPR(res PRResult, match func(PRInfo) bool) *PRInfo {
	for _, pr := range res.PRs {
		if match(pr) {
			return &pr
		}
	}
	return nil
}

// ageGated is the evidence min-age applies to: a tip at or behind the base,
// so it has no commits outside it. A branch just made from a local main that
// lags the base is an ancestor and would otherwise read proven.
func ageGated(ev Evidence) bool { return ev == EvSameCommit || ev == EvAncestor }

// minAge demotes an age-gated proof on a branch younger than MinAge, or of
// unknown age (no creation time): a brand-new branch looks merged.
func (c *classifier) minAge(j *judge, r RefFact) {
	if !ageGated(j.ev) {
		return
	}
	if r.Created.IsZero() || c.o.Now.IsZero() {
		j.weak(ReasonFreshBranch, "no commits outside the base, and its age is unknown")
		return
	}
	b := born(r)
	if r.AgeSource == AgeFromMtime {
		b = r.Created // the last write of the file holding the ref is its own age
	}
	if age := c.o.Now.Sub(b); age < c.o.MinAge {
		j.weak(ReasonFreshBranch, fmt.Sprintf("no commits outside the base, and only %s old (under %s)", age.Round(time.Second), c.o.MinAge))
	}
}

// capFallback keeps every verdict below proven when the base is the local
// default branch (no remote default to compare with).
func (c *classifier) capFallback(j *judge) {
	if c.s.Base.Fallback && j.ev != "" {
		j.weak(ReasonBaseIsLocal, "the base is the local "+c.s.Base.Ref+", not a remote branch")
	}
}

// born is the later of the tip's commit time and the ref's creation.
func born(r RefFact) time.Time {
	if r.Created.After(r.TipTime) {
		return r.Created
	}
	return r.TipTime
}

// setAge records the row's age in whole days. It stays unknown (AgeKnown
// false) with no clock, no birth time, or a birth in the future.
func (c *classifier) setAge(row *Row, r RefFact) {
	b := born(r)
	if c.o.Now.IsZero() || b.IsZero() || !c.o.Now.After(b) {
		return
	}
	row.AgeDays = int(c.o.Now.Sub(b) / (24 * time.Hour))
	row.AgeKnown = true
}

func weakDetail(ws []Evidence) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = string(w)
	}
	return strings.Join(parts, ", ") + " only; not a proof"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
