package cleanup

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/dirtsnap"
)

// The caps on a dirt check, equal to the ones a rescue enforces on a source
// (rescue.MaxUntrackedFiles and MaxUntrackedBytes; a test in package rescue
// pins them equal, because cleanup may not import rescue). A state a rescue
// accepted but these caps refuse reads as not covered, which is safe.
const (
	DirtMaxFiles = 20000
	DirtMaxBytes = 512 << 20
)

// dirtLimits is what dirtCovered passes to dirtsnap.Build; a variable so a
// test can lower it.
var dirtLimits = dirtsnap.Limits{MaxFiles: DirtMaxFiles, MaxBytes: DirtMaxBytes}

// dirtTimeout bounds one git call of the dirt check. The worktree Git's 30 s
// default would turn a slow hash of a large tree into "not covered".
const dirtTimeout = 2 * time.Minute

// dirtRunner is a dirtsnap.Runner over the worktree-level Git wg, with the
// fsmonitor off and a longer timeout. A non-zero exit is an error. wg.RunInput
// sets GIT_OPTIONAL_LOCKS=0, so the status inside Build leaves the index as it
// was.
func dirtRunner(wg Git) dirtsnap.Runner {
	g := Git{Dir: wg.Dir, Timeout: dirtTimeout, Config: []string{"core.fsmonitor=false"}}
	return func(stdin []byte, args ...string) ([]byte, error) {
		o, err := g.RunInput(string(stdin), args...)
		if err != nil {
			return nil, err
		}
		if o.Code != 0 {
			return nil, gitFailure(o, args...)
		}
		return []byte(o.Stdout), nil
	}
}

// dirtCovered reports whether record r holds everything uncommitted in the
// worktree w: its index equals the record's first commit (I) and its working
// tree, as dirtsnap names it, equals the record's tree (W). The record is
// W, whose parent is I and whose grandparent is the source head. Every error
// is false. The checks run cheapest first:
//
//  1. the worktree's head is the record's source head, and W's tree and W~2
//     (its grandparent, the commit I hangs from) resolve, with W~2 equal to
//     that head (a forged chain of another shape fails here);
//  2. no index entry is skip-worktree or assume-unchanged (status cannot see
//     an edit to one, so the comparison below would miss it);
//  3. no intent-to-add entry (`diff-files --diff-filter=A`);
//  4. `diff-index --cached --quiet I` exits 0: the index tree is I, and an
//     unmerged or intent-to-add entry makes it differ (this step is the
//     unmerged guard: Build below has no unmerged check of its own);
//  5. dirtsnap.Build of the working tree equals W's tree (a nested
//     repository, submodule or special file is unsupported, over the caps
//     is too large; both are errors).
//
// Declared losses, because git does not record them and the copy a rescue
// makes cannot carry them: file mode bits other than the executable bit, and
// empty directories. A change of only those still reads as covered.
func dirtCovered(wg Git, w WorktreeFact, r RescueRecord) bool {
	return len(dirtCoveredRecords(wg, w, []RescueRecord{r})) > 0
}

// RecordCoversDirt reports whether the rescue record named by recordRef
// covers the index and working tree of the checkout at root, whose HEAD is
// head: the same check preservedOf applies to a linked worktree, which it
// skips for the primary checkout. It only reads. A record that is not listed
// or a failed read (a git call that errors or exits unexpectedly, such as a
// timeout or an unreadable index) is false with the error; a state the record
// does not cover, including a tree over the caps or one dirtsnap will not
// describe, is false with a nil error.
//
// It checks content and head only: the record's source head, index tree and
// working tree against the checkout. Unlike preservedOf it does not compare
// the record's identity (its Wt-Source path and ref, or that the ref name is
// the record's own commit id), so a caller must separately know that
// recordRef is the record it just wrote for this checkout.
func RecordCoversDirt(g Git, root, head, recordRef string) (bool, error) {
	recs, err := ListRecords(g)
	if err != nil {
		return false, err
	}
	for _, r := range recs {
		if r.Ref != recordRef {
			continue
		}
		// g's Env and -c settings are kept, with fsmonitor off appended after
		// them (a caller cannot turn it back on): a stale hook must not hide an
		// edit from the check.
		wg := WithoutFSMonitor(g)
		wg.Dir = root
		w := WorktreeFact{Path: root, Head: head}
		covered, err := dirtCoveredErr(wg, w, []RescueRecord{r})
		if err != nil {
			return false, fmt.Errorf("could not read the root to check record %s: %w", recordRef, err)
		}
		return len(covered) > 0, nil
	}
	return false, fmt.Errorf("rescue record %s is not listed", recordRef)
}

