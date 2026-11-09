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
	ReasonNotChecked            = "not_checked"             // Unknown: the pull-request state of another remote's branch is never asked of gh
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
	ReasonPreserved             = "preserved"               // a rescue record covers the worktree's commits (informational, never lowers the status) or also its uncommitted work (soft: less sure)
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

// MinAgeFloor is the smallest MinAge `wt cleanup --min-age` accepts. Any
// positive value used to be taken, and 1ns turns the fresh-branch rule off:
// a branch made a second ago reads as proven merged. Classify itself does not
// enforce it (a test may pass any Options); the command refuses a lower value.
const MinAgeFloor = time.Minute

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

	// The facts below serve removal only and are never serialised (RowJSON
	// is built field by field; TestCleanupJSONUnchangedByNewRowFields).

	// Ref is the full refname the row names: refs/heads/<b> for a branch row
	// and a worktree on a branch (the spelling for-each-ref gave, when the
	// branch is among the heads), refs/remotes/<r>/<b> for a remote row, ""
	// for a detached worktree.
	Ref string
	// Tip is Ref's full commit id as gathered ("" when Ref is "" or its head
	// was not found).
	Tip string
	// Head is a worktree row's HEAD commit id; "" for branch and remote rows.
	Head string
	// Losses is what removing the row would lose.
	Losses Losses
	// BranchProven is Ruling A: the branch alone, ignoring the worktree's
	// gates (dirty or ignored files, blockers), would classify proven. For a
	// branch row it is Status == proven; for a remote row or a detached
	// worktree it is false.
	BranchProven bool
	// Preserved is the rescue record the classifier applied to a worktree
	// row: its unpushed or detached commits read as covered (evidence
	// rescued), and with CoversDirt its uncommitted work too (whatever the
	// evidence, which a merged branch keeps). nil when no record applies, and
	// always nil for branch and remote rows.
	Preserved *Preserved
}

// Losses is what removing a row would lose. A zero Losses loses nothing that
// exists only here; any other value names a loss, and GatesUnknown or a
// negative Unpushed say a loss could not be ruled out.
type Losses struct {
	Dirty          bool  // tracked changes or non-ignored untracked files
	IgnoredEntries int   // ignored entries deleted with the directory
	IgnoredBytes   int64 // their size on disk
	// Unpushed: 0 = the content is in the base; >0 counts commits reachable
	// from no remote-tracking ref (only counted for trees-match /
	// no-added-changes proofs and unproven branches); -1 when the count
	// failed or the branch's tip has no proof at all.
	Unpushed       int
	DetachedUnique bool // a detached HEAD the base does not reach
	// GatesUnknown: the worktree's gates could not be read (GatesErr), so
	// Dirty, IgnoredEntries, IgnoredBytes and DetachedUnique are zero only
	// because nothing was read, not because nothing would be lost.
	GatesUnknown bool
}

// EffectiveLosses is what removing r would lose once a rescue record is
// counted: for a row with Preserved, a counted Unpushed (> 0) and a detached
// HEAD's commits read as covered, and Dirty too when the record also holds
// the uncommitted work (CoversDirt); an uncounted Unpushed (-1), ignored
// files and GatesUnknown are untouched, since a record keeps none of them.
// Without Preserved it is Losses unchanged. Losses itself stays raw.
func (r Row) EffectiveLosses() Losses {
	l := r.Losses // losses: raw, the view is built from the raw value
	if r.Preserved == nil {
		return l
	}
	if l.Unpushed > 0 {
		l.Unpushed = 0
	}
	l.DetachedUnique = false
	if r.Preserved.CoversDirt {
		l.Dirty = false
	}
	return l
}

// Report is a classified snapshot.
type Report struct {
	Repo  string
	Base  Base
	Fetch FetchResult
	GH    GHState
	Rows  []Row
	// MinAge is the Options.MinAge Classify used (DefaultMinAge when none
	// was given). Not part of the JSON contract.
	MinAge time.Duration
	// Notes are extra header lines the caller adds (HeaderLines appends
	// them, one sanitised line each). Not part of the JSON contract.
	Notes []string
}

