package cleanup

import (
	"regexp"
	"strconv"
	"strings"
)

// Evidence names the signal behind a verdict; it is kept for the row and the
// JSON.
type Evidence string

const (
	// Proofs: any one of these makes a branch Merged.
	EvSameCommit       Evidence = "same-commit"        // tip is the base commit
	EvAncestor         Evidence = "ancestor"           // tip is an ancestor of the base
	EvTreesMatch       Evidence = "trees-match"        // tip's tree is the base's tree
	EvNoAddedChanges   Evidence = "no-added-changes"   // tip adds nothing to the merge-base
	EvMergeAddsNothing Evidence = "merge-adds-nothing" // merging tip into base changes nothing
	EvPRTip            Evidence = "pr-tip"             // a merged PR's head is the tip (set by the classifier)

	// Weak signals: never proofs. Both show patch equivalence, not that the
	// content is still in the base (a later revert on main defeats them).
	EvCherryEquivalent Evidence = "cherry-equivalent"
	EvPatchID          Evidence = "patch-id"
)

// Verdict is the ladder's answer.
type Verdict int

const (
	NotMerged    Verdict = iota // no rung proves it
	Merged                      // a rung proves it
	Undetermined                // a git error or unexpected exit stopped the ladder
)

func (v Verdict) String() string {
	switch v {
	case NotMerged:
		return "NotMerged"
	case Merged:
		return "Merged"
	case Undetermined:
		return "Undetermined"
	}
	return "Verdict(" + strconv.Itoa(int(v)) + ")"
}

// patchIDWalkCap bounds the base commits WeakSignals hashes for patch-id.
const patchIDWalkCap = 500

var fullCommitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsFullCommitID reports whether s is a full 40-hex commit id as git prints
// it: the one definition wt's cleanup and removal share.
func IsFullCommitID(s string) bool { return fullCommitID.MatchString(s) }

// qualified reports whether ref is a full 40-hex commit id or a plain
// refs/heads/<b> or refs/remotes/<r>/<b> name (no revision syntax). A short
// name is refused: git resolves `t2` to a tag t2 before the branch t2, so a
// branch could be judged by another object entirely.
func qualified(ref string) bool {
	if fullCommitID.MatchString(ref) {
		return true
	}
	if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/remotes/") {
		return false
	}
	return !strings.ContainsAny(ref, "^~:?*[\\ \t\n") && !strings.Contains(ref, "@{") && !strings.Contains(ref, "..")
}

// commitOf resolves ref to a commit sha. ok is false for an unqualified ref,
// an absent one and any git failure alike: the caller cannot judge.
func commitOf(g Git, ref string) (string, bool) {
	if !qualified(ref) {
		return "", false
	}
	o, err := g.Run("rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || o.Code != 0 {
		return "", false
	}
	sha := strings.TrimSpace(o.Stdout)
	return sha, sha != ""
}

// treeOf resolves a commit sha to its tree sha.
func treeOf(g Git, commit string) (string, bool) {
	o, err := g.Run("rev-parse", "--verify", "--quiet", commit+"^{tree}")
	if err != nil || o.Code != 0 {
		return "", false
	}
	sha := strings.TrimSpace(o.Stdout)
	return sha, sha != ""
}

// mergeBase returns the merge-base of two commits. ok is false when there is
// none (exit 1) or git failed.
func mergeBase(g Git, a, b string) (string, bool) {
	o, err := g.Run("merge-base", a, b)
	if err != nil || o.Code != 0 {
		return "", false
	}
	sha := strings.TrimSpace(o.Stdout)
	return sha, sha != ""
}

// mergeDriverConfigured reports whether any `merge.<name>.driver` is set. A
// custom driver can drop one side's change and still exit 0, which would make
// merge-tree's "adds nothing" a lie. err is set for an unexpected exit.
func mergeDriverConfigured(g Git) (bool, error) {
	o, err := g.Run("config", "--get-regexp", `^merge\..*\.driver$`)
	if err != nil {
		return false, err
	}
	switch o.Code {
	case 0:
		return strings.TrimSpace(o.Stdout) != "", nil
	case 1:
		return false, nil // no such key
	}
	return false, gitFailure(o, "config", "--get-regexp", `^merge\..*\.driver$`)
}