// dirtCoveredRecords keeps, in order, the records of rs dirtCovered accepts;
// every error reads as not covered (see dirtCoveredErr for the one caller that
// tells them apart).
func dirtCoveredRecords(wg Git, w WorktreeFact, rs []RescueRecord) []RescueRecord {
	out, _ := dirtCoveredErr(wg, w, rs)
	return out
}

// dirtCoveredErr is dirtCoveredRecords with the failures kept apart from the
// answers. A state the records do not cover (a head, chain, index or tree that
// differs, a hidden index flag, an intent-to-add entry, a working tree over the
// caps or one dirtsnap will not describe) is the records dropped from the
// result with a nil error; a git call that could not run or exited with a code
// that is not its "differs" answer (a timeout, an unreadable index, a failed
// hash) is an error, and the result is then nil. The checks of
// dirtCovered's doc, in the same order.
//
// The checks that look only at the worktree (2, 3 and 5) run once for the
// whole slice: a source rescued more than once at one head leaves a candidate
// per rescue, and step 5 hashes every changed file. Step 1 still runs first
// per record, so a worktree whose head is not a record's forks no process at
// all (TestDirtCoveredCheapChecksFirst).
func dirtCoveredErr(wg Git, w WorktreeFact, rs []RescueRecord) ([]RescueRecord, error) {
	if w.Head == "" {
		return nil, nil
	}
	type candidate struct {
		r     RescueRecord
		wtree string
	}
	var cands []candidate
	for _, r := range rs {
		if w.Head != r.SourceHead || len(r.Commit) < recordIDLen {
			continue
		}
		o, err := wg.Run("rev-parse", "--verify", "--end-of-options", r.Commit+"^{tree}")
		if err != nil {
			return nil, err
		}
		if o.Code != 0 { // the record's objects are gone: not covered
			continue
		}
		wtree := strings.TrimSpace(o.Stdout)
		o, err = wg.Run("rev-parse", "--verify", "--end-of-options", r.Commit+"~2")
		if err != nil {
			return nil, err
		}
		if o.Code != 0 || strings.TrimSpace(o.Stdout) != r.SourceHead {
			continue
		}
		cands = append(cands, candidate{r, wtree})
	}
	if len(cands) == 0 {
		return nil, nil
	}
	hidden, err := HiddenIndexFlags(wg)
	if err != nil {
		return nil, err
	}
	if hidden {
		return nil, nil
	}
	diffFiles := []string{"diff-files", "--diff-filter=A", "--name-only", "-z"}
	o, err := wg.Run(diffFiles...)
	if err != nil {
		return nil, err
	}
	if o.Code != 0 {
		return nil, gitFailure(o, diffFiles...)
	}
	if o.Stdout != "" {
		return nil, nil
	}
	var indexed []candidate
	for _, c := range cands {
		diffIndex := []string{"diff-index", "--cached", "--quiet", c.r.Commit + "^", "--"}
		o, err := wg.Run(diffIndex...)
		if err != nil {
			return nil, err
		}
		switch o.Code {
		case 0:
			indexed = append(indexed, c)
		case 1: // --quiet: the index differs from I
		default:
			return nil, gitFailure(o, diffIndex...)
		}
	}
	if len(indexed) == 0 {
		return nil, nil
	}
	got, err := dirtsnap.Build(wg.Dir, dirtRunner(wg), false, dirtLimits)
	if errors.Is(err, dirtsnap.ErrTooLarge) || errors.Is(err, dirtsnap.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RescueRecord
	for _, c := range indexed {
		if c.wtree == got {
			out = append(out, c.r)
		}
	}
	return out, nil
}
