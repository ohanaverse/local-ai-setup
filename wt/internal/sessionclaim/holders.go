package sessionclaim

import (
	"os"
	"path/filepath"
	"sort"
)

// Holders returns the live claims in commonDir/wt-sessions grouped by the
// worktree path each holds — what the cleanup list and the launch warnings
// read to show who else holds a checkout. The map's keys are Claim.Path
// values, and each path's holders are sorted by pid so the rendering is
// stable.
//
// A holder is a parseable claim under the contract's liveness rule
// (docs/contracts/session-claims.md: its pid runs and started when the claim
// says): a dead pid's leftover file holds nothing, and neither does a running
// pid the OS reused for an unrelated process — its real start time differs
// from the claim's by more than startTolerance, and naming that stranger a
// holder would hold a checkout on a process that never claimed it. A claim by
// self's pid or one of self's parents is this session's family, not another
// holder (a nested wt runs inside the session that already holds the
// checkout), and a running pid whose start time cannot be read is neither
// counted nor deleted — scan's rule for a claim nobody can verify.
//
// Holders takes no lock and changes nothing — it never prunes: stale files
// remain for Prune and the writers to remove. It reads while other sessions
// run, and a reader that tidied up could delete a claim a concurrent Hold just
// wrote. No wt-sessions directory means no holders.
func Holders(commonDir string, self Claim) (map[string][]Claim, error) {
	files, err := readClaims(filepath.Join(commonDir, "wt-sessions"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	holders := map[string][]Claim{}
	var mine map[int]bool
	for _, f := range files {
		if !f.ok || f.c.PID == self.PID {
			continue
		}
		if mine == nil {
			mine = ancestors()
		}
		if mine[f.c.PID] {
			continue
		}
		if alive, _ := live(f.c); !alive {
			continue
		}
		holders[f.c.Path] = append(holders[f.c.Path], f.c)
	}
	for _, cs := range holders {
		sort.Slice(cs, func(i, j int) bool { return cs[i].PID < cs[j].PID })
	}
	return holders, nil
}
