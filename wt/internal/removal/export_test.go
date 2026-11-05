package removal

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// ExtFixture exposes the test fixture to package removal_test, whose tests
// use only removal's exported API (what another package could do).
type ExtFixture struct{ f *fixture }

// NewExtFixture is a fresh fixture with gh stubbed.
func NewExtFixture(t *testing.T) ExtFixture { noGh(t); return ExtFixture{newFixture(t)} }

func (x ExtFixture) Worktree(branch string) string { return x.f.Worktree(branch) }
func (x ExtFixture) Dirty(dir, file string)        { x.f.Dirty(dir, file) }
func (x ExtFixture) Commit(dir, file, content, msg string) string {
	return x.f.Commit(dir, file, content, msg)
}
func (x ExtFixture) UnmergedBranch(name string)                 { x.f.unmergedBranch(name) }
func (x ExtFixture) Lock(path string)                           { x.f.Lock(path) }
func (x ExtFixture) MergedWorktree(branch string) string        { return x.f.mergedWorktree(branch) }
func (x ExtFixture) RefTip(ref string) (string, bool)           { return x.f.refTip(ref) }
func (x ExtFixture) Report() (cleanup.Snapshot, cleanup.Report) { return x.f.report() }
func (x ExtFixture) Row(r cleanup.Report, k cleanup.Kind, name string) cleanup.Row {
	return x.f.row(r, k, name)
}

// Engine is an Engine over the fixture's repository from snapshot s.
func (x ExtFixture) Engine(s cleanup.Snapshot) Engine { return x.f.engine(s, x.f.inputs(), Options{}) }
