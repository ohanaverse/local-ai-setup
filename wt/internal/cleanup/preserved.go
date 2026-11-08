package cleanup

import (
	"strconv"
	"strings"
)

// Preserved says a rescue record covers a worktree's work: the record ref and
// commit, the head the rescue read (the classifier compares a branch's fresh
// tip with it) and where the copy lives. It is set only when every match rule
// holds; whether the covered part exists is the classifier's question.
// CoversDirt is true when the worktree is dirty and the record also holds
// that uncommitted state (dirtCovered); false for a clean worktree.
type Preserved struct {
	Ref, Commit, SourceHead, DestBranch, DestPath string
	CoversDirt                                    bool
}

// recordIDLen is how many leading digits of the record commit name its ref.
const recordIDLen = 16

// preservedOf returns the first record that matches ws, or nil. It is nil for
// a worktree that has unreadable gates or any hard blocker, or is the primary
// or bare one. For a dirty worktree a record must also hold the uncommitted
// state (dirtCovered), else the next record is tried; the result then has
// CoversDirt. A clean worktree runs no dirt check. A record matches only
// when its ref is the prefix plus the commit's first 16 digits, its source
// path, ref and head equal the worktree's, its commit and parent exist, and
// the worktree's HEAD adds nothing the record ref and remote-tracking refs
// do not reach.
func preservedOf(g Git, ws WorktreeState, recs []RescueRecord) *Preserved {
	w := ws.Fact
	if len(recs) == 0 || ws.GatesErr != "" || len(ws.Gates.Blockers) > 0 ||
		w.Primary || w.Bare || w.Head == "" {
		return nil
	}
	ref := "detached"
	if w.Branch != "" {
		ref = w.Branch
	} else if !w.Detached {
		return nil
	}
	path := resolve(w.Path)
	wg := Git{Dir: w.Path, Timeout: g.Timeout}
	for _, r := range recs {
		if len(r.Commit) < recordIDLen || r.Ref != RecordRefPrefix+r.Commit[:recordIDLen] ||
			r.SourcePath != path || r.SourceRef != ref || r.SourceHead != w.Head ||
			!recordCovers(g, r, w.Head) {
			continue
		}
		if ws.Gates.Dirty && !dirtCovered(wg, w, r) {
			continue
		}
		return &Preserved{
			Ref: r.Ref, Commit: r.Commit, SourceHead: r.SourceHead,
			DestBranch: r.DestBranch, DestPath: r.DestPath, CoversDirt: ws.Gates.Dirty,
		}
	}
	return nil
}

// recordCovers is whether the record's commit and its parent exist and head
// reaches nothing that neither the record ref nor a remote-tracking ref does.
// Any git failure is "no".
func recordCovers(g Git, r RescueRecord, head string) bool {
	for _, obj := range []string{r.Commit, r.Commit + "^"} {
		o, err := g.Run("cat-file", "-e", obj+"^{commit}")
		if err != nil || o.Code != 0 {
			return false
		}
	}
	o, err := g.Run("rev-list", "--count", head, "--not", "--remotes", r.Ref)
	if err != nil || o.Code != 0 {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(o.Stdout))
	return err == nil && n == 0
}

// worktreeState is one worktree's state as Gather and Rescan both read it:
// the gates, then whether a record preserves it. A third caller must use it
// too, or it would skip the record.
func worktreeState(g Git, w WorktreeFact, gin GateInputs, recs []RescueRecord) WorktreeState {
	ws := gateState(g, w, gin)
	ws.Preserved = preservedOf(g, ws, recs)
	return ws
}
