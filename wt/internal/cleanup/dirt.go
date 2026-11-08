package cleanup

import (
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
//     unmerged or intent-to-add entry makes it differ;
//  5. dirtsnap.Build of the working tree equals W's tree (a nested
//     repository, submodule, special file or unmerged path is unsupported,
//     over the caps is too large; both are errors).
//
// Declared losses, because git does not record them and the copy a rescue
// makes cannot carry them: file mode bits other than the executable bit, and
// empty directories. A change of only those still reads as covered.
func dirtCovered(wg Git, w WorktreeFact, r RescueRecord) bool {
	if w.Head == "" || w.Head != r.SourceHead || len(r.Commit) < recordIDLen {
		return false
	}
	o, err := wg.Run("rev-parse", "--verify", "--end-of-options", r.Commit+"^{tree}")
	if err != nil || o.Code != 0 {
		return false
	}
	wtree := strings.TrimSpace(o.Stdout)
	o, err = wg.Run("rev-parse", "--verify", "--end-of-options", r.Commit+"~2")
	if err != nil || o.Code != 0 || strings.TrimSpace(o.Stdout) != r.SourceHead {
		return false
	}
	if hidden, err := hiddenIndexFlags(wg); err != nil || hidden {
		return false
	}
	o, err = wg.Run("diff-files", "--diff-filter=A", "--name-only", "-z")
	if err != nil || o.Code != 0 || o.Stdout != "" {
		return false
	}
	o, err = wg.Run("diff-index", "--cached", "--quiet", r.Commit+"^", "--")
	if err != nil || o.Code != 0 {
		return false
	}
	got, err := dirtsnap.Build(wg.Dir, dirtRunner(wg), false, dirtLimits)
	return err == nil && got == wtree
}