// judge collects one row's findings; status applies the precedence. info
// holds informational reasons: shown after the others, never counted by
// status or by fill's "nothing found" test.
type judge struct {
	blockers, unique, unknown, soft []Reason
	info                            []Reason
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
	r.Reasons = slices.Concat(j.unique, j.unknown, j.soft, j.info)
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
	r := Report{Repo: s.Repo, Base: s.Base, Fetch: s.Fetch, GH: c.gh, MinAge: o.MinAge}
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
			cmp.Compare(a.Status.Rank(), b.Status.Rank()),
			cmp.Compare(kindOrder(a.Kind), kindOrder(b.Kind)),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Remote, b.Remote),
			cmp.Compare(a.Path, b.Path),
		)
	})
	return r
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
	row := Row{Kind: KindWorktree, Path: w.Path, Branch: branch, Name: branch, Ref: w.Branch, Head: w.Head}
	if branch == "" {
		row.Name = filepath.Base(w.Path)
	} else {
		// Unknown until the branch's head is found and its proof read.
		row.Losses.Unpushed = -1
	}
	var j judge
	g := ws.Gates
	// The blockers found before a gates error still block: in use wins.
	j.blockers = append(j.blockers, g.Blockers...)
	if ws.GatesErr != "" {
		j.unsure(ReasonGatesUnavailable, ws.GatesErr)
		row.Losses.GatesUnknown = true
	} else {
		// Only gates that read cleanly say what is lost; a gates error
		// leaves them zero and sets GatesUnknown instead.
		row.Losses.Dirty = g.Dirty
		row.Losses.IgnoredEntries = g.IgnoredCount
		row.Losses.IgnoredBytes = g.IgnoredBytes
		row.Losses.DetachedUnique = g.DetachedUnique
		// Covered dirt is not raised; the preserved reason (soft) says it
		// instead, and Losses.Dirty stays raw.
		if g.Dirty && !c.dirtCovered(ws) {
			j.uniq(ReasonDirty, "uncommitted or untracked files")
		}
		if g.DetachedUnique && !c.detachedPreserved(ws) {
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
		// The exact spelling for-each-ref gave: on a case-insensitive
		// volume the worktree's HEAD may spell the branch differently.
		row.Ref, row.Tip = h.Ref, h.Tip
		row.Losses.Unpushed = c.unpushedOf(h)
		covered := c.branchPreserved(ws, h)
		c.local(&j, h, covered)
		c.setAge(&row, h)
		row.BranchProven = c.branchProven(h, covered)
		if j.ev == EvRescued {
			row.Preserved = c.preserve(&j, ws.Preserved)
		}
	case w.Detached:
		p, ok := c.s.Proofs[w.Head]
		switch {
		case !ok || p.Verdict == Undetermined:
			j.unsure(ReasonMergeProofUnavailable, mergeProofDetail)
		case p.Verdict == Merged:
			j.ev = p.Evidence
		case c.detachedPreserved(ws):
			// Set before minAge: rescued is not age-gated.
			j.ev = EvRescued
			row.Preserved = c.preserve(&j, ws.Preserved)
		}
		// A detached HEAD's age is when it last moved there.
		c.minAge(&j, RefFact{Created: ws.HeadSince})
		c.setAge(&row, RefFact{Created: ws.HeadSince})
	}
	// Covered dirt on a row whose commits the ladder (not the record)
	// accounts for, a merged branch or a detached HEAD the base reaches: the
	// record still applies, so the dirt is not silently left out of the
	// reasons and the effective losses. The evidence stays the ladder's.
	if row.Preserved == nil && len(j.blockers) == 0 && c.dirtCovered(ws) {
		row.Preserved = c.preserve(&j, ws.Preserved)
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
	row := Row{Kind: KindBranch, Name: h.Name, Branch: h.Name, Remote: h.Remote, Ref: h.Ref, Tip: h.Tip}
	row.Losses.Unpushed = -1
	var j judge
	if c.s.NoDefault {
		j.unsure(ReasonNoDefaultBranch, "no default branch, so no base to compare with")
	} else {
		row.Losses.Unpushed = c.unpushedOf(h)
		c.local(&j, h, false)
		c.setAge(&row, h)
	}
	c.capFallback(&j)
	j.fill(&row)
	row.BranchProven = row.Status == StatusProven
	return row
}

// branchProven is Ruling A: h's own judgement, the one a worktree row on it
// is built from but without the worktree's gates, is proven. covered is the
// same flag the worktree row passed to local.
func (c *classifier) branchProven(h RefFact, covered bool) bool {
	var j judge
	c.local(&j, h, covered)
	c.capFallback(&j)
	return j.status() == StatusProven
}

// preservable is whether ws's record may be applied at all: one was matched,
// the gates read cleanly with no hard blocker, and any dirt they found is
// held by the record too (CoversDirt).
func preservable(ws WorktreeState) bool {
	return ws.Preserved != nil && ws.GatesErr == "" && len(ws.Gates.Blockers) == 0 &&
		(!ws.Gates.Dirty || ws.Preserved.CoversDirt)
}

// dirtCovered is whether ws's uncommitted work reads as covered: the record
// applies (preservable) and holds the dirt, the worktree is not on the
// default branch, its HEAD is the head the record was written from, and for
// a branch its fresh tip is that head too. When true the row gets Preserved
// (from the ladder's rescued rung or the attach step in worktree) and no
// dirty reason; when false the row reads as with no record.
func (c *classifier) dirtCovered(ws WorktreeState) bool {
	w := ws.Fact
	if !preservable(ws) || !ws.Gates.Dirty || !ws.Preserved.CoversDirt ||
		!IsFullCommitID(w.Head) || w.Head != ws.Preserved.SourceHead {
		return false
	}
	branch := strings.TrimPrefix(w.Branch, "refs/heads/")
	switch {
	case branch == "":
		return w.Detached
	case c.isDefault(branch):
		return false
	}
	h, ok := c.head(w.Branch)
	return ok && h.Tip == ws.Preserved.SourceHead
}

// branchPreserved is whether a rescue record covers the unpushed commits of
// the worktree's branch h: the record applies, the tip has no merge proof
// (NotMerged), its counted unpushed commits exist (> 0, never unknown), and
// the fresh tip is the head the record was written from. A dirty worktree
// also needs its dirt covered (dirtCovered), so a row never reads its commits
// covered while it still shows the dirt.
func (c *classifier) branchPreserved(ws WorktreeState, h RefFact) bool {
	if !preservable(ws) || (ws.Gates.Dirty && !c.dirtCovered(ws)) ||
		!IsFullCommitID(h.Tip) || h.Tip != ws.Preserved.SourceHead {
		return false
	}
	p, ok := c.s.Proofs[h.Tip]
	return ok && p.Verdict == NotMerged && p.Unpushed > 0
}

// detachedPreserved is whether a rescue record covers a detached worktree's
// HEAD: the record applies, the HEAD is unique (DetachedUnique), has no merge
// proof (NotMerged) and is the head the record was written from; a dirty one
// also needs its dirt covered (dirtCovered).
func (c *classifier) detachedPreserved(ws WorktreeState) bool {
	w := ws.Fact
	if !w.Detached || !preservable(ws) || (ws.Gates.Dirty && !c.dirtCovered(ws)) || !ws.Gates.DetachedUnique ||
		!IsFullCommitID(w.Head) || w.Head != ws.Preserved.SourceHead {
		return false
	}
	p, ok := c.s.Proofs[w.Head]
	return ok && p.Verdict == NotMerged
}

// preserve adds the preserved reason and returns the row's copy of p. It is
// soft when the record also holds uncommitted work (removing the original
// then still needs the loss accepted), informational otherwise. The trailer
// values are untrusted text, so they go through flat.
func (c *classifier) preserve(j *judge, p *Preserved) *Preserved {
	detail := "preserved by " + flat(p.Ref) + " (copy: " + flat(p.DestPath) + ")"
	if p.CoversDirt {
		j.weak(ReasonPreserved, detail+", including its uncommitted changes")
	} else {
		j.info = append(j.info, Reason{ReasonPreserved, detail})
	}
	cp := *p
	return &cp
}

// unpushedOf is Losses.Unpushed for local head h: 0 when a proof other than
// trees-match / no-added-changes puts the content in the base (the count is
// not taken then); otherwise the counted commits reachable from no
// remote-tracking ref (trees-match / no-added-changes proofs and unproven
// branches); -1 when the count failed or the tip has no proof.
func (c *classifier) unpushedOf(h RefFact) int {
	p, ok := c.s.Proofs[h.Tip]
	switch {
	case !ok || p.Unpushed < 0:
		return -1
	case p.Verdict == Merged && !treeProof(p.Evidence):
		return 0
	}
	return p.Unpushed
}

const mergeProofDetail = "the merge checks could not finish (a git error, no common history, or a configured merge driver)"

// local judges a local branch's content: the ladder, the PR rung, the
// open-PR veto, and (only when nothing proves a merge) unpushed commits and a
// merged PR whose head moved on. covered says a rescue record covers the
// unpushed commits (branchPreserved): unpushed is not raised, every other
// check runs as without it, and the evidence becomes rescued afterwards
// (before minAge, which does not gate it).
func (c *classifier) local(j *judge, h RefFact, covered bool) {
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
		case ok && p.Unpushed > 0 && !covered:
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
				j.unsure(ReasonPRUnknown, cmp.Or(res.Err, noPRLookupDetail))
			}
			if len(p.Weak) > 0 {
				j.weak(ReasonWeakSignal, weakDetail(p.Weak))
			}
			if covered {
				j.ev = EvRescued
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
	row := Row{Kind: KindRemote, Name: rf.Name, Branch: rf.Name, Remote: rf.Remote, Ref: rf.Ref, Tip: rf.Tip}
	var j judge
	if c.s.NoDefault {
		j.unsure(ReasonNoDefaultBranch, "no default branch, so no base to compare with")
		j.fill(&row)
		return row
	}
	// PRs and BasePRs are keyed by a bare branch name and describe the own
	// repository, which is origin: another remote's row neither reads them
	// nor has a lookup of its own (nothing asks gh about upstream/foo), so
	// it is judged on git's proofs, and its PR state is unknown (below).
	origin := rf.Remote == "origin"
	var res PRResult // zero value (LookupUnknown) for another remote's row, which has no lookup
	if origin {
		res = c.s.PRs[rf.Name] // a miss is LookupUnknown
		switch {
		case res.Lookup == LookupUnknown:
			j.unsure(ReasonCannotVerify, cmp.Or(res.Err, noPRLookupDetail))
		case c.gh != GHOK:
			j.unsure(ReasonCannotVerify, c.ghNote)
		}
	} else {
		// Fail closed: nothing asks gh about another remote's branch, so its
		// pull-request state is unknown, never "no PR". The row can read at
		// most less_sure; git's proofs below still give its evidence. This
		// is not a gh failure (not_checked, not cannot_verify), so the gh
		// wording and the header's gh line never claim it is.
		j.unsure(ReasonNotChecked, notCheckedDetail)
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
	// The origin checks (this run's origin fetch, the live "who targets this
	// branch" query, the PR rung the removal engine re-verifies) apply to an
	// origin row only: they are origin facts, keyed by a bare name, and
	// nothing deletes any other remote's branch.
	if origin {
		if why := c.originFetchProblem(); why != "" {
			j.unsure(ReasonCannotVerify, why)
		}
	}
	candidate := j.ev != ""
	deps, baseErr := c.dependants(rf.Name, candidate, origin)
	if baseErr != "" {
		j.unsure(ReasonCannotVerify, baseErr)
	}
	for _, n := range deps {
		j.block(BlockBaseOfOpenPR, fmt.Sprintf("open PR #%d targets this branch", n))
	}
	if j.ev != "" {
		// Proven needs a MERGED PR into the default branch whose head is this
		// tip and whose merge commit is in the base (the removal engine
		// re-verifies exactly that before it pushes); an unknown lookup is
		// already unsure above, so the soft reasons are only for an answered
		// lookup.
		if origin && res.Lookup != LookupUnknown {
			c.remotePRRung(&j, rf, res)
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

// originFetchProblem says why this run's fetch does not support deleting an
// origin branch, or "": origin must have been fetched cleanly now, or a PR
// opened against the branch since the last fetch is invisible.
func (c *classifier) originFetchProblem() string {
	f := c.s.Fetch
	switch {
	case f.OK:
		return ""
	case f.Skipped != "":
		return "origin was not fetched (" + f.Skipped + "); open pull requests that target this branch may be missed"
	case f.At.IsZero():
		return "origin was not fetched this run (--no-fetch); open pull requests that target this branch may be missed"
	}
	return "origin was not fetched cleanly this run (" + fetchErrText(f.Err) + "); open pull requests that target this branch may be missed"
}

// dependants are the open PRs, from any head repository, that target name:
// those found through head lookups of other branches plus the live per-branch
// base query. When the row is a deletion candidate (needAnswer) a missing,
// failed or truncated base query is an error text. The base query and the
// open PRs found by head lookups are origin's (both are keyed by a bare name in
// the own repository): a row of another remote reads neither and needs neither.
func (c *classifier) dependants(name string, needAnswer, origin bool) ([]int, string) {
	if !origin {
		// openPRsBasedOn matches a bare base name in the own repository
		// (origin): its answer says nothing about another remote's branch.
		return nil, ""
	}
	out := c.openPRsBasedOn(name)
	b, ok := c.s.BasePRs[name]
	var why string
	switch {
	case !ok || b.Lookup == LookupUnknown:
		if needAnswer {
			why = cmp.Or(b.Err, "the open pull requests that target this branch could not be listed")
		}
	default:
		for _, pr := range b.PRs {
			if pr.State == "OPEN" && !slices.Contains(out, pr.Number) {
				out = append(out, pr.Number)
			}
		}
	}
	slices.Sort(out)
	return out, why
}

// remotePRRung is the origin row's PR proof for a row whose content is in the
// base: see remoteRow.
func (c *classifier) remotePRRung(j *judge, rf RefFact, res PRResult) {
	exact := findPR(res, func(pr PRInfo) bool {
		return pr.State == "MERGED" && pr.BaseRef == c.s.Base.Default && pr.HeadRefOid == rf.Tip && c.s.Merges[pr.MergeCommit] == TriYes
	})
	if exact != nil {
		if j.pr == nil {
			j.pr = exact
		}
		return
	}
	if moved := c.movedPR(res, rf.Tip); moved != nil {
		j.weak(ReasonPRHeadMoved, fmt.Sprintf("content is in the base, but PR #%d merged a different head; the branch moved after the merge", moved.Number))
		return
	}
	if unk := findPR(res, func(pr PRInfo) bool {
		return pr.State == "MERGED" && pr.BaseRef == c.s.Base.Default && pr.HeadRefOid == rf.Tip && c.s.Merges[pr.MergeCommit] == TriUnknown
	}); unk != nil {
		j.unsure(ReasonPRMergeUnknown, fmt.Sprintf("PR #%d's merge commit could not be checked against the base", unk.Number))
		return
	}
	j.weak(ReasonNoMergedPR, "content is in the base, but no PR for this branch merged into "+c.s.Base.Default+" with a merge commit in the base")
}

// prTip is the PR rung: a MERGED PR into the default branch whose head is the
// tip and whose merge commit is in the base. Several merged PRs can share a
// head: a verifiable one proves the rung whoever comes first, so the
// unverifiable ones are named only when none is verifiable — leaving their
// reason in would keep a proven row in LESS SURE for a check the other PR
// already answered.
func (c *classifier) prTip(j *judge, r RefFact, res PRResult) {
	if res.Lookup != LookupKnown {
		return
	}
	var merged *PRInfo
	var unknown []string
	for _, pr := range res.PRs {
		if pr.State != "MERGED" || pr.BaseRef != c.s.Base.Default || pr.HeadRefOid != r.Tip {
			continue
		}
		switch c.s.Merges[pr.MergeCommit] {
		case TriYes:
			if merged == nil {
				p := pr
				merged = &p
			}
		case TriUnknown:
			unknown = append(unknown, fmt.Sprintf("PR #%d", pr.Number))
		}
	}
	if merged != nil {
		j.ev = EvPRTip
		j.pr = merged
		if c.gh != GHOK {
			j.weak(ReasonCannotVerify, c.ghNote)
		}
		return
	}
	if len(unknown) > 0 {
		j.unsure(ReasonPRMergeUnknown, fmt.Sprintf("%s's merge commit could not be checked against the base", strings.Join(unknown, ", ")))
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
