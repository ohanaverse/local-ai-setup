package cleanup

import "slices"

// StubHeldForTest makes every gate check report the worktrees at paths as held
// by another wt session (BlockHeld), for the rest of the test
// (`t.Cleanup(cleanup.StubHeldForTest(p))`). A real claim only ever lands on
// the primary checkout, which is a hard stop anyway, so a linked worktree can
// be held only through this seam. Production code never calls it.
func StubHeldForTest(paths ...string) (restore func()) {
	old := checkGates
	resolved := make([]string, len(paths))
	for i, p := range paths {
		resolved[i] = resolve(p)
	}
	checkGates = func(g Git, w WorktreeFact, in GateInputs) (Gates, error) {
		out, err := old(g, w, in)
		if slices.Contains(resolved, resolve(w.Path)) && !slices.ContainsFunc(out.Blockers, func(r Reason) bool { return r.Code == BlockHeld }) {
			out.Blockers = append(out.Blockers, Reason{BlockHeld, w.Path})
		}
		return out, err
	}
	return func() { checkGates = old }
}
