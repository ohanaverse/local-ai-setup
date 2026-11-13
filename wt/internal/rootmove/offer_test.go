package rootmove

import (
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// rowsOf classifies f's repository exactly as the cleanup list does (the
// gather fixture.engine() runs, no fetch) and returns every row, the folded
// remote rows included.
func rowsOf(t *testing.T, f *fixture) []cleanup.Row {
	t.Helper()
	return cleanup.Classify(f.engine().Rescue.Prior, cleanup.Options{Now: later()}).AllRows()
}

// primaryRow returns the root row (the primary checkout's) among rows,
// failing the test when classification produced none.
func primaryRow(t *testing.T, rows []cleanup.Row) cleanup.Row {
	t.Helper()
	for _, r := range rows {
		if r.Primary {
			return r
		}
	}
	t.Fatal("no primary row among the classified rows")
	return cleanup.Row{}
}

// TestOfferedRootOnFeatureBranch pins the offer shape on a real repository:
// the root checked out on a feature branch — its row carries the drift label
// — is offered with no message. Why: the drifting root is the one row the
// cleanup list's R action exists for, and the offer must ride the row's
// ReasonRootDrift signal, the same one the list shows as the root being off
// its default branch.
func TestOfferedRootOnFeatureBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.onBranch("feature")
	row := primaryRow(t, rowsOf(t, f))
	if !hasReason(row.Reasons, cleanup.ReasonRootDrift) {
		t.Fatalf("the root row carries no %s reason: %+v", cleanup.ReasonRootDrift, row.Reasons)
	}
	if offered, why := Offered(row); !offered || why != "" {
		t.Errorf("Offered = %v, %q; want true, %q", offered, why, "")
	}
}

// TestOfferedRootOnDefaultSaysAlreadyThere pins R501a's default-case message
// on real default fixtures. The brief's exact literal "root is already on
// main" is the main-default rendering and is asserted verbatim there; on a
// trunk-default fixture the same code renders "root is already on trunk".
// Why: a cleanup.Row carries no default-branch name, so on a default-branch
// root the row's Branch IS the default's name and the message must take it
// from there — truthful in every repository, not only a main-default one.
func TestOfferedRootOnDefaultSaysAlreadyThere(t *testing.T) {
	t.Parallel()
	t.Run("main default", func(t *testing.T) {
		f := newFixture(t)
		row := primaryRow(t, rowsOf(t, f))
		if row.Branch != "main" {
			t.Fatalf("root row branch = %q, want %q", row.Branch, "main")
		}
		if offered, why := Offered(row); offered || why != "root is already on main" {
			t.Errorf("Offered = %v, %q; want false, %q", offered, why, "root is already on main")
		}
	})
	t.Run("trunk default", func(t *testing.T) {
		f := newFixture(t)
		f.git("switch", "-q", "-c", "trunk")
		f.git("push", "-q", "origin", "trunk")
		f.git("remote", "set-head", "origin", "trunk")
		row := primaryRow(t, rowsOf(t, f))
		if row.Branch != "trunk" {
			t.Fatalf("root row branch = %q, want %q", row.Branch, "trunk")
		}
		if offered, why := Offered(row); offered || why != "root is already on trunk" {
			t.Errorf("Offered = %v, %q; want false, %q", offered, why, "root is already on trunk")
		}
	})
}

// TestOfferedDetachedRoot pins that a detached root is offered like a named
// drift: its row carries the drift label ("root is detached") with Branch ""
// and reads (true, ""). Why: the brief offers "root on a non-default branch
// or detached" alike — the ruling's "named drift or detached" — and the
// detached row is the drift family with no branch name to show.
func TestOfferedDetachedRoot(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.git("switch", "-q", "--detach", "HEAD")
	row := primaryRow(t, rowsOf(t, f))
	if row.Branch != "" {
		t.Fatalf("root row branch = %q, want %q (detached)", row.Branch, "")
	}
	if !hasReason(row.Reasons, cleanup.ReasonRootDrift) {
		t.Fatalf("the root row carries no %s reason: %+v", cleanup.ReasonRootDrift, row.Reasons)
	}
	if offered, why := Offered(row); !offered || why != "" {
		t.Errorf("Offered = %v, %q; want true, %q", offered, why, "")
	}
}

// TestOfferedNonRootRowsRefused pins the refusal message on every non-root
// row kind a real repository classifies: a linked worktree, a local branch no
// worktree holds and a remote branch. Why: the brief refuses "any non-root
// row" — restore-root moves the root's branch and nothing else — so the menu
// says so rather than staying silent.
func TestOfferedNonRootRowsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.Worktree("topic")
	f.git("branch", "loose")
	kinds := map[cleanup.Kind]bool{}
	for _, row := range rowsOf(t, f) {
		if row.Primary {
			continue
		}
		kinds[row.Kind] = true
		if offered, why := Offered(row); offered || why != "only the root can be restored" {
			t.Errorf("%s %s: Offered = %v, %q; want false, %q", row.Kind, row.Name, offered, why, "only the root can be restored")
		}
	}
	// Each non-root kind must actually have appeared, or the loop above
	// proved nothing about it (AllRows keeps the folded remote rows, so
	// origin/main is the remote row here).
	for _, k := range []cleanup.Kind{cleanup.KindWorktree, cleanup.KindBranch, cleanup.KindRemote} {
		if !kinds[k] {
			t.Errorf("the fixture classified no %s row", k)
		}
	}
}

// TestOfferedNoDefaultRootSaysNothingToRestoreTo pins R501b's fourth shape:
// a root in a repository with no resolvable default — its row carries
// cleanup.ReasonNoDefaultBranch and no drift label, none is invented without
// a default — is not offered and says why. Why: restore-root's whole move
// names a default branch to return to, so the menu explains instead of
// offering an action that cannot run.
func TestOfferedNoDefaultRootSaysNothingToRestoreTo(t *testing.T) {
	t.Parallel()
	f := localOnly(t, "trunk", false) // no origin and no main/master: no default resolves
	row := primaryRow(t, rowsOf(t, f))
	if !hasReason(row.Reasons, cleanup.ReasonNoDefaultBranch) {
		t.Fatalf("the root row carries no %s reason: %+v", cleanup.ReasonNoDefaultBranch, row.Reasons)
	}
	if offered, why := Offered(row); offered || why != "no default branch to restore to" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "no default branch to restore to")
	}
}

// TestOfferedUsesRowNotGit pins that Offered is a pure function of the row it
// is handed — it reads the row, never git. The first two rows differ only in
// one Reasons entry yet demand opposite verdicts, from the one repository
// state this process sits in whatever it is: a git read gives one answer and
// could not satisfy both. The refusal also quotes the row's own branch name
// verbatim, and flipping Primary alone flips the verdict again.
func TestOfferedUsesRowNotGit(t *testing.T) {
	t.Parallel()
	row := cleanup.Row{Kind: cleanup.KindWorktree, Name: "root-dir", Branch: "trunk", Primary: true}
	if offered, why := Offered(row); offered || why != "root is already on trunk" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "root is already on trunk")
	}
	drifting := row
	drifting.Reasons = []cleanup.Reason{{Code: cleanup.ReasonRootDrift, Detail: "root is on feature, not trunk"}}
	if offered, why := Offered(drifting); !offered || why != "" {
		t.Errorf("Offered = %v, %q; want true, %q", offered, why, "")
	}
	row.Primary = false
	if offered, why := Offered(row); offered || why != "only the root can be restored" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "only the root can be restored")
	}
}
