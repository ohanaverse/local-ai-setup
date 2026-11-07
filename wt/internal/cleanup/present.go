package cleanup

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// This file is the words of `wt cleanup`: the group order and headings, the
// one-line evidence for a row and the header lines. The plain output and the
// list (a later task) both read it, so it carries no terminal styling and
// does no I/O. It only reads a Report.

// Group is the rows of one status, in the order Classify made them.
type Group struct {
	Status  Status
	Heading string
	Rows    []Row
}

// GroupOrder is the display order of the groups.
var GroupOrder = []Status{StatusProven, StatusLessSure, StatusUniqueWork, StatusInUse}

// Heading is a group's heading.
func Heading(s Status) string {
	switch s {
	case StatusProven:
		return "PROVEN MERGED"
	case StatusLessSure:
		return "LOOKS MERGED, LESS SURE"
	case StatusUniqueWork:
		return "HAS WORK THAT EXISTS NOWHERE ELSE"
	case StatusInUse:
		return "IN USE / LOCKED (cannot remove)"
	}
	return strings.ToUpper(string(s))
}

// Groups splits r into its groups in GroupOrder, each keeping the row order
// of r. A group with no rows is left out.
func Groups(r Report) []Group {
	var out []Group
	for _, st := range GroupOrder {
		g := Group{Status: st, Heading: Heading(st)}
		for _, row := range r.Rows {
			if row.Status == st {
				g.Rows = append(g.Rows, row)
			}
		}
		if len(g.Rows) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// Tickable reports whether a row may be ticked for removal: proven rows only,
// whatever the kind. In-use rows never, and a remote row that is not proven
// never (it stays out of PROVEN unless a merged PR backs it).
func (r Row) Tickable() bool { return r.Status == StatusProven && len(r.Blockers) == 0 }

// DisplayName is the name a row is listed under: a remote row names its
// remote, so it is not mistaken for the local branch of the same name. It
// is one printable line (a detached worktree's directory name can hold any
// byte); Row.Name stays raw for matching.
func DisplayName(r Row) string {
	if r.Kind == KindRemote && r.Remote != "" {
		return flat(r.Remote + "/" + r.Name)
	}
	return flat(r.Name)
}

// reasonWording is one phrase per reason or blocker code. A code the
// classifier can emit and this table lacks fails TestEveryCodeHasWording.
var reasonWording = map[string]string{
	// Reason codes.
	ReasonNoDefaultBranch:       "no default branch found",
	ReasonMergeProofUnavailable: "could not check whether it is merged",
	ReasonGatesUnavailable:      "could not read the worktree's state",
	ReasonBranchUnknown:         "its branch is not among the refs",
	ReasonPRUnknown:             "PR lookup failed",
	ReasonPRMergeUnknown:        "could not check the merged PR against the base",
	ReasonUnpushedUnknown:       "could not count unpushed commits",
	ReasonCannotVerify:          "needs gh",
	ReasonUnpushed:              "unpushed commits",
	ReasonNetZeroUnpushed:       "commits that undo each other exist only on this machine",
	ReasonPRHeadMoved:           "branch moved on after its PR merged",
	ReasonOpenPR:                "open PR",
	ReasonDirty:                 "uncommitted or untracked files",
	ReasonDetachedUnique:        "detached HEAD the base does not reach",
	ReasonNotMerged:             "not merged",
	ReasonIgnoredFiles:          "ignored files",
	ReasonWeakSignal:            "patches appear in the base, not proven",
	ReasonFreshBranch:           "too new to trust",
	ReasonBaseIsLocal:           "compared with the local default branch only",
	ReasonNoMergedPR:            "no merged PR",
	ReasonClaimsUnavailable:     "could not check other wt sessions",
	// Blocker codes (BlockOpenPR shares ReasonOpenPR's code, so its wording).
	BlockDefaultBranch:     "the default branch",
	BlockBaseOfOpenPR:      "an open PR targets it",
	BlockPrimary:           "primary checkout",
	BlockCurrent:           "you are here",
	BlockHeld:              "held by another wt session",
	BlockLocked:            "locked",
	BlockNested:            "contains another worktree",
	BlockOperation:         "a git operation is in progress",
	BlockProtectedArtifact: "holds a registered model artifact",
}

// detailIsSentence lists the codes whose Detail already reads as the whole
// explanation; every other code is its wording with the detail in brackets.
var detailIsSentence = map[string]bool{
	ReasonUnpushed: true, ReasonPRHeadMoved: true, ReasonOpenPR: true,
	ReasonDetachedUnique: true, ReasonFreshBranch: true, ReasonBaseIsLocal: true,
	ReasonNoMergedPR: true, BlockBaseOfOpenPR: true,
}

// ReasonText is one reason or blocker as a phrase. A code with no wording
// reads as the bare code, so an unforeseen reason is still shown. A detail
// that only repeats the wording is said once. The detail is made one
// printable line (flat) whatever its source: tool text reaches it, and
// every row sink (--plain, the TUI) prints it.
func ReasonText(code, detail string) string {
	detail = flat(detail)
	w, ok := reasonWording[code]
	if !ok {
		w = code
	}
	switch {
	case detail == "" || detail == w:
		return w
	case detailIsSentence[code]:
		return detail
	}
	return w + " (" + detail + ")"
}

// evidenceText is the proof a row rests on, in words.
func evidenceText(row Row, base string) string {
	if base == "" {
		base = "the base"
	}
	switch row.Evidence {
	case EvSameCommit:
		return "same commit as " + base
	case EvAncestor:
		return "ancestor of " + base
	case EvTreesMatch:
		return "same files as " + base
	case EvNoAddedChanges:
		return "adds nothing to " + base
	case EvMergeAddsNothing:
		return "squash merged, content is in " + base
	case EvPRTip:
		if row.PR != nil {
			return fmt.Sprintf("squash merged, PR #%d", row.PR.Number)
		}
		return "its PR merged"
	case EvCherryEquivalent, EvPatchID:
		return "patches appear in " + base + ", not proven"
	case "":
		return ""
	}
	return string(row.Evidence)
}

// deepestCurrent is the path of the longest Current worktree row: every
// worktree that contains the caller's directory is Current, and only the
// innermost one is where the caller is.
func deepestCurrent(r Report) string {
	best := ""
	for _, row := range r.Rows {
		if row.Kind != KindWorktree {
			continue
		}
		for _, b := range row.Blockers {
			if b.Code == BlockCurrent && len(row.Path) > len(best) {
				best = row.Path
			}
		}
	}
	return best
}

// RowText is the one-line evidence for a row: what blocks it, then what
// lowers it, then the proof, joined by "; ". What keeps a row from being
// removed comes first, so a narrow screen or a skimming reader meets it
// before a long proof sentence. An Unknown row says why through its reasons.
// A weak-signal reason is left out when the evidence already says "not
// proven" in the same words.
func RowText(rep Report, row Row) string {
	var parts []string
	here := deepestCurrent(rep)
	nested := len(NestedPaths(row))
	for _, b := range row.Blockers {
		if b.Code == BlockNested {
			// One phrase for them all, where the first one stood: the
			// primary checkout holds every worktree under it, and a path
			// each made its line grow with them. The JSON keeps the paths.
			if nested > 0 {
				parts = append(parts, nestedText(nested))
				nested = 0
			}
			continue
		}
		if b.Code == BlockCurrent {
			if row.Path == here {
				parts = append(parts, "you are here")
			} else {
				parts = append(parts, "contains the directory you are in")
			}
			continue
		}
		parts = append(parts, ReasonText(b.Code, b.Detail))
	}
	weakEvidence := row.Evidence == EvCherryEquivalent || row.Evidence == EvPatchID
	for _, r := range row.Reasons {
		if r.Code == ReasonWeakSignal && r.Detail == "" && weakEvidence {
			continue
		}
		detail := r.Detail
		if rep.GH == GHUnavailable && (r.Code == ReasonPRUnknown || r.Code == ReasonCannotVerify) {
			// gh answered for no branch: the header's gh line says so and
			// names the distinct causes (ghCauseText); gh's own text under
			// every row only repeats it. The JSON keeps it per row.
			detail = ghUnavailableDetail
		}
		parts = append(parts, ReasonText(r.Code, detail))
	}
	if ev := evidenceText(row, rep.Base.Ref); ev != "" {
		parts = append(parts, ev)
	}
	if len(parts) == 0 {
		return "no evidence"
	}
	return strings.Join(parts, "; ")
}

// ghUnavailableDetail is a PR reason's shown detail when gh answered for no
// branch (the JSON keeps gh's one-line error).
const ghUnavailableDetail = "PR lookup failed"

// nestedText is the nested-worktree blockers as one phrase.
func nestedText(n int) string {
	if n == 1 {
		return "contains 1 other worktree"
	}
	return fmt.Sprintf("contains %d other worktrees", n)
}

// NestedPaths are the paths of the worktrees inside row's (its nested
// blockers' details), in blocker order, each one printable line. RowText
// only counts them.
func NestedPaths(row Row) []string {
	var out []string
	for _, b := range row.Blockers {
		if b.Code == BlockNested {
			out = append(out, flat(b.Detail))
		}
	}
	return out
}

// LossLines is what removing r would lose, one plain sentence each, in a
// fixed order: uncommitted changes, ignored files, commits only here, a
// detached HEAD's commits. Unreadable gates or an uncounted number of commits
// are said as possible losses, never left out. A row that loses nothing has
// no lines. Every word is this package's own (numbers and sizes only), so no
// tool text reaches the confirmation through it.
func LossLines(r Row) []string {
	var out []string
	l := r.Losses
	if l.Dirty {
		out = append(out, "uncommitted changes will be lost")
	}
	if l.GatesUnknown {
		out = append(out, "could not read the worktree's state; uncommitted or ignored files may be lost")
	}
	if n := l.IgnoredEntries; n > 0 {
		s := fmt.Sprintf("%d ignored %s", n, plural(n, "entry", "entries"))
		if l.IgnoredBytes > 0 {
			s += ", " + localmodels.FormatSize(l.IgnoredBytes)
		}
		out = append(out, s+" will be deleted")
	}
	switch n := l.Unpushed; {
	case n == 1:
		out = append(out, "1 commit exists only on this machine")
	case n > 1:
		out = append(out, fmt.Sprintf("%d commits exist only on this machine", n))
	case n < 0:
		out = append(out, "could not count the commits that exist only on this machine; some may be lost")
	}
	if l.DetachedUnique {
		out = append(out, "the detached HEAD's commits are on no branch and will be lost")
	}
	return out
}

// plural is one when n is 1, else many.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// AgeText is a row's age as "today" or "3d ago"; "" when it is not known.
func AgeText(row Row) string {
	switch {
	case !row.AgeKnown:
		return ""
	case row.AgeDays <= 0:
		return "today"
	}
	return fmt.Sprintf("%dd ago", row.AgeDays)
}

// agoText is how long before now at was.
func agoText(at, now time.Time) string {
	d := now.Sub(at)
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

const (
	ghCausesShown = 2   // distinct causes the gh line names
	ghCauseMax    = 100 // runes of one cause on the gh line
)

// ghCauseText is why gh answered nothing: the distinct details of the rows'
// PR-lookup reasons, each sanitised again to one printable line (the line
// reaches the terminal), first-seen order, at most ghCausesShown of them
// joined with " | " (a cause may hold "; " itself) and the rest counted as
// "(+N more)". The synthetic noPRLookupDetail is left out beside a real
// cause. "" when no row carries one.
func ghCauseText(r Report) string {
	var causes []string
	seen := map[string]bool{}
	for _, row := range r.Rows {
		for _, re := range row.Reasons {
			if re.Code != ReasonPRUnknown && re.Code != ReasonCannotVerify {
				continue
			}
			c := capRunes(flat(re.Detail), ghCauseMax)
			if c == "" || seen[c] {
				continue
			}
			seen[c] = true
			causes = append(causes, c)
		}
	}
	if len(causes) > 1 {
		causes = slices.DeleteFunc(causes, func(c string) bool { return c == noPRLookupDetail })
	}
	if len(causes) > ghCausesShown {
		more := fmt.Sprintf("(+%d more)", len(causes)-ghCausesShown)
		return strings.Join(causes[:ghCausesShown], " | ") + " " + more
	}
	return strings.Join(causes, " | ")
}

// HeaderLines are the lines above the groups: the repository, the base (and
// whether it is a local fallback), the fetch result and how fresh the base
// is, and what gh could answer; then a note when MinAge was lowered below
// DefaultMinAge, and the caller's Notes, one sanitised line each.
func HeaderLines(r Report, now time.Time) []string {
	lines := []string{"repo:  " + r.Repo}

	base := "none (no default branch)"
	if r.Base.Ref != "" {
		base = r.Base.Ref
		if r.Base.Fallback {
			base += " (local fallback: there is no remote default, so no row is better than less sure)"
		}
	}
	lines = append(lines, "base:  "+base)

	switch {
	case r.Fetch.Skipped != "":
		lines = append(lines, "fetch: skipped ("+r.Fetch.Skipped+")")
	case r.Fetch.At.IsZero():
		lines = append(lines, "fetch: skipped; the base is as old as the last fetch, merges since may be missed")
	case r.Fetch.OK:
		lines = append(lines, "fetch: ok, "+agoText(r.Fetch.At, now)+otherRemotesText(r.Fetch))
	default:
		lines = append(lines, "fetch: FAILED ("+fetchErrText(r.Fetch.Err)+"); the base may be stale, merges since the last fetch may be missed"+otherRemotesText(r.Fetch))
	}

	switch r.GH {
	case GHOK:
		lines = append(lines, "gh:    ok")
	case GHPartial:
		lines = append(lines, "gh:    partial; some branches could not be checked against their PRs")
	case GHUnavailable:
		why := ""
		if c := ghCauseText(r); c != "" {
			why = " (" + c + ")"
		}
		lines = append(lines, "gh:    unavailable"+why+"; PR evidence is missing and rows that need it say so")
	default:
		lines = append(lines, "gh:    "+string(r.GH))
	}

	return append(lines, NoteLines(r)...)
}

// NoteLines are the caveats a reader must see beside the rows: the lowered
// min-age, then the caller's Notes, one sanitised line each. The text header
// and the --json `notes` array both carry exactly these.
func NoteLines(r Report) []string {
	var lines []string
	// A zero MinAge is a Report not built by Classify: nothing was lowered.
	if r.MinAge > 0 && r.MinAge < DefaultMinAge {
		lines = append(lines, "min-age: lowered to "+shortDuration(r.MinAge)+" (default "+shortDuration(DefaultMinAge)+"): fresh branches may read as merged")
	}
	for _, n := range r.Notes {
		lines = append(lines, flat(n))
	}
	return lines
}

// shortDuration is d as the user would type it: "2m", not "2m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// otherRemotesText says how the remotes besides origin fared, as a header
// suffix ("; also fetched upstream; not fetched: fork (reason)"), or "" when
// origin is the only remote. Origin's own result is the header line itself.
func otherRemotesText(f FetchResult) string {
	var ok, bad, skipped []string
	for _, r := range f.Remotes {
		switch {
		case r.Name == "origin":
		case r.Skipped:
			skipped = append(skipped, flat(r.Name))
		case r.OK:
			ok = append(ok, flat(r.Name))
		default:
			bad = append(bad, flat(r.Name)+" FAILED ("+fetchErrText(r.Err)+")")
		}
	}
	var parts []string
	if len(ok) > 0 {
		parts = append(parts, "also fetched "+strings.Join(ok, ", "))
	}
	if len(bad) > 0 {
		parts = append(parts, strings.Join(bad, "; "))
	}
	if len(skipped) > 0 {
		parts = append(parts, "skipped (skipFetchAll) "+strings.Join(skipped, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}