// Prove runs the evidence ladder for tip against base; the first proof wins.
//
// base and tip must each be a fully-qualified branch ref (refs/heads/<b> or
// refs/remotes/<r>/<b>) or a full 40-hex commit id; anything else, including
// a short name, which git may resolve to a same-named tag, is Undetermined.
//
// Every rung reads git's exit code: 0 and 1 are answers, anything else (or a
// failure to run git) is Undetermined at once — the ladder never goes on to a
// later rung as if an erroring one had said "no". A false Merged loses work,
// so only a clean proof returns Merged. A configured merge driver makes the
// merge-tree rung unanswerable, so it is Undetermined too.
func Prove(g Git, base, tip string) (Evidence, Verdict) {
	b, ok := commitOf(g, base)
	if !ok {
		return "", Undetermined
	}
	t, ok := commitOf(g, tip)
	if !ok {
		return "", Undetermined
	}

	// 1. same commit
	if b == t {
		return EvSameCommit, Merged
	}

	// 2. ancestor
	o, err := g.Run("merge-base", "--is-ancestor", t, b)
	if err != nil {
		return "", Undetermined
	}
	switch o.Code {
	case 0:
		return EvAncestor, Merged
	case 1:
	default:
		return "", Undetermined
	}

	// 3. trees match
	bt, ok := treeOf(g, b)
	if !ok {
		return "", Undetermined
	}
	tt, ok := treeOf(g, t)
	if !ok {
		return "", Undetermined
	}
	if bt == tt {
		return EvTreesMatch, Merged
	}

	// 4. no changes added since the merge-base. No merge-base (unrelated
	// histories) leaves this rung and the next unanswerable.
	mb, ok := mergeBase(g, b, t)
	if !ok {
		return "", Undetermined
	}
	o, err = g.Run("diff-tree", "--quiet", mb, t)
	if err != nil {
		return "", Undetermined
	}
	switch o.Code {
	case 0:
		return EvNoAddedChanges, Merged
	case 1:
	default:
		return "", Undetermined
	}

	// 5. merging tip into base adds nothing: the merged tree is base's tree.
	// Unlike git cherry, this fails safe when the change was later reverted.
	// merge-tree honours .gitattributes merge drivers, and a driver can keep
	// base's side and exit 0; with any driver configured (or the check
	// failing) the rung cannot be trusted. Earlier rungs only prove, so
	// Undetermined here loses no answer.
	if driver, err := mergeDriverConfigured(g); err != nil || driver {
		return "", Undetermined
	}
	o, err = g.Run("merge-tree", "--write-tree", b, t)
	if err != nil {
		return "", Undetermined
	}
	switch o.Code {
	case 0:
		first, _, _ := strings.Cut(o.Stdout, "\n")
		if strings.TrimSpace(first) == bt {
			return EvMergeAddsNothing, Merged
		}
		return "", NotMerged
	case 1:
		return "", NotMerged // conflict: tip changes what base changed differently
	}
	return "", Undetermined
}

// WeakSignals reports the signals that suggest, but never prove, tip's work is
// in base: git cherry calls every commit equivalent, or the branch's whole
// diff has the patch-id of one base commit since the merge-base. A git error
// omits that signal; so does a base walk longer than patchIDWalkCap. base and
// tip follow Prove's contract (qualified refs or full commit ids); anything
// else gets no signals.
func WeakSignals(g Git, base, tip string) []Evidence {
	if !qualified(base) || !qualified(tip) {
		return nil
	}
	var out []Evidence
	if cherryAllEquivalent(g, base, tip) {
		out = append(out, EvCherryEquivalent)
	}
	if patchIDMatch(g, base, tip) {
		out = append(out, EvPatchID)
	}
	return out
}

// cherryAllEquivalent is true when `git cherry` lists at least one commit and
// marks every one "-" (an equivalent change is in base). Cherry ignores merge
// commits, one reason this is not a proof.
func cherryAllEquivalent(g Git, base, tip string) bool {
	o, err := g.Run("cherry", base, tip)
	if err != nil || o.Code != 0 {
		return false
	}
	out := strings.TrimSpace(o.Stdout)
	if out == "" {
		return false
	}
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "- ") {
			return false
		}
	}
	return true
}

// diffFlags pins the diff shape so patch-ids of the branch diff and of base
// commits are computed alike, whatever the user's diff config.
var diffFlags = []string{"-p", "--no-color", "--no-ext-diff", "--no-renames"}

// patchIDs runs `git patch-id --stable` over a diff and returns the ids.
func patchIDs(g Git, diff string) ([]string, bool) {
	if strings.TrimSpace(diff) == "" {
		return nil, true
	}
	o, err := g.RunInput(diff, "patch-id", "--stable")
	if err != nil || o.Code != 0 {
		return nil, false
	}
	var ids []string
	for _, l := range strings.Split(o.Stdout, "\n") {
		if id, _, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			ids = append(ids, id)
		}
	}
	return ids, true
}

// patchIDMatch is true when the branch's whole diff (merge-base..tip) has the
// patch-id of one commit on base since the merge-base: a squash merge.
func patchIDMatch(g Git, base, tip string) bool {
	b, ok := commitOf(g, base)
	if !ok {
		return false
	}
	t, ok := commitOf(g, tip)
	if !ok {
		return false
	}
	mb, ok := mergeBase(g, b, t)
	if !ok {
		return false
	}
	o, err := g.Run("rev-list", "--count", "--no-merges", mb+".."+b)
	if err != nil || o.Code != 0 {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(o.Stdout))
	if err != nil || n == 0 || n > patchIDWalkCap {
		return false // too long a walk to judge: undetermined, so omitted
	}

	o, err = g.Run(append(append([]string{"diff-tree"}, diffFlags...), mb, t)...)
	if err != nil || o.Code != 0 {
		return false
	}
	want, ok := patchIDs(g, o.Stdout)
	if !ok || len(want) != 1 {
		return false
	}

	args := append(append([]string{"log", "--no-merges", "--format=commit %H"}, diffFlags...), mb+".."+b)
	o, err = g.Run(args...)
	if err != nil || o.Code != 0 {
		return false
	}
	have, ok := patchIDs(g, o.Stdout)
	if !ok {
		return false
	}
	for _, id := range have {
		if id == want[0] {
			return true
		}
	}
	return false
}
