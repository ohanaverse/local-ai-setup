package rootmove

import (
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/gittest"
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

// bareFixture is a real bare repository (a `clone --bare` of the shared
// template's origin): the primary entry of a repository with no main working
// tree, which the list shows as one row (Row.Bare).
func bareFixture(t *testing.T) *fixture {
	t.Helper()
	tpl := gittest.Seed(t, gittest.SeedOpts{})
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "repo.git")
	gitIn(t, tmp, "clone", "-q", "--bare", tpl.Origin, dir)
	gittest.Lineage(t, dir)
	return &fixture{t: t, Origin: tpl.Origin, Dir: dir}
}

// TestOfferedRootOnFeatureBranch pins the offer shape on a real repository:
// the root checked out on a feature branch is offered with no message —
// R501d case 6, a named branch off the default. Why: that root is the one
// row the cleanup list's R action exists for. The drift-label assert pins the
// classifier contract behind the list's own line (this row carries
// ReasonRootDrift), while the offer itself no longer depends on the label.
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
// on real default fixtures, and the case-5 guard behind it: the root row on
// the default carries the classifier's BlockDefaultBranch blocker (its Detail
// is the default's name), which is exactly what "demonstrably on the default"
// keys on. The brief's exact literal "root is already on main" is the
// main-default rendering and is asserted verbatim there; on a trunk-default
// fixture the same code renders "root is already on trunk". Why: a
// cleanup.Row carries no default-branch name, so on a default-branch root
// the row's Branch IS the default's name and the message must take it from
// there — truthful in every repository, not only a main-default one.
func TestOfferedRootOnDefaultSaysAlreadyThere(t *testing.T) {
	t.Parallel()
	t.Run("main default", func(t *testing.T) {
		f := newFixture(t)
		row := primaryRow(t, rowsOf(t, f))
		if row.Branch != "main" {
			t.Fatalf("root row branch = %q, want %q", row.Branch, "main")
		}
		if !hasReason(row.Blockers, cleanup.BlockDefaultBranch) {
			t.Fatalf("the root row carries no %s blocker: %+v", cleanup.BlockDefaultBranch, row.Blockers)
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
		if !hasReason(row.Blockers, cleanup.BlockDefaultBranch) {
			t.Fatalf("the root row carries no %s blocker: %+v", cleanup.BlockDefaultBranch, row.Blockers)
		}
		if offered, why := Offered(row); offered || why != "root is already on trunk" {
			t.Errorf("Offered = %v, %q; want false, %q", offered, why, "root is already on trunk")
		}
	})
}

// TestOfferedDetachedRoot pins R501c: a detached root is NOT offered. Its
// row carries the drift label ("root is detached") and Branch "", yet the
// menu explains instead of offering — the explanation in restore-root's
// CodeDetachedRoot vocabulary ("there is no branch to move", preflight's
// detached refusal). Why: the move operates on "the branch the primary
// checkout has checked out" and a detached root has none, so an offered R
// could only fail.
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
	if offered, why := Offered(row); offered || why != "the root is detached — there is no branch to move" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "the root is detached — there is no branch to move")
	}
}

// TestOfferedBareRootSaysNoCheckout pins R501d's case 2 on a real bare
// repository: its row is Primary and Bare with Branch "" — the bare and
// detached guards both hold — and the bare case must win, naming the missing
// checkout. Why: restore-root refuses a bare repository ("it has no root
// checkout to move"), so the menu explains; judging Branch first would
// mislabel it as detached.
func TestOfferedBareRootSaysNoCheckout(t *testing.T) {
	t.Parallel()
	row := primaryRow(t, rowsOf(t, bareFixture(t)))
	if !row.Bare || row.Branch != "" {
		t.Fatalf("bare row shape: Bare=%v Branch=%q; want Bare with %q", row.Bare, row.Branch, "")
	}
	if offered, why := Offered(row); offered || why != "a bare repository has no checkout to restore" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "a bare repository has no checkout to restore")
	}
}

// TestOfferedRootOffLocalFallbackDefaultIsOffered pins R501d's case 6 on the
// shape a catch-all "otherwise" used to mislabel: a repository with no
// origin whose default resolves only by the local fallback (local main), its
// root on a named branch off it. The row carries no drift label (none is
// invented without origin/HEAD naming a default), no no-default label and no
// default-branch blocker — and the root is offered anyway. Why: "root on a
// non-default branch" is the brief's own first case, and `wt restore-root`
// moves exactly this root.
func TestOfferedRootOffLocalFallbackDefaultIsOffered(t *testing.T) {
	t.Parallel()
	f := localOnly(t, "main", false) // no origin: the default resolves only as the local main
	f.git("switch", "-q", "-c", "dev")
	row := primaryRow(t, rowsOf(t, f))
	if row.Branch != "dev" {
		t.Fatalf("root row branch = %q, want %q", row.Branch, "dev")
	}
	if hasReason(row.Reasons, cleanup.ReasonRootDrift) || hasReason(row.Reasons, cleanup.ReasonNoDefaultBranch) {
		t.Fatalf("the root row must carry neither drift nor no-default: %+v", row.Reasons)
	}
	if hasReason(row.Blockers, cleanup.BlockDefaultBranch) {
		t.Fatalf("the root row must not carry the %s blocker: %+v", cleanup.BlockDefaultBranch, row.Blockers)
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

// TestOfferedNoDefaultRootSaysNothingToRestoreTo pins R501b's shape: a root
// in a repository with no resolvable default — its row carries
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
// one Blockers entry yet demand opposite verdicts, from the one repository
// state this process sits in whatever it is: a git read gives one answer and
// could not satisfy both. The refusal also quotes the row's own branch name
// verbatim, and flipping Primary alone flips the verdict again.
func TestOfferedUsesRowNotGit(t *testing.T) {
	t.Parallel()
	row := cleanup.Row{Kind: cleanup.KindWorktree, Name: "root-dir", Branch: "trunk", Primary: true,
		Blockers: []cleanup.Reason{{Code: cleanup.BlockDefaultBranch, Detail: "trunk"}}}
	if offered, why := Offered(row); offered || why != "root is already on trunk" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "root is already on trunk")
	}
	offDefault := row
	offDefault.Blockers = nil
	if offered, why := Offered(offDefault); !offered || why != "" {
		t.Errorf("Offered = %v, %q; want true, %q", offered, why, "")
	}
	row.Primary = false
	if offered, why := Offered(row); offered || why != "only the root can be restored" {
		t.Errorf("Offered = %v, %q; want false, %q", offered, why, "only the root can be restored")
	}
}
